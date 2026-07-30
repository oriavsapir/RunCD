// Package expander turns apps[] x environments[env].projects into the flat
// list of sync units the reconcile loop iterates over (§5.1's ApplicationSet
// equivalent).
package expander

import (
	"fmt"

	"github.com/argorun/argorun/internal/config"
)

// SyncUnit is one (app, project) pair ready to reconcile.
type SyncUnit struct {
	App        string
	Project    string
	Region     string
	Sync       config.SyncPolicy
	SourceRepo string
	SourcePath string
}

// Expand resolves every apps[] entry against its environment's project list,
// applies overrides/exclude, and validates that every overrides/exclude
// entry names a project actually in that resolved list — a typo fails the
// whole config load rather than silently mis-expanding (§5.1, §7).
func Expand(root *config.Root) ([]SyncUnit, error) {
	var units []SyncUnit

	for _, app := range root.Apps {
		env := root.Environments[app.Env] // Parse() already guarantees this exists.

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

			units = append(units, SyncUnit{
				App:        app.Name,
				Project:    project,
				Region:     region,
				Sync:       sync,
				SourceRepo: app.Source.Repo,
				SourcePath: app.Source.Path,
			})
		}
	}

	return units, nil
}
