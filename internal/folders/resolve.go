package folders

import (
	"context"
	"fmt"

	"github.com/runcd/runcd/internal/config"
)

// ResolveConfig returns a copy of root with each environment's Folders
// resolved (via resolver) and merged into its Projects list — deduped, so
// listing the same project both explicitly and via a folder is harmless.
// root itself is never mutated.
func ResolveConfig(ctx context.Context, resolver Resolver, root *config.Root) (*config.Root, error) {
	resolved := *root
	resolved.Environments = make(map[string]config.Environment, len(root.Environments))
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
				return nil, fmt.Errorf("environment %q: resolve folder %q: %w", name, folderID, err)
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
	return &resolved, nil
}

// ResolveMembership resolves every id in folderIDs (deduped by the caller
// isn't required — this dedupes internally) into its member projects,
// returning a folder ID -> member project IDs map for rbac.CanSyncFolders.
func ResolveMembership(ctx context.Context, resolver Resolver, folderIDs []string) (map[string][]string, error) {
	membership := make(map[string][]string, len(folderIDs))
	for _, id := range folderIDs {
		if _, ok := membership[id]; ok {
			continue
		}
		projects, err := resolver.ProjectsInFolder(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve folder %q: %w", id, err)
		}
		membership[id] = projects
	}
	return membership, nil
}
