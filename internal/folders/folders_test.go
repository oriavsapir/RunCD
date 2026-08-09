package folders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/runcd/runcd/internal/config"
)

// fakeResolver is a simple in-memory Resolver for tests.
type fakeResolver struct {
	byFolder map[string][]string
	err      map[string]error
}

func (f *fakeResolver) ProjectsInFolder(_ context.Context, folderID string) ([]string, error) {
	if err, ok := f.err[folderID]; ok {
		return nil, err
	}
	return f.byFolder[folderID], nil
}

func TestResolveConfig_MergesFolderProjectsIntoEnvironment(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"123": {"folder-project-a", "folder-project-b"},
	}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"explicit-project"}, Folders: []string{"123"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), resolver, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 3 {
		t.Fatalf("expected 3 projects (1 explicit + 2 from folder), got %+v", got)
	}
}

func TestResolveConfig_DedupesProjectListedBothExplicitlyAndViaFolder(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"123": {"shared-project", "folder-only-project"},
	}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"shared-project"}, Folders: []string{"123"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), resolver, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 2 {
		t.Fatalf("expected shared-project deduped, got %+v", got)
	}
}

func TestResolveConfig_NoFoldersLeavesEnvironmentUnchanged(t *testing.T) {
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"explicit-project"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), &fakeResolver{}, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 1 || got[0] != "explicit-project" {
		t.Fatalf("expected unchanged project list, got %+v", got)
	}
}

func TestResolveConfig_ResolverErrorPropagatesWithEnvironmentContext(t *testing.T) {
	boom := errors.New("boom")
	resolver := &fakeResolver{err: map[string]error{"123": boom}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Folders: []string{"123"}},
		},
	}
	_, err := ResolveConfig(context.Background(), resolver, root)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the underlying error wrapped, got %v", err)
	}
}

func TestResolveMembership_ResolvesEachDistinctFolder(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"111": {"a", "b"},
		"222": {"c"},
	}}
	membership, err := ResolveMembership(context.Background(), resolver, []string{"111", "222", "111"})
	if err != nil {
		t.Fatalf("ResolveMembership: %v", err)
	}
	if len(membership) != 2 {
		t.Fatalf("expected 2 distinct folders resolved, got %+v", membership)
	}
	if len(membership["111"]) != 2 || len(membership["222"]) != 1 {
		t.Fatalf("unexpected membership: %+v", membership)
	}
}

// TestResolveConfig_DoesNotMutateInputRoot guards ResolveConfig's own
// documented contract ("root itself is never mutated") — a caller hot-
// reloading config every RECONCILE_INTERVAL tick relies on being able to
// keep using its own root after passing it in.
func TestResolveConfig_DoesNotMutateInputRoot(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"123": {"folder-project"},
	}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"explicit-project"}, Folders: []string{"123"}},
		},
	}
	if _, err := ResolveConfig(context.Background(), resolver, root); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := root.Environments["prd"].Projects
	if len(got) != 1 || got[0] != "explicit-project" {
		t.Fatalf("expected the input root's Projects list unmutated, got %+v", got)
	}
}

// fakeLister is a folderLister for exercising GCPResolver's own
// cache/TTL/singleflight/stale-fallback logic without a live Resource
// Manager client.
type fakeLister struct {
	mu    sync.Mutex
	calls int
	fn    func(folderID string) ([]string, error)
}

func (f *fakeLister) listProjectsInFolder(_ context.Context, folderID string) ([]string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(folderID)
}

func TestGCPResolver_CacheHitAvoidsFetch(t *testing.T) {
	lister := &fakeLister{fn: func(string) ([]string, error) {
		t.Fatal("fetch must not be called on a fresh cache hit")
		return nil, nil
	}}
	r := &GCPResolver{
		lister: lister,
		cache: map[string]folderCacheEntry{
			"123": {projects: []string{"cached-project"}, fetchedAt: time.Now()},
		},
	}
	got, err := r.ProjectsInFolder(context.Background(), "123")
	if err != nil {
		t.Fatalf("ProjectsInFolder: %v", err)
	}
	if len(got) != 1 || got[0] != "cached-project" {
		t.Fatalf("expected the cached projects, got %+v", got)
	}
}

func TestGCPResolver_CacheExpiresAfterTTL(t *testing.T) {
	lister := &fakeLister{fn: func(string) ([]string, error) {
		return []string{"fresh-project"}, nil
	}}
	r := &GCPResolver{
		CacheTTL: time.Millisecond,
		lister:   lister,
		cache: map[string]folderCacheEntry{
			"123": {projects: []string{"stale-project"}, fetchedAt: time.Now().Add(-time.Hour)},
		},
	}
	got, err := r.ProjectsInFolder(context.Background(), "123")
	if err != nil {
		t.Fatalf("ProjectsInFolder: %v", err)
	}
	if len(got) != 1 || got[0] != "fresh-project" {
		t.Fatalf("expected a fresh fetch once the cache entry is past its TTL, got %+v", got)
	}
	lister.mu.Lock()
	calls := lister.calls
	lister.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch, got %d", calls)
	}
}

