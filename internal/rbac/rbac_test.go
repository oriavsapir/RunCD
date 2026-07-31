package rbac

import (
	"testing"

	"github.com/runcd/runcd/internal/expander"
)

func TestParse_Valid(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
  - subject: platform-team@company.com
    role: syncer
    scope: ["env:prd"]
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Roles) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Roles))
	}
}

func TestParse_UnknownRoleRejected(t *testing.T) {
	_, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: superadmin
    scope: ["*"]
`))
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestCanSync_WildcardScope(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	if !CanSync(cfg, "alice@company.com", unit) {
		t.Fatal("expected wildcard scope to permit sync")
	}
}

func TestCanSync_EnvScope(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: platform-team@company.com
    role: syncer
    scope: ["env:prd"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inScope := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	outOfScope := expander.SyncUnit{App: "widget-api", Project: "example-dev-01", Env: "dev"}

	if !CanSync(cfg, "platform-team@company.com", inScope) {
		t.Fatal("expected env:prd to permit a prd-env unit")
	}
	if CanSync(cfg, "platform-team@company.com", outOfScope) {
		t.Fatal("expected env:prd to deny a dev-env unit")
	}
}

func TestCanSync_AppScope(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: bob@company.com
    role: syncer
    scope: ["app:widget-api@example-prod-eu"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	exact := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	otherProject := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Env: "prd"}
	otherApp := expander.SyncUnit{App: "notification-service", Project: "example-prod-eu", Env: "prd"}

	if !CanSync(cfg, "bob@company.com", exact) {
		t.Fatal("expected exact app@project scope to match")
	}
	if CanSync(cfg, "bob@company.com", otherProject) {
		t.Fatal("expected a different project to be denied")
	}
	if CanSync(cfg, "bob@company.com", otherApp) {
		t.Fatal("expected a different app to be denied")
	}
}

func TestCanSync_UnknownSubjectDenied(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	if CanSync(cfg, "mallory@evil.example", unit) {
		t.Fatal("expected an unlisted subject to be denied")
	}
}

func TestCanSync_NoMatchingScopeDenied(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: bob@company.com
    role: syncer
    scope: ["env:staging"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	if CanSync(cfg, "bob@company.com", unit) {
		t.Fatal("expected a scope for a different env to deny")
	}
}

// TestStore_SetSwapsWhatGetReturns is config-hot-reload's core invariant:
// a Store must reflect a newly-loaded rbac.yaml without callers needing
// their own synchronization.
func TestStore_SetSwapsWhatGetReturns(t *testing.T) {
	before, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store := NewStore(before)

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	if !CanSync(store.Get(), "alice@company.com", unit) {
		t.Fatal("expected alice to be granted access from the initial config")
	}

	after, err := Parse([]byte(`
roles:
  - subject: bob@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store.Set(after)

	if CanSync(store.Get(), "alice@company.com", unit) {
		t.Fatal("expected alice's grant to be gone after the reload replaced it")
	}
	if !CanSync(store.Get(), "bob@company.com", unit) {
		t.Fatal("expected bob to be granted access from the reloaded config")
	}
}

func TestHasAnyGrant(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: dev-only@company.com
    role: syncer
    scope: ["env:dev"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !HasAnyGrant(cfg, "dev-only@company.com") {
		t.Fatal("expected a subject with a scoped rule to have some grant")
	}
	if HasAnyGrant(cfg, "nobody@company.com") {
		t.Fatal("expected a subject with no rule at all to have no grant")
	}
	if HasAnyGrant(nil, "dev-only@company.com") {
		t.Fatal("expected a nil config to fail closed")
	}
}
