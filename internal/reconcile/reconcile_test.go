package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/diff"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/manifest"
	"github.com/runcd/runcd/internal/registry"
	"github.com/runcd/runcd/internal/testutil"
)

const validDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"

func autoSync() config.SyncPolicy {
	t := true
	return config.SyncPolicy{Auto: &t}
}

func manualSync() config.SyncPolicy {
	f := false
	return config.SyncPolicy{Auto: &f}
}

// observeSync is auto-sync enabled but shadowed by Observe — proves Observe
// takes precedence over Auto, not just over a manual sync's force.
func observeSync() config.SyncPolicy {
	t := true
	return config.SyncPolicy{Auto: &t, Observe: &t}
}

type fakeManifests struct {
	byApp map[string][]byte
}

func (f *fakeManifests) Get(_ context.Context, unit expander.SyncUnit) ([]byte, error) {
	raw, ok := f.byApp[unit.App]
	if !ok {
		return nil, fmt.Errorf("no manifest for app %q", unit.App)
	}
	return raw, nil
}

type fakeCloudRun struct {
	services            map[string]*cloudrun.LiveService // key: project/app
	jobs                map[string]*cloudrun.LiveJob     // key: project/app
	deployErr           map[string]error                 // key: project/app — forces DeployService/DeployJob to fail
	listServiceNamesErr map[string]error                 // key: project — forces ListServiceNames to fail
	getServiceCalls     atomic.Int64                     // counts GetService invocations, to prove a genuine re-fetch happens; RunOnce calls this concurrently
}

func (f *fakeCloudRun) GetService(_ context.Context, project, _, name, _ string) (*cloudrun.LiveService, error) {
	f.getServiceCalls.Add(1)
	live, ok := f.services[project+"/"+name]
	if !ok {
		return nil, cloudrun.ErrNotProvisioned
	}
	return live, nil
}

func (f *fakeCloudRun) GetJob(_ context.Context, project, _, name, _ string) (*cloudrun.LiveJob, error) {
	live, ok := f.jobs[project+"/"+name]
	if !ok {
		return nil, cloudrun.ErrNotProvisioned
	}
	return live, nil
}

// DeployService simulates a real deploy call: it REPLACES the fake's live
// state with a new object (as a real Cloud Run deploy would — a live read
// taken before this call can never observe it), rather than mutating the
// old one in place. That distinction is deliberate: it's what makes a test
// relying on a stale pre-deploy snapshot fail instead of accidentally pass.
func (f *fakeCloudRun) DeployService(_ context.Context, project, _, name string, desired cloudrun.ServiceState) error {
	key := project + "/" + name
	if err, ok := f.deployErr[key]; ok {
		return err
	}
	existing, ok := f.services[key]
	if !ok {
		return cloudrun.ErrNotProvisioned
	}
	// Mirrors the real GCPAdminClient: mutate the fetched live object in
	// place, only touching traffic/env when the caller actually manages
	// them — an unmanaged field must survive a deploy untouched, not get
	// reset to desired's zero value.
	next := cloudrun.ServiceState{
		ImageDigest:                  desired.ImageDigest,
		TrafficLatestRevisionPercent: existing.TrafficLatestRevisionPercent,
		EnvVars:                      existing.EnvVars,
		SecretRefs:                   existing.SecretRefs,
	}
	if desired.TrafficLatestRevisionPercent != nil {
		next.TrafficLatestRevisionPercent = desired.TrafficLatestRevisionPercent
	}
	if desired.EnvVars != nil || desired.SecretRefs != nil {
		next.EnvVars = desired.EnvVars
		next.SecretRefs = desired.SecretRefs
	}
	f.services[key] = &cloudrun.LiveService{
		ServiceState:                next,
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
		LatestRevisionCreating:      false,
	}
	return nil
}

func (f *fakeCloudRun) DeployJob(_ context.Context, project, _, name string, desired cloudrun.ServiceState) error {
	key := project + "/" + name
	if err, ok := f.deployErr[key]; ok {
		return err
	}
	job, ok := f.jobs[key]
	if !ok {
		return cloudrun.ErrNotProvisioned
	}
	job.ImageDigest = desired.ImageDigest
	job.HasExecutionForDesiredDigest = true
	job.LatestExecutionStatus = cloudrun.ExecutionSucceeded
	return nil
}

// ListServiceNames derives its answer from the same services map every
// other method here reads/writes, keyed "project/app" — no separate state
// to keep in sync. listServiceNamesErr (keyed by project) lets a test force
// one specific project's scan to fail without affecting any other.
func (f *fakeCloudRun) ListServiceNames(_ context.Context, project, _ string) ([]string, error) {
	if err, ok := f.listServiceNamesErr[project]; ok {
		return nil, err
	}
	var names []string
	prefix := project + "/"
	for key := range f.services {
		if after, ok := strings.CutPrefix(key, prefix); ok {
			names = append(names, after)
		}
	}
	return names, nil
}

type fakePreconditions struct{ topics map[string]bool }

func (f *fakePreconditions) TopicExists(_ context.Context, project, name string) (bool, error) {
	return f.topics[project+"/"+name], nil
}
func (f *fakePreconditions) SubscriptionExists(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

func serviceYAML() []byte {
	return []byte(fmt.Sprintf("image:\n  digest: %s\n", validDigest))
}

// liveServiceDigest reads back the fake's current image digest for
// project/app, failing the test clearly (rather than nil-panicking) if the
// entry doesn't exist.
func liveServiceDigest(t *testing.T, cr *fakeCloudRun, key string) string {
	t.Helper()
	live, ok := cr.services[key]
	if !ok {
		t.Fatalf("no fake Cloud Run service state for %q", key)
	}
	return live.ImageDigest
}

func TestRunOnce_SyncedWritesApplicationsRow(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us", Region: "us-central1"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 1 || results[0].Status != "Synced" || results[0].Health != "Healthy" {
		t.Fatalf("unexpected result: %+v", results)
	}

	var status, health, desired string
	err = db.QueryRowContext(context.Background(), `SELECT status, health, desired_image FROM applications WHERE name = $1 AND target_gcp_project = $2`,
		"widget-api", "example-prod-us").Scan(&status, &health, &desired)
	if err != nil {
		t.Fatalf("query applications: %v", err)
	}
	if status != "Synced" || health != "Healthy" || desired != validDigest {
		t.Fatalf("unexpected row: status=%s health=%s desired=%s", status, health, desired)
	}
}

func TestRunOnce_OutOfSyncOnDigestMismatch(t *testing.T) {
	db := testutil.NewPostgres(t)
	liveDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: liveDigest},
				HasRevisionForDesiredDigest: false,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "OutOfSync" || results[0].Health != "Missing" {
		t.Fatalf("unexpected result: %+v", results)
	}
}

func TestRunOnce_WorkerPoolSyncedIgnoringTraffic(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf("resourceType: workerPool\nimage:\n  digest: %s\n", validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image", "traffic"}, // traffic managed but must be ignored for workerPool
		Manifests:     &fakeManifests{byApp: map[string][]byte{"worker": manifestYAML}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-dev-01/worker": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "worker", Project: "example-dev-01"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" || results[0].Health != "Healthy" {
		t.Fatalf("unexpected result: %+v", results)
	}
}

func TestRunOnce_JobHealthyOnSucceededExecution(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf("resourceType: job\nimage:\n  digest: %s\n", validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"batch-job": manifestYAML}},
		CloudRun: &fakeCloudRun{jobs: map[string]*cloudrun.LiveJob{
			"example-dev-01/batch-job": {
				ServiceState:                 cloudrun.ServiceState{ImageDigest: validDigest},
				HasExecutionForDesiredDigest: true,
				LatestExecutionStatus:        cloudrun.ExecutionSucceeded,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "batch-job", Project: "example-dev-01"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" || results[0].Health != "Healthy" {
		t.Fatalf("unexpected result: %+v", results)
	}

	var status, health string
	if err := db.QueryRowContext(context.Background(), `SELECT status, health FROM applications WHERE name = $1 AND target_gcp_project = $2`,
		"batch-job", "example-dev-01").Scan(&status, &health); err != nil {
		t.Fatalf("query applications: %v", err)
	}
	if status != "Synced" || health != "Healthy" {
		t.Fatalf("unexpected row: status=%s health=%s", status, health)
	}
}

func TestRunOnce_JobNotYetExecutedIsMissing(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf("resourceType: job\nimage:\n  digest: %s\n", validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"batch-job": manifestYAML}},
		CloudRun:      &fakeCloudRun{jobs: map[string]*cloudrun.LiveJob{}}, // never run
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "batch-job", Project: "example-new-project"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != StatusMissing || results[0].Health != StatusMissing {
		t.Fatalf("expected Missing/Missing, got %+v", results[0])
	}
}

func TestRunOnce_InvalidManifestMarkedInvalid(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"bad-app": []byte("image:\n  digest: latest\n")}},
		CloudRun:      &fakeCloudRun{},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "bad-app", Project: "example-dev-01"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != StatusInvalid || results[0].Err == nil {
		t.Fatalf("expected Invalid with an error, got %+v", results[0])
	}
}

