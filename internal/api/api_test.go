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
	"testing"

	"github.com/argorun/argorun/internal/cloudrun"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/rbac"
	"github.com/argorun/argorun/internal/reconcile"
	"github.com/argorun/argorun/internal/testutil"
)

const validDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"

type fakeAuth struct {
	// tokenToEmail maps a bearer token to the email it authenticates as;
	// any token not in this map is rejected.
	tokenToEmail map[string]string
}

func (f *fakeAuth) Verify(_ context.Context, token string) (string, error) {
	email, ok := f.tokenToEmail[token]
	if !ok {
		return "", errors.New("invalid token")
	}
	return email, nil
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

	h := &Handler{
		Auth: &fakeAuth{tokenToEmail: map[string]string{
			"admin-token":    "admin@company.com",
			"dev-only-token": "dev-only@company.com",
		}},
		RBAC:  rbacCfg,
		Units: StaticUnits{"widget-api/example-prod-eu": unit},
		Reconciler: &reconcile.Reconciler{
			DB:            db,
			ManagedFields: []string{"image"},
			Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
			CloudRun:      cr,
			Preconditions: fakePreconditions{},
		},
	}
	return h, cr
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
	h.Reconciler.DB = &failingDB{real: h.Reconciler.DB.(*sql.DB)}

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
	h.Reconciler.Preconditions = failingPreconditions{}
	h.Reconciler.Manifests = &fakeManifests{byApp: map[string][]byte{
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
