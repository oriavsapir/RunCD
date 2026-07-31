package reconcile

import (
	"context"
	"fmt"

	"github.com/runcd/runcd/internal/expander"
)

// Orphan is a live Cloud Run service that exists in a project/region a
// current sync unit still targets, but isn't declared by any of them —
// ArgoCD's prune equivalent (§ prune), read-only: nothing is ever deleted,
// this only flags.
type Orphan struct {
	Project string
	Region  string
	App     string
}

// DetectOrphans lists live Cloud Run services per distinct (project,
// region) pair that units still cover, and reports any not named by a
// current unit there.
//
// Scope, deliberately: only (project, region) pairs at least one surviving
// unit still targets are scanned. If every app targeting a given project
// was removed from runcd.yaml in the same change, that project's own
// orphans go undetected — there's no longer anything in the expanded unit
// set pointing at it. Closing that gap needs the full config.Root (every
// environments[env].projects entry, not just ones a surviving app still
// references), a larger change than this first cut; "even just flagging"
// (the roadmap's own bar) is what this covers.
func (r *Reconciler) DetectOrphans(ctx context.Context, units []expander.SyncUnit) ([]Orphan, error) {
	type scope struct{ project, region string }
	expectedApps := make(map[scope]map[string]bool)
	for _, u := range units {
		key := scope{u.Project, u.Region}
		if expectedApps[key] == nil {
			expectedApps[key] = make(map[string]bool)
		}
		expectedApps[key][u.App] = true
	}

	var orphans []Orphan
	for key, apps := range expectedApps {
		live, err := r.CloudRun.ListServiceNames(ctx, key.project, key.region)
		if err != nil {
			return nil, fmt.Errorf("list services in %s/%s: %w", key.project, key.region, err)
		}
		for _, name := range live {
			if !apps[name] {
				orphans = append(orphans, Orphan{Project: key.project, Region: key.region, App: name})
			}
		}
	}
	return orphans, nil
}