func TestRunOnce_MissingPreconditionMarkedInvalidButStillAssessesHealth(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestWithRequires := []byte(fmt.Sprintf(`
image:
  digest: %s
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestWithRequires}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{topics: map[string]bool{}}, // topic missing
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != StatusInvalid {
		t.Fatalf("expected Invalid status on missing precondition, got %+v", results[0])
	}
	if results[0].Health != "Healthy" {
		t.Fatalf("expected health still assessed from live state, got %+v", results[0])
	}
	if results[0].Err == nil {
		t.Fatal("expected precondition error to be surfaced")
	}
}

// TestRunOnce_PreconditionFailureNotOverwrittenByJobEnvValidation proves a
// real precondition-failure reason survives even when the same unit also
// hits the (separate) job+managed-env rejection, rather than being silently
// overwritten by that later, less specific check.
func TestRunOnce_PreconditionFailureNotOverwrittenByJobEnvValidation(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestWithRequires := []byte(fmt.Sprintf(`
resourceType: job
image:
  digest: %s
env:
  FOO: bar
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image", "env"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestWithRequires}},
		CloudRun:      &fakeCloudRun{},
		Preconditions: &fakePreconditions{topics: map[string]bool{}}, // topic missing
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "orders-events") {
		t.Fatalf("expected the precondition failure to survive as the reported error, got %+v", results[0].Err)
	}
}

func TestRunOnce_ResourceNotProvisionedMarkedMissing(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      &fakeCloudRun{services: map[string]*cloudrun.LiveService{}}, // nothing provisioned
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-new-project"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != StatusMissing || results[0].Health != StatusMissing {
		t.Fatalf("expected Missing/Missing, got %+v", results[0])
	}
}

