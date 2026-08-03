// Package imageupdater is the optional git-write-back half of an
// argocd-image-updater equivalent: it resolves an app's image.track or
// image.version (internal/manifest) against Artifact Registry's current
// tags and, when the resolved digest differs from what's committed,
// rewrites just that digest in the app's service.yaml. Everything
// downstream — internal/reconcile, internal/diff, sync_events — keeps
// treating image.digest as the one committed source of truth; this package
// only ever changes what's committed, never how it's consumed (NFR2).
package imageupdater

import (
	"context"
	"fmt"
	"path"

	"github.com/runcd/runcd/internal/manifest"
	"github.com/runcd/runcd/internal/registry"
)

// Tag is one tag/digest pair for an Artifact Registry image — an alias, not
// a distinct type, so a Resolver here is structurally a reconcile.TagResolver
// too (same tag-resolution algorithm now lives in internal/registry, shared
// with internal/reconcile's per-project track/version override).
type Tag = registry.Tag

// Resolver lists an Artifact Registry image's tags — an interface so Update
// can be tested without live GCP calls, the same interface+fake pattern as
// cloudrun.AdminClient/precondition.Checker.
type Resolver interface {
	ListTags(ctx context.Context, repository string) ([]Tag, error)
}

// GitHub is the subset of githubapp.Client Update needs — read-with-sha plus
// write, mirroring gitsource.FileFetcher's narrow-interface shape.
type GitHub interface {
	GetFileWithSHA(ctx context.Context, repo, ref, path string) ([]byte, string, error)
	PutFile(ctx context.Context, repo, branch, path, message string, content []byte, sha string) error
}

// Manifest identifies one app's service definition to check — deliberately
// just repo+path, not a full expander.SyncUnit: every environment an app
// targets shares the same manifest file (§5.1), so callers should dedupe to
// one Manifest per unique (Repo, Path) before calling Update, not once per
// sync unit.
type Manifest struct {
	Repo string
	Path string
}

// Update fetches m's manifest, and if it declares image.track or
// image.version, resolves it against Artifact Registry and commits an
// updated image.digest if the resolved digest differs from what's
// committed. Returns the new digest (empty if nothing changed) — a
// manifest with neither track nor version set is a silent no-op, the same
// "unconfigured means inert" shape every other add-on in this repo has.
func Update(ctx context.Context, gh GitHub, resolver Resolver, m Manifest) (newDigest string, err error) {
	data, sha, err := gh.GetFileWithSHA(ctx, m.Repo, "", m.Path)
	if err != nil {
		return "", fmt.Errorf("fetch %s:%s: %w", m.Repo, m.Path, err)
	}
	sd, err := manifest.Parse(data)
	if err != nil {
		return "", fmt.Errorf("parse %s:%s: %w", m.Repo, m.Path, err)
	}
	if sd.Image.Track == "" && sd.Image.Version == "" {
		return "", nil
	}

	tags, err := resolver.ListTags(ctx, sd.Image.Repository)
	if err != nil {
		return "", fmt.Errorf("list tags for %s: %w", sd.Image.Repository, err)
	}
	resolved, err := registry.Resolve(tags, sd.Image.Track, sd.Image.Version, path.Base(sd.Image.Repository))
	if err != nil {
		return "", fmt.Errorf("resolve %s:%s: %w", m.Repo, m.Path, err)
	}
	if resolved == sd.Image.Digest {
		return "", nil
	}

	rewritten, err := rewriteDigest(data, resolved)
	if err != nil {
		return "", fmt.Errorf("rewrite %s:%s: %w", m.Repo, m.Path, err)
	}

	msg := fmt.Sprintf("imageupdater: %s -> %s", m.Path, resolved)
	if err := gh.PutFile(ctx, m.Repo, "", m.Path, msg, rewritten, sha); err != nil {
		return "", fmt.Errorf("commit %s:%s: %w", m.Repo, m.Path, err)
	}
	return resolved, nil
}
