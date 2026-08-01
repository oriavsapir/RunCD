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

func TestSyncPolicy_MergeObserve(t *testing.T) {
	trueVal := true
	defaults := SyncPolicy{}
	envOverride := SyncPolicy{Observe: &trueVal}

	merged := defaults.Merge(envOverride)
	if merged.Observe == nil || !*merged.Observe {
		t.Fatalf("expected env override observe=true, got %+v", merged.Observe)
	}
}

func TestParse_SyncObserve(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      observe: true
defaults:
  region: us-central1
  managedFields: [image]
apps:
  - name: widget-api
    env: prd
    source: { repo: git@github.com:org/deployment.git, path: services/widget-api/ }
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sync := root.Environments["prd"].Sync
	if sync.Observe == nil || !*sync.Observe {
		t.Fatalf("expected environments.prd.sync.observe=true, got %+v", sync.Observe)
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

func TestParse_SyncWindowsValid(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      auto: true
      syncWindows:
        - kind: deny
          days: [Sat, Sun]
        - kind: allow
          days: [Mon, Tue, Wed, Thu, Fri]
          startHour: 9
          endHour: 17
defaults:
  region: us-central1
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	windows := root.Environments["prd"].Sync.SyncWindows
	if len(windows) != 2 || windows[0].Kind != SyncWindowDeny || windows[1].Kind != SyncWindowAllow {
		t.Fatalf("syncWindows not parsed correctly: %+v", windows)
	}
}

func TestParse_SyncWindowsRejectUnknownKind(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      syncWindows:
        - kind: block
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an unknown syncWindows kind")
	}
}

func TestParse_SyncWindowsRejectInvalidDay(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      syncWindows:
        - kind: deny
          days: [Someday]
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an invalid syncWindows day")
	}
}

func TestParse_SyncWindowsRejectOutOfRangeHour(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      syncWindows:
        - kind: allow
          startHour: 25
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an out-of-range syncWindows hour")
	}
}

func TestParse_SyncWindowsRejectAmbiguousEqualNonZeroHours(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      syncWindows:
        - kind: deny
          startHour: 5
          endHour: 5
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an ambiguous non-zero equal startHour/endHour (likely a typo for a narrow window)")
	}
}

func TestParse_SyncWindowsAllowExplicitAllDayZeroHours(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    sync:
      syncWindows:
        - kind: deny
          days: [Sat, Sun]
          startHour: 0
          endHour: 0
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err != nil {
		t.Fatalf("expected explicit 0/0 all-day window to be accepted, got: %v", err)
	}
}

func TestSyncPolicyMerge_SyncWindowsReplacedWholesaleWhenOverridden(t *testing.T) {
	base := SyncPolicy{SyncWindows: []SyncWindow{{Kind: SyncWindowDeny, Days: []string{"Sat"}}}}
	override := SyncPolicy{SyncWindows: []SyncWindow{{Kind: SyncWindowAllow}}}
	merged := base.Merge(override)
	if len(merged.SyncWindows) != 1 || merged.SyncWindows[0].Kind != SyncWindowAllow {
		t.Fatalf("expected override's syncWindows to replace base's entirely, got %+v", merged.SyncWindows)
	}

	unset := SyncPolicy{}
	merged = base.Merge(unset)
	if len(merged.SyncWindows) != 1 || merged.SyncWindows[0].Kind != SyncWindowDeny {
		t.Fatalf("expected base's syncWindows to survive an override that doesn't set it, got %+v", merged.SyncWindows)
	}
}

func TestParse_AppIgnoreFieldsAndIgnorePreconditions(t *testing.T) {
	yaml := []byte(`
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
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	app := root.Apps[0]
	if len(app.IgnoreFields) != 1 || app.IgnoreFields[0] != "traffic" {
		t.Fatalf("ignoreFields not parsed: %+v", app.IgnoreFields)
	}
	if len(app.IgnorePreconditions) != 1 || app.IgnorePreconditions[0] != "pubsubTopic:orders-events" {
		t.Fatalf("ignorePreconditions not parsed: %+v", app.IgnorePreconditions)
	}
}

func TestParse_AppIgnoreFieldsRejectsUnknownField(t *testing.T) {
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
    ignoreFields: [bogus]
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an ignoreFields entry runcd doesn't know how to manage")
	}
}

func TestParse_IgnorePreconditionsRejectsUnknownType(t *testing.T) {
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
    ignorePreconditions: ["pubsubTopik:orders-events"]
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an ignorePreconditions entry with a typo'd precondition type")
	}
}

func TestParse_IgnorePreconditionsRejectsMalformedEntry(t *testing.T) {
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
    ignorePreconditions: ["not-a-type-name-pair"]
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an ignorePreconditions entry with no type:name separator")
	}
}

func TestParse_ManagedFieldsAcceptsEnv(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
defaults:
  region: us-central1
  managedFields: [image, env]
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Defaults.ManagedFields) != 2 || root.Defaults.ManagedFields[1] != "env" {
		t.Fatalf("expected env accepted as a managed field, got %+v", root.Defaults.ManagedFields)
	}
}

func TestParse_EnvironmentFolders(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    folders: ["123456789012"]
defaults:
  region: us-central1
`)
	root, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	folders := root.Environments["prd"].Folders
	if len(folders) != 1 || folders[0] != "123456789012" {
		t.Fatalf("folders not parsed: %+v", folders)
	}
}

func TestParse_DuplicateFolderInEnvironmentRejected(t *testing.T) {
	yaml := []byte(`
environments:
  prd:
    projects: [example-prod-us]
    folders: ["123", "123"]
defaults:
  region: us-central1
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a folder listed twice in one environment")
	}
}