func TestRunOnce_PreconditionFailureSurvivesUnprovisionedResource(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestWithRequires := []byte(fmt.Sprintf(`
image:
  digest: %s
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestWithRequires}},
		CloudRun:      &fakeCloudRun{services: map[string]*cloudrun.LiveService{}}, // not provisioned
		Preconditions: &fakePreconditions{topics: map[string]bool{}},               // and precondition missing
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-new-project"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// The precondition failure must not be masked by the "not provisioned"
	// branch running afterwards — Status should stay Invalid, not flip to
	// Missing, even though the resource is also unprovisioned.
	if results[0].Status != StatusInvalid {
		t.Fatalf("expected Status=Invalid (precondition failure takes precedence), got %+v", results[0])
	}
	if results[0].Health != StatusMissing {
		t.Fatalf("expected Health=Missing (resource genuinely not provisioned), got %+v", results[0])
	}
}

func TestRunOnce_ConcurrentUnitsAllPersisted(t *testing.T) {
	db := testutil.NewPostgres(t)
	byApp := map[string][]byte{}
	services := map[string]*cloudrun.LiveService{}
	var units []expander.SyncUnit
	for i := 0; i < 20; i++ {
		app := fmt.Sprintf("app-%d", i)
		byApp[app] = serviceYAML()
		services["example-dev-01/"+app] = &cloudrun.LiveService{
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		}
		units = append(units, expander.SyncUnit{App: app, Project: "example-dev-01"})
	}

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: byApp},
		CloudRun:      &fakeCloudRun{services: services},
		Preconditions: &fakePreconditions{},
		Workers:       4,
	}

	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 20 {
		t.Fatalf("expected 20 results, got %d", len(results))
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM applications WHERE target_gcp_project = 'example-dev-01'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 20 {
		t.Fatalf("expected 20 rows persisted, got %d", count)
	}
}

// flakyDB wraps a real *sql.DB and forces ExecContext/QueryRowContext to
// fail whenever the query's first arg matches failApp — used to prove one
// unit's write failure no longer cancels its siblings' in-flight work (the
// errgroup fix). *sql.Row is a concrete struct with no exported way to
// construct an erroring one directly, so the QueryRowContext override
// forces a real error by querying a table that doesn't exist.
type flakyDB struct {
	*sql.DB
	failApp string
}

// matchesFailApp only matches the applications-table write itself, not
// every DB call naming failApp as an argument — the per-unit sync_locks
// lock (acquireLock, ExecContext) also passes app as its first arg, and
// would otherwise be the one that fails instead of the applications
// upsert these tests are actually about.
func (f *flakyDB) matchesFailApp(query string, args []any) bool {
	if !strings.Contains(query, "applications") {
		return false
	}
	app, ok := args[0].(string)
	return ok && app == f.failApp
}

func (f *flakyDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if len(args) > 0 && f.matchesFailApp(query, args) {
		return nil, fmt.Errorf("simulated write failure for %s", args[0])
	}
	return f.DB.ExecContext(ctx, query, args...)
}

func (f *flakyDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if len(args) > 0 && f.matchesFailApp(query, args) {
		return f.DB.QueryRowContext(ctx, "SELECT 1 FROM __simulated_write_failure__")
	}
	return f.DB.QueryRowContext(ctx, query, args...)
}

func TestRunOnce_OneUnitWriteFailureDoesNotDiscardSiblingResults(t *testing.T) {
	realDB := testutil.NewPostgres(t)
	db := &flakyDB{DB: realDB, failApp: "bad-app"}

	byApp := map[string][]byte{}
	services := map[string]*cloudrun.LiveService{}
	var units []expander.SyncUnit
	for i := 0; i < 20; i++ {
		app := fmt.Sprintf("app-%d", i)
		if i == 7 {
			app = "bad-app"
		}
		byApp[app] = serviceYAML()
		services["example-dev-01/"+app] = &cloudrun.LiveService{
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		}
		units = append(units, expander.SyncUnit{App: app, Project: "example-dev-01"})
	}

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: byApp},
		CloudRun:      &fakeCloudRun{services: services},
		Preconditions: &fakePreconditions{},
		Workers:       4,
	}

	results, err := r.RunOnce(context.Background(), units)
	if err == nil {
		t.Fatal("expected RunOnce to report the one write failure")
	}
	if len(results) != 20 {
		t.Fatalf("expected 20 computed results regardless of the write failure, got %d", len(results))
	}
	for _, res := range results {
		if res.Status != "Synced" {
			t.Fatalf("every unit should have computed Synced (fake Cloud Run always matches), got %+v", res)
		}
		if res.Unit.App == "bad-app" {
			if res.Err == nil {
				t.Fatalf("expected bad-app's Result to carry its own upsert error, not just RunOnce's aggregate one, got %+v", res)
			}
		} else if res.Err != nil {
			t.Fatalf("expected %s's Result to have no error, got %v", res.Unit.App, res.Err)
		}
	}

	var count int
	if err := realDB.QueryRowContext(context.Background(), `SELECT count(*) FROM applications WHERE target_gcp_project = 'example-dev-01'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	// 19 good units must be persisted despite bad-app's write failing — a
	// context-cancellation bug would drop some/all of these to 0.
	if count != 19 {
		t.Fatalf("expected 19 of 20 rows persisted (bad-app's write legitimately failed), got %d", count)
	}
}

func TestRunOnce_DeploysOutOfSyncAutoUnit(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: oldDigest},
			HasRevisionForDesiredDigest: false,
			LatestRevisionReady:         true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" {
		t.Fatalf("expected Synced after auto-deploy, got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != validDigest {
		t.Fatalf("expected fake Cloud Run to have received the deploy, got digest %q", got)
	}
	// Proves the post-deploy check is a genuine second GetService call, not
	// a reuse of the pre-deploy snapshot: once before diffing, once again
	// after the deploy to confirm what actually landed.
	if got := cr.getServiceCalls.Load(); got != 2 {
		t.Fatalf("expected GetService called twice (pre-deploy check + post-deploy re-check), got %d", got)
	}

	var trigger, actor, fromImage, toImage, result string
	err = db.QueryRowContext(context.Background(), `
		SELECT trigger, actor, from_image, to_image, result FROM sync_events
		WHERE application = $1 AND target_gcp_project = $2`,
		"widget-api", "example-dev-01").Scan(&trigger, &actor, &fromImage, &toImage, &result)
	if err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if trigger != "auto" || actor != "runcd-controller" || fromImage != oldDigest || toImage != validDigest || result != "succeeded" {
		t.Fatalf("unexpected sync_events row: trigger=%s actor=%s from=%s to=%s result=%s", trigger, actor, fromImage, toImage, result)
	}
}

// TestRunOnce_ObserveModeSkipsAutoDeployButStillTracksDrift is shadow mode's
// core promise: the loop still renders/diffs/persists real drift every
// tick, it just never acts on it — even with auto-sync enabled.
func TestRunOnce_ObserveModeSkipsAutoDeployButStillTracksDrift(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: oldDigest},
			HasRevisionForDesiredDigest: false,
			LatestRevisionReady:         true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: observeSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "OutOfSync" {
		t.Fatalf("expected OutOfSync to still be tracked despite observe mode, got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != oldDigest {
		t.Fatalf("expected no deploy in observe mode, but live digest changed to %q", got)
	}
}

// TestRunOnce_DeniedSyncWindowBlocksAutoDeployButNotManualSync is the
// reconcile-level guarantee behind the roadmap's "auto-sync only allow/deny
// between these hours": a deny window blocks RunOnce's auto path, but a
// human's ManualSync (force=true) always bypasses it.
func TestRunOnce_DeniedSyncWindowBlocksAutoDeployButNotManualSync(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	// 2026-08-01 is a Saturday (UTC) — inside the deny window below.
	saturday := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
		Now:           func() time.Time { return saturday },
	}

	sync := autoSync()
	sync.SyncWindows = []config.SyncWindow{{Kind: config.SyncWindowDeny, Days: []string{"Sat", "Sun"}}}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-dev-01", Sync: sync}

	results, err := r.RunOnce(context.Background(), []expander.SyncUnit{unit})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != string(diff.OutOfSync) {
		t.Fatalf("expected the deny window to block the auto deploy, got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != oldDigest {
		t.Fatalf("expected no deploy while inside a deny window, got digest %q", got)
	}

	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Status != "Synced" {
		t.Fatalf("expected a manual (forced) sync to bypass the sync window, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != validDigest {
		t.Fatalf("expected manual sync to have deployed despite the deny window, got digest %q", got)
	}
}

func TestRunOnce_DeployFailureRecordedAndStatusStaysOutOfSync(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{
			services: map[string]*cloudrun.LiveService{
				"example-dev-01/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
			},
			deployErr: map[string]error{"example-dev-01/widget-api": fmt.Errorf("quota exceeded")},
		},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "OutOfSync" {
		t.Fatalf("expected Status to stay OutOfSync after a failed deploy, got %+v", results[0])
	}
	if results[0].Err == nil {
		t.Fatal("expected the deploy failure to be surfaced on Err")
	}

	var result string
	var errMsg sql.NullString
	err = db.QueryRowContext(context.Background(), `SELECT result, error FROM sync_events WHERE application = $1`, "widget-api").Scan(&result, &errMsg)
	if err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if result != "failed" || !errMsg.Valid || errMsg.String == "" {
		t.Fatalf("expected a failed sync_events row with an error message, got result=%s error=%v", result, errMsg)
	}
}

func TestRunOnce_ManualSyncNeverDeploys(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "OutOfSync" {
		t.Fatalf("expected gated unit to stay OutOfSync (no auto-deploy), got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != oldDigest {
		t.Fatal("expected no deploy to have been attempted for a manual-sync unit")
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sync_events rows for a gated unit, got %d", count)
	}
}

func TestRunOnce_FailedPreconditionNeverDeploysEvenIfAuto(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestWithRequires := []byte(fmt.Sprintf(`
image:
  digest: %s
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestWithRequires}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{topics: map[string]bool{}}, // missing
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != StatusInvalid {
		t.Fatalf("expected Invalid (precondition failed), got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != oldDigest {
		t.Fatal("expected no deploy attempt: a failed precondition must block deploy before any Cloud Run write (§5.10/§6)")
	}
}

// TestRunOnce_CrashMidSync_DeployAlreadyTookEffect models §8's required
// scenario where the leader crashes AFTER Cloud Run accepted the deploy but
// BEFORE the controller could write the outcome to sync_events — the row
// is left stuck at result=in_progress forever. The new leader must not
// trust that row; it re-derives truth from live Cloud Run state and must
// end up Synced without erroring or re-deploying.
func TestRunOnce_CrashMidSync_DeployAlreadyTookEffect(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Simulate the applications row already existing from a prior pass,
	// and the crashed leader's sync_events(in_progress) row left stuck.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO applications (name, target_gcp_project, desired_image, live_image, status, health, last_reconciled_at)
		VALUES ('widget-api', 'example-dev-01', $1, $2, 'OutOfSync', 'Degraded', now())`, validDigest, oldDigest); err != nil {
		t.Fatalf("seed applications: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_events (application, target_gcp_project, trigger, actor, from_image, to_image, started_at, result)
		VALUES ('widget-api', 'example-dev-01', 'auto', 'runcd-controller', $1, $2, now(), 'in_progress')`, oldDigest, validDigest); err != nil {
		t.Fatalf("seed crashed sync_events row: %v", err)
	}

	// The deploy Cloud Run itself already accepted — a real crash would
	// leave Cloud Run's state exactly like this, controller state or not.
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		},
	}}

	newLeader := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := newLeader.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" || results[0].Health != "Healthy" {
		t.Fatalf("expected the new leader to recompute Synced/Healthy from live state, got %+v", results[0])
	}

	// The crashed row must remain untouched — never read, never updated.
	var staleResult string
	err = db.QueryRowContext(context.Background(), `
		SELECT result FROM sync_events WHERE application = 'widget-api' AND result = 'in_progress'`).Scan(&staleResult)
	if err != nil {
		t.Fatalf("expected the stale in_progress row to still exist untouched: %v", err)
	}

	// No new sync_events row either: already-Synced means no deploy attempt.
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected only the one (stale) sync_events row, got %d", count)
	}
}

// TestRunOnce_CrashMidSync_DeployNeverTookEffect covers the other half of
// §8's scenario: the leader crashed before Cloud Run ever accepted the
// deploy (or before the call completed). The stale in_progress row must not
// block the new leader from safely retrying (NFR6: idempotent retry).
func TestRunOnce_CrashMidSync_DeployNeverTookEffect(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO applications (name, target_gcp_project, desired_image, live_image, status, health, last_reconciled_at)
		VALUES ('widget-api', 'example-dev-01', $1, $2, 'OutOfSync', 'Degraded', now())`, validDigest, oldDigest); err != nil {
		t.Fatalf("seed applications: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_events (application, target_gcp_project, trigger, actor, from_image, to_image, started_at, result)
		VALUES ('widget-api', 'example-dev-01', 'auto', 'runcd-controller', $1, $2, now(), 'in_progress')`, oldDigest, validDigest); err != nil {
		t.Fatalf("seed crashed sync_events row: %v", err)
	}

	// Cloud Run never actually received the deploy — still on the old digest.
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: oldDigest},
			HasRevisionForDesiredDigest: false,
			LatestRevisionReady:         true,
		},
	}}

	newLeader := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := newLeader.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" {
		t.Fatalf("expected the new leader to retry the deploy and succeed, got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != validDigest {
		t.Fatal("expected the new leader to have actually redeployed")
	}

	var succeededCount, staleCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api' AND result = 'succeeded'`).Scan(&succeededCount); err != nil {
		t.Fatalf("succeeded count query: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api' AND result = 'in_progress'`).Scan(&staleCount); err != nil {
		t.Fatalf("stale count query: %v", err)
	}
	if succeededCount != 1 {
		t.Fatalf("expected exactly one new succeeded sync_events row, got %d", succeededCount)
	}
	if staleCount != 1 {
		t.Fatalf("expected the original crashed row to remain, untouched, got %d", staleCount)
	}
}

// TestRunOnce_LockHeldByConcurrentManualSyncSkipsAutoDeployWithoutUpserting
// is RunOnce's own copy of the ErrSyncInProgress skip-the-upsert guard
// (mirrored from ManualSync's TestManualSync_LockContention_
// DoesNotUpsertStaleResult) — the actual race PROGRESS.md flags is a manual
// sync (any replica) racing the *auto-reconcile loop* for the same unit, not
// just two manual syncs, and RunOnce has its own separate guard in its
// g.Go closure that was never exercised on its own.
func TestRunOnce_LockHeldByConcurrentManualSyncSkipsAutoDeployWithoutUpserting(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Simulate a concurrent manual sync already holding this unit's lock.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_locks (application, target_gcp_project, holder, expires_at)
		VALUES ('widget-api', 'example-dev-01', 'concurrent-manual-sync', now() + interval '1 minute')`); err != nil {
		t.Fatalf("seed sync_locks: %v", err)
	}

	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-dev-01/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !errors.Is(results[0].Err, ErrSyncInProgress) {
		t.Fatalf("expected the auto pass to see ErrSyncInProgress against a lock the concurrent manual sync holds, got %+v", results[0])
	}
	if got := liveServiceDigest(t, cr, "example-dev-01/widget-api"); got != oldDigest {
		t.Fatal("expected no deploy attempt while the lock is held by the concurrent manual sync")
	}

	// The losing auto pass must skip the upsert entirely (same reasoning as
	// ManualSync's own guard): its pre-lock snapshot must never race the
	// eventual winner's write.
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&count); err != nil {
		t.Fatalf("query applications: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no applications row written by the losing auto pass, found %d", count)
	}
}

func TestManualSync_ForcesDeployRegardlessOfAutoFlag(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	// manualSync() = auto:false — a gated target that RunOnce would leave
	// OutOfSync forever without a human triggering it.
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Status != "Synced" {
		t.Fatalf("expected Synced after manual sync, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != validDigest {
		t.Fatalf("expected the deploy to have actually happened, got digest %q", got)
	}

	var trigger, actor string
	err = db.QueryRowContext(context.Background(), `SELECT trigger, actor FROM sync_events WHERE application = 'widget-api'`).Scan(&trigger, &actor)
	if err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if trigger != "manual" || actor != "alice@company.com" {
		t.Fatalf("expected trigger=manual actor=alice@company.com, got trigger=%s actor=%s", trigger, actor)
	}
}

// TestManualSync_NotifiesEvenWhenUpsertFails is ManualSync's counterpart
// to TestRunOnce_NotifiesEvenWhenUpsertFails — same bug, same fix, in the
// human-triggered sync path.
func TestManualSync_NotifiesEvenWhenUpsertFails(t *testing.T) {
	realDB := testutil.NewPostgres(t)
	db := &flakyDB{DB: realDB, failApp: "widget-api"}
	notifier := &fakeNotifier{}
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
		Notifier:      notifier,
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us"}
	if _, err := r.ManualSync(context.Background(), unit, "alice@company.com"); err == nil {
		t.Fatal("expected ManualSync to report the simulated write failure")
	}
	if len(notifier.evaluated) != 1 {
		t.Fatalf("expected the notifier to still be evaluated despite the upsert failure, got %d calls", len(notifier.evaluated))
	}
}

func TestManualSync_AlreadySyncedStillDeploysIdempotently(t *testing.T) {
	db := testutil.NewPostgres(t)
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Status != "Synced" {
		t.Fatalf("expected Synced, got %+v", res)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one sync_events row recording the (no-op) manual sync, got %d", count)
	}
}

// TestManualSync_ObserveMode_RejectsWithErrObserveMode proves observe mode
// blocks even a forced manual sync, not just auto-sync — the whole point of
// shadow mode (onboard a unit without granting runcd any authority to
// change it yet) would be defeated if a human could still force a deploy.
func TestManualSync_ObserveMode_RejectsWithErrObserveMode(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: observeSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if !errors.Is(res.Err, ErrObserveMode) {
		t.Fatalf("expected ErrObserveMode, got %+v", res.Err)
	}
	if res.Status != "OutOfSync" {
		t.Fatalf("expected drift to still be reported as OutOfSync, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != oldDigest {
		t.Fatalf("expected no deploy in observe mode, but live digest changed to %q", got)
	}
}

func TestManualSync_NeverDeploysOnFailedPrecondition(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestWithRequires := []byte(fmt.Sprintf(`
image:
  digest: %s
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestWithRequires}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{topics: map[string]bool{}}, // missing
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Status != StatusInvalid {
		t.Fatalf("expected Invalid, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != oldDigest {
		t.Fatal("expected no deploy attempt for a failed precondition, even on a forced manual sync")
	}
}

// failPostDeployFetchCloudRun wraps fakeCloudRun so GetService succeeds for
// every call except the second — the pre-deploy fetch (call 1) must
// succeed for the unit to even be diffed OutOfSync and attempt a deploy;
// only the post-deploy re-check (call 2) fails, modeling a real transient
// GCP read error (or eventually-consistent propagation lag) right after a
// deploy that itself succeeded.
type failPostDeployFetchCloudRun struct {
	*fakeCloudRun
	calls atomic.Int64
	err   error
}

func (f *failPostDeployFetchCloudRun) GetService(ctx context.Context, project, region, name, digest string) (*cloudrun.LiveService, error) {
	if f.calls.Add(1) == 2 {
		return nil, f.err
	}
	return f.fakeCloudRun.GetService(ctx, project, region, name, digest)
}

// TestRunOnce_PostDeployFetchFailure_SyncEventSucceedsButStatusStaysPreDeploy
// exercises deploySyncUnit's documented "deploy succeeded, confirmation
// didn't" branch (reconcile.go's postLive fetch-error path): the deploy
// call itself is not in doubt, so sync_events must still read "succeeded"
// (with no error — writing a real deploy as "failed" here would be a false
// alarm), but res.Status/Health must stay exactly what applyLiveState
// computed before the deploy, not be guessed as Synced, since nothing
// actually confirmed the new state landed. The lock must still be
// released, and the next poll's fresh fetch is the thing that actually
// confirms convergence.
func TestRunOnce_PostDeployFetchFailure_SyncEventSucceedsButStatusStaysPreDeploy(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	boom := errors.New("transient GCP read error")
	cr := &failPostDeployFetchCloudRun{
		fakeCloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-dev-01/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: oldDigest},
				HasRevisionForDesiredDigest: false,
				LatestRevisionReady:         true,
			},
		}},
		err: boom,
	}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != string(diff.OutOfSync) {
		t.Fatalf("expected Status to stay at its pre-deploy value (OutOfSync) when the post-deploy confirmation fails, got %+v", results[0])
	}
	if results[0].Err == nil || !errors.Is(results[0].Err, boom) {
		t.Fatalf("expected the post-deploy fetch failure to be surfaced on Err, got %+v", results[0].Err)
	}
	// The deploy itself really did happen — Cloud Run's own state reflects
	// the new digest even though this pass couldn't confirm it.
	if got := liveServiceDigest(t, cr.fakeCloudRun, "example-dev-01/widget-api"); got != validDigest {
		t.Fatalf("expected the deploy to have actually taken effect, got digest %q", got)
	}

	var result string
	var errMsg sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT result, error FROM sync_events WHERE application = 'widget-api' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&result, &errMsg); err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if result != "succeeded" {
		t.Fatalf("expected sync_events to read succeeded (the deploy call itself succeeded), got %q", result)
	}
	if errMsg.Valid && errMsg.String != "" {
		t.Fatalf("expected no error recorded on a sync_events row for a deploy that actually succeeded, got %q", errMsg.String)
	}

	var lockCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_locks WHERE application = 'widget-api'`).Scan(&lockCount); err != nil {
		t.Fatalf("count sync_locks: %v", err)
	}
	if lockCount != 0 {
		t.Fatalf("expected the lock to be released despite the post-deploy fetch failure, got %d rows", lockCount)
	}
}

func TestUpsert_StatusSinceResetsOnlyWhenStatusChanges(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	first, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Status:       "OutOfSync",
		Health:       "Degraded",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.StatusSince.IsZero() || first.HealthSince.IsZero() {
		t.Fatalf("expected StatusSince/HealthSince to be set, got %+v", first)
	}

	// Same status, different health: StatusSince should NOT move, HealthSince should.
	time.Sleep(10 * time.Millisecond)
	second, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Status:       "OutOfSync",
		Health:       "Missing",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !second.StatusSince.Equal(first.StatusSince) {
		t.Fatalf("expected StatusSince unchanged (status didn't change), first=%v second=%v", first.StatusSince, second.StatusSince)
	}
	if !second.HealthSince.After(first.HealthSince) {
		t.Fatalf("expected HealthSince to advance (health changed), first=%v second=%v", first.HealthSince, second.HealthSince)
	}
}

// TestUpsert_EmptyDesiredImageDoesNotOverwritePreviousValue
// regression-tests a data-loss bug: a transient manifest-fetch failure
// leaves Result.DesiredImage empty for that pass (reconcile() never got as
// far as setting it), and the upsert used to blindly overwrite the
// previously-recorded desired_image with that blank value — discarding a
// perfectly good prior value purely because of an ephemeral fetch error.
func TestUpsert_EmptyDesiredImageDoesNotOverwritePreviousValue(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	first, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Status:       "Synced",
		Health:       "Healthy",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.DesiredImage != validDigest {
		t.Fatalf("expected DesiredImage=%s after first upsert, got %+v", validDigest, first)
	}

	// Simulate a pass whose manifest fetch failed: DesiredImage never got set.
	_, err = r.upsert(context.Background(), Result{
		Unit:   expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		Status: "Invalid",
		Health: "Invalid",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var stored string
	err = db.QueryRowContext(context.Background(), `SELECT desired_image FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-dev-01'`).Scan(&stored)
	if err != nil {
		t.Fatalf("query desired_image: %v", err)
	}
	if stored != validDigest {
		t.Fatalf("expected desired_image to stay %q despite the empty-DesiredImage upsert, got %q", validDigest, stored)
	}
}

// TestUpsert_EmptyLiveImageDoesNotOverwritePreviousValue is
// live_image's counterpart to the desired_image test above — same
// exposure, same fix: a transient live-state fetch failure leaves
// res.LiveImage empty, and nullIfEmpty("") turns that into SQL NULL, which
// must not clobber a previously-observed live_image.
func TestUpsert_EmptyLiveImageDoesNotOverwritePreviousValue(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	first, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		LiveImage:    validDigest,
		Status:       "Synced",
		Health:       "Healthy",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.DesiredImage != validDigest {
		t.Fatalf("expected DesiredImage=%s after first upsert, got %+v", validDigest, first)
	}

	// Simulate a pass whose live-state fetch transiently failed: LiveImage
	// never got set (applyLiveState returns before reaching that line).
	_, err = r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Status:       "Invalid",
		Health:       "Invalid",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var stored string
	err = db.QueryRowContext(context.Background(), `SELECT live_image FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-dev-01'`).Scan(&stored)
	if err != nil {
		t.Fatalf("query live_image: %v", err)
	}
	if stored != validDigest {
		t.Fatalf("expected live_image to stay %q despite the empty-LiveImage upsert, got %q", validDigest, stored)
	}
}

// TestUpsert_PersistsTrackVersionRepository checks the three new columns
// round-trip through upsert alongside desired_image.
func TestUpsert_PersistsTrackVersionRepository(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	_, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Track:        "stable",
		Repository:   "us-central1-docker.pkg.dev/proj/repo/image",
		Status:       "Synced",
		Health:       "Healthy",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var track, version, repository string
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(track, ''), COALESCE(version, ''), COALESCE(repository, '') FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&track, &version, &repository)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if track != "stable" || version != "" || repository != "us-central1-docker.pkg.dev/proj/repo/image" {
		t.Fatalf("got track=%q version=%q repository=%q", track, version, repository)
	}
}

// TestUpsert_EmptyDesiredImagePreservesTrackVersionRepository mirrors
// TestUpsert_EmptyDesiredImageDoesNotOverwritePreviousValue: a transient
// manifest-fetch failure must not blank out a previously-recorded
// track/version/repository either, even though (unlike desired_image) an
// empty string is also a legitimate, permanent value for these three.
func TestUpsert_EmptyDesiredImagePreservesTrackVersionRepository(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	_, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		DesiredImage: validDigest,
		Version:      "1.2",
		Repository:   "us-central1-docker.pkg.dev/proj/repo/image",
		Status:       "Synced",
		Health:       "Healthy",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Simulate a pass whose manifest fetch failed: DesiredImage (and
	// therefore Track/Version/Repository, set from the same manifest)
	// never got set.
	_, err = r.upsert(context.Background(), Result{
		Unit:   expander.SyncUnit{App: "widget-api", Project: "example-dev-01"},
		Status: "Invalid",
		Health: "Invalid",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var version, repository string
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(version, ''), COALESCE(repository, '') FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&version, &repository)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if version != "1.2" || repository != "us-central1-docker.pkg.dev/proj/repo/image" {
		t.Fatalf("expected version/repository to survive the empty-DesiredImage upsert, got version=%q repository=%q", version, repository)
	}
}

// TestUpsert_PersistsResourceType checks resource_type round-trips through
// upsert, and survives a transient manifest-fetch failure the same way
// track/version/repository do (keyed off desired_image's own emptiness,
// since "job"/"workerPool"/"service" is never itself a legitimate empty
// value — manifest.Parse always defaults it).
func TestUpsert_PersistsResourceType(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{DB: db}

	_, err := r.upsert(context.Background(), Result{
		Unit:         expander.SyncUnit{App: "sweep-job", Project: "example-dev-01"},
		DesiredImage: validDigest,
		ResourceType: "job",
		Status:       "Synced",
		Health:       "Healthy",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var resourceType string
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(resource_type, '') FROM applications WHERE name = 'sweep-job' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&resourceType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if resourceType != "job" {
		t.Fatalf("got resource_type=%q, want job", resourceType)
	}

	// Simulate a pass whose manifest fetch failed: ResourceType never got set.
	_, err = r.upsert(context.Background(), Result{
		Unit:   expander.SyncUnit{App: "sweep-job", Project: "example-dev-01"},
		Status: "Invalid",
		Health: "Invalid",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(resource_type, '') FROM applications WHERE name = 'sweep-job' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&resourceType)
	if err != nil {
		t.Fatalf("query after failed fetch: %v", err)
	}
	if resourceType != "job" {
		t.Fatalf("expected resource_type to survive the empty-DesiredImage upsert, got %q", resourceType)
	}
}

type fakeNotifier struct {
	evaluated []Result
}

func (f *fakeNotifier) Evaluate(_ context.Context, res Result) error {
	f.evaluated = append(f.evaluated, res)
	return nil
}

func TestRunOnce_InvokesNotifierWithFinalResult(t *testing.T) {
	db := testutil.NewPostgres(t)
	notifier := &fakeNotifier{}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
		Notifier:      notifier,
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us"}}
	if _, err := r.RunOnce(context.Background(), units); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(notifier.evaluated) != 1 {
		t.Fatalf("expected the notifier to be evaluated once, got %d", len(notifier.evaluated))
	}
	got := notifier.evaluated[0]
	if got.Status != "Synced" || got.StatusSince.IsZero() {
		t.Fatalf("expected the notifier to see the final, persisted result, got %+v", got)
	}
}

// erroringNotifier proves a notification failure never fails the pass.
type erroringNotifier struct{}

func (erroringNotifier) Evaluate(_ context.Context, _ Result) error {
	return fmt.Errorf("slack is down")
}

// TestRunOnce_NotifiesEvenWhenUpsertFails regression-tests a bug where
// notify() was skipped entirely whenever the same-pass upsert errored —
// meaning a genuine deploy failure (already recorded to sync_events) could
// go completely unalerted if the final applications-table write also hit
// a transient error.
func TestRunOnce_NotifiesEvenWhenUpsertFails(t *testing.T) {
	realDB := testutil.NewPostgres(t)
	db := &flakyDB{DB: realDB, failApp: "bad-app"}
	notifier := &fakeNotifier{}

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"bad-app": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/bad-app": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
		Notifier:      notifier,
	}

	units := []expander.SyncUnit{{App: "bad-app", Project: "example-prod-us"}}
	if _, err := r.RunOnce(context.Background(), units); err == nil {
		t.Fatal("expected RunOnce to report the simulated write failure")
	}
	if len(notifier.evaluated) != 1 {
		t.Fatalf("expected the notifier to still be evaluated despite the upsert failure, got %d calls", len(notifier.evaluated))
	}
}

func TestRunOnce_NotifierFailureDoesNotFailThePass(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
		Notifier:      erroringNotifier{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("expected RunOnce to succeed despite the notifier failing, got %v", err)
	}
	if results[0].Status != "Synced" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}

// blockingCloudRun wraps fakeCloudRun so a test can pin down exactly when a
// DeployService call is in flight — the window a concurrent second attempt
// needs to overlap with, to test the lock deterministically instead of via
// a hopeful sleep.
type blockingCloudRun struct {
	*fakeCloudRun
	started chan struct{}
	proceed chan struct{}
}

func (b *blockingCloudRun) DeployService(ctx context.Context, project, region, name string, desired cloudrun.ServiceState) error {
	close(b.started)
	<-b.proceed
	return b.fakeCloudRun.DeployService(ctx, project, region, name, desired)
}

// TestManualSync_ConcurrentAttemptsOnSameUnitOneWins is the regression test
// for the race PROGRESS.md flagged: a manual sync (any replica) and the
// auto-reconcile loop — or, as here, two manual syncs — deploying the same
// unit at once. Without the sync_locks-backed lock in deploySyncUnit, both
// would call DeployService concurrently.
func TestManualSync_ConcurrentAttemptsOnSameUnitOneWins(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &blockingCloudRun{
		fakeCloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
		}},
		started: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}

	type outcome struct {
		res Result
		err error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		res, err := r.ManualSync(context.Background(), unit, "first@company.com")
		firstDone <- outcome{res, err}
	}()

	select {
	case <-cr.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first ManualSync never reached DeployService")
	}

	// The first attempt is holding the lock, blocked inside DeployService —
	// this second attempt must be rejected immediately, not block waiting
	// for the first to finish.
	secondRes, err := r.ManualSync(context.Background(), unit, "second@company.com")
	if err != nil {
		t.Fatalf("second ManualSync: %v", err)
	}
	if !errors.Is(secondRes.Err, ErrSyncInProgress) {
		t.Fatalf("expected second concurrent attempt to get ErrSyncInProgress, got %+v", secondRes)
	}

	close(cr.proceed)
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first ManualSync: %v", first.err)
	}
	if first.res.Status != "Synced" {
		t.Fatalf("expected the first (unblocked) attempt to have deployed successfully, got %+v", first.res)
	}
	if got := liveServiceDigest(t, cr.fakeCloudRun, "example-prod-us/widget-api"); got != validDigest {
		t.Fatalf("expected the first attempt's deploy to have actually happened, got digest %q", got)
	}
}

// TestManualSync_LockReleasedAfterAttemptCompletes checks the lock doesn't
// outlive its attempt — a second, later (non-concurrent) sync of the same
// unit must succeed once the first has actually finished.
func TestManualSync_LockReleasedAfterAttemptCompletes(t *testing.T) {
	db := testutil.NewPostgres(t)
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}

	if _, err := r.ManualSync(context.Background(), unit, "first@company.com"); err != nil {
		t.Fatalf("first ManualSync: %v", err)
	}
	res, err := r.ManualSync(context.Background(), unit, "second@company.com")
	if err != nil {
		t.Fatalf("second ManualSync: %v", err)
	}
	if errors.Is(res.Err, ErrSyncInProgress) {
		t.Fatalf("expected the lock to be released after the first attempt completed, got %+v", res)
	}
}

// TestManualSync_LockContention_DoesNotUpsertStaleResult is the regression
// test for the race described on ManualSync's ErrSyncInProgress guard: a
// losing attempt must skip the upsert entirely, not just the deploy, or its
// stale pre-lock result can clobber the winner's write.
func TestManualSync_LockContention_DoesNotUpsertStaleResult(t *testing.T) {
	db := testutil.NewPostgres(t)
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}

	// Simulate another attempt already holding this unit's lock.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_locks (application, target_gcp_project, holder, expires_at)
		VALUES ('widget-api', 'example-prod-us', 'other-attempt', now() + interval '1 minute')`); err != nil {
		t.Fatalf("seed sync_locks: %v", err)
	}

	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, LatestRevisionReady: true},
		}},
		Preconditions: &fakePreconditions{},
	}

	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if !errors.Is(res.Err, ErrSyncInProgress) {
		t.Fatalf("expected ErrSyncInProgress, got %+v", res)
	}

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM applications WHERE name = 'widget-api' AND target_gcp_project = 'example-prod-us'`,
	).Scan(&count); err != nil {
		t.Fatalf("query applications: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no applications row written by the losing attempt, found %d", count)
	}
}

