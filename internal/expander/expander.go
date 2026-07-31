// Package expander turns apps[] x environments[env].projects into the flat
// list of sync units the reconcile loop iterates over (§5.1's ApplicationSet
// equivalent).
package expander

import (
	"fmt"

	"github.com/runcd/runcd/internal/config"
)

// SyncUnit is one (app, project) pair ready to reconcile.
type SyncUnit struct {
	App     string
	Project string
	// Env is the environments[] key this unit was expanded from — used by
	// RBAC scope matching ("env:prd", §5.9), not by the reconcile/diff path.
	Env        string
	Region     string
	Sync       config.SyncPolicy
	SourceRepo string
	SourcePath string
	// IgnoreFields/IgnorePreconditions are copied verbatim from the app's
	// config entry — see config.App's doc comments.
	IgnoreFields        []string
	IgnorePreconditions []string
}

// Expand resolves every apps[] entry against its environment's project list,
// applies overrides/exclude, and validates that every overrides/exclude
// entry names a project actually in that resolved list — a typo fails the
// whole config load rather than silently mis-expanding (§5.1, §7).
func Expand(root *config.Root) ([]SyncUnit, error) {
	var units []SyncUnit

	for _, app := range root.Apps {
		// Parse() already rejects an app referencing an unknown environment,
		// but Expand can be called on a Root built some other way (a test
		// fixture, a future hot-reload path) — don't silently produce zero
		// sync units for a typo'd env, fail loudly like every other invalid
		// reference in this package does.
		env, ok := root.Environments[app.Env]
		if !ok {
			return nil, fmt.Errorf("app %q references unknown environment %q", app.Name, app.Env)
		}

		resolved := make(map[string]bool, len(env.Projects))
		for _, p := range env.Projects {
			resolved[p] = true
		}

		for project := range app.Overrides {
			if !resolved[project] {
				return nil, fmt.Errorf("app %q: overrides references project %q not in environment %q", app.Name, project, app.Env)
			}
		}
		excluded := make(map[string]bool, len(app.Exclude))
		for _, project := range app.Exclude {
			if !resolved[project] {
				return nil, fmt.Errorf("app %q: exclude references project %q not in environment %q", app.Name, project, app.Env)
			}
			// A project both overridden and excluded is a real config
			// mistake, not a meaningless combination worth silently
			// resolving one way — the override would otherwise expand
			// (or fail to expand) to nothing, unreachable and unused,
			// exactly the kind of silent misexpansion the checks above
			// this one already fail loudly on instead.
			if _, ok := app.Overrides[project]; ok {
				return nil, fmt.Errorf("app %q: project %q is both overridden and excluded", app.Name, project)
			}
			excluded[project] = true
		}

		sync := root.Defaults.Sync.Merge(env.Sync)

		for _, project := range env.Projects {
			if excluded[project] {
				continue
			}

			region := root.Defaults.Region
			if env.Region != "" {
				region = env.Region
			}
			if o, ok := app.Overrides[project]; ok && o.Region != "" {
				region = o.Region
			}
			if region == "" {
				return nil, fmt.Errorf("app %q project %q: no region resolved — set defaults.region, environments[%q].region, or overrides[%q].region", app.Name, project, app.Env, project)
			}

			units = append(units, SyncUnit{
				App:                 app.Name,
				Project:             project,
				Env:                 app.Env,
				Region:              region,
				Sync:                sync,
				SourceRepo:          app.Source.Repo,
				SourcePath:          app.Source.Path,
				IgnoreFields:        app.IgnoreFields,
				IgnorePreconditions: app.IgnorePreconditions,
			})
		}
	}

	return units, nil
}
