package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// unit mirrors internal/api/units.go's unitView JSON shape. Kept as an
// independent copy rather than importing internal/api — the CLI is a
// separate consumer of a stable HTTP contract, the same relationship
// web/src/lib/types.ts has to it.
type unit struct {
	App                 string   `json:"app"`
	Project             string   `json:"project"`
	Env                 string   `json:"env"`
	Region              string   `json:"region"`
	Auto                bool     `json:"auto"`
	DesiredImage        string   `json:"desiredImage"`
	LiveImage           string   `json:"liveImage"`
	Status              string   `json:"status"`
	Health              string   `json:"health"`
	LastReconciledAt    *string  `json:"lastReconciledAt"`
	CanSync             bool     `json:"canSync"`
	IgnoreFields        []string `json:"ignoreFields"`
	IgnorePreconditions []string `json:"ignorePreconditions"`
}

// syncEvent mirrors internal/api/units.go's sync_events JSON shape.
type syncEvent struct {
	ID         int64  `json:"id"`
	Trigger    string `json:"trigger"`
	Actor      string `json:"actor"`
	FromImage  string `json:"fromImage"`
	ToImage    string `json:"toImage"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Result     string `json:"result"`
	Error      string `json:"error"`
}

// rbacRule mirrors internal/api/rbac.go's rbacRoleView JSON shape.
type rbacRule struct {
	Subject string   `json:"subject"`
	Role    string   `json:"role"`
	Scope   []string `json:"scope"`
}

// syncResponse mirrors internal/api/api.go's syncResponse JSON shape.
type syncResponse struct {
	App     string `json:"app"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Health  string `json:"health"`
}

// dryRunResponse mirrors internal/api/units.go's dryRunView JSON shape.
type dryRunResponse struct {
	App          string `json:"app"`
	Project      string `json:"project"`
	Status       string `json:"status"`
	Health       string `json:"health"`
	DesiredImage string `json:"desiredImage"`
	LiveImage    string `json:"liveImage"`
}

// orphan mirrors internal/api/units.go's orphanView JSON shape.
type orphan struct {
	Project string `json:"project"`
	Region  string `json:"region"`
	App     string `json:"app"`
}

// apiError carries the HTTP status alongside the response body, so a
// caller can distinguish e.g. 409 (sync already in progress) from a
// genuine failure without string-matching the message.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	text := http.StatusText(e.status)
	if text == "" {
		text = fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("%s: %s", text, strings.TrimSpace(e.body))
}

// client is a thin wrapper around the runcd HTTP API — no retries, no
// connection pooling beyond http.DefaultClient's own, matching how small
// this surface is (five endpoints, all fast, all idempotent-safe to just
// fail and let the user re-run).
//
// No client-level Timeout: sync blocks synchronously through the
// controller's fetch->diff->precondition->deploy->re-fetch sequence,
// which routinely exceeds any timeout short enough to be reasonable for
// the read endpoints (units/get/history/rbac). Each call site instead
// gets its own context deadline sized for what it actually does — see
// main.go.
// maxResponseBytes bounds every response body this CLI reads — a
// misbehaving backend or proxy shouldn't be able to OOM this process on a
// huge response.
const maxResponseBytes = 10 << 20 // 10 MiB

type client struct {
	baseURL string
	token   string // "" if the API isn't behind IAP (or auth is handled some other way in front of this process)
	http    *http.Client
}

func newClient(baseURL, token string) *client {
	return &client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: &http.Client{}}
}

// do's target (c.baseURL, set once at startup from RUNCD_API_URL — see
// main.go) is exactly what this CLI exists to call, not attacker-supplied
// input reaching an otherwise-fixed internal service — gosec's G704 SSRF
// heuristic can't distinguish "the URL is the whole point" from a real
// SSRF sink.
func (c *client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil) //nolint:gosec
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req) //nolint:gosec
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Capped at maxResponseBytes, same class of guard already applied to
	// internal/githubapp's reads — an unbounded io.ReadAll would let a
	// misbehaving backend or proxy OOM this process on a huge response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("response exceeded %d byte limit", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{status: resp.StatusCode, body: string(body)}
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *client) listUnits(ctx context.Context) ([]unit, error) {
	var units []unit
	err := c.do(ctx, http.MethodGet, "/api/units", &units)
	return units, err
}

func (c *client) getUnit(ctx context.Context, project, app string) (unit, error) {
	var u unit
	err := c.do(ctx, http.MethodGet, "/api/units/"+url.PathEscape(project)+"/"+url.PathEscape(app), &u)
	return u, err
}

func (c *client) getHistory(ctx context.Context, project, app string) ([]syncEvent, error) {
	var events []syncEvent
	err := c.do(ctx, http.MethodGet, "/api/units/"+url.PathEscape(project)+"/"+url.PathEscape(app)+"/history", &events)
	return events, err
}

func (c *client) listRBAC(ctx context.Context) ([]rbacRule, error) {
	var rules []rbacRule
	err := c.do(ctx, http.MethodGet, "/api/rbac", &rules)
	return rules, err
}

func (c *client) sync(ctx context.Context, project, app string) (syncResponse, error) {
	var res syncResponse
	err := c.do(ctx, http.MethodPost, "/api/sync/"+url.PathEscape(project)+"/"+url.PathEscape(app), &res)
	return res, err
}

func (c *client) dryRun(ctx context.Context, project, app string) (dryRunResponse, error) {
	var res dryRunResponse
	err := c.do(ctx, http.MethodGet, "/api/units/"+url.PathEscape(project)+"/"+url.PathEscape(app)+"/dry-run", &res)
	return res, err
}

func (c *client) listOrphans(ctx context.Context) ([]orphan, error) {
	var orphans []orphan
	err := c.do(ctx, http.MethodGet, "/api/orphans", &orphans)
	return orphans, err
}

// identityTokenTimeout bounds the gcloud subprocess — unlike every network
// call this CLI makes (each gets its own readTimeout/syncTimeout deadline),
// this had none at all: a hung or interactive gcloud (a re-auth prompt, a
// DNS stall) would otherwise block the CLI forever.
const identityTokenTimeout = 30 * time.Second

// identityToken shells out to gcloud for a Google-signed identity token
// audienced to audience — the documented way to authenticate a script/CLI
// against an IAP-protected URL directly
// (https://cloud.google.com/iap/docs/authentication-howto), reusing
// whatever account the user already has active in gcloud rather than
// implementing OAuth here. Returns "" if audience is empty (no IAP in
// front — e.g. a self-hosted deployment using auth.GoogleAuthenticator, or
// calling the controller directly as an authorized Cloud Run invoker).
func identityToken(ctx context.Context, audience string) (string, error) {
	if audience == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, identityTokenTimeout)
	defer cancel()
	// audience is operator-configured (RUNCD_IAP_AUDIENCE), passed as one
	// argv element to a fixed binary/subcommand — not shell-interpreted, so
	// there's no injection surface for gosec's G204 to actually flag here.
	out, err := exec.CommandContext(ctx, "gcloud", "auth", "print-identity-token", "--audiences="+audience).Output() //nolint:gosec
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("gcloud auth print-identity-token: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("gcloud auth print-identity-token: %w (is the gcloud CLI installed and on PATH?)", err)
	}
	return strings.TrimSpace(string(out)), nil
}
