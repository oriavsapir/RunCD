// Package precondition checks the `requires` entries on a service
// definition (§5.10) before a sync unit is considered deployable. Checker is
// an interface so this can be tested against a fake instead of live Pub/Sub
// calls; GCPChecker (gcp.go) is the real implementation.
package precondition

import (
	"context"
	"fmt"

	"github.com/runcd/runcd/internal/manifest"
)

// Checker verifies a single precondition exists in a target project.
type Checker interface {
	TopicExists(ctx context.Context, project, name string) (bool, error)
	SubscriptionExists(ctx context.Context, project, name string) (bool, error)
}

// Check runs every requires entry against project and returns an error
// naming the first missing one — sync fails loudly, not silently (§5.10).
func Check(ctx context.Context, checker Checker, project string, requires []manifest.Precondition) error {
	for _, req := range requires {
		var exists bool
		var err error
		switch req.Type {
		case "pubsubTopic":
			exists, err = checker.TopicExists(ctx, project, req.Name)
		case "pubsubSubscription":
			exists, err = checker.SubscriptionExists(ctx, project, req.Name)
		default:
			return fmt.Errorf("unknown precondition type %q", req.Type)
		}
		if err != nil {
			return fmt.Errorf("checking precondition %s %q: %w", req.Type, req.Name, err)
		}
		if !exists {
			return fmt.Errorf("precondition failed: %s %q does not exist in %s", req.Type, req.Name, project)
		}
	}
	return nil
}
