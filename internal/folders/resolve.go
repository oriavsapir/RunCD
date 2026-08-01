package folders

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/runcd/runcd/internal/config"
)

// resolveConcurrency bounds how many ProjectsInFolder calls run at once —
// generous relative to any real deployment's distinct-folder count, and
// matches the bounded-fan-out pattern reconcile.RunOnce/DetectOrphans
// already use elsewhere in this codebase, rather than resolving folders one
// at a time (which made cold-start/cache-miss reconcile ticks scale
// linearly with folder count).
const resolveConcurrency = 8

type folderResult struct {
	projects []string
	err      error
}

func resolveAll(ctx context.Context, resolver Resolver, folderIDs []string) []folderResult {
	results := make([]folderResult, len(folderIDs))
	var g errgroup.Group
	g.SetLimit(resolveConcurrency)
	for i, id := range folderIDs {
		g.Go(func() error {
			projects, err := resolver.ProjectsInFolder(ctx, id)
			results[i] = folderResult{projects: projects, err: err}
			return nil // never fail the group — errors collected by the caller
		})
	}
	_ = g.Wait() // g.Go above never returns a non-nil error, so Wait never does either
	return results
}

// ResolveConfig returns a copy of root with each environment's Folders
// resolved (via resolver) and merged into its Projects list — deduped, so
// listing the same project both explicitly and via a folder is harmless.
// root itself is never mutated.
//
// One environment's folder failing to resolve (a transient Resource
// Manager error, say) doesn't block any other environment's resolution,
// or config loading as a whole — that environment's Projects list simply
// falls back to whatever's explicitly listed for this one tick, and the
// error is joined into the returned error for the caller to log. Callers
// should treat a non-nil error here as a partial-result signal to log, not
// a reason to abort the whole reload — the sibling RBAC-folder-membership
// resolution (ResolveMembership) already gets this same treatment in
// cmd/controller/main.go.
func ResolveConfig(ctx context.Context, resolver Resolver, root *config.Root) (*config.Root, error) {
	resolved := *root
	resolved.Environments = make(map[string]config.Environment, len(root.Environments))

	type job struct {
		envName  string
		folderID string
	}
	var jobs []job
	for name, env := range root.Environments {
		for _, folderID := range env.Folders {
			jobs = append(jobs, job{envName: name, folderID: folderID})
		}
	}

	folderIDs := make([]string, len(jobs))
	for i, j := range jobs {
		folderIDs[i] = j.folderID
	}
	results := resolveAll(ctx, resolver, folderIDs)

	// Grouped back per environment, in the same order env.Folders itself
	// lists them — jobs was built by iterating each environment's Folders
	// in order, so this reconstructs exactly that per-environment ordering
	// for the merge loop below.
	byEnv := make(map[string][]folderResult, len(root.Environments))
	for i, j := range jobs {
		byEnv[j.envName] = append(byEnv[j.envName], results[i])
	}

	var errs []error
	for name, env := range root.Environments {
		if len(env.Folders) == 0 {
			resolved.Environments[name] = env
			continue
		}

		seen := make(map[string]bool, len(env.Projects))
		merged := make([]string, 0, len(env.Projects))
		for _, p := range env.Projects {
			if !seen[p] {
				seen[p] = true
				merged = append(merged, p)
			}
		}
		for i, res := range byEnv[name] {
			if res.err != nil {
				errs = append(errs, fmt.Errorf("environment %q: resolve folder %q: %w", name, env.Folders[i], res.err))
				continue
			}
			for _, p := range res.projects {
				if !seen[p] {
					seen[p] = true
					merged = append(merged, p)
				}
			}
		}
		env.Projects = merged
		resolved.Environments[name] = env
	}
	return &resolved, errors.Join(errs...)
}

// ResolveMembership resolves every id in folderIDs (deduped by the caller
// isn't required — this dedupes internally) into its member projects,
// returning a folder ID -> member project IDs map for rbac.CanSyncFolders.
// One folder's resolution failing doesn't block any other's — its entry
// is simply absent from the returned map (so any "folder:<id>" scope
// referencing it matches nothing for this tick), with the error joined
// into the returned error for the caller to log.
func ResolveMembership(ctx context.Context, resolver Resolver, folderIDs []string) (map[string][]string, error) {
	unique := make([]string, 0, len(folderIDs))
	seen := make(map[string]bool, len(folderIDs))
	for _, id := range folderIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}

	results := resolveAll(ctx, resolver, unique)

	membership := make(map[string][]string, len(unique))
	var errs []error
	for i, id := range unique {
		if results[i].err != nil {
			errs = append(errs, fmt.Errorf("resolve folder %q: %w", id, results[i].err))
			continue
		}
		membership[id] = results[i].projects
	}
	return membership, errors.Join(errs...)
}
