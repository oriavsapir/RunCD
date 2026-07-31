package gitsource

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/runcd/runcd/internal/expander"
)

type countingFetcher struct {
	calls atomic.Int64
	data  []byte
}

func (f *countingFetcher) GetFile(_ context.Context, _, _, _ string) ([]byte, error) {
	f.calls.Add(1)
	return f.data, nil
}

// TestGet_ConcurrentUnitsSharingAManifestCoalesce regression-tests an
// avoidable-API-call bug: every sync unit fetched its manifest
// independently, even when many units (one app fanning out across every
// project it targets) share the exact same repo+path.
func TestGet_ConcurrentUnitsSharingAManifestCoalesce(t *testing.T) {
	fetcher := &countingFetcher{data: []byte("manifest content")}
	src := &Source{Client: fetcher}

	unitFor := func(project string) expander.SyncUnit {
		return expander.SyncUnit{App: "widget-api", Project: project, SourceRepo: "org/deployment", SourcePath: "services/widget-api/app.yaml"}
	}

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			unit := unitFor(fmt.Sprintf("p%d", i))
			if _, err := src.Get(context.Background(), unit); err != nil {
				t.Errorf("Get: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 fetch for %d concurrent units sharing a manifest, got %d", concurrency, got)
	}
}

func TestGet_DifferentManifestsFetchedSeparately(t *testing.T) {
	fetcher := &countingFetcher{data: []byte("manifest content")}
	src := &Source{Client: fetcher}

	unitA := expander.SyncUnit{App: "widget-api", Project: "p1", SourceRepo: "org/deployment", SourcePath: "services/widget-api/app.yaml"}
	unitB := expander.SyncUnit{App: "other-app", Project: "p1", SourceRepo: "org/deployment", SourcePath: "services/other-app/app.yaml"}

	if _, err := src.Get(context.Background(), unitA); err != nil {
		t.Fatalf("Get unitA: %v", err)
	}
	if _, err := src.Get(context.Background(), unitB); err != nil {
		t.Fatalf("Get unitB: %v", err)
	}

	if got := fetcher.calls.Load(); got != 2 {
		t.Fatalf("expected 2 fetches for 2 distinct manifests, got %d", got)
	}
}

// TestGet_RefetchesAfterCacheExpires regression-tests that the cache
// doesn't hide a manifest change forever — it must expire and refetch.
func TestGet_RefetchesAfterCacheExpires(t *testing.T) {
	fetcher := &countingFetcher{data: []byte("v1")}
	src := &Source{Client: fetcher, CacheTTL: time.Millisecond}
	unit := expander.SyncUnit{App: "widget-api", Project: "p1", SourceRepo: "org/deployment", SourcePath: "services/widget-api/app.yaml"}

	if _, err := src.Get(context.Background(), unit); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := src.Get(context.Background(), unit); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if got := fetcher.calls.Load(); got != 2 {
		t.Fatalf("expected the cache to expire and refetch, got %d calls", got)
	}
}
