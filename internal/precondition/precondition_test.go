package precondition

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/runcd/runcd/internal/manifest"
)

type fakeChecker struct {
	topics        map[string]bool
	subscriptions map[string]bool
	err           error
	topicCalls    []string
}

func (f *fakeChecker) TopicExists(_ context.Context, project, name string) (bool, error) {
	f.topicCalls = append(f.topicCalls, project+"/"+name)
	if f.err != nil {
		return false, f.err
	}
	return f.topics[project+"/"+name], nil
}

func (f *fakeChecker) SubscriptionExists(_ context.Context, project, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.subscriptions[project+"/"+name], nil
}

func TestCheck_AllPreconditionsMet(t *testing.T) {
	checker := &fakeChecker{topics: map[string]bool{"example-acme-prod/orders-events": true}}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestCheck_MissingTopicFailsLoudly checks the actual documented contract —
// "returns an error naming the first missing one" — not just err != nil,
// which would also pass against a message that names nothing at all.
func TestCheck_MissingTopicFailsLoudly(t *testing.T) {
	checker := &fakeChecker{}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
	})
	if err == nil {
		t.Fatal("expected error for missing topic")
	}
	if !strings.Contains(err.Error(), "orders-events") || !strings.Contains(err.Error(), "example-acme-prod") {
		t.Fatalf("expected error to name the missing topic and project, got %q", err.Error())
	}
}

func TestCheck_MissingSubscriptionFailsLoudly(t *testing.T) {
	checker := &fakeChecker{}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubSubscription", Name: "orders-sub"},
	})
	if err == nil {
		t.Fatal("expected error for missing subscription")
	}
	if !strings.Contains(err.Error(), "orders-sub") || !strings.Contains(err.Error(), "example-acme-prod") {
		t.Fatalf("expected error to name the missing subscription and project, got %q", err.Error())
	}
}

// TestCheck_FirstFailureStopsCheckingRemainingPreconditions guards Check's
// "first missing one" contract — a second, unrelated precondition entry
// (here, one that would itself error as an unknown type) must never even be
// reached once an earlier one has already failed.
func TestCheck_FirstFailureStopsCheckingRemainingPreconditions(t *testing.T) {
	checker := &fakeChecker{}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
		{Type: "bogusType", Name: "whatever"},
	})
	if err == nil {
		t.Fatal("expected error for missing first precondition")
	}
	if !strings.Contains(err.Error(), "orders-events") {
		t.Fatalf("expected the error to name the first (missing topic), not fall through to the second, got %q", err.Error())
	}
}

func TestCheck_AllPreconditionsCheckedWhenEarlierOnesPass(t *testing.T) {
	checker := &fakeChecker{topics: map[string]bool{
		"example-acme-prod/orders-events": true,
	}}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
		{Type: "pubsubTopic", Name: "shipments-events"},
	})
	if err == nil {
		t.Fatal("expected error for the second, missing topic")
	}
	if !strings.Contains(err.Error(), "shipments-events") {
		t.Fatalf("expected the second precondition to actually be checked, got %q", err.Error())
	}
	if len(checker.topicCalls) != 2 {
		t.Fatalf("expected both preconditions to be checked, got calls %+v", checker.topicCalls)
	}
}

// TestCheck_CheckerErrorPropagates ensures a real Checker failure (a
// Pub/Sub API error, not just "doesn't exist") surfaces as an error rather
// than being swallowed or misreported as a missing precondition.
func TestCheck_CheckerErrorPropagates(t *testing.T) {
	checker := &fakeChecker{err: errors.New("pubsub: connection refused")}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
	})
	if err == nil {
		t.Fatal("expected the checker's error to propagate")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected the underlying checker error to be wrapped/visible, got %q", err.Error())
	}
}

func TestCheck_UnknownTypeRejected(t *testing.T) {
	checker := &fakeChecker{}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "secretExists", Name: "whatever"},
	})
	if err == nil {
		t.Fatal("expected error for unknown precondition type")
	}
}

func TestCheck_NoRequiresIsNoOp(t *testing.T) {
	checker := &fakeChecker{}
	if err := Check(context.Background(), checker, "example-acme-prod", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
