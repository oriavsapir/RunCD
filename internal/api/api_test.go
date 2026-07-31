package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/rbac"
	"github.com/runcd/runcd/internal/reconcile"
	"github.com/runcd/runcd/internal/testutil"
)

const validDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"

type fakeAuth struct {
	// tokenToEmail maps a bearer token to the email it authenticates as;
	// any token not in this map is rejected.
	tokenToEmail map[string]string
}

func (f *fakeAuth) Verify(r *http.Request) (string, error) {
	token, ok := fakeBearerToken(r)
	if !ok {
		return "", errors.New("missing bearer token")
	}
	email, ok := f.tokenToEmail[token]
	if !ok {
		return "", errors.New("invalid token")
	}
	return email, nil
}

func fakeBearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

type fakeManifests struct{ byApp map[string][]byte }

func (f *fakeManifests) Get(_ context.Context, unit expander.SyncUnit) ([]byte, error) {
	raw, ok := f.byApp[unit.App]
	if !ok {
		return nil, fmt.Errorf("no manifest for %q", unit.App)
	}
	return raw, nil
}

func serviceYAML() []byte {
	return []byte(fmt.Sprintf("image:\n  digest: %s\n", validDigest))
}

type fakeCloudRun struct {
	services map[string]*cloudrun.LiveService
}

func (f *fakeCloudRun) GetService(_ context.Context, project, _, name, _ string) (*cloudrun.LiveService, error) {
	live, ok := f.services[project+"/"+name]
	if !ok {
		return nil, cloudrun.ErrNotProvisioned
	}
	return live, nil
}
func (f *fakeCloudRun) GetJob(context.Context, string, string, string, string) (*cloudrun.LiveJob, error) {
	return nil, cloudrun.ErrNotProvisioned
}
func (f *fakeCloudRun) DeployService(_ context.Context, project, _, name string, desired cloudrun.ServiceState) error {
	key := project + "/" + name
	if _, ok := f.services[key]; !ok {
		return cloudrun.ErrNotProvisioned
	}
	f.services[key] = &cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{ImageDigest: desired.ImageDigest},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	return nil
}
func (f *fakeCloudRun) DeployJob(context.Context, string, string, string, cloudrun.ServiceState) error {
	return cloudrun.ErrNotProvisioned
}
func (f *fakeCloudRun) ListServiceNames(_ context.Context, project, _ string) ([]string, error) {
	var names []string
	prefix := project + "/"
	for key := range f.services {
		if after, ok := strings.CutPrefix(key, prefix); ok {
			names = append(names, after)
		}
	}
	return names, nil
}

type fakeNotifier struct{}

func (fakeNotifier) Evaluate(context.Context, reconcile.Result) error { return nil }

type fakePreconditions struct{}

func (fakePreconditions) TopicExists(context.Context, string, string) (bool, error) { return true, nil }
func (fakePreconditions) SubscriptionExists(context.Context, string, string) (bool, error) {
	return true, nil
}

func newTestHandler(t *testing.T) (*Handler, *fakeCloudRun) {
	t.Helper()
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-eu/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
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

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	status := &PostgresStatusStore{DB: db}
	metricsHandler, err := NewMetricsHandler(status)
	if err != nil {
		t.Fatalf("NewMetricsHandler: %v", err)
	}

	h := &Handler{
		Auth: &fakeAuth{tokenToEmail: map[string]string{
			"admin-token":    "admin@company.com",
			"dev-only-token": "dev-only@company.com",
		}},
		RBAC:    rbac.NewStore(rbacCfg),
		Units:   StaticUnits{"widget-api/example-prod-eu": unit},
		Status:  status,
		Metrics: metricsHandler,
		Reconciler: newReconcilerPointer(&reconcile.Reconciler{
			DB:            db,
			ManagedFields: []string{"image"},
			Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
			CloudRun:      cr,
			Preconditions: fakePreconditions{},
		}),
	}
	return h, cr
}

func newReconcilerPointer(r *reconcile.Reconciler) *atomic.Pointer[reconcile.Reconciler] {
	p := &atomic.Pointer[reconcile.Reconciler]{}
	p.Store(r)
	return p
}

