// Package gitsource implements reconcile.ManifestSource by fetching each
// sync unit's manifest straight from GitHub's Contents API, authenticated
// as a GitHub App (internal/githubapp) — no local clone.
package gitsource

import (
	"context"

	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/githubapp"
)

type Source struct {
	Client *githubapp.Client
}

// Get fetches unit's manifest from its source repo's default branch —
// SyncUnit carries no ref, only repo+path (§5.1).
func (s *Source) Get(ctx context.Context, unit expander.SyncUnit) ([]byte, error) {
	return s.Client.GetFile(ctx, unit.SourceRepo, "", unit.SourcePath)
}
