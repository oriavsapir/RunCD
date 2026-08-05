package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeAPI serves exactly what internal/api's routes would, using fixed
// fixture responses — enough to exercise runcd's request-building and
// output rendering without needing the real controller/a database.
func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/units", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"app":"widget-api","project":"example-prod-eu","env":"prd","region":"us-central1","auto":true,"status":"Synced","health":"Healthy","canSync":true}]`)
	})
	mux.HandleFunc("GET /api/units/example-prod-eu/widget-api", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"app":"widget-api","project":"example-prod-eu","env":"prd","region":"us-central1","auto":true,"desiredImage":"sha256:abcdef0123456789","liveImage":"sha256:abcdef0123456789","status":"Synced","health":"Healthy","canSync":true}`)
	})
	mux.HandleFunc("GET /api/units/example-prod-eu/widget-api/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id":1,"trigger":"manual","actor":"alice@company.com","fromImage":"sha256:aaaa","toImage":"sha256:bbbb","startedAt":"2026-01-01T00:00:00Z","result":"succeeded"}]`)
	})
	mux.HandleFunc("GET /api/rbac", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"subject":"alice@company.com","role":"admin","scope":["*"]}]`)
	})
	mux.HandleFunc("POST /api/sync/example-prod-eu/widget-api", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"app":"widget-api","project":"example-prod-eu","status":"Synced","health":"Healthy"}`)
	})
	mux.HandleFunc("POST /api/sync/example-prod-eu/locked-app", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sync already in progress for this app/project", http.StatusConflict)
	})
	mux.HandleFunc("GET /api/units/example-prod-eu/widget-api/dry-run", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"app":"widget-api","project":"example-prod-eu","status":"OutOfSync","health":"Healthy","desiredImage":"sha256:cccc","liveImage":"sha256:dddd"}`)
	})
	mux.HandleFunc("GET /api/orphans", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"project":"example-prod-eu","region":"us-central1","app":"leftover-app"}]`)
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"configRepo":"acme/deployment","configBranch":"main","configPath":"runcd.yaml","rbacPath":"rbac.yaml","reconcileIntervalSeconds":30,"managedFields":["image","traffic"],"notificationsEnabled":true,"notifyByEnv":{"prod":{"sink":"prod-incidents","rules":["syncFailed","healthDegraded"]},"dev":{"sink":"default","rules":["syncFailed"]}}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRun_Units(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"units"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "widget-api") || !strings.Contains(out, "Synced") {
		t.Fatalf("expected unit table, got: %s", out)
	}
}

func TestRun_Get(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"get", "example-prod-eu", "widget-api"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Status:          Synced") {
		t.Fatalf("expected diff output, got: %s", out)
	}
}

func TestRun_History(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"history", "example-prod-eu", "widget-api"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "alice@company.com") {
		t.Fatalf("expected history table, got: %s", stdout.String())
	}
}

func TestRun_Rbac(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"rbac"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "admin") {
		t.Fatalf("expected rbac table, got: %s", stdout.String())
	}
}

func TestRun_Sync(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"sync", "example-prod-eu", "widget-api"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "status=Synced") {
		t.Fatalf("expected sync result, got: %s", stdout.String())
	}
}

func TestRun_SyncDryRun(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"sync", "example-prod-eu", "widget-api", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "OutOfSync") {
		t.Fatalf("expected dry-run preview output, got: %s", out)
	}
}

func TestRun_Orphans(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"orphans"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "leftover-app") {
		t.Fatalf("expected orphans table, got: %s", stdout.String())
	}
}

func TestRun_Config(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"config"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Slack notifications: enabled") {
		t.Fatalf("expected notifications enabled, got: %s", out)
	}
	if !strings.Contains(out, "prod") || !strings.Contains(out, "prod-incidents") ||
		!strings.Contains(out, "dev") || !strings.Contains(out, "default") {
		t.Fatalf("expected per-environment notify table, got: %s", out)
	}
}

// TestRun_SyncConflictSurfacesAPIError checks that a 409 from the API
// (another attempt already in flight — see reconcile.ErrSyncInProgress)
// comes back as a distinguishable error, not a silently-swallowed failure.
func TestRun_SyncConflictSurfacesAPIError(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{"sync", "example-prod-eu", "locked-app"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error for a 409 response")
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *apiError, got %T: %v", err, err)
	}
	if apiErr.status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", apiErr.status)
	}
}

func TestRun_MissingBaseURL(t *testing.T) {
	t.Setenv("RUNCD_API_URL", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{"units"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error when RUNCD_API_URL is unset")
	}
}

func TestRun_WrongArgCount(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"get", "only-one-arg"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for wrong argument count")
	}
}

// TestRun_ValidateNeedsNoAPIURL checks that validate runs without
// RUNCD_API_URL set at all — it's local-only, unlike every other command.
func TestRun_ValidateNeedsNoAPIURL(t *testing.T) {
	t.Setenv("RUNCD_API_URL", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", "../../examples/full"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stdout=%s", err, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "expands to 6 sync unit(s)") {
		t.Fatalf("expected expand summary, got: %s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("expected no failures, got: %s", out)
	}
}

func TestRun_ValidateFailsOnBadExclude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/runcd.yaml", []byte(`
environments:
  dev:
    projects: [acme-dev-01]
defaults:
  region: us-central1
  managedFields: [image, traffic, env]
apps:
  - name: checkout-service
    env: dev
    exclude: [does-not-exist-project]
    source: { repo: "git@github.com:acme/deployment.git", branch: main, path: "service.yaml" }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", dir}, &stdout, &stderr); err == nil {
		t.Fatalf("expected validation failure, got none; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "FAIL") {
		t.Fatalf("expected a FAIL line, got: %s", stdout.String())
	}
}

func TestRun_ValidateCatchesUnrecognizedField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/runcd.yaml", []byte(`
environments:
  dev:
    projects: [acme-dev-01]
defaults:
  region: us-central1
  managedFields: [image, traffic, env]
  sync: { auto: false, blabla: 300 }
apps:
  - name: checkout-service
    env: dev
    source: { repo: "git@github.com:acme/deployment.git", path: "service.yaml" }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", dir}, &stdout, &stderr); err == nil {
		t.Fatalf("expected validation failure, got none; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `field blabla not found`) {
		t.Fatalf("expected the unrecognized field to be named, got: %s", stdout.String())
	}
}

// TestRun_ValidateSkipsLiveChecksByDefault confirms --check-gcp/--check-slack
// are opt-in — without them, validate must never attempt a network call
// (there's no ADC/webhook configured in this test environment, so it would
// fail loudly if it tried).
func TestRun_ValidateSkipsLiveChecksByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/runcd.yaml", []byte(`
environments:
  dev:
    projects: [acme-dev-01]
defaults:
  region: us-central1
  managedFields: [image, traffic, env]
apps:
  - name: checkout-service
    env: dev
    source: { repo: "git@github.com:acme/deployment.git", path: "service.yaml" }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stdout=%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), "project") || strings.Contains(stdout.String(), "slack sink") {
		t.Fatalf("expected no live-check output without the flags, got: %s", stdout.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}
