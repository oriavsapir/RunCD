// Package githubapp authenticates to GitHub as a GitHub App and fetches
// manifest files over the Contents API — no local clone, no git binary, so
// the controller's distroless runtime image stays git-free.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// maxResponseBytes bounds every GitHub API response body this client reads
// (manifest file content, error bodies) — an unbounded io.ReadAll on a
// compromised or misbehaving upstream could otherwise OOM every replica
// mid reconcile pass. Generous for a manifest file (§5.1's format is a
// small YAML doc) or an error body (diagnostic text only).
const maxResponseBytes = 10 << 20 // 10 MiB

// readLimited reads at most maxResponseBytes from r, erroring if the body
// is truncated rather than silently returning a partial manifest.
func readLimited(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeded %d byte limit", maxResponseBytes)
	}
	return body, nil
}

// decodeJSONLimited decodes a JSON response body into out, capped at
// maxResponseBytes — the same size guard readLimited gives the raw-bytes
// paths (manifest fetch, error bodies), applied to the success-path JSON
// decodes (installation lookup, token mint) that would otherwise stream an
// unbounded response straight into json.Decoder.
func decodeJSONLimited(r io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(r, maxResponseBytes+1)).Decode(out)
}

// Client mints short-lived GitHub App installation tokens (cached per repo)
// and uses them to read files from arbitrary repos the app is installed on.
type Client struct {
	AppID      string
	PrivateKey *rsa.PrivateKey
	HTTPClient *http.Client // nil means http.DefaultClient

	mu     sync.Mutex
	tokens map[string]cachedToken // keyed "owner/repo"
	// installationIDs caches the installation ID per owner/repo (see
	// cachedInstallationID for why).
	installationIDs map[string]int64
	// minting is a singleflight group keyed "owner/repo" so concurrent
	// cache misses for the same repo coalesce into one JWT + token-mint
	// round-trip instead of each independently hitting GitHub's API.
	minting singleflight.Group
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// NewClient parses a GitHub App's PEM-encoded private key (PKCS#1 or
// PKCS#8) as downloaded from the app's settings page.
func NewClient(appID string, pemBytes []byte) (*Client, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		// Common deployment footgun: a multi-line PEM passed through a
		// Cloud Run secret-as-env-var sometimes arrives with literal `\n`
		// two-character sequences instead of real newlines. Retry once
		// with those normalized before giving up.
		block, _ = pem.Decode(bytes.ReplaceAll(pemBytes, []byte(`\n`), []byte("\n")))
	}
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in GitHub App private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse GitHub App private key: %w", err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("GitHub App private key is not RSA")
		}
		key = rsaKey
	}
	return &Client{
		AppID:           appID,
		PrivateKey:      key,
		tokens:          make(map[string]cachedToken),
		installationIDs: make(map[string]int64),
	}, nil
}

// defaultHTTPTimeout guards against a hung connection blocking a caller's
// goroutine indefinitely when its context carries no deadline of its own
// (e.g. the reconcile loop's ticker-driven ctx) — http.DefaultClient has no
// timeout at all.
const defaultHTTPTimeout = 30 * time.Second

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient
}

// GetFile fetches path from repo at ref (empty ref means the repo's default
// branch). repo accepts "owner/repo", an https URL, or a git@github.com:
// SSH-style URL (only the owner/repo is ever used — auth is always the App
// installation token, never SSH).
func (c *Client) GetFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	owner, name, err := parseOwnerRepo(repo)
	if err != nil {
		return nil, err
	}
	tok, err := c.installationToken(ctx, owner, name)
	if err != nil {
		return nil, err
	}

	u := &url.URL{
		Scheme: "https",
		Host:   "api.github.com",
		Path:   fmt.Sprintf("/repos/%s/%s/contents/%s", owner, name, strings.TrimPrefix(path, "/")),
	}
	if ref != "" {
		q := u.Query()
		q.Set("ref", ref)
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s/%s:%s: %w", owner, name, path, err)
	}
	req.Header.Set("Authorization", "token "+tok)
	req.Header.Set("Accept", "application/vnd.github.raw")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s/%s:%s: %w", owner, name, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := readLimited(resp.Body)
		return nil, fmt.Errorf("fetch %s/%s:%s: %s: %s", owner, name, path, resp.Status, body)
	}
	return readLimited(resp.Body)
}

