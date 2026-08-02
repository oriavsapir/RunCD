package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleImageEvent_UnconfiguredReturns404(t *testing.T) {
	h := &Handler{} // ImageEvents nil — feature never configured
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/events/image", nil)
	rec := httptest.NewRecorder()

	h.handleImageEvent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when ImageEvents is unconfigured, got %d", rec.Code)
	}
}

func TestHandleImageEvent_InvalidTokenRejected(t *testing.T) {
	h := &Handler{
		ImageEvents: &fakeAuth{tokenToEmail: map[string]string{
			"trigger-token": "trigger@example.iam.gserviceaccount.com",
		}},
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/events/image", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	h.handleImageEvent(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an invalid token, got %d", rec.Code)
	}
}

func TestHandleImageEvent_LeaderNudgesReconcile(t *testing.T) {
	var nudged int
	h := &Handler{
		ImageEvents: &fakeAuth{tokenToEmail: map[string]string{
			"trigger-token": "trigger@example.iam.gserviceaccount.com",
		}},
		IsLeader:       func() bool { return true },
		NudgeReconcile: func() { nudged++ },
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/events/image", nil)
	req.Header.Set("Authorization", "Bearer trigger-token")
	rec := httptest.NewRecorder()

	h.handleImageEvent(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if nudged != 1 {
		t.Fatalf("expected NudgeReconcile to be called exactly once, got %d", nudged)
	}
}

// TestHandleImageEvent_NonLeaderDoesNotNudge is the concrete version of the
// "any replica can receive the event, only the leader reconciles" design —
// a non-leader must not try to nudge its own reconcile loop (RunOnce never
// runs there anyway) or reach for any cross-replica messaging.
func TestHandleImageEvent_NonLeaderDoesNotNudge(t *testing.T) {
	var nudged int
	h := &Handler{
		ImageEvents: &fakeAuth{tokenToEmail: map[string]string{
			"trigger-token": "trigger@example.iam.gserviceaccount.com",
		}},
		IsLeader:       func() bool { return false },
		NudgeReconcile: func() { nudged++ },
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/events/image", nil)
	req.Header.Set("Authorization", "Bearer trigger-token")
	rec := httptest.NewRecorder()

	h.handleImageEvent(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 even on a non-leader replica, got %d", rec.Code)
	}
	if nudged != 0 {
		t.Fatalf("expected NudgeReconcile not to be called on a non-leader replica, got %d calls", nudged)
	}
}

func TestHandleImageEvent_NilIsLeaderAndNudgeDoNotPanic(t *testing.T) {
	h := &Handler{
		ImageEvents: &fakeAuth{tokenToEmail: map[string]string{
			"trigger-token": "trigger@example.iam.gserviceaccount.com",
		}},
		// IsLeader/NudgeReconcile deliberately left nil.
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/events/image", nil)
	req.Header.Set("Authorization", "Bearer trigger-token")
	rec := httptest.NewRecorder()

	h.handleImageEvent(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
}
