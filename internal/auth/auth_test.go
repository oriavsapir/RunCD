package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// requestWithBearer is always used with the same malformed-JWT string —
// every offline-rejection test just needs *some* bearer token present so
// Verify gets far enough to actually exercise its own parsing, not
// bearerToken's separate missing-header check.
func requestWithBearer() *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer not-a-jwt")
	return r
}

// TestGoogleAuthenticator_MalformedTokenRejected checks the offline
// rejection path only (a garbage string fails JWT parsing before any
// network call to Google's cert endpoint) — there's no way to test the
// happy path without a live Google-issued token, consistent with this
// repo's "no real GCP calls in tests" posture.
func TestGoogleAuthenticator_MalformedTokenRejected(t *testing.T) {
	g := &GoogleAuthenticator{Audience: "test-client-id"}
	_, err := g.Verify(requestWithBearer())
	if err == nil {
		t.Fatal("expected a malformed token to be rejected")
	}
}

func TestGoogleAuthenticator_MissingBearerTokenRejected(t *testing.T) {
	g := &GoogleAuthenticator{Audience: "test-client-id"}
	_, err := g.Verify(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil))
	if err == nil {
		t.Fatal("expected a request with no Authorization header to be rejected")
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
	if _, err := g.Verify(requestWithBearer()); err == nil {
		t.Fatal("expected Verify to fail closed when Audience is empty")
	}
}

func TestEventarcAuthenticator_MalformedTokenRejected(t *testing.T) {
	e := &EventarcAuthenticator{Audience: "test-aud", ServiceAccount: "trigger@example.iam.gserviceaccount.com"}
	if _, err := e.Verify(requestWithBearer()); err == nil {
		t.Fatal("expected a malformed token to be rejected")
	}
}

func TestEventarcAuthenticator_MissingBearerTokenRejected(t *testing.T) {
	e := &EventarcAuthenticator{Audience: "test-aud", ServiceAccount: "trigger@example.iam.gserviceaccount.com"}
	if _, err := e.Verify(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)); err == nil {
		t.Fatal("expected a request with no Authorization header to be rejected")
	}
}

// TestEventarcAuthenticator_EmptyAudienceFailsClosed mirrors
// GoogleAuthenticator's identical fail-closed requirement: idtoken.Validate
// treats an empty audience as "skip the check."
func TestEventarcAuthenticator_EmptyAudienceFailsClosed(t *testing.T) {
	e := &EventarcAuthenticator{Audience: "", ServiceAccount: "trigger@example.iam.gserviceaccount.com"}
	if _, err := e.Verify(requestWithBearer()); err == nil {
		t.Fatal("expected Verify to fail closed when Audience is empty")
	}
}

// TestEventarcAuthenticator_EmptyServiceAccountFailsClosed is this type's
// own extra requirement beyond GoogleAuthenticator's: an empty
// ServiceAccount must never be treated as "accept any caller."
func TestEventarcAuthenticator_EmptyServiceAccountFailsClosed(t *testing.T) {
	e := &EventarcAuthenticator{Audience: "test-aud", ServiceAccount: ""}
	if _, err := e.Verify(requestWithBearer()); err == nil {
		t.Fatal("expected Verify to fail closed when ServiceAccount is empty")
	}
}

// TestNewIAPAuthenticator_RejectsEmptyAudience mirrors the same
// fail-closed requirement for the IAP path: an empty audience must be
// rejected at construction, before any request is ever verified.
func TestNewIAPAuthenticator_RejectsEmptyAudience(t *testing.T) {
	if _, err := NewIAPAuthenticator(""); err == nil {
		t.Fatal("expected NewIAPAuthenticator to reject an empty audience")
	}
}

// TestIAPAuthenticator_MissingAssertionHeaderRejected checks the offline
// rejection path only — there's no way to test the happy path without a
// live IAP-signed assertion.
func TestIAPAuthenticator_MissingAssertionHeaderRejected(t *testing.T) {
	a, err := NewIAPAuthenticator("/projects/123/locations/us-central1/services/runcd")
	if err != nil {
		t.Fatalf("NewIAPAuthenticator: %v", err)
	}
	_, err = a.Verify(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil))
	if err == nil {
		t.Fatal("expected a request with no IAP assertion header to be rejected")
	}
}

func TestIAPAuthenticator_MalformedAssertionRejected(t *testing.T) {
	a, err := NewIAPAuthenticator("/projects/123/locations/us-central1/services/runcd")
	if err != nil {
		t.Fatalf("NewIAPAuthenticator: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	r.Header.Set(iapAssertionHeader, "not-a-jwt")
	if _, err := a.Verify(r); err == nil {
		t.Fatal("expected a malformed IAP assertion to be rejected")
	}
}