// postSync builds and sends a sync request, failing the test immediately
// on any request-construction error rather than risking a nil dereference.
func postSync(t *testing.T, url, bearerToken string) *http.Response {
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

func TestHandleSync_MissingTokenRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandleSync_InvalidTokenRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "garbage-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandleSync_UnknownAppRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/nonexistent-app", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleSync_OutOfScopeSubjectForbidden(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	// dev-only@company.com is scoped to env:dev; widget-api/example-prod-eu is prd.
	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "dev-only-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

// failingDB forces the reconciler's write path to fail with an error
// containing details that must never reach an HTTP caller.
type failingDB struct{ real *sql.DB }

func (f *failingDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return f.real.ExecContext(ctx, query, args...)
}

func (f *failingDB) QueryRowContext(ctx context.Context, _ string, _ ...any) *sql.Row {
	return f.real.QueryRowContext(ctx, "SELECT 1 FROM __simulated_write_failure__")
}

// TestHandleSync_InfraErrorReturns500WithoutLeakingDetail regression-tests
// an information-exposure bug: the 500 path used to echo the reconciler's
// raw error (potentially DB error text, GCP error detail) straight into the
// HTTP response body for any RBAC-authorized caller.
func TestHandleSync_InfraErrorReturns500WithoutLeakingDetail(t *testing.T) {
	h, _ := newTestHandler(t)
	h.Reconciler.Load().DB = &failingDB{real: h.Reconciler.Load().DB.(*sql.DB)}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "__simulated_write_failure__") {
		t.Fatalf("response body leaked internal error detail: %s", body)
	}
}

// TestHandleSync_LockedUnitReturns409 checks that a unit currently locked
// by another in-flight deploy attempt (see internal/reconcile's
// sync_locks-backed lock) surfaces as 409, not a generic 500/200 — unlike
// most res.Err cases, "someone else is already syncing this" is
// unambiguous and worth telling the caller.
func TestHandleSync_LockedUnitReturns409(t *testing.T) {
	h, _ := newTestHandler(t)
	db := h.Reconciler.Load().DB.(*sql.DB)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_locks (application, target_gcp_project, holder, expires_at)
		VALUES ('widget-api', 'example-prod-eu', 'other-attempt', now() + interval '1 minute')`); err != nil {
		t.Fatalf("seed sync_locks: %v", err)
	}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

type failingPreconditions struct{}

func (failingPreconditions) TopicExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (failingPreconditions) SubscriptionExists(context.Context, string, string) (bool, error) {
	return false, nil
}

// TestHandleSync_BusinessLevelErrorNotLeakedInSuccessfulResponse
// regression-tests a second information-exposure path distinct from the
// 500 case above: a ManualSync call can succeed (err == nil) while its
// Result carries a non-nil res.Err — including, in other code paths, raw
// wrapped infra errors from a failed live-state fetch or deploy. The 200
// response must never echo res.Err text, only the categorical
// status/health.
func TestHandleSync_BusinessLevelErrorNotLeakedInSuccessfulResponse(t *testing.T) {
	h, _ := newTestHandler(t)
	h.Reconciler.Load().Preconditions = failingPreconditions{}
	h.Reconciler.Load().Manifests = &fakeManifests{byApp: map[string][]byte{
		"widget-api": []byte(fmt.Sprintf("image:\n  digest: %s\nrequires:\n  - type: pubsubTopic\n    name: some-topic\n", validDigest)),
	}}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (sync attempted, just blocked), got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "precondition") || strings.Contains(string(body), "some-topic") {
		t.Fatalf("response body leaked business-level error detail: %s", body)
	}

	var parsed syncResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if parsed.Status != "Invalid" {
		t.Fatalf("expected Status=Invalid, got %+v", parsed)
	}
}

func TestHandleSync_AuthorizedAdminSyncsSuccessfully(t *testing.T) {
	h, cr := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body syncResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "Synced" {
		t.Fatalf("expected Status=Synced, got %+v", body)
	}
	live, ok := cr.services["example-prod-eu/widget-api"]
	if !ok {
		t.Fatal("no fake Cloud Run service state for example-prod-eu/widget-api")
	}
	if live.ImageDigest != validDigest {
		t.Fatalf("expected the deploy to have actually happened, got digest %q", live.ImageDigest)
	}
}

// getWithBearer issues a GET request with an optional bearer token,
// failing the test immediately on any request-construction error.
func getWithBearer(t *testing.T, url, bearerToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return resp
}

func TestHandleListUnits_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/units", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandleListRBAC_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/rbac", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestHandleListRBAC_ReturnsConfiguredRoles checks that any authenticated
// caller — not just an admin — can read the role list, matching every
// other read view's open-to-any-authenticated-caller posture (§5.9).
func TestHandleListRBAC_ReturnsConfiguredRoles(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/rbac", "dev-only-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var roles []rbacRoleView
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d: %+v", len(roles), roles)
	}
}

func TestHandleConfig_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/config", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestHandleConfig_ReturnsRuntimeInfo checks the static fields (set at
// Handler construction) and the dynamic ones (read live off the current
// Reconciler) both come through, and that the Slack webhook URL itself
// never does — only whether one's configured.
func TestHandleConfig_ReturnsRuntimeInfo(t *testing.T) {
	h, _ := newTestHandler(t)
	h.RuntimeInfo = RuntimeInfo{
		ConfigRepo:               "acme/deployment",
		ConfigBranch:             "main",
		ConfigPath:               "runcd.yaml",
		RBACPath:                 "rbac.yaml",
		ReconcileIntervalSeconds: 30,
	}
	h.Reconciler.Load().Notifier = fakeNotifier{}

	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/config", "dev-only-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "hooks.slack.com") {
		t.Fatalf("response leaked a webhook URL: %s", body)
	}

	var got configView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := configView{
		ConfigRepo:               "acme/deployment",
		ConfigBranch:             "main",
		ConfigPath:               "runcd.yaml",
		RBACPath:                 "rbac.yaml",
		ReconcileIntervalSeconds: 30,
		ManagedFields:            []string{"image"},
		NotificationsEnabled:     true,
	}
	if got.ConfigRepo != want.ConfigRepo || got.ConfigBranch != want.ConfigBranch ||
		got.ConfigPath != want.ConfigPath || got.RBACPath != want.RBACPath ||
		got.ReconcileIntervalSeconds != want.ReconcileIntervalSeconds ||
		got.NotificationsEnabled != want.NotificationsEnabled ||
		len(got.ManagedFields) != 1 || got.ManagedFields[0] != "image" {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestHandleListUnits_PendingBeforeAnySync checks that a unit present in
// config but never reconciled shows up as Pending, not absent — the
// applications table has no row for it yet.
func TestHandleListUnits_PendingBeforeAnySync(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/units", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var units []unitView
	if err := json.NewDecoder(resp.Body).Decode(&units); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("expected 1 unit, got %d: %+v", len(units), units)
	}
	if units[0].App != "widget-api" || units[0].Status != pendingStatus {
		t.Fatalf("expected widget-api with Status=Pending, got %+v", units[0])
	}
}

// TestHandleListUnits_ReflectsPersistedStateAfterSync checks that once a
// unit has been synced, the list (and detail) endpoints reflect its real
// persisted status/health instead of Pending.
func TestHandleListUnits_ReflectsPersistedStateAfterSync(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	syncResp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = syncResp.Body.Close() }()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected sync to succeed, got %d", syncResp.StatusCode)
	}

	listResp := getWithBearer(t, srv.URL+"/api/units", "admin-token")
	defer func() { _ = listResp.Body.Close() }()
	var units []unitView
	if err := json.NewDecoder(listResp.Body).Decode(&units); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(units) != 1 || units[0].Status != "Synced" || units[0].DesiredImage != validDigest {
		t.Fatalf("expected 1 Synced unit with desiredImage=%s, got %+v", validDigest, units)
	}

	detailResp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = detailResp.Body.Close() }()
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", detailResp.StatusCode)
	}
	var detail unitView
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.Status != "Synced" || detail.LastReconciledAt == nil {
		t.Fatalf("expected Synced detail with LastReconciledAt set, got %+v", detail)
	}
}

// TestHandleListUnits_CanSyncReflectsCallersOwnRBACScope regression-tests
// the gated Sync button's data source: the dashboard can't evaluate
// rbac.CanSync itself, so canSync must be computed per the caller's own
// identity, not a fixed value — an admin (scope "*") sees canSync=true for
// the prd unit; a syncer scoped to "env:dev" sees canSync=false for it.
func TestHandleListUnits_CanSyncReflectsCallersOwnRBACScope(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	adminResp := getWithBearer(t, srv.URL+"/api/units", "admin-token")
	defer func() { _ = adminResp.Body.Close() }()
	var adminUnits []unitView
	if err := json.NewDecoder(adminResp.Body).Decode(&adminUnits); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if len(adminUnits) != 1 || !adminUnits[0].CanSync {
		t.Fatalf("expected admin to see canSync=true, got %+v", adminUnits)
	}

	devResp := getWithBearer(t, srv.URL+"/api/units", "dev-only-token")
	defer func() { _ = devResp.Body.Close() }()
	var devUnits []unitView
	if err := json.NewDecoder(devResp.Body).Decode(&devUnits); err != nil {
		t.Fatalf("decode dev-only response: %v", err)
	}
	if len(devUnits) != 1 || devUnits[0].CanSync {
		t.Fatalf("expected dev-only (scoped to env:dev) to see canSync=false for the prd unit, got %+v", devUnits)
	}
}

func TestHandleUnitDetail_UnknownUnitRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/nonexistent-app", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestHandleOrphans_FlagsLiveServiceAbsentFromConfig checks the endpoint
// end-to-end: a live Cloud Run service the fixture's config never declared
// shows up as an orphan, and the one it does declare doesn't.
func TestHandleOrphans_FlagsLiveServiceAbsentFromConfig(t *testing.T) {
	h, cr := newTestHandler(t)
	cr.services["example-prod-eu/leftover-app"] = &cloudrun.LiveService{
		ServiceState: cloudrun.ServiceState{ImageDigest: validDigest},
	}
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/orphans", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var orphans []orphanView
	if err := json.NewDecoder(resp.Body).Decode(&orphans); err != nil {
		t.Fatalf("decode orphans response: %v", err)
	}
	if len(orphans) != 1 || orphans[0].App != "leftover-app" || orphans[0].Project != "example-prod-eu" {
		t.Fatalf("expected exactly one orphan (leftover-app), got %+v", orphans)
	}
}

func TestHandleOrphans_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/orphans", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestHandleDryRun_ReportsOutOfSyncWithoutDeployingOrPersisting is the API
// counterpart to reconcile.TestDryRun_ComputesResultWithoutDeployingOrPersisting:
// the same guarantee has to hold end-to-end through the HTTP handler, not
// just at the Reconciler method.
func TestHandleDryRun_ReportsOutOfSyncWithoutDeployingOrPersisting(t *testing.T) {
	h, cr := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/widget-api/dry-run", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var v dryRunView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode dry-run response: %v", err)
	}
	if v.Status != "OutOfSync" {
		t.Fatalf("expected OutOfSync, got %+v", v)
	}
	if got := cr.services["example-prod-eu/widget-api"].ImageDigest; got == validDigest {
		t.Fatalf("dry run must never deploy, but live digest changed to %q", got)
	}

	histResp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/widget-api/history", "admin-token")
	defer func() { _ = histResp.Body.Close() }()
	var events []syncEventView
	if err := json.NewDecoder(histResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no sync_events written by a dry run, got %d", len(events))
	}
}

func TestHandleDryRun_UnknownUnitRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/nonexistent-app/dry-run", "admin-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestHandleMetrics_ReflectsSyncedUnitAndSyncEvent checks the /metrics
// endpoint against the same data a manual sync just persisted — the
// dashboard's own read views already prove ListApplications/SyncHistory
// work, this proves the metrics aggregation on top of that data.
func TestHandleMetrics_ReflectsSyncedUnitAndSyncEvent(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	syncResp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = syncResp.Body.Close() }()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected sync to succeed, got %d", syncResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/metrics") //nolint:noctx // test-only, no context needed
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	text := string(body)

	// The OTel Prometheus exporter adds its own otel_scope_* labels to
	// every line, so this checks substrings (name, labels, trailing value)
	// rather than an exact line match.
	if !metricLineHasValue(text, "runcd_sync_status_total", `status="Synced"`, "1") {
		t.Fatalf("expected a Synced status counter of 1, got:\n%s", text)
	}
	if !metricLineHasValue(text, "runcd_sync_events_total", `result="succeeded",trigger="manual"`, "1") {
		t.Fatalf("expected a manual/succeeded sync_events counter of 1, got:\n%s", text)
	}
}

// metricLineHasValue reports whether text has a line starting with metric,
// containing labelSubstr somewhere in its label set, and ending with the
// given value — tolerant of the OTel Prometheus exporter's extra
// otel_scope_* labels and their arbitrary ordering.
func metricLineHasValue(text, metric, labelSubstr, value string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, metric+"{") && strings.Contains(line, labelSubstr) && strings.HasSuffix(line, " "+value) {
			return true
		}
	}
	return false
}

// TestHandleMetrics_RequiresNoAuth confirms /metrics is deliberately open —
// a scraper generally carries no IAP/OAuth identity.
func TestHandleMetrics_RequiresNoAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics") //nolint:noctx // test-only, no context needed
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no Authorization header, got %d", resp.StatusCode)
	}
}

// TestHandleUnitHistory_ReturnsSyncEventAfterSync checks the history
// endpoint surfaces the audit trail a manual sync writes to sync_events.
func TestHandleUnitHistory_ReturnsSyncEventAfterSync(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := httptest.NewServer(NewMux(h))
	defer srv.Close()

	syncResp := postSync(t, srv.URL+"/api/sync/example-prod-eu/widget-api", "admin-token")
	defer func() { _ = syncResp.Body.Close() }()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected sync to succeed, got %d", syncResp.StatusCode)
	}

	histResp := getWithBearer(t, srv.URL+"/api/units/example-prod-eu/widget-api/history", "admin-token")
	defer func() { _ = histResp.Body.Close() }()
	if histResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", histResp.StatusCode)
	}
	var events []syncEventView
	if err := json.NewDecoder(histResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 sync event, got %d: %+v", len(events), events)
	}
	if events[0].Trigger != "manual" || events[0].Actor != "admin@company.com" || events[0].Result != "succeeded" {
		t.Fatalf("unexpected sync event: %+v", events[0])
	}
}
