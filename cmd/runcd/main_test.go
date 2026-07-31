package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestRun_UnknownCommand(t *testing.T) {
	srv := fakeAPI(t)
	t.Setenv("RUNCD_API_URL", srv.URL)
	t.Setenv("RUNCD_IAP_AUDIENCE", "")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}
