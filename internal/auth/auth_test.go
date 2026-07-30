package auth

import (
	"context"
	"testing"
)

// TestGoogleAuthenticator_MalformedTokenRejected checks the offline
// rejection path only (a garbage string fails JWT parsing before any
// network call to Google's cert endpoint) — there's no way to test the
// happy path without a live Google-issued token, consistent with this
// repo's "no real GCP calls in tests" posture.
func TestGoogleAuthenticator_MalformedTokenRejected(t *testing.T) {
	g := &GoogleAuthenticator{Audience: "test-client-id"}
	_, err := g.Verify(context.Background(), "not-a-jwt")
	if err == nil {
		t.Fatal("expected a malformed token to be rejected")
	}
}

func TestGoogleAuthenticator_EmptyTokenRejected(t *testing.T) {
	g := &GoogleAuthenticator{Audience: "test-client-id"}
	_, err := g.Verify(context.Background(), "")
	if err == nil {
		t.Fatal("expected an empty token to be rejected")
	}
}
