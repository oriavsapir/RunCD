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
	Env          string
	Region       string
	Sync         config.SyncPolicy
	SourceRepo   string
	SourcePath   string
	SourceBranch string
	// IgnoreFields/IgnorePreconditions are copied verbatim from the app's
	// config entry — see config.App's doc comments.
	IgnoreFields        []string
	IgnorePreconditions []string
	// Track/Version, when set, override this project's manifest-declared
	// image.track/image.version — see config.Override.
	Track   string
	Version string
}

// Expand resolves every apps[] entry against its environment's project list,
// applies overrides/exclude, and validates that every overrides/exclude
// entry names a project actually in that resolved list — a typo fails the
// whole config load rather than silently mis-expanding (§5.1, §7).
func Expand(root *config.Root) ([]SyncUnit, error) {
	var units []SyncUnit
	// config.Parse already rejects two apps colliding on (app, project),
	// but only over each environment's explicitly-declared Projects — it
	// does no I/O, so it can't see a folder-resolved addition
	// (environments[env].folders, merged in later by folders.ResolveConfig
	// before this Expand call). Two app entries sharing a name in
	// different environments whose folders happen to resolve to an
	// overlapping project would otherwise silently produce two SyncUnits
	// for the same (app, project) key — sync_locks stops a literal
	// double-deploy, but the applications row becomes a last-write-wins
	// race between units that may carry different region/sync settings,
	// with nothing surfacing the collision anywhere.
	seenAppProject := make(map[string]bool)

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
			var track, version string
			if o, ok := app.Overrides[project]; ok {
				if o.Region != "" {
					region = o.Region
				}
				track, version = o.Track, o.Version
			}
			if region == "" {
				return nil, fmt.Errorf("app %q project %q: no region resolved — set defaults.region, environments[%q].region, or overrides[%q].region", app.Name, project, app.Env, project)
			}

			key := app.Name + "/" + project
			if seenAppProject[key] {
				return nil, fmt.Errorf("app %q: two sync units would collide on project %q — check for the same app name reused across environments whose folders/projects overlap", app.Name, project)
			}
			seenAppProject[key] = true

			units = append(units, SyncUnit{
				App:                 app.Name,
				Project:             project,
				Env:                 app.Env,
				Region:              region,
				Sync:                sync,
				SourceRepo:          app.Source.Repo,
				SourcePath:          app.Source.Path,
				SourceBranch:        app.Source.Branch,
				IgnoreFields:        app.IgnoreFields,
				IgnorePreconditions: app.IgnorePreconditions,
				Track:               track,
				Version:             version,
			})
		}
	}

	return units, nil
}
