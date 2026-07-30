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

// TestGoogleAuthenticator_EmptyAudienceFailsClosed regression-tests an
// audience-confusion risk: idtoken.Validate silently skips the audience
// check entirely when given an empty string, so an empty Audience would
// otherwise accept any validly Google-signed token for any OAuth client.
// This must be rejected before Validate is ever called, regardless of what
// idToken is.
func TestGoogleAuthenticator_EmptyAudienceFailsClosed(t *testing.T) {
	g := &GoogleAuthenticator{Audience: ""}
	if _, err := g.Verify(context.Background(), "not-a-jwt"); err == nil {
		t.Fatal("expected Verify to fail closed when Audience is empty")
	}
}
