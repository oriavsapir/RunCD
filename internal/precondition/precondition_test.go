package precondition

import (
	"context"
	"testing"

	"github.com/runcd/runcd/internal/manifest"
)

type fakeChecker struct {
	topics        map[string]bool
	subscriptions map[string]bool
}

func (f *fakeChecker) TopicExists(_ context.Context, project, name string) (bool, error) {
	return f.topics[project+"/"+name], nil
}

func (f *fakeChecker) SubscriptionExists(_ context.Context, project, name string) (bool, error) {
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

func TestCheck_MissingTopicFailsLoudly(t *testing.T) {
	checker := &fakeChecker{}
	err := Check(context.Background(), checker, "example-acme-prod", []manifest.Precondition{
		{Type: "pubsubTopic", Name: "orders-events"},
	})
	if err == nil {
		t.Fatal("expected error for missing topic")
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