// TestManualSync_ExpiredLockIsReclaimed proves acquireLock's
// "WHERE sync_locks.expires_at < now()" clause actually lets a later
// attempt take over an expired lock row left behind by a holder that
// crashed mid-deploy, rather than treating any existing row as permanently
// held — that TTL is the whole point of using a TTL-based lock instead of a
// session-held one (see migrations/00003_sync_lock.sql).
func TestManualSync_ExpiredLockIsReclaimed(t *testing.T) {
	db := testutil.NewPostgres(t)
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}

	// Simulate a crashed holder's lock row whose TTL has already elapsed.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sync_locks (application, target_gcp_project, holder, expires_at)
		VALUES ('widget-api', 'example-prod-us', 'crashed-holder', now() - interval '1 minute')`); err != nil {
		t.Fatalf("seed expired sync_locks row: %v", err)
	}

	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if errors.Is(res.Err, ErrSyncInProgress) {
		t.Fatalf("expected the expired lock to be reclaimed, not treated as still held, got %+v", res)
	}
	if res.Status != "Synced" {
		t.Fatalf("expected the reclaiming attempt to have deployed, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != validDigest {
		t.Fatalf("expected the deploy to have actually happened after reclaiming the lock, got digest %q", got)
	}
}

// acquireLockFailDB forces ExecContext against sync_locks to fail outright
// (a real DB error, not just "the row is already held") — acquireLock must
// surface this distinctly from ErrSyncInProgress rather than silently
// treating a connection error as lock contention.
type acquireLockFailDB struct {
	*sql.DB
	err error
}

func (f *acquireLockFailDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "sync_locks") {
		return nil, f.err
	}
	return f.DB.ExecContext(ctx, query, args...)
}

// TestManualSync_AcquireLockDBErrorSurfacedDistinctlyFromContention proves a
// genuine DB error acquiring the sync_locks row (network blip, pool
// exhaustion) is reported as its own wrapped error, not misread as
// ErrSyncInProgress — a caller distinguishing the two (e.g. to decide
// whether retrying immediately is safe) needs that distinction to be real.
func TestManualSync_AcquireLockDBErrorSurfacedDistinctlyFromContention(t *testing.T) {
	realDB := testutil.NewPostgres(t)
	boom := errors.New("simulated connection failure")
	db := &acquireLockFailDB{DB: realDB, err: boom}

	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if errors.Is(res.Err, ErrSyncInProgress) {
		t.Fatalf("a real DB error acquiring the lock must not be reported as ErrSyncInProgress, got %+v", res)
	}
	if res.Err == nil || !errors.Is(res.Err, boom) {
		t.Fatalf("expected the underlying DB error wrapped on Result.Err, got %+v", res.Err)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("expected no deploy attempt when the lock couldn't even be acquired")
	}
}

// TestManualSync_PreDeployUpsertFailurePreventsDeployAndSyncEvent exercises
// deploySyncUnit's own "upsert applications row before deploy" write (the
// one guaranteeing a brand-new unit's sync_events FK target exists) failing
// — must abort before ever calling deploy() or writing sync_events, and
// must still release the lock it already acquired rather than leaving it
// held for the rest of lockTTL.
func TestManualSync_PreDeployUpsertFailurePreventsDeployAndSyncEvent(t *testing.T) {
	realDB := testutil.NewPostgres(t)
	db := &flakyDB{DB: realDB, failApp: "widget-api"}
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	// ManualSync's own top-level upsert (after reconcile returns) also hits
	// the same flaky DB for this app, so ManualSync itself is expected to
	// return an error too — the assertion below on res.Err is what actually
	// proves deploySyncUnit's *earlier*, pre-deploy upsert is what aborted
	// the deploy, not just the later one.
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err == nil {
		t.Fatal("expected ManualSync to report the simulated write failure")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "upsert applications row before deploy") {
		t.Fatalf("expected the pre-deploy upsert failure to be surfaced distinctly, got %+v", res.Err)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != oldDigest {
		t.Fatal("expected no deploy attempt when the pre-deploy upsert itself failed")
	}

	var eventCount int
	if err := realDB.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events WHERE application = 'widget-api'`).Scan(&eventCount); err != nil {
		t.Fatalf("count sync_events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected no sync_events row written when the pre-deploy upsert failed, got %d", eventCount)
	}

	// The lock this attempt acquired must still be released (its defer runs
	// regardless of the early return) — otherwise a later attempt would be
	// stuck waiting out the rest of lockTTL for no reason.
	var lockCount int
	if err := realDB.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_locks WHERE application = 'widget-api'`).Scan(&lockCount); err != nil {
		t.Fatalf("count sync_locks: %v", err)
	}
	if lockCount != 0 {
		t.Fatalf("expected the lock to be released despite the pre-deploy upsert failing, got %d rows", lockCount)
	}
}

// cancelOnDeployCloudRun wraps fakeCloudRun so a test can simulate
// leadership loss happening precisely during the deploy call: Cloud Run
// itself accepts the deploy (a real deploy call, once submitted, isn't
// something losing leadership can retroactively undo), but the calling
// process's own context is cancelled before the call returns — modeling the
// exact race deploySyncUnit's crash-safety contract is built around.
type cancelOnDeployCloudRun struct {
	*fakeCloudRun
	cancel context.CancelFunc
}

func (c *cancelOnDeployCloudRun) DeployService(ctx context.Context, project, region, name string, desired cloudrun.ServiceState) error {
	c.cancel()
	return c.fakeCloudRun.DeployService(ctx, project, region, name, desired)
}

// TestRunOnce_LeadershipLostMidDeploy_SyncEventStillFinalized is the direct
// test of the crash-safety contract deploySyncUnit/updateSyncEvent/
// releaseLock document via context.WithoutCancel: losing leadership (the
// caller's ctx cancelled) in the exact window between Cloud Run accepting a
// deploy and this pass finishing its own bookkeeping must not leave the
// sync_events row stuck at "in_progress" forever, and must not leave the
// per-unit lock held for the rest of lockTTL — both writes are deliberately
// detached from ctx's cancellation for exactly this reason. The final
// applications-table upsert back in RunOnce is NOT detached, so it's
// expected (and asserted) to fail here — next tick's fresh reconcile pass
// re-derives the correct status from live state regardless.
func TestRunOnce_LeadershipLostMidDeploy_SyncEventStillFinalized(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cr := &cancelOnDeployCloudRun{
		fakeCloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-dev-01/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: oldDigest},
				HasRevisionForDesiredDigest: false,
				LatestRevisionReady:         true,
			},
		}},
		cancel: cancel,
	}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-dev-01", Sync: autoSync()}}
	results, err := r.RunOnce(ctx, units)
	// The final applications upsert in RunOnce uses the same (now-cancelled)
	// ctx, so it's expected to fail — this is what proves the assertion
	// below isn't vacuous (deploySyncUnit's own writes really did have to
	// detach to survive).
	if err == nil {
		t.Fatal("expected RunOnce's own final upsert to fail against the cancelled context")
	}
	if results[0].Err == nil {
		t.Fatalf("expected the cancelled-context final upsert failure to be surfaced, got %+v", results[0])
	}

	// The deploy itself went through (Cloud Run "accepted" it before ctx was
	// cancelled) — the fake's post-deploy state must reflect that.
	if got := liveServiceDigest(t, cr.fakeCloudRun, "example-dev-01/widget-api"); got != validDigest {
		t.Fatalf("expected the deploy to have taken effect despite the mid-deploy cancellation, got digest %q", got)
	}

	// The core assertion: sync_events must be finalized (not stuck at
	// in_progress) even though the ctx that deploySyncUnit ran under was
	// cancelled mid-flight — updateSyncEvent's context.WithoutCancel must
	// have actually protected this write.
	var result string
	if err := db.QueryRowContext(context.Background(), `
		SELECT result FROM sync_events WHERE application = 'widget-api' AND target_gcp_project = 'example-dev-01'`,
	).Scan(&result); err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if result == "in_progress" {
		t.Fatal("expected sync_events to be finalized despite the mid-deploy context cancellation, found it stuck in_progress")
	}

	// releaseLock must likewise have survived the cancellation — otherwise
	// this lock sits held for the rest of lockTTL for no reason.
	var lockCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_locks WHERE application = 'widget-api'`).Scan(&lockCount); err != nil {
		t.Fatalf("count sync_locks: %v", err)
	}
	if lockCount != 0 {
		t.Fatalf("expected the lock to be released despite the mid-deploy context cancellation, got %d rows", lockCount)
	}
}

