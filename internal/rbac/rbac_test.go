package rbac

import (
	"sync"
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

// TestCanSync_NilConfigFailsClosed checks CanSync/CanSyncFolders never panic
// and always deny when no config has ever loaded (e.g. a controller that
// hasn't finished booting) — fail closed, not open.
func TestCanSync_NilConfigFailsClosed(t *testing.T) {
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}
	if CanSync(nil, "alice@company.com", unit) {
		t.Fatal("expected a nil config to deny CanSync")
	}
	if CanSyncFolders(nil, map[string][]string{"123": {"example-prod-eu"}}, "alice@company.com", unit) {
		t.Fatal("expected a nil config to deny CanSyncFolders even with a resolved membership map")
	}
}

// TestHasAnyGrant_WildcardScope checks the "*" scope — the most permissive
// form — is itself recognized as a real grant, not just the narrower
// env/app/folder forms the other HasAnyGrant tests exercise.
func TestHasAnyGrant_WildcardScope(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !HasAnyGrant(cfg, "alice@company.com") {
		t.Fatal("expected a wildcard scope to count as a grant")
	}
}

// TestHasAnyGrant_OneRecognizedScopeAmongMalformedOnesCounts checks a rule
// with multiple scope entries only needs one well-formed entry to count —
// an earlier malformed entry in the same list must not short-circuit the
// loop into denying a later valid one.
func TestHasAnyGrant_OneRecognizedScopeAmongMalformedOnesCounts(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: mixed@company.com
    role: syncer
    scope: ["evn:prod", "app:someapp", "env:prd"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !HasAnyGrant(cfg, "mixed@company.com") {
		t.Fatal("expected the one well-formed env:prd scope to count despite the other malformed entries")
	}
}

// TestStore_ConcurrentAccess exercises Set/Get and
// SetFolderMembership/FolderMembership from many goroutines while
// CanSyncFolders reads concurrently — the whole point of atomic.Pointer here
// is hot-reload safety under exactly this kind of concurrent access, and the
// suite's -race flag needs something that actually contends on it.
func TestStore_ConcurrentAccess(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["*"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store := NewStore(cfg)
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Env: "prd"}

	const iterations = 500
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				store.Set(cfg)
				store.SetFolderMembership(map[string][]string{"123": {"example-prod-eu"}})
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = CanSyncFolders(store.Get(), store.FolderMembership(), "alice@company.com", unit)
				_ = HasAnyGrant(store.Get(), "alice@company.com")
			}
		}()
	}
	wg.Wait()
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

// TestHasAnyGrant_EmptyScopeDoesNotCount is the regression test for a real
// bypass: a rule row existing for a subject isn't the same as that rule
// actually granting anything — an empty Scope list matches nothing under
// CanSync, so it must not count as "any grant" here either.
func TestHasAnyGrant_EmptyScopeDoesNotCount(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: vacuous@company.com
    role: syncer
    scope: []
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if HasAnyGrant(cfg, "vacuous@company.com") {
		t.Fatal("expected a rule with an empty scope to grant nothing")
	}
}

// TestHasAnyGrant_UnrecognizedScopeDoesNotCount guards against a typo'd
// scope (e.g. "evn:prod" instead of "env:prod") passing HasAnyGrant just
// because Scope is non-empty — it must never match a real CanSync check
// either, so it shouldn't grant access to a fleet-wide scan like orphans.
func TestHasAnyGrant_UnrecognizedScopeDoesNotCount(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: typo@company.com
    role: syncer
    scope: ["evn:prod"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if HasAnyGrant(cfg, "typo@company.com") {
		t.Fatal("expected an unrecognized scope form to grant nothing")
	}
}

// TestHasAnyGrant_MalformedAppScopeDoesNotCount guards against an
// "app:name" scope missing its required "@project" suffix — the prefix is
// recognized, but scopeMatches's own strings.Cut(app, "@") never matches
// any real unit without it, so this must not count as "any grant" either.
func TestHasAnyGrant_MalformedAppScopeDoesNotCount(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: malformed@company.com
    role: syncer
    scope: ["app:someapp"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if HasAnyGrant(cfg, "malformed@company.com") {
		t.Fatal("expected an app scope missing @project to grant nothing")
	}
}

func TestCanSyncFolders_FolderScopeMatchesMemberProject(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: platform-team@company.com
    role: syncer
    scope: ["folder:123456789012"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	membership := map[string][]string{"123456789012": {"example-prod-us", "example-prod-eu"}}

	inFolder := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Env: "prd"}
	notInFolder := expander.SyncUnit{App: "widget-api", Project: "example-dev-01", Env: "dev"}

	if !CanSyncFolders(cfg, membership, "platform-team@company.com", inFolder) {
		t.Fatal("expected a project resolved as a folder member to be permitted")
	}
	if CanSyncFolders(cfg, membership, "platform-team@company.com", notInFolder) {
		t.Fatal("expected a project not in the folder's membership to be denied")
	}
}

func TestCanSyncFolders_NilMembershipNeverMatchesFolderScope(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: platform-team@company.com
    role: syncer
    scope: ["folder:123456789012"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Env: "prd"}
	if CanSyncFolders(cfg, nil, "platform-team@company.com", unit) {
		t.Fatal("expected a folder scope to never match without a resolved membership map")
	}
	// CanSync itself (no membership map at all) must behave identically.
	if CanSync(cfg, "platform-team@company.com", unit) {
		t.Fatal("expected CanSync (folder-blind) to deny a folder-only scope")
	}
}

func TestStore_FolderMembershipDefaultsToEmptyNotNil(t *testing.T) {
	store := NewStore(&Config{})
	if store.FolderMembership() == nil {
		t.Fatal("expected NewStore to seed an empty (non-nil) folder membership map")
	}
	membership := map[string][]string{"111": {"a"}}
	store.SetFolderMembership(membership)
	if len(store.FolderMembership()["111"]) != 1 {
		t.Fatalf("expected SetFolderMembership to be reflected by FolderMembership, got %+v", store.FolderMembership())
	}
}

func TestFolderScopes_CollectsDistinctFolderIDs(t *testing.T) {
	cfg, err := Parse([]byte(`
roles:
  - subject: alice@company.com
    role: admin
    scope: ["folder:111", "folder:222", "env:prd"]
  - subject: bob@company.com
    role: syncer
    scope: ["folder:111"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ids := FolderScopes(cfg)
	if len(ids) != 2 {
		t.Fatalf("expected 2 distinct folder IDs, got %+v", ids)
	}
}
