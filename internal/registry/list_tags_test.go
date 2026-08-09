package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingListFn fakes the real Artifact Registry call, counting invocations
// so tests can assert on cache hits/misses and singleflight coalescing
// without a live client.
type countingListFn struct {
	calls atomic.Int64
	tags  []Tag
	err   error
}

func (f *countingListFn) list(_ context.Context, _ string) ([]Tag, error) {
	f.calls.Add(1)
	return f.tags, f.err
}

func newTestClient(fn func(context.Context, string) ([]Tag, error), ttl time.Duration) *Client {
	return &Client{
		cache:    make(map[string]tagsCacheEntry),
		cacheTTL: ttl,
		listFn:   fn,
	}
}

func TestListTags_CachesWithinTTL(t *testing.T) {
	fake := &countingListFn{tags: []Tag{{Name: "v1", Digest: "sha256:aaa"}}}
	c := newTestClient(fake.list, time.Minute)

	for i := 0; i < 3; i++ {
		tags, err := c.ListTags(context.Background(), "us-central1-docker.pkg.dev/proj/repo/image")
		if err != nil {
			t.Fatalf("ListTags #%d: %v", i, err)
		}
		if len(tags) != 1 || tags[0].Name != "v1" {
			t.Fatalf("ListTags #%d = %v", i, tags)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("expected 1 real fetch across 3 calls within TTL, got %d", got)
	}
}

func TestListTags_RefetchesAfterTTLExpires(t *testing.T) {
	fake := &countingListFn{tags: []Tag{{Name: "v1", Digest: "sha256:aaa"}}}
	c := newTestClient(fake.list, time.Millisecond)

	if _, err := c.ListTags(context.Background(), "repo"); err != nil {
		t.Fatalf("first ListTags: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.ListTags(context.Background(), "repo"); err != nil {
		t.Fatalf("second ListTags: %v", err)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("expected the cache to expire and refetch, got %d calls", got)
	}
}

func TestListTags_ConcurrentCallersCoalesce(t *testing.T) {
	fake := &countingListFn{tags: []Tag{{Name: "v1", Digest: "sha256:aaa"}}}
	c := newTestClient(fake.list, time.Minute)

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.ListTags(context.Background(), "repo"); err != nil {
				t.Errorf("ListTags: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 real fetch for %d concurrent callers, got %d", concurrency, got)
	}
}

// TestListTags_FailedFetchIsNotCached regression-tests ListTags's own doc
// comment claim ("only a successful fetch is cached") — a transient API
// error must not wedge a repository's tags stale (or errored) past the
// point the underlying issue clears.
func TestListTags_FailedFetchIsNotCached(t *testing.T) {
	boom := errors.New("boom")
	fake := &countingListFn{err: boom}
	c := newTestClient(fake.list, time.Minute)

	if _, err := c.ListTags(context.Background(), "repo"); !errors.Is(err, boom) {
		t.Fatalf("expected the fetch error to surface, got %v", err)
	}

	fake.err = nil
	fake.tags = []Tag{{Name: "v1", Digest: "sha256:aaa"}}
	tags, err := c.ListTags(context.Background(), "repo")
	if err != nil {
		t.Fatalf("ListTags after transient error cleared: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected the retry to actually fetch fresh tags, got %v", tags)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("expected the failed first attempt to not be cached (2 real fetches total), got %d", got)
	}
}

func TestListTags_DefaultTTLUsedWhenUnset(t *testing.T) {
	fake := &countingListFn{tags: []Tag{{Name: "v1", Digest: "sha256:aaa"}}}
	c := newTestClient(fake.list, 0) // cacheTTL unset means DefaultTagsCacheTTL

	if _, err := c.ListTags(context.Background(), "repo"); err != nil {
		t.Fatalf("first ListTags: %v", err)
	}
	if _, err := c.ListTags(context.Background(), "repo"); err != nil {
		t.Fatalf("second ListTags: %v", err)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("expected DefaultTagsCacheTTL (30s) to still be in effect for a second immediate call, got %d fetches", got)
	}
}

func TestListTags_DifferentRepositoriesFetchedSeparately(t *testing.T) {
	fake := &countingListFn{tags: []Tag{{Name: "v1", Digest: "sha256:aaa"}}}
	c := newTestClient(fake.list, time.Minute)

	for i := 0; i < 2; i++ {
		if _, err := c.ListTags(context.Background(), fmt.Sprintf("repo%d", i)); err != nil {
			t.Fatalf("ListTags repo%d: %v", i, err)
		}
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("expected 2 fetches for 2 distinct repositories, got %d", got)
	}
}

// TestListTags_NilListFnFallsBackToUncachedImplementation guards against a
// nil-pointer panic on c.listFn(...) for a Client built as a zero-value
// literal (bypassing NewClient, which is the only place listFn normally
// gets set) — same fallback convention as folders.GCPResolver's lister
// field. A malformed repository is used so the fallback's own validation
// (parseRepository) returns an error before ever touching the real,
// also-nil Artifact Registry client field.
func TestListTags_NilListFnFallsBackToUncachedImplementation(t *testing.T) {
	c := &Client{cache: make(map[string]tagsCacheEntry), cacheTTL: time.Minute}
	_, err := c.ListTags(context.Background(), "not-a-valid-repository")
	if err == nil {
		t.Fatal("expected an error from the real listTagsUncached path, not a nil-pointer panic")
	}
	if !strings.Contains(err.Error(), "not a valid Artifact Registry docker path") {
		t.Fatalf("expected the fallback to reach listTagsUncached's own validation, got: %v", err)
	}
}
