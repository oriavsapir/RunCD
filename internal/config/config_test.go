package config

import "testing"

func TestParse_Valid(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
    sync: { auto: true, interval: 30 }
  prd:
    projects: [example-prod-us, example-prod-eu]
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
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Apps) != 1 || root.Apps[0].Name != "widget-api" {
		t.Fatalf("apps not parsed: %+v", root.Apps)
	}
	if root.Environments["prd"].Sync.Auto == nil || *root.Environments["prd"].Sync.Auto != false {
		t.Fatalf("prd sync.auto not parsed correctly: %+v", root.Environments["prd"].Sync)
	}
}

func TestParse_UnknownEnvRejected(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
apps:
  - name: widget-api
    env: staging
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for app referencing unknown environment")
	}
}

func TestParse_NotifyRules(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
notify:
  slackWebhookUrl: https://hooks.slack.com/services/x
  rules:
    - on: syncFailed
    - on: healthDegraded
      forMinutes: 10
    - on: outOfSyncGated
      forHours: 4
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Notify.Rules) != 3 {
		t.Fatalf("expected 3 notify rules, got %d", len(root.Notify.Rules))
	}
	if root.Notify.Rules[1].ForMinutes == nil || *root.Notify.Rules[1].ForMinutes != 10 {
		t.Fatalf("expected healthDegraded forMinutes=10, got %+v", root.Notify.Rules[1])
	}
}

func TestParse_NotifyUnknownRuleRejected(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
notify:
  rules:
    - on: somethingElse
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an unknown notify rule")
	}
}

func TestParse_HealthDegradedRequiresForMinutes(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
notify:
  rules:
    - on: healthDegraded
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for healthDegraded missing forMinutes")
	}
}

func TestParse_OutOfSyncGatedRequiresForHours(t *testing.T) {
	yaml := []byte(`
environments:
  dev:
    projects: [example-dev-01]
notify:
  rules:
    - on: outOfSyncGated
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for outOfSyncGated missing forHours")
	}
}

func TestSyncPolicy_Merge(t *testing.T) {
	trueVal, falseVal := true, false
	three00 := 300

	defaults := SyncPolicy{
		SelfHeal: &trueVal,
		Retry:    &RetryPolicy{Limit: 5, BackoffSeconds: 5},
	}
	envOverride := SyncPolicy{Auto: &falseVal, Interval: &three00}

	merged := defaults.Merge(envOverride)
	if merged.Auto == nil || *merged.Auto != false {
		t.Fatalf("expected env override auto=false, got %+v", merged.Auto)
	}
	if merged.Interval == nil || *merged.Interval != 300 {
		t.Fatalf("expected env override interval=300, got %+v", merged.Interval)
	}
	if merged.SelfHeal == nil || *merged.SelfHeal != true {
		t.Fatalf("expected inherited selfHeal=true, got %+v", merged.SelfHeal)
	}
	if merged.Retry == nil || merged.Retry.Limit != 5 {
		t.Fatalf("expected inherited retry from defaults, got %+v", merged.Retry)
	}
}

func TestParse_DuplicateProjectInEnvironmentRejected(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us, example-prod-us]
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a project listed twice in one environment")
	}
}

func TestParse_DuplicateAppNameRejected(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
defaults:
  region: us-central1
apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api-v2/ }
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for two apps sharing the same name")
	}
}

func TestParse_NotifyRulesRequireWebhookURL(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
defaults:
  region: us-central1
notify:
  rules:
    - on: syncFailed
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for notify.rules with no slackWebhookUrl")
	}
}

func TestParse_NotifyRulesRejectMalformedWebhookURL(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
defaults:
  region: us-central1
notify:
  slackWebhookUrl: "not a url"
  rules:
    - on: syncFailed
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a malformed slackWebhookUrl")
	}
}