// TestDryRun_ComputesResultWithoutDeployingOrPersisting guards against the
// exact trap dry-run implementations fall into: it must short-circuit
// before deploySyncUnit, not just skip the deploy() call inside it — a dry
// run that reached deploySyncUnit would still take the sync_locks row,
// upsert applications, and write a sync_events row, none of which a preview
// should ever do.
func TestDryRun_ComputesResultWithoutDeployingOrPersisting(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res := r.DryRun(context.Background(), unit)

	if res.Status != string(diff.OutOfSync) {
		t.Fatalf("expected the preview to report OutOfSync, got %+v", res)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != oldDigest {
		t.Fatalf("dry run must never deploy, but live digest changed to %q", got)
	}

	var appCount, lockCount, eventCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM applications`).Scan(&appCount); err != nil {
		t.Fatalf("query applications: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_locks`).Scan(&lockCount); err != nil {
		t.Fatalf("query sync_locks: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&eventCount); err != nil {
		t.Fatalf("query sync_events: %v", err)
	}
	if appCount != 0 || lockCount != 0 || eventCount != 0 {
		t.Fatalf("expected no persisted rows from a dry run, got applications=%d sync_locks=%d sync_events=%d", appCount, lockCount, eventCount)
	}
}

// TestDryRun_ObserveModeSurfacesObserving proves DryRun tells a caller when
// a real sync would be blocked by shadow mode — without this, a preview of
// an observing unit looks identical to any other non-auto unit's preview,
// hiding that even a forced manual sync would go nowhere.
func TestDryRun_ObserveModeSurfacesObserving(t *testing.T) {
	db := testutil.NewPostgres(t)
	oldDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: oldDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: observeSync()}
	res := r.DryRun(context.Background(), unit)

	if res.Status != string(diff.OutOfSync) {
		t.Fatalf("expected the preview to still report real drift, got %+v", res)
	}
	if !res.Observing {
		t.Fatalf("expected Observing=true so the caller knows a real sync would be blocked, got %+v", res)
	}
}

