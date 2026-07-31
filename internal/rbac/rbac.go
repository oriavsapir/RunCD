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
func CanSync(cfg *Config, subject string, unit expander.SyncUnit) bool {
	if cfg == nil {
		return false // fail closed: no config means no grants, not a panic
	}
	for _, rule := range cfg.Roles {
		if rule.Subject != subject {
			continue
		}
		for _, scope := range rule.Scope {
			if scopeMatches(scope, unit) {
				return true
			}
		}
	}
	return false
}

// HasAnyGrant reports whether subject has any admin/syncer rule at all,
// regardless of scope — for an endpoint like orphan detection that isn't
// scoped to one specific unit (it fans out across every project/region the
// whole config touches, making real GCP calls along the way), so there's no
// single unit to check CanSync against. A caller with no sync grant
// anywhere has no legitimate reason to burn that quota.
func HasAnyGrant(cfg *Config, subject string) bool {
	if cfg == nil {
		return false
	}
	for _, rule := range cfg.Roles {
		if rule.Subject == subject {
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
type Store struct {
	v atomic.Pointer[Config]
}

func NewStore(cfg *Config) *Store {
	s := &Store{}
	s.Set(cfg)
	return s
}

func (s *Store) Set(cfg *Config) { s.v.Store(cfg) }

func (s *Store) Get() *Config { return s.v.Load() }

func scopeMatches(scope string, unit expander.SyncUnit) bool {
	if scope == "*" {
		return true
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
