package expander

import (
	"testing"

	"github.com/runcd/runcd/internal/config"
)

func rootFixture(t *testing.T) *config.Root {
	t.Helper()
	root, err := config.Parse([]byte(`
environments:
  dev:
    projects: [example-dev-01]
    sync: { auto: true, interval: 30 }
  prd:
    projects: [example-prod-us, example-prod-eu, example-internal]
    region: us-central1
    sync: { auto: false, interval: 300 }

defaults:
  region: us-central1
  managedFields: [image]

apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
    overrides:
      example-prod-eu: { region: europe-west1 }
    exclude: [example-internal]
  - name: notification-service
    env: dev
    source: { repo: git@github.com:org/example-monorepo.git, path: apps/notification-service/ }
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	return root
}

func TestExpand_FanOutWithOverridesAndExclude(t *testing.T) {
	units, err := Expand(rootFixture(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byProject := map[string]SyncUnit{}
	for _, u := range units {
		byProject[u.App+"@"+u.Project] = u
	}

	if len(units) != 3 {
		t.Fatalf("expected 3 sync units (2 widget-api + 1 notification-service), got %d: %+v", len(units), units)
	}

	if _, ok := byProject["widget-api@example-internal"]; ok {
		t.Fatal("example-internal should have been excluded for widget-api")
	}

	us := byProject["widget-api@example-prod-us"]
	if us.Region != "us-central1" {
		t.Fatalf("expected widget-api@example-prod-us to inherit environment region, got %q", us.Region)
	}
	if us.Env != "prd" {
		t.Fatalf("expected Env=prd, got %q", us.Env)
	}
	if us.Sync.Auto == nil || *us.Sync.Auto != false {
		t.Fatalf("expected prd auto=false, got %+v", us.Sync)
	}

	eu := byProject["widget-api@example-prod-eu"]
	if eu.Region != "europe-west1" {
		t.Fatalf("expected per-project override region, got %q", eu.Region)
	}

	dev := byProject["notification-service@example-dev-01"]
	if dev.Region != "us-central1" {
		t.Fatalf("expected notification-service to fall back to defaults.region, got %q", dev.Region)
	}
	if dev.Sync.Auto == nil || *dev.Sync.Auto != true {
		t.Fatalf("expected dev auto=true, got %+v", dev.Sync)
	}
}

func TestExpand_OverrideTrackVersionPropagatesToOneProjectOnly(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  prd:
    projects: [proj-a, proj-b]
    region: us-central1
defaults:
  region: us-central1
apps:
  - name: widget-service
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-service/service.yaml }
    overrides:
      proj-b: { version: "0.310" }
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	units, err := Expand(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byProject := map[string]SyncUnit{}
	for _, u := range units {
		byProject[u.Project] = u
	}
	if got := byProject["proj-a"]; got.Track != "" || got.Version != "" {
		t.Fatalf("proj-a should have no override, got track=%q version=%q", got.Track, got.Version)
	}
	if got := byProject["proj-b"].Version; got != "0.310" {
		t.Fatalf("proj-b.Version = %q, want 0.310", got)
	}
}

// TestExpand_OverrideTrackPropagatesToOneProjectOnly is
// TestExpand_OverrideTrackVersionPropagatesToOneProjectOnly's sibling for
// track (rather than version) — Expand copies whichever of the two is set
// verbatim onto the one project's SyncUnit, and only that one.
func TestExpand_OverrideTrackPropagatesToOneProjectOnly(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  prd:
    projects: [proj-a, proj-b]
    region: us-central1
defaults:
  region: us-central1
apps:
  - name: widget-service
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-service/service.yaml }
    overrides:
      proj-b: { track: stable }
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	units, err := Expand(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byProject := map[string]SyncUnit{}
	for _, u := range units {
		byProject[u.Project] = u
	}
	if got := byProject["proj-a"]; got.Track != "" || got.Version != "" {
		t.Fatalf("proj-a should have no override, got track=%q version=%q", got.Track, got.Version)
	}
	if got := byProject["proj-b"]; got.Track != "stable" || got.Version != "" {
		t.Fatalf("proj-b: expected track=stable version=\"\", got track=%q version=%q", got.Track, got.Version)
	}
}

func TestExpand_InvalidOverrideProjectRejected(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  prd:
    projects: [example-prod-us]
apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
    overrides:
      example-typo-project: { region: europe-west1 }
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if _, err := Expand(root); err == nil {
		t.Fatal("expected error for overrides referencing a project not in the environment")
	}
}

func TestExpand_InvalidExcludeProjectRejected(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  prd:
    projects: [example-prod-us]
apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
    exclude: [example-typo-project]
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if _, err := Expand(root); err == nil {
		t.Fatal("expected error for exclude referencing a project not in the environment")
	}
}

// TestExpand_CollidingAppProjectRejected covers a case config.Parse itself
// can't catch: Parse only checks each environment's explicitly-declared
// Projects (it does no I/O), but folders.ResolveConfig runs after Parse and
// can merge a folder-resolved project into an environment — if that
// resolved project happens to overlap with a project a same-named app in a
// different environment already targets, two SyncUnits would collide on
// the same (app, project) key. Building the Root directly (not via Parse)
// simulates that post-folder-resolution state.
func TestExpand_CollidingAppProjectRejected(t *testing.T) {
	root := &config.Root{
		Environments: map[string]config.Environment{
			"a": {Projects: []string{"shared-project"}, Region: "us-central1"},
			"b": {Projects: []string{"shared-project"}, Region: "us-central1"},
		},
		Apps: []config.App{
			{Name: "widget-api", Env: "a", Source: config.Source{Repo: "git@github.com:org/deployment.git", Path: "services/widget-api/"}},
			{Name: "widget-api", Env: "b", Source: config.Source{Repo: "git@github.com:org/deployment.git", Path: "services/widget-api-v2/"}},
		},
	}
	if _, err := Expand(root); err == nil {
		t.Fatal("expected error: two app entries colliding on the same (app, project) key")
	}
}

func TestExpand_CopiesIgnoreFieldsAndIgnorePreconditions(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  prd:
    projects: [example-prod-us]
defaults:
  region: us-central1
  managedFields: [image, traffic]
apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
    ignoreFields: [traffic]
    ignorePreconditions: ["pubsubTopic:orders-events"]
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	units, err := Expand(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("expected 1 sync unit, got %d", len(units))
	}
	u := units[0]
	if len(u.IgnoreFields) != 1 || u.IgnoreFields[0] != "traffic" {
		t.Fatalf("expected ignoreFields copied onto the sync unit, got %+v", u.IgnoreFields)
	}
	if len(u.IgnorePreconditions) != 1 || u.IgnorePreconditions[0] != "pubsubTopic:orders-events" {
		t.Fatalf("expected ignorePreconditions copied onto the sync unit, got %+v", u.IgnorePreconditions)
	}
}

func TestExpand_NoAppsProducesNoUnits(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  dev:
    projects: [example-dev-01]
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	units, err := Expand(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("expected no sync units, got %+v", units)
	}
}

func TestExpand_SourceBranchPropagates(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  dev:
    projects: [example-dev-01]

defaults:
  region: us-central1
  managedFields: [image]

apps:
  - name: widget-api
    env: dev
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/app.yaml, branch: staging }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	units, err := Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(units) != 1 || units[0].SourceBranch != "staging" {
		t.Fatalf("expected SourceBranch=staging, got %+v", units)
	}
}

func TestExpand_NoResolvedRegionIsRejected(t *testing.T) {
	root, err := config.Parse([]byte(`
environments:
  dev:
    projects: [example-dev-01]
apps:
  - name: widget-api
    env: dev
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
`))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if _, err := Expand(root); err == nil {
		t.Fatal("expected error when neither defaults.region, environment.region, nor an override sets a region")
	}
}
