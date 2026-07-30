package precondition

import (
	"context"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/pubsub" //nolint:staticcheck // v2 restructures Topic/Subscription (Publisher/Subscriber) without an Exists check; v1 is still supported and is exactly the shape this Checker needs.
	"golang.org/x/sync/singleflight"
)

// DefaultExistsCacheTTL caps how long a TopicExists/SubscriptionExists
// result is reused. Whether a precondition's topic/subscription exists
// essentially never changes between polls (it changes when someone runs
// Terraform, not every reconcile pass) — without this, every unit's every
// `requires` entry would cost a live Pub/Sub API call on every single pass,
// scaling linearly with units × preconditions for state that's effectively
// static. Longer than gitsource's manifest cache since preconditions are
// even more stable than manifests, but still short enough that a newly
// satisfied precondition is picked up within a couple of polls, not stuck
// stale indefinitely.
const DefaultExistsCacheTTL = 60 * time.Second

type existsCacheEntry struct {
	exists    bool
	fetchedAt time.Time
}

// GCPChecker implements Checker against the real Pub/Sub Admin API, one
// client cached per target project (§5.10). Construction is coalesced per
// project via singleflight — the map mutex is only ever held for a plain
// lookup/insert, never across pubsub.NewClient's credential-resolution
// network call, so a cold-start dial for one project can't stall lookups
// for every other project in the reconcile worker pool.
type GCPChecker struct {
	// ExistsCacheTTL overrides DefaultExistsCacheTTL; zero means use the default.
	ExistsCacheTTL time.Duration

	mu      sync.Mutex
	clients map[string]*pubsub.Client
	group   singleflight.Group

	existsMu    sync.Mutex
	existsCache map[string]existsCacheEntry
	existsGroup singleflight.Group
}

func NewGCPChecker() *GCPChecker {
	return &GCPChecker{clients: make(map[string]*pubsub.Client)}
}

// Close releases every per-project client created so far.
func (c *GCPChecker) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.clients {
		_ = cl.Close()
	}
	return nil
}

func (c *GCPChecker) client(ctx context.Context, project string) (*pubsub.Client, error) {
	c.mu.Lock()
	if cl, ok := c.clients[project]; ok {
		c.mu.Unlock()
		return cl, nil
	}
	c.mu.Unlock()

	v, err, _ := c.group.Do(project, func() (any, error) {
		c.mu.Lock()
		if cl, ok := c.clients[project]; ok {
			c.mu.Unlock()
			return cl, nil
		}
		c.mu.Unlock()

		// context.WithoutCancel: this construction is shared via
		// singleflight across every concurrent caller for this project,
		// not just the one whose ctx happened to trigger it — using that
		// caller's ctx directly would fail every other waiter's client
		// lookup too if that one caller's context is cancelled/times out
		// mid-construction, even though their own contexts are still valid.
		cl, err := pubsub.NewClient(context.WithoutCancel(ctx), project)
		if err != nil {
			return nil, fmt.Errorf("create pubsub client for %s: %w", project, err)
		}
		c.mu.Lock()
		c.clients[project] = cl
		c.mu.Unlock()
		return cl, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*pubsub.Client), nil
}

// checkExists caches and coalesces a single existence check by
// kind/project/name. Only successful results are cached — a transient API
// error is never cached, so it can't block a unit past the point where the
// underlying issue actually clears.
func (c *GCPChecker) checkExists(ctx context.Context, kind, project, name string, fetch func(context.Context, *pubsub.Client) (bool, error)) (bool, error) {
	key := kind + "/" + project + "/" + name
	ttl := c.ExistsCacheTTL
	if ttl <= 0 {
		ttl = DefaultExistsCacheTTL
	}

	if exists, ok := c.cachedExists(key, ttl); ok {
		return exists, nil
	}

	v, err, _ := c.existsGroup.Do(key, func() (any, error) {
		if exists, ok := c.cachedExists(key, ttl); ok {
			return exists, nil
		}

		cl, err := c.client(ctx, project)
		if err != nil {
			return false, err
		}
		// context.WithoutCancel: shared via singleflight across every
		// concurrent caller for this kind/project/name — see the identical
		// note on client() above.
		exists, err := fetch(context.WithoutCancel(ctx), cl)
		if err != nil {
			return false, err
		}

		c.existsMu.Lock()
		if c.existsCache == nil {
			c.existsCache = make(map[string]existsCacheEntry)
		}
		c.existsCache[key] = existsCacheEntry{exists: exists, fetchedAt: time.Now()}
		c.existsMu.Unlock()
		return exists, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

func (c *GCPChecker) cachedExists(key string, ttl time.Duration) (bool, bool) {
	c.existsMu.Lock()
	defer c.existsMu.Unlock()
	e, ok := c.existsCache[key]
	if !ok || time.Since(e.fetchedAt) >= ttl {
		return false, false
	}
	return e.exists, true
}

func (c *GCPChecker) TopicExists(ctx context.Context, project, name string) (bool, error) {
	return c.checkExists(ctx, "topic", project, name, func(ctx context.Context, cl *pubsub.Client) (bool, error) {
		return cl.Topic(name).Exists(ctx)
	})
}

func (c *GCPChecker) SubscriptionExists(ctx context.Context, project, name string) (bool, error) {
	return c.checkExists(ctx, "sub", project, name, func(ctx context.Context, cl *pubsub.Client) (bool, error) {
		return cl.Subscription(name).Exists(ctx)
	})
}
