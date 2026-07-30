package reconcile

import (
	"context"
	"fmt"
	"testing"

	"github.com/argorun/argorun/internal/cloudrun"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/testutil"
)

const validDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"

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
	services map[string]*cloudrun.LiveService // key: project/app
	jobs     map[string]*cloudrun.LiveJob     // key: project/app
}

func (f *fakeCloudRun) GetService(_ context.Context, project, _, name, _ string) (*cloudrun.LiveService, error) {
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