func TestGCPResolver_FetchErrorWithNoCacheEntryPropagates(t *testing.T) {
	boom := errors.New("boom")
	lister := &fakeLister{fn: func(string) ([]string, error) { return nil, boom }}
	r := &GCPResolver{lister: lister, cache: map[string]folderCacheEntry{}}
	if _, err := r.ProjectsInFolder(context.Background(), "123"); !errors.Is(err, boom) {
		t.Fatalf("expected the underlying fetch error, got %v", err)
	}
}

// TestGCPResolver_FetchErrorFallsBackToStaleCacheAndRetriesNextCall covers
// both halves of the documented fallback behavior: a transient fetch error
// serves the last-known membership instead of collapsing to "zero
// projects" (which would flag every one of that folder's projects as
// orphaned for the tick), and — because the error path deliberately never
// refreshes fetchedAt — a sustained outage must keep retrying on every
// subsequent call rather than pinning the stale snapshot for another full
// TTL.
func TestGCPResolver_FetchErrorFallsBackToStaleCacheAndRetriesNextCall(t *testing.T) {
	boom := errors.New("boom")
	lister := &fakeLister{fn: func(string) ([]string, error) { return nil, boom }}
	r := &GCPResolver{
		CacheTTL: time.Millisecond,
		lister:   lister,
		cache: map[string]folderCacheEntry{
			"123": {projects: []string{"stale-project"}, fetchedAt: time.Now().Add(-time.Hour)},
		},
	}

	for attempt := 1; attempt <= 2; attempt++ {
		got, err := r.ProjectsInFolder(context.Background(), "123")
		if err != nil {
			t.Fatalf("attempt %d: expected the stale cache to be served without an error, got %v", attempt, err)
		}
		if len(got) != 1 || got[0] != "stale-project" {
			t.Fatalf("attempt %d: expected the stale cached projects, got %+v", attempt, got)
		}
		lister.mu.Lock()
		calls := lister.calls
		lister.mu.Unlock()
		if calls != attempt {
			t.Fatalf("attempt %d: expected %d fetch attempts so far (error must not refresh fetchedAt), got %d", attempt, attempt, calls)
		}
	}
}

// TestGCPResolver_SingleflightCoalescesConcurrentCalls covers concurrent
// callers for the same folder ID (e.g. config resolution and RBAC
// membership resolution racing in the same tick) sharing one real fetch.
func TestGCPResolver_SingleflightCoalescesConcurrentCalls(t *testing.T) {
	release := make(chan struct{})
	var calls int32
	lister := &fakeLister{fn: func(string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return []string{"project-a"}, nil
	}}
	r := &GCPResolver{lister: lister, cache: map[string]folderCacheEntry{}}

	const n = 10
	var wg sync.WaitGroup
	results := make([][]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.ProjectsInFolder(context.Background(), "123")
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let every goroutine reach group.Do before releasing the one fetch
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 real fetch for concurrent callers sharing a folder ID, got %d", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: unexpected error: %v", i, err)
		}
		if len(results[i]) != 1 || results[i][0] != "project-a" {
			t.Fatalf("caller %d: unexpected result %+v", i, results[i])
		}
	}
}

// TestResolveAll_OrdersResultsByInputIndexBeyondConcurrencyLimit exercises
// resolveAll's bounded fan-out (resolveConcurrency) with more folders than
// the limit, verifying results[i] always corresponds to folderIDs[i] even
// though goroutines complete out of order.
func TestResolveAll_OrdersResultsByInputIndexBeyondConcurrencyLimit(t *testing.T) {
	const n = resolveConcurrency*2 + 3
	ids := make([]string, n)
	byFolder := make(map[string][]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("folder-%d", i)
		byFolder[ids[i]] = []string{"project-for-" + ids[i]}
	}
	resolver := &fakeResolver{byFolder: byFolder}

	results := resolveAll(context.Background(), resolver, ids)
	if len(results) != n {
		t.Fatalf("expected %d results, got %d", n, len(results))
	}
	for i, id := range ids {
		want := "project-for-" + id
		if len(results[i].projects) != 1 || results[i].projects[0] != want {
			t.Fatalf("result[%d] for folder %q: expected %q, got %+v", i, id, want, results[i].projects)
		}
	}
}

// concurrencyTrackingResolver records the peak number of concurrent
// ProjectsInFolder calls it saw, for asserting resolveAll's SetLimit
// actually bounds fan-out rather than running every folder at once.
type concurrencyTrackingResolver struct {
	current *int32
	maxSeen *int32
}

func (c concurrencyTrackingResolver) ProjectsInFolder(_ context.Context, folderID string) ([]string, error) {
	n := atomic.AddInt32(c.current, 1)
	for {
		old := atomic.LoadInt32(c.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(c.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(c.current, -1)
	return []string{folderID}, nil
}

func TestResolveAll_BoundsConcurrency(t *testing.T) {
	const n = resolveConcurrency * 3
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("folder-%d", i)
	}
	var current, maxSeen int32
	resolver := concurrencyTrackingResolver{current: &current, maxSeen: &maxSeen}

	results := resolveAll(context.Background(), resolver, ids)
	if len(results) != n {
		t.Fatalf("expected %d results, got %d", n, len(results))
	}
	if got := atomic.LoadInt32(&maxSeen); got > resolveConcurrency {
		t.Fatalf("expected concurrency bounded at %d, saw a peak of %d", resolveConcurrency, got)
	}
}