// installationToken returns a cached token for owner/repo, minting a fresh
// one when missing or close to expiry. Concurrent misses for the same
// owner/repo coalesce into a single mint via c.minting.
func (c *Client) installationToken(ctx context.Context, owner, repo string) (string, error) {
	key := owner + "/" + repo

	c.mu.Lock()
	if t, ok := c.tokens[key]; ok && time.Now().Before(t.expiresAt) {
		c.mu.Unlock()
		return t.token, nil
	}
	c.mu.Unlock()

	v, err, _ := c.minting.Do(key, func() (any, error) {
		c.mu.Lock()
		if t, ok := c.tokens[key]; ok && time.Now().Before(t.expiresAt) {
			c.mu.Unlock()
			return t.token, nil
		}
		c.mu.Unlock()

		// context.WithoutCancel: this mint is shared via singleflight
		// across every concurrent caller for this owner/repo — see the
		// identical note in gitsource.go.
		mintCtx := context.WithoutCancel(ctx)
		jwt, err := c.appJWT()
		if err != nil {
			return "", err
		}
		installationID, err := c.cachedInstallationID(mintCtx, jwt, owner, repo)
		if err != nil {
			return "", err
		}
		tok, expiresAt, err := c.mintToken(mintCtx, jwt, installationID)
		if err != nil {
			return "", err
		}

		c.mu.Lock()
		// Refresh a couple minutes early rather than racing the exact expiry.
		c.tokens[key] = cachedToken{token: tok, expiresAt: expiresAt.Add(-2 * time.Minute)}
		c.mu.Unlock()
		return tok, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// cachedInstallationID avoids re-discovering the installation on every
// token remint — a GitHub App's installation on a given repo is
// effectively permanent (it only changes if the app is uninstalled and
// reinstalled), so one lookup per owner/repo for the process lifetime is
// enough.
func (c *Client) cachedInstallationID(ctx context.Context, jwt, owner, repo string) (int64, error) {
	key := owner + "/" + repo
	c.mu.Lock()
	if id, ok := c.installationIDs[key]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	id, err := c.installationID(ctx, jwt, owner, repo)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	c.installationIDs[key] = id
	c.mu.Unlock()
	return id, nil
}

type installationResponse struct {
	ID int64 `json:"id"`
}

func (c *Client) installationID(ctx context.Context, jwt, owner, repo string) (int64, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/installation", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("build installation lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("find installation for %s/%s: %w", owner, repo, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := readLimited(resp.Body)
		return 0, fmt.Errorf("find installation for %s/%s: %s: %s", owner, repo, resp.Status, body)
	}
	var out installationResponse
	if err := decodeJSONLimited(resp.Body, &out); err != nil {
		return 0, fmt.Errorf("decode installation response: %w", err)
	}
	return out.ID, nil
}

type installationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) mintToken(ctx context.Context, jwt string, installationID int64) (string, time.Time, error) {
	u := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		body, _ := readLimited(resp.Body)
		return "", time.Time{}, fmt.Errorf("mint installation token: %s: %s", resp.Status, body)
	}
	var out installationTokenResponse
	if err := decodeJSONLimited(resp.Body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token response: %w", err)
	}
	return out.Token, out.ExpiresAt, nil
}

// appJWT builds the short-lived RS256 JWT GitHub requires to authenticate
// as the App itself (as opposed to one of its installations).
func (c *Client) appJWT() (string, error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(), // clock drift tolerance, per GitHub's docs
		"exp": now.Add(9 * time.Minute).Unix(),   // GitHub caps this at 10 minutes
		"iss": c.AppID,
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.PrivateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseOwnerRepo(repo string) (owner, name string, err error) {
	repo = strings.TrimSuffix(repo, ".git")
	switch {
	case strings.HasPrefix(repo, "git@github.com:"):
		repo = strings.TrimPrefix(repo, "git@github.com:")
	case strings.HasPrefix(repo, "https://github.com/"):
		repo = strings.TrimPrefix(repo, "https://github.com/")
	case strings.HasPrefix(repo, "github.com/"):
		repo = strings.TrimPrefix(repo, "github.com/")
	}
	parts := strings.SplitN(repo, "/", 2)
	// A well-formed "owner/repo" never has a third segment (repo names
	// can't contain "/") — reject that here instead of silently treating
	// the extra segment as part of the repo name, which would fail later
	// as a confusing 404 rather than a clear config-time error.
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", repo)
	}
	return parts[0], parts[1], nil
}
