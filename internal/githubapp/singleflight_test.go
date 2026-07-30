package githubapp

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingTransport fakes GitHub's installation-discovery and
// token-minting endpoints without any real network access, counting how
// many times each is actually hit.
type countingTransport struct {
	installationCalls atomic.Int64
	mintCalls         atomic.Int64
	// tokenTTL controls how far in the future each minted token's
	// expires_at is — short-lived by default so tests can force a remint
	// without waiting a full hour.
	tokenTTL time.Duration
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case strings.HasSuffix(req.URL.Path, "/installation"):
		t.installationCalls.Add(1)
		return jsonResponse(`{"id": 42}`), nil
	case strings.Contains(req.URL.Path, "/access_tokens"):
		t.mintCalls.Add(1)
		ttl := t.tokenTTL
		if ttl == 0 {
			ttl = time.Hour
		}
		body := `{"token": "test-token", "expires_at": "` + time.Now().Add(ttl).Format(time.RFC3339) + `"}`
		return statusResponse(http.StatusCreated, body), nil
	default:
		return jsonResponse(`{}`), nil
	}
}

func jsonResponse(body string) *http.Response {
	return statusResponse(http.StatusOK, body)
}

func statusResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

func testClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := NewClient("123456", pemBytes)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}
	return client
}

// TestInstallationToken_ConcurrentMissesCoalesce regression-tests a
// cache-stampede bug: concurrent installationToken calls for the same repo
// used to each independently mint a fresh JWT + installation token instead
// of coalescing into one.
func TestInstallationToken_ConcurrentMissesCoalesce(t *testing.T) {
	transport := &countingTransport{}
	client := testClient(t, transport)

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if _, err := client.installationToken(t.Context(), "owner", "repo"); err != nil {
				t.Errorf("installationToken: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := transport.installationCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 installation lookup for %d concurrent misses, got %d", concurrency, got)
	}
	if got := transport.mintCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 token mint for %d concurrent misses, got %d", concurrency, got)
	}
}

// TestInstallationToken_ReusesCachedInstallationIDAcrossRemints
// regression-tests an avoidable-API-call bug: every token remint used to
// re-discover the installation ID from scratch, even though it never
// changes for a given repo — doubling GitHub API calls on every refresh.
func TestInstallationToken_ReusesCachedInstallationIDAcrossRemints(t *testing.T) {
	transport := &countingTransport{tokenTTL: time.Millisecond}
	client := testClient(t, transport)

	if _, err := client.installationToken(t.Context(), "owner", "repo"); err != nil {
		t.Fatalf("first installationToken: %v", err)
	}
	// The cached token already reads as expired (2-minute early-refresh
	// margin outweighs the 1ms TTL), so this call must remint.
	if _, err := client.installationToken(t.Context(), "owner", "repo"); err != nil {
		t.Fatalf("second installationToken: %v", err)
	}

	if got := transport.mintCalls.Load(); got != 2 {
		t.Fatalf("expected 2 token mints across the two calls, got %d", got)
	}
	if got := transport.installationCalls.Load(); got != 1 {
		t.Fatalf("expected the installation lookup to happen only once and be cached, got %d", got)
	}
}
