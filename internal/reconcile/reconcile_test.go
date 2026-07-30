package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/argorun/argorun/internal/cloudrun"
	"github.com/argorun/argorun/internal/config"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/testutil"
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
	services        map[string]*cloudrun.LiveService // key: project/app
	jobs            map[string]*cloudrun.LiveJob     // key: project/app
	deployErr       map[string]error                 // key: project/app — forces DeployService/DeployJob to fail
	getServiceCalls atomic.Int64                     // counts GetService invocations, to prove a genuine re-fetch happens; RunOnce calls this concurrently
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
	if _, ok := f.services[key]; !ok {
		return cloudrun.ErrNotProvisioned
	}
	f.services[key] = &cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{ImageDigest: desired.ImageDigest, TrafficLatestRevisionPercent: desired.TrafficLatestRevisionPercent},
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

func (f *flakyDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if len(args) > 0 {
		if app, ok := args[0].(string); ok && app == f.failApp {
			return nil, fmt.Errorf("simulated write failure for %s", app)
		}
	}
	return f.DB.ExecContext(ctx, query, args...)
}

func (f *flakyDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if len(args) > 0 {
		if app, ok := args[0].(string); ok && app == f.failApp {
			return f.DB.QueryRowContext(ctx, "SELECT 1 FROM __simulated_write_failure__")
		}
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
	if trigger != "auto" || actor != "argorun-controller" || fromImage != oldDigest || toImage != validDigest || result != "succeeded" {
		t.Fatalf("unexpected sync_events row: trigger=%s actor=%s from=%s to=%s result=%s", trigger, actor, fromImage, toImage, result)
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
		VALUES ('widget-api', 'example-dev-01', 'auto', 'argorun-controller', $1, $2, now(), 'in_progress')`, oldDigest, validDigest); err != nil {
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
		VALUES ('widget-api', 'example-dev-01', 'auto', 'argorun-controller', $1, $2, now(), 'in_progress')`, oldDigest, validDigest); err != nil {
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
