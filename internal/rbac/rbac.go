// Package rbac implements the flat subject -> role -> scope model from
// §5.9: deliberately simpler than ArgoCD's Casbin policy engine. Everyone
// authenticated gets read-only access to everything by default (enforced by
// the API layer, not here); this package only answers "may subject sync
// this unit."
package rbac

import (
	"fmt"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/runcd/runcd/internal/expander"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleSyncer Role = "syncer"
)

type Rule struct {
	Subject string   `yaml:"subject"`
	Role    Role     `yaml:"role"`
	Scope   []string `yaml:"scope"`
}

type Config struct {
	Roles []Rule `yaml:"roles"`
}

// Parse decodes rbac.yaml and rejects an unknown role — a typo here should
// fail loudly rather than silently granting no access.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rbac.yaml: %w", err)
	}
	for _, rule := range cfg.Roles {
		if rule.Role != RoleAdmin && rule.Role != RoleSyncer {
			return nil, fmt.Errorf("subject %q: unknown role %q (must be admin or syncer)", rule.Subject, rule.Role)
		}
	}
	return &cfg, nil
}

// CanSync reports whether subject (an authenticated email — see internal/auth)
// may sync unit, per any admin/syncer rule whose scope covers it.
//
// subject here is checked by literal string equality against Rule.Subject —
// including when that subject is a Google Workspace group email. Real group
// membership resolution would need a Google Workspace Admin SDK call, which
// this v1 doesn't make (matching this repo's "no real GCP calls yet"
// posture); a group listed in rbac.yaml only matches if it's passed in
// directly as subject, not by resolving its members.
//
// Equivalent to CanSyncFolders with a nil folderMembership — a "folder:<id>"
// scope just never matches. Kept as its own function so every call site
// that doesn't have a resolved folder membership map (most of this
// package's tests) doesn't need to thread one through for no reason.
func CanSync(cfg *Config, subject string, unit expander.SyncUnit) bool {
	return CanSyncFolders(cfg, nil, subject, unit)
}

// CanSyncFolders is CanSync plus "folder:<id>" scope support: folderMembership
// maps a GCP folder ID (as written in a rule's scope) to the project IDs
// resolved to be under it (internal/folders, refreshed on the same
// hot-reload cadence as everything else) — a scope matches if unit.Project
// is one of them.
func CanSyncFolders(cfg *Config, folderMembership map[string][]string, subject string, unit expander.SyncUnit) bool {
	if cfg == nil {
		return false // fail closed: no config means no grants, not a panic
	}
	for _, rule := range cfg.Roles {
		if rule.Subject != subject {
			continue
		}
		for _, scope := range rule.Scope {
			if scopeMatches(scope, unit, folderMembership) {
				return true
			}
		}
	}
	return false
}

// FolderScopes collects the distinct folder IDs referenced by any rule's
// "folder:<id>" scope — the caller (main.go) resolves each into its member
// projects (internal/folders) to build the map CanSyncFolders needs.
func FolderScopes(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, rule := range cfg.Roles {
		for _, scope := range rule.Scope {
			if id, ok := strings.CutPrefix(scope, "folder:"); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// HasAnyGrant reports whether subject has at least one admin/syncer rule
// that actually grants something (a non-empty Scope) — for an endpoint
// like orphan detection that isn't scoped to one specific unit (it fans
// out across every project/region the whole config touches, making real
// GCP calls along the way), so there's no single unit to check CanSync
// against. A caller with no real grant anywhere has no legitimate reason
// to burn that quota. A rule with an empty Scope grants nothing under
// CanSync/CanSyncFolders either, so it must not count as "any grant" here
// — checking Subject alone would let a rule like `scope: []` (or any
// vacuous/unrecognized scope entry) pass this check despite authorizing
// zero units.
func HasAnyGrant(cfg *Config, subject string) bool {
	if cfg == nil {
		return false
	}
	for _, rule := range cfg.Roles {
		if rule.Subject != subject {
			continue
		}
		for _, scope := range rule.Scope {
			if isRecognizedScope(scope) {
				return true
			}
		}
	}
	return false
}

// isRecognizedScope reports whether scope is one of the forms scopeMatches
// actually knows how to evaluate — "*", "env:", "app:", or "folder:". A
// typo like "evn:prod" has a non-empty Scope but would never match a real
// CanSync/CanSyncFolders check, so len(Scope) > 0 alone isn't enough for
// HasAnyGrant either.
func isRecognizedScope(scope string) bool {
	if scope == "*" {
		return true
	}
	for _, prefix := range [...]string{"env:", "app:", "folder:"} {
		if strings.HasPrefix(scope, prefix) {
			return true
		}
	}
	return false
}

// Store holds a *Config that can be swapped out for a freshly-loaded one
// without disrupting concurrent readers — config hot-reload (§8: RBAC
// changes take effect on the next config poll, no controller restart)
// refreshes it from a background goroutine while API handlers read it per
// request.
//
// FolderMembership is a second, independently-swapped value (not bundled
// atomically with Config) — the same looseness this package's hot-reload
// already has relative to config/notify's own reload (see
// cmd/controller/main.go's reconcileLoop, which never synchronizes those
// either): a momentary mismatch between a just-reloaded rbac.yaml and a
// not-yet-refreshed folder membership map for one tick is an acceptable
// staleness window, not a new correctness risk.
type Store struct {
	v      atomic.Pointer[Config]
	folder atomic.Pointer[map[string][]string]
}

func NewStore(cfg *Config) *Store {
	s := &Store{}
	s.Set(cfg)
	empty := map[string][]string{}
	s.SetFolderMembership(empty)
	return s
}

func (s *Store) Set(cfg *Config) { s.v.Store(cfg) }

func (s *Store) Get() *Config { return s.v.Load() }

// SetFolderMembership replaces the folder ID -> member project IDs map
// CanSyncFolders consults for "folder:<id>" scopes.
func (s *Store) SetFolderMembership(m map[string][]string) { s.folder.Store(&m) }

// FolderMembership returns the current folder membership map, or nil if
// none has ever been set.
func (s *Store) FolderMembership() map[string][]string {
	p := s.folder.Load()
	if p == nil {
		return nil
	}
	return *p
}

func scopeMatches(scope string, unit expander.SyncUnit, folderMembership map[string][]string) bool {
	if scope == "*" {
		return true
	}
	if folderID, ok := strings.CutPrefix(scope, "folder:"); ok {
		for _, p := range folderMembership[folderID] {
			if p == unit.Project {
				return true
			}
		}
		return false
	}
	if env, ok := strings.CutPrefix(scope, "env:"); ok {
		return env == unit.Env
	}
	if app, ok := strings.CutPrefix(scope, "app:"); ok {
		name, project, found := strings.Cut(app, "@")
		return found && name == unit.App && project == unit.Project
	}
	return false
}
