package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// unit mirrors internal/api/units.go's unitView JSON shape. Kept as an
// independent copy rather than importing internal/api — the CLI is a
// separate consumer of a stable HTTP contract, the same relationship
// web/src/lib/types.ts has to it.
type unit struct {
	App              string  `json:"app"`
	Project          string  `json:"project"`
	Env              string  `json:"env"`
	Region           string  `json:"region"`
	Auto             bool    `json:"auto"`
	DesiredImage     string  `json:"desiredImage"`
	LiveImage        string  `json:"liveImage"`
	Status           string  `json:"status"`
	Health           string  `json:"health"`
	LastReconciledAt *string `json:"lastReconciledAt"`
	CanSync          bool    `json:"canSync"`
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

// apiError carries the HTTP status alongside the response body, so a
// caller can distinguish e.g. 409 (sync already in progress) from a
// genuine failure without string-matching the message.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", http.StatusText(e.status), strings.TrimSpace(e.body))
}

// client is a thin wrapper around the runcd HTTP API — no retries, no
// connection pooling beyond http.DefaultClient's own, matching how small
// this surface is (five endpoints, all fast, all idempotent-safe to just
// fail and let the user re-run).
type client struct {
	baseURL string
	token   string // "" if the API isn't behind IAP (or auth is handled some other way in front of this process)
	http    *http.Client
}

func newClient(baseURL, token string) *client {
	return &client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
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
	err := c.do(ctx, http.MethodGet, "/api/units/"+project+"/"+app, &u)
	return u, err
}

func (c *client) getHistory(ctx context.Context, project, app string) ([]syncEvent, error) {
	var events []syncEvent
	err := c.do(ctx, http.MethodGet, "/api/units/"+project+"/"+app+"/history", &events)
	return events, err
}

func (c *client) listRBAC(ctx context.Context) ([]rbacRule, error) {
	var rules []rbacRule
	err := c.do(ctx, http.MethodGet, "/api/rbac", &rules)
	return rules, err
}

func (c *client) sync(ctx context.Context, project, app string) (syncResponse, error) {
	var res syncResponse
	err := c.do(ctx, http.MethodPost, "/api/sync/"+project+"/"+app, &res)
	return res, err
}

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
