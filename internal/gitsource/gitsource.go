// Package gitsource implements reconcile.ManifestSource by fetching each
// sync unit's manifest straight from GitHub's Contents API, authenticated
// as a GitHub App (internal/githubapp) — no local clone.
package gitsource

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/argorun/argorun/internal/expander"
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
	cache map[string]cachedManifest
	group singleflight.Group
}

type cachedManifest struct {
	data      []byte
	fetchedAt time.Time
}

// Get fetches unit's manifest from its source repo's default branch —
// SyncUnit carries no ref, only repo+path (§5.1). Concurrent/near-concurrent
// requests for the same repo+path (the common case: one app's manifest
// shared across every project it targets) are coalesced and briefly cached
// rather than each hitting GitHub's API independently.
func (s *Source) Get(ctx context.Context, unit expander.SyncUnit) ([]byte, error) {
	key := unit.SourceRepo + "@" + unit.SourcePath
	ttl := s.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}

	if data, ok := s.cached(key, ttl); ok {
		return data, nil
	}

	v, err, _ := s.group.Do(key, func() (any, error) {
		if data, ok := s.cached(key, ttl); ok {
			return data, nil
		}
		data, err := s.Client.GetFile(ctx, unit.SourceRepo, "", unit.SourcePath)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.cache == nil {
			s.cache = make(map[string]cachedManifest)
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

func (s *Source) cached(key string, ttl time.Duration) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok || time.Since(e.fetchedAt) >= ttl {
		return nil, false
	}
	return e.data, true
}
