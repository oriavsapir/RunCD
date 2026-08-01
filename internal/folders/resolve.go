package folders

import (
	"context"
	"errors"
	"fmt"

	"github.com/runcd/runcd/internal/config"
)

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
		for _, folderID := range env.Folders {
			projects, err := resolver.ProjectsInFolder(ctx, folderID)
			if err != nil {
				errs = append(errs, fmt.Errorf("environment %q: resolve folder %q: %w", name, folderID, err))
				continue
			}
			for _, p := range projects {
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
	membership := make(map[string][]string, len(folderIDs))
	var errs []error
	for _, id := range folderIDs {
		if _, ok := membership[id]; ok {
			continue
		}
		projects, err := resolver.ProjectsInFolder(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve folder %q: %w", id, err))
			continue
		}
		membership[id] = projects
	}
	return membership, errors.Join(errs...)
}
