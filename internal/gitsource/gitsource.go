// Package gitsource implements reconcile.ManifestSource by fetching each
// sync unit's manifest straight from GitHub's Contents API, authenticated
// as a GitHub App (internal/githubapp) — no local clone.
package gitsource

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/runcd/runcd/internal/expander"
)

// DefaultCacheTTL caps how long a fetched manifest is reused across sync
// units that share the same repo+path — one app fanning out to N target
// projects all reference the identical manifest, so without this every one
// of those N units would refetch it from GitHub every single reconcile
// pass. Shorter than any sane RECONCILE_INTERVAL, so a manifest change is
// still picked up within a poll or two, not cached stale indefinitely.
const DefaultCacheTTL = 10 * time.Second

// FileFetcher is the subset of githubapp.Client Source needs — an
// interface so Source can be tested without a live GitHub App/network.
type FileFetcher interface {
	GetFile(ctx context.Context, repo, ref, path string) ([]byte, error)
}

type Source struct {
	Client FileFetcher
	// CacheTTL overrides DefaultCacheTTL; zero means use the default.
	CacheTTL time.Duration

	mu    sync.Mutex
	cache map[cacheKey]cachedManifest
	group singleflight.Group
}

// cacheKey is a struct, not a concatenated string — repo values are SSH
// URLs that already contain "@" (e.g. "git@github.com:org/repo.git"), so a
// naive repo+"@"+path join is ambiguous: two different (repo, path) pairs
// could concatenate to the same string. A struct key has no such ambiguity.
// branch is part of the key too — normally every unit sharing a (repo,
// path) also shares the same app-level branch, but two different apps could
// in principle reference the same file at different revisions, and caching
// keyed on (repo, path) alone would serve one app's content to the other.
type cacheKey struct {
	repo, path, branch string
}

type cachedManifest struct {
	data      []byte
	fetchedAt time.Time
}

// Get fetches unit's manifest from its source repo at unit.SourceBranch (a
// branch, tag, or commit SHA — empty means the repo's default branch,
// §5.1). Concurrent/near-concurrent requests for the same repo+path+branch
// (the common case: one app's manifest shared across every project it
// targets) are coalesced and briefly cached rather than each hitting
// GitHub's API independently.
func (s *Source) Get(ctx context.Context, unit expander.SyncUnit) ([]byte, error) {
	key := cacheKey{repo: unit.SourceRepo, path: unit.SourcePath, branch: unit.SourceBranch}
	ttl := s.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}

	if data, ok := s.cached(key, ttl); ok {
		return data, nil
	}

	// singleflight.Group.Do needs a string key, not a struct like cacheKey —
	// "\x00" is not a valid byte in a repo URL, file path, or git ref, so the
	// join is unambiguous (same reasoning as cacheKey above).
	groupKey := unit.SourceRepo + "\x00" + unit.SourcePath + "\x00" + unit.SourceBranch
	v, err, _ := s.group.Do(groupKey, func() (any, error) {
		if data, ok := s.cached(key, ttl); ok {
			return data, nil
		}
		// context.WithoutCancel: this fetch is shared via singleflight
		// across every concurrent caller for this repo+path+branch, not just
		// the one that triggered it — using that one caller's ctx directly
		// would fail every other waiter's fetch too if that caller's
		// context is cancelled (e.g. an HTTP request disconnecting) while
		// the shared fetch is still in flight.
		data, err := s.Client.GetFile(context.WithoutCancel(ctx), unit.SourceRepo, unit.SourceBranch, unit.SourcePath)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.cache == nil {
			s.cache = make(map[cacheKey]cachedManifest)
		}
		s.cache[key] = cachedManifest{data: data, fetchedAt: time.Now()}
		s.mu.Unlock()
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Source) cached(key cacheKey, ttl time.Duration) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok || time.Since(e.fetchedAt) >= ttl {
		return nil, false
	}
	return e.data, true
}
