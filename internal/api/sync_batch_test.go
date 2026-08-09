package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/rbac"
	"github.com/runcd/runcd/internal/reconcile"
	"github.com/runcd/runcd/internal/testutil"
)

// newBatchTestHandler builds a Handler with two units in different
// environments/projects — widget-api (env prd, project example-prod-eu) and
// worker-svc (env dev, project example-dev-eu) — so RBAC/filter behavior
// that depends on more than one unit actually has something to differ over.
func newBatchTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-eu/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
		"example-dev-eu/worker-svc":  {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}

	rbacCfg, err := rbac.Parse([]byte(`
roles:
  - subject: admin@company.com
    role: admin
    scope: ["*"]
  - subject: dev-only@company.com
    role: syncer
    scope: ["env:dev"]
`))
	if err != nil {
		t.Fatalf("rbac.Parse: %v", err)
	}

	units := StaticUnits{
		"widget-api/example-prod-eu": {App: "widget-api", Project: "example-prod-eu", Env: "prd"},
		"worker-svc/example-dev-eu":  {App: "worker-svc", Project: "example-dev-eu", Env: "dev"},
	}
	status := &PostgresStatusStore{DB: db}
	metricsHandler, err := NewMetricsHandler(status)
	if err != nil {
		t.Fatalf("NewMetricsHandler: %v", err)
	}

	h := &Handler{
		//nolint:gosec // G101: fakeAuth fixture tokens, not real credentials.
		Auth: &fakeAuth{tokenToEmail: map[string]string{
			"admin-token":    "admin@company.com",
			"dev-only-token": "dev-only@company.com",
		}},
		RBAC:    rbac.NewStore(rbacCfg),
		Units:   units,
		Status:  status,
		Metrics: metricsHandler,
		Reconciler: newReconcilerPointer(&reconcile.Reconciler{
			DB:            db,
			ManagedFields: []string{"image"},
			Manifests: &fakeManifests{byApp: map[string][]byte{
				"widget-api": serviceYAML(),
				"worker-svc": serviceYAML(),
			}},
			CloudRun:      cr,
			Preconditions: fakePreconditions{},
		}),
	}
	return h, db
}

