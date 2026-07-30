// Package rbac implements the flat subject -> role -> scope model from
// §5.9: deliberately simpler than ArgoCD's Casbin policy engine. Everyone
// authenticated gets read-only access to everything by default (enforced by
// the API layer, not here); this package only answers "may subject sync
// this unit."
package rbac

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/argorun/argorun/internal/expander"
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
