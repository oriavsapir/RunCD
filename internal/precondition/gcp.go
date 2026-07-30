package precondition

import (
	"context"
	"fmt"
	"sync"

	"cloud.google.com/go/pubsub" //nolint:staticcheck // v2 restructures Topic/Subscription (Publisher/Subscriber) without an Exists check; v1 is still supported and is exactly the shape this Checker needs.
	"golang.org/x/sync/singleflight"
)

// GCPChecker implements Checker against the real Pub/Sub Admin API, one
// client cached per target project (§5.10). Construction is coalesced per
// project via singleflight — the map mutex is only ever held for a plain
// lookup/insert, never across pubsub.NewClient's credential-resolution
// network call, so a cold-start dial for one project can't stall lookups
// for every other project in the reconcile worker pool.
type GCPChecker struct {
	mu      sync.Mutex
	clients map[string]*pubsub.Client
	group   singleflight.Group
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

func (c *GCPChecker) TopicExists(ctx context.Context, project, name string) (bool, error) {
	cl, err := c.client(ctx, project)
	if err != nil {
		return false, err
	}
	return cl.Topic(name).Exists(ctx)
}

func (c *GCPChecker) SubscriptionExists(ctx context.Context, project, name string) (bool, error) {
	cl, err := c.client(ctx, project)
	if err != nil {
		return false, err
	}
	return cl.Subscription(name).Exists(ctx)
}