// TestDryRun_ObservingSetEvenWhenUnitIsMissing is the regression test for a
// real gap: Observing was only ever set on applyLiveState's success path, so
// an observing unit that's Missing (not yet provisioned) reported
// Observing=false — exactly the case an operator most needs the real value
// for, since it's another reason nothing has deployed yet.
func TestDryRun_ObservingSetEvenWhenUnitIsMissing(t *testing.T) {
	db := testutil.NewPostgres(t)
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: observeSync()}
	res := r.DryRun(context.Background(), unit)

	if res.Status != StatusMissing {
		t.Fatalf("expected Missing status for an unprovisioned unit, got %+v", res)
	}
	if !res.Observing {
		t.Fatalf("expected Observing=true even on the Missing/fetch-error path, got %+v", res)
	}
}

// TestDryRun_DoesNotBlockAConcurrentRealSync is the concrete version of "no
// sync_locks row": a dry run running against a unit a real manual sync is
// about to touch must not contend for that unit's lock at all.
func TestDryRun_DoesNotBlockAConcurrentRealSync(t *testing.T) {
	db := testutil.NewPostgres(t)
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {
			ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
			HasRevisionForDesiredDigest: true,
			LatestRevisionReady:         true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}

	r.DryRun(context.Background(), unit)

	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if errors.Is(res.Err, ErrSyncInProgress) {
		t.Fatalf("a prior dry run must not leave a lock behind, got %+v", res)
	}
}

