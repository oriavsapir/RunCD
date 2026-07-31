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
  sync:
    retry: { limit: 5, backoffSeconds: 5 }
    selfHeal: true

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
	if us.Sync.SelfHeal == nil || *us.Sync.SelfHeal != true {
		t.Fatalf("expected selfHeal inherited from defaults, got %+v", us.Sync)
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