func postSyncBatch(t *testing.T, url, bearerToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// decodeBatchResults decodes resp's body but does not close it — callers
// are expected to defer that themselves right after the request, matching
// this package's usual pattern, so bodyclose's static check can see it.
func decodeBatchResults(t *testing.T, resp *http.Response) map[string]batchSyncResult {
	t.Helper()
	var results []batchSyncResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byApp := make(map[string]batchSyncResult, len(results))
	for _, r := range results {
		byApp[r.App] = r
	}
	return byApp
}

func TestHandleSyncBatch_MissingTokenRejected(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestHandleSyncBatch_SyncsInScopeAndSkipsOutOfScope is the core behavior:
// a caller scoped to only "env:dev" gets worker-svc actually synced and
// widget-api reported as skipped/forbidden — not a 403 for the whole
// request, since a partial-access caller should still get what they can.
func TestHandleSyncBatch_SyncsInScopeAndSkipsOutOfScope(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "dev-only-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	byApp := decodeBatchResults(t, resp)

	if len(byApp) != 2 {
		t.Fatalf("expected results for both units, got %d: %+v", len(byApp), byApp)
	}
	if got := byApp["widget-api"]; got.Skipped != "forbidden" {
		t.Fatalf("expected widget-api skipped=forbidden, got %+v", got)
	}
	if got := byApp["worker-svc"]; got.Skipped != "" || got.Status == "" {
		t.Fatalf("expected worker-svc to actually sync, got %+v", got)
	}
}

// TestHandleSyncBatch_AdminSyncsBothUnits confirms a fully-scoped caller
// gets every unit actually attempted, none skipped.
func TestHandleSyncBatch_AdminSyncsBothUnits(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	byApp := decodeBatchResults(t, resp)

	for app, r := range byApp {
		if r.Skipped != "" {
			t.Fatalf("app %q unexpectedly skipped: %+v", app, r)
		}
		if r.Status == "" {
			t.Fatalf("app %q has no status: %+v", app, r)
		}
	}
}

func TestHandleSyncBatch_ProjectFilterNarrowsToOneUnit(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync?project=example-dev-eu", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	byApp := decodeBatchResults(t, resp)

	if len(byApp) != 1 {
		t.Fatalf("expected exactly one unit for project filter, got %d: %+v", len(byApp), byApp)
	}
	if _, ok := byApp["worker-svc"]; !ok {
		t.Fatalf("expected worker-svc in results, got %+v", byApp)
	}
}

// TestHandleSyncBatch_ObserveModeUnitSkipped checks a unit in observe mode
// reports skipped=observing rather than being silently synced or failing
// the whole batch.
func TestHandleSyncBatch_ObserveModeUnitSkipped(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	observe := true
	h.Units = StaticUnits{
		"widget-api/example-prod-eu": {
			App: "widget-api", Project: "example-prod-eu", Env: "prd",
			Sync: config.SyncPolicy{Observe: &observe},
		},
	}
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	byApp := decodeBatchResults(t, resp)

	if got := byApp["widget-api"]; got.Skipped != "observing" {
		t.Fatalf("expected skipped=observing, got %+v", got)
	}
}

// TestHandleSyncBatch_OutOfSyncFilterExcludesAlreadySynced seeds an
// already-Synced applications row for widget-api, then confirms
// ?filter=outOfSync leaves it out while still including worker-svc, which
// has never been reconciled (no row at all — treated as "not confirmed
// synced," not skipped).
func TestHandleSyncBatch_OutOfSyncFilterExcludesAlreadySynced(t *testing.T) {
	h, db := newBatchTestHandler(t)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO applications (name, target_gcp_project, desired_image, live_image, status, health, status_since, health_since, last_reconciled_at)
		VALUES ('widget-api', 'example-prod-eu', $1, $1, 'Synced', 'Healthy', now(), now(), now())`,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("seed applications: %v", err)
	}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync?filter=outOfSync", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	byApp := decodeBatchResults(t, resp)

	if _, ok := byApp["widget-api"]; ok {
		t.Fatalf("expected already-Synced widget-api excluded, got %+v", byApp)
	}
	if _, ok := byApp["worker-svc"]; !ok {
		t.Fatalf("expected never-reconciled worker-svc included, got %+v", byApp)
	}
}

// TestHandleSyncBatch_OneUnitDeployFailureDoesNotBlockOthers checks the
// batch's "one bad unit can't take down the rest" invariant against a real
// deploy failure (not just a blocked-before-deploy precondition outcome):
// widget-api's deploy fails against Cloud Run, worker-svc — a distinct
// unit, sharing the same fakeCloudRun — still syncs, and the overall
// response is 422 because at least one unit's DeployFailed is true.
func TestHandleSyncBatch_OneUnitDeployFailureDoesNotBlockOthers(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	cr := h.Reconciler.Load().CloudRun.(*fakeCloudRun)

	// widget-api's deploy attempt fails (a real "deploy attempted and
	// failed" outcome, not a fetch/precondition failure) while worker-svc's
	// own state is untouched.
	cr.deployErrFor = map[string]error{
		"example-prod-eu/widget-api": errors.New("simulated cloud run deploy failure"),
	}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 (one unit's deploy failed), got %d", resp.StatusCode)
	}
	byApp := decodeBatchResults(t, resp)

	widget := byApp["widget-api"]
	if !widget.DeployFailed || widget.Skipped != "" {
		t.Fatalf("expected widget-api to have DeployFailed=true and not be skipped, got %+v", widget)
	}
	worker := byApp["worker-svc"]
	if worker.DeployFailed || worker.Skipped != "" || worker.Status == "" {
		t.Fatalf("expected worker-svc to still sync successfully despite widget-api's failure, got %+v", worker)
	}
}

// TestHandleSyncBatch_ZeroGrantCallerWithOutOfSyncFilterAllForbidden pins the
// cost-optimization path in handleSyncBatch: a caller with no grant at all
// skips the (expensive) filterOutOfSync DB scan entirely, but every
// candidate must still be evaluated per-unit and reported forbidden — the
// optimization must never become an authorization bypass.
func TestHandleSyncBatch_ZeroGrantCallerWithOutOfSyncFilterAllForbidden(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	h.Auth = &fakeAuth{tokenToEmail: map[string]string{
		"no-grant-token": "no-grant@company.com",
	}}
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync?filter=outOfSync", "no-grant-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	byApp := decodeBatchResults(t, resp)

	if len(byApp) != 2 {
		t.Fatalf("expected both units still reported (not silently dropped), got %d: %+v", len(byApp), byApp)
	}
	for app, r := range byApp {
		if r.Skipped != "forbidden" {
			t.Fatalf("app %q: expected skipped=forbidden for a zero-grant caller, got %+v", app, r)
		}
	}
}

// unitListerLookupOnly is unitLookupOnly's counterpart in this file — the
// sync-batch handler specifically requires UnitLister (List), not just
// UnitLookup (Find), to fan a bulk sync out over every candidate.
type unitListerLookupOnly struct{}

func (unitListerLookupOnly) Find(string, string) (expander.SyncUnit, bool) {
	return expander.SyncUnit{}, false
}

func TestHandleSyncBatch_UnsupportedListingReturns501(t *testing.T) {
	h, _ := newBatchTestHandler(t)
	h.Units = unitListerLookupOnly{}
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSyncBatch(t, srv.URL+"/api/sync", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
}
