package precondition

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/pubsub" //nolint:staticcheck // same v1-vs-v2 rationale as gcp.go's own import.
)

func TestNewGCPChecker_InitializesClientsMap(t *testing.T) {
	c := NewGCPChecker()
	if c.clients == nil {
		t.Fatal("expected clients map to be initialized, not nil")
	}
}

func TestGCPChecker_CachedExists_MissWhenEmpty(t *testing.T) {
	c := NewGCPChecker()
	if _, ok := c.cachedExists("topic/proj/name", DefaultExistsCacheTTL); ok {
		t.Fatal("expected a miss against an empty cache")
	}
}

func TestGCPChecker_CachedExists_HitWithinTTL(t *testing.T) {
	c := NewGCPChecker()
	c.existsCache = map[string]existsCacheEntry{
		"topic/proj/name": {exists: true, fetchedAt: time.Now()},
	}
	exists, ok := c.cachedExists("topic/proj/name", DefaultExistsCacheTTL)
	if !ok || !exists {
		t.Fatalf("expected a fresh cache hit reporting true, got ok=%v exists=%v", ok, exists)
	}
}

func TestGCPChecker_CachedExists_ExpiredAfterTTL(t *testing.T) {
	c := NewGCPChecker()
	c.existsCache = map[string]existsCacheEntry{
		"topic/proj/name": {exists: true, fetchedAt: time.Now().Add(-2 * time.Second)},
	}
	if _, ok := c.cachedExists("topic/proj/name", time.Second); ok {
		t.Fatal("expected an expired entry to miss")
	}
}

// checkExistsClient returns a *pubsub.Client that never needs real
// credentials or a live network dial — the emulator env var short-circuits
// ADC resolution and gRPC dials lazily, so this is safe to use as long as
// the fetch closure passed to checkExists never actually issues an RPC
// (checkExists's own fetch func parameter is what's under test here, not
// pubsub itself).
func checkExistsClient(t *testing.T) *pubsub.Client {
	t.Helper()
	t.Setenv("PUBSUB_EMULATOR_HOST", "localhost:1")
	cl, err := pubsub.NewClient(context.Background(), "test-project")
	if err != nil {
		t.Fatalf("construct pubsub client for test: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func TestGCPChecker_CheckExists_FetchCalledOnceThenCached(t *testing.T) {
	c := NewGCPChecker()
	cl := checkExistsClient(t)
	c.mu.Lock()
	c.clients["proj"] = cl
	c.mu.Unlock()

	calls := 0
	fetch := func(context.Context, *pubsub.Client) (bool, error) {
		calls++
		return true, nil
	}

	exists, err := c.checkExists(context.Background(), "topic", "proj", "name", fetch)
	if err != nil || !exists {
		t.Fatalf("first call: exists=%v err=%v", exists, err)
	}
	exists, err = c.checkExists(context.Background(), "topic", "proj", "name", fetch)
	if err != nil || !exists {
		t.Fatalf("second call: exists=%v err=%v", exists, err)
	}
	if calls != 1 {
		t.Fatalf("expected fetch called exactly once (second call served from cache), got %d calls", calls)
	}
}

// TestGCPChecker_CheckExists_ErrorNotCached guards the documented invariant
// in checkExists's own comment: a transient fetch error must never be
// cached, or a precondition that starts failing (Pub/Sub hiccup) would stay
// wedged as "missing" past the point the underlying issue actually clears.
func TestGCPChecker_CheckExists_ErrorNotCached(t *testing.T) {
	c := NewGCPChecker()
	cl := checkExistsClient(t)
	c.mu.Lock()
	c.clients["proj"] = cl
	c.mu.Unlock()

	calls := 0
	fetch := func(context.Context, *pubsub.Client) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("transient pubsub error")
		}
		return true, nil
	}

	if _, err := c.checkExists(context.Background(), "topic", "proj", "name", fetch); err == nil {
		t.Fatal("expected the first, failing fetch's error to propagate")
	}
	exists, err := c.checkExists(context.Background(), "topic", "proj", "name", fetch)
	if err != nil || !exists {
		t.Fatalf("expected the retry to succeed uncached, got exists=%v err=%v", exists, err)
	}
	if calls != 2 {
		t.Fatalf("expected fetch retried after the failed attempt (not served from a cached error), got %d calls", calls)
	}
}

func TestGCPChecker_CheckExists_CustomTTLOverridesDefault(t *testing.T) {
	c := NewGCPChecker()
	c.ExistsCacheTTL = time.Millisecond
	cl := checkExistsClient(t)
	c.mu.Lock()
	c.clients["proj"] = cl
	c.mu.Unlock()

	calls := 0
	fetch := func(context.Context, *pubsub.Client) (bool, error) {
		calls++
		return true, nil
	}

	if _, err := c.checkExists(context.Background(), "topic", "proj", "name", fetch); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.checkExists(context.Background(), "topic", "proj", "name", fetch); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected the short custom TTL to expire before the second call, forcing a re-fetch; got %d calls", calls)
	}
}