// TestRunOnce_IgnoreFieldsExcludesFieldFromDiff is the resource-exclusions
// guarantee: a unit's ignoreFields removes a field from the diff entirely,
// even though it's still in the reconciler's global ManagedFields — a
// traffic mismatch that would otherwise be OutOfSync is invisible once
// traffic is ignored for this one app.
func TestRunOnce_IgnoreFieldsExcludesFieldFromDiff(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf("image:\n  digest: %s\ntraffic:\n  latestRevisionPercent: 100\n", validDigest))
	liveTraffic := 50
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image", "traffic"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestYAML}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest, TrafficLatestRevisionPercent: &liveTraffic},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", IgnoreFields: []string{"traffic"}}
	results, err := r.RunOnce(context.Background(), []expander.SyncUnit{unit})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" {
		t.Fatalf("expected Synced once traffic is ignored for this app despite a real traffic mismatch (manifest wants 100%%, live is 50%%), got %+v", results[0])
	}
}

// TestRunOnce_IgnorePreconditionsSkipsNamedPrecondition is the precondition
// half of resource exclusions: an app-level ignorePreconditions entry
// bypasses one specific missing precondition without disabling the check
// for anything else.
func TestRunOnce_IgnorePreconditionsSkipsNamedPrecondition(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf(`
image:
  digest: %s
requires:
  - type: pubsubTopic
    name: orders-events
`, validDigest))
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestYAML}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: validDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{topics: map[string]bool{}}, // orders-events missing
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", IgnorePreconditions: []string{"pubsubTopic:orders-events"}}
	results, err := r.RunOnce(context.Background(), []expander.SyncUnit{unit})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if results[0].Status != "Synced" {
		t.Fatalf("expected the missing-but-ignored precondition to not block sync, got %+v", results[0])
	}
	if results[0].Err != nil {
		t.Fatalf("expected no precondition error once ignored, got %v", results[0].Err)
	}
}

