package reconcile

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

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

type orphanScope struct{ project, region string }

// DetectOrphans lists live Cloud Run services per distinct (project,
// region) pair that units still cover, and reports any not named by a
// current unit there. Scans every distinct pair concurrently (bounded to
// DefaultWorkers, same as RunOnce): one project's API error doesn't abort
// the whole scan or discard results already found for every other
// project — it's returned alongside the (possibly still non-empty)
// results, same "one bad unit can't take down the fleet" principle RunOnce
// already follows.
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
	expectedApps := make(map[orphanScope]map[string]bool)
	for _, u := range units {
		key := orphanScope{u.Project, u.Region}
		if expectedApps[key] == nil {
			expectedApps[key] = make(map[string]bool)
		}
		expectedApps[key][u.App] = true
	}

	scopes := make([]orphanScope, 0, len(expectedApps))
	for key := range expectedApps {
		scopes = append(scopes, key)
	}

	type scopeResult struct {
		names []string
		err   error
	}
	results := make([]scopeResult, len(scopes))

	var g errgroup.Group
	g.SetLimit(DefaultWorkers)
	for i, key := range scopes {
		g.Go(func() error {
			names, err := r.CloudRun.ListServiceNames(ctx, key.project, key.region)
			results[i] = scopeResult{names: names, err: err}
			return nil // never fail the group — a project's error is collected below, not fatal to the others
		})
	}
	_ = g.Wait() // g.Go above never returns a non-nil error, so Wait never does either

	var orphans []Orphan
	var errs []error
	for i, key := range scopes {
		res := results[i]
		if res.err != nil {
			errs = append(errs, fmt.Errorf("list services in %s/%s: %w", key.project, key.region, res.err))
			continue
		}
		apps := expectedApps[key]
		for _, name := range res.names {
			if !apps[name] {
				orphans = append(orphans, Orphan{Project: key.project, Region: key.region, App: name})
			}
		}
	}
	return orphans, errors.Join(errs...)
}
