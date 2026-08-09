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
	err   error
}

func (f *countingFetcher) GetFile(_ context.Context, _, _, _ string) ([]byte, error) {
	f.calls.Add(1)
	return f.data, f.err
}

type refCapturingFetcher struct {
	mu   sync.Mutex
	refs []string
}

func (f *refCapturingFetcher) GetFile(_ context.Context, _, ref, _ string) ([]byte, error) {
	f.mu.Lock()
	f.refs = append(f.refs, ref)
	f.mu.Unlock()
	return []byte(ref), nil
}

// TestGet_PassesUnitBranchAsRef checks that SourceBranch reaches
// FileFetcher.GetFile's ref parameter — a manifest sourced from a
// non-default branch must actually be fetched from it, not silently from
// the repo's default branch.
func TestGet_PassesUnitBranchAsRef(t *testing.T) {
	fetcher := &refCapturingFetcher{}
	src := &Source{Client: fetcher}

	data, err := src.Get(context.Background(), expander.SyncUnit{
		SourceRepo: "org/deployment", SourcePath: "app.yaml", SourceBranch: "staging",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "staging" {
		t.Fatalf("expected GetFile called with ref=staging, got %q", data)
	}
}

// TestGet_DifferentBranchesDontShareCache checks that two units on the same
// repo+path but different branches don't collide in the cache — otherwise
// the second app to fetch would silently receive the first app's branch's
// content.
func TestGet_DifferentBranchesDontShareCache(t *testing.T) {
	fetcher := &refCapturingFetcher{}
	src := &Source{Client: fetcher}
	ctx := context.Background()

	main, err := src.Get(ctx, expander.SyncUnit{SourceRepo: "org/deployment", SourcePath: "app.yaml", SourceBranch: ""})
	if err != nil {
		t.Fatalf("Get(default): %v", err)
	}
	staging, err := src.Get(ctx, expander.SyncUnit{SourceRepo: "org/deployment", SourcePath: "app.yaml", SourceBranch: "staging"})
	if err != nil {
		t.Fatalf("Get(staging): %v", err)
	}
	if string(main) == string(staging) {
		t.Fatalf("expected distinct content per branch, got %q for both", main)
	}
	if string(main) != "" || string(staging) != "staging" {
		t.Fatalf("got main=%q staging=%q", main, staging)
	}
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

// TestGet_FetchErrorIsNotCached ensures a failed fetch doesn't populate the
// cache — otherwise a transient GitHub API error would wedge every unit
// sharing this manifest onto that error (or, worse, onto a zero-value nil
// manifest) until the TTL happens to expire.
func TestGet_FetchErrorIsNotCached(t *testing.T) {
	fetcher := &countingFetcher{err: fmt.Errorf("boom")}
	src := &Source{Client: fetcher}
	unit := expander.SyncUnit{App: "widget-api", Project: "p1", SourceRepo: "org/deployment", SourcePath: "services/widget-api/app.yaml"}

	if _, err := src.Get(context.Background(), unit); err == nil {
		t.Fatal("expected the fetch error to surface")
	}

	fetcher.err = nil
	fetcher.data = []byte("manifest content")
	data, err := src.Get(context.Background(), unit)
	if err != nil {
		t.Fatalf("Get after transient error cleared: %v", err)
	}
	if string(data) != "manifest content" {
		t.Fatalf("expected the retry to actually fetch fresh content, got %q", data)
	}
	if got := fetcher.calls.Load(); got != 2 {
		t.Fatalf("expected the failed first attempt to not be cached (2 real fetches total), got %d", got)
	}
}