func TestEffectiveManagedFields(t *testing.T) {
	got := effectiveManagedFields([]string{"image", "traffic"}, []string{"traffic"})
	if len(got) != 1 || got[0] != "image" {
		t.Fatalf("expected traffic subtracted, got %+v", got)
	}

	got = effectiveManagedFields([]string{"image", "traffic"}, nil)
	if len(got) != 2 {
		t.Fatalf("expected no change with no ignore list, got %+v", got)
	}
}

func TestFilterPreconditions(t *testing.T) {
	requires := []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
		{Type: "pubsubTopic", Name: "shipping-events"},
	}
	got := filterPreconditions(requires, []string{"pubsubTopic:orders-events"})
	if len(got) != 1 || got[0].Name != "shipping-events" {
		t.Fatalf("expected only the named precondition removed, got %+v", got)
	}
}

// TestManualSync_IgnoreFieldsImage_ForcedSyncDoesNotChangeLiveImage is the
// regression test for a real bug: force (manual sync) always calls deploy
// regardless of diff status, so a unit with ignoreFields: [image] must
// still never actually change the live image — the deploy payload has to
// substitute the live digest for the ignored field, not the manifest's.
func TestManualSync_IgnoreFieldsImage_ForcedSyncDoesNotChangeLiveImage(t *testing.T) {
	db := testutil.NewPostgres(t)
	liveDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: liveDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}}, // manifest wants validDigest
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync(), IgnoreFields: []string{"image"}}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if got := liveServiceDigest(t, cr, "example-prod-us/widget-api"); got != liveDigest {
		t.Fatalf("ignoreFields: [image] must make a forced sync a no-op for image, but live digest changed to %q (manifest wanted %q)", got, validDigest)
	}
}

// TestManualSync_EnvManaged_DeploysEnvAndSecrets checks the whole env/secrets
// path end-to-end: a manifest declaring env + secrets, with "env" in
// managedFields, actually deploys both to the fake Cloud Run service.
func TestManualSync_EnvManaged_DeploysEnvAndSecrets(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf(`
image:
  digest: %s
env:
  LOG_LEVEL: debug
secrets:
  - name: DB_PASSWORD
    secret: db-password
    version: "3"
`, validDigest))
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}, LatestRevisionReady: true},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image", "env"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestYAML}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	res, err := r.ManualSync(context.Background(), unit, "alice@company.com")
	if err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	if res.Status != "Synced" {
		t.Fatalf("expected Synced, got %+v", res)
	}
	live, ok := cr.services["example-prod-us/widget-api"]
	if !ok {
		t.Fatal("no fake Cloud Run service state for example-prod-us/widget-api")
	}
	if live.EnvVars["LOG_LEVEL"] != "debug" {
		t.Fatalf("expected env var deployed, got %+v", live.EnvVars)
	}
	if live.SecretRefs["DB_PASSWORD"] != (cloudrun.SecretRef{Secret: "db-password", Version: "3"}) {
		t.Fatalf("expected secret ref deployed, got %+v", live.SecretRefs)
	}
}

// TestManualSync_EnvNotManaged_LeavesLiveEnvUntouched is the counterpart:
// without "env" in managedFields, a forced sync must never touch the
// live service's existing env vars, even though the manifest happens to
// declare some (a real gotcha if managedFields is later widened —
// whatever's live should survive until "env" is explicitly opted into).
func TestManualSync_EnvNotManaged_LeavesLiveEnvUntouched(t *testing.T) {
	db := testutil.NewPostgres(t)
	manifestYAML := []byte(fmt.Sprintf(`
image:
  digest: %s
env:
  LOG_LEVEL: debug
`, validDigest))
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {
			ServiceState:        cloudrun.ServiceState{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EnvVars: map[string]string{"EXISTING": "value"}},
			LatestRevisionReady: true,
		},
	}}
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"}, // env not managed
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": manifestYAML}},
		CloudRun:      cr,
		Preconditions: &fakePreconditions{},
	}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Sync: manualSync()}
	if _, err := r.ManualSync(context.Background(), unit, "alice@company.com"); err != nil {
		t.Fatalf("ManualSync: %v", err)
	}
	live, ok := cr.services["example-prod-us/widget-api"]
	if !ok {
		t.Fatal("no fake Cloud Run service state for example-prod-us/widget-api")
	}
	if live.EnvVars["EXISTING"] != "value" || live.EnvVars["LOG_LEVEL"] != "" {
		t.Fatalf("expected live env untouched since env isn't managed, got %+v", live.EnvVars)
	}
}

type fakeTagResolver struct {
	tags map[string][]registry.Tag // key: repository
	err  error
}

func (f *fakeTagResolver) ListTags(_ context.Context, repository string) ([]registry.Tag, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[repository], nil
}

const overrideRepo = "us-central1-docker.pkg.dev/proj/repo/widget-service"

func manifestWithRepo() []byte {
	return []byte(fmt.Sprintf("image:\n  digest: %s\n  repository: %s\n", validDigest, overrideRepo))
}

// TestRunOnce_TrackVersionOverrideResolvesLiveDigest proves a unit's
// config.Override track/version (expander.SyncUnit.Track/Version) is
// resolved live against the manifest's image.repository, superseding the
// digest actually committed in the manifest — the mechanism behind letting
// one project in an environment pin a different version than the rest.
func TestRunOnce_TrackVersionOverrideResolvesLiveDigest(t *testing.T) {
	db := testutil.NewPostgres(t)
	overrideDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-service": manifestWithRepo()}},
		TagResolver: &fakeTagResolver{tags: map[string][]registry.Tag{
			overrideRepo: {{Name: "0.310.0", Digest: overrideDigest}},
		}},
		CloudRun: &fakeCloudRun{services: map[string]*cloudrun.LiveService{
			"proj-b/widget-service": {
				ServiceState:                cloudrun.ServiceState{ImageDigest: overrideDigest},
				HasRevisionForDesiredDigest: true,
				LatestRevisionReady:         true,
			},
		}},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-service", Project: "proj-b", Region: "us-central1", Version: "0.310"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 1 || results[0].DesiredImage != overrideDigest || results[0].Status != "Synced" {
		t.Fatalf("expected the override version's resolved digest to be desired and Synced, got %+v", results)
	}
}

func TestRunOnce_TrackVersionOverrideWithoutRepositoryIsInvalid(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-api": serviceYAML()}}, // no image.repository
		TagResolver:   &fakeTagResolver{},
		CloudRun:      &fakeCloudRun{},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us", Region: "us-central1", Version: "1.2"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 1 || results[0].Status != "Invalid" {
		t.Fatalf("expected Invalid without image.repository to resolve against, got %+v", results)
	}
}

func TestRunOnce_TrackVersionOverrideWithoutTagResolverIsInvalid(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-service": manifestWithRepo()}},
		// TagResolver deliberately nil
		CloudRun:      &fakeCloudRun{},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-service", Project: "proj-b", Region: "us-central1", Version: "0.310"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 1 || results[0].Status != "Invalid" {
		t.Fatalf("expected Invalid with no TagResolver configured, got %+v", results)
	}
}

func TestRunOnce_TrackVersionOverrideNoMatchingTagIsInvalid(t *testing.T) {
	db := testutil.NewPostgres(t)
	r := &Reconciler{
		DB:            db,
		ManagedFields: []string{"image"},
		Manifests:     &fakeManifests{byApp: map[string][]byte{"widget-service": manifestWithRepo()}},
		TagResolver:   &fakeTagResolver{tags: map[string][]registry.Tag{}}, // no tags at all satisfy "9.9"
		CloudRun:      &fakeCloudRun{},
		Preconditions: &fakePreconditions{},
	}

	units := []expander.SyncUnit{{App: "widget-service", Project: "proj-b", Region: "us-central1", Version: "9.9"}}
	results, err := r.RunOnce(context.Background(), units)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 1 || results[0].Status != "Invalid" {
		t.Fatalf("expected Invalid when no tag satisfies the override version, got %+v", results)
	}
}
