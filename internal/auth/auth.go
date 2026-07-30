// Package auth verifies the identity behind an API request (§5.9). An
// interface, not just one concrete implementation, so the API layer can be
// tested without a live Google token and so the identity source (direct
// Google OAuth vs. Identity-Aware Proxy) can be swapped per deployment.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"google.golang.org/api/idtoken"
)

// Authenticator verifies the caller's identity from an incoming request and
// returns the authenticated email — the subject rbac.CanSync checks
// against. Takes the whole request, not just a token, because different
// identity sources carry their credential in different places (a bearer
// token in Authorization for direct Google OAuth, a dedicated header for
// IAP).
type Authenticator interface {
	Verify(r *http.Request) (email string, err error)
}

// GoogleAuthenticator validates a Google-issued ID token (from the
// Authorization: Bearer header) against Google's public keys and the
// configured OAuth client ID (audience). Direct-OIDC path — use
// IAPAuthenticator instead when the service is fronted by Identity-Aware
// Proxy, which has already authenticated the caller before the request
// ever reaches here.
type GoogleAuthenticator struct {
	Audience string
}

func (g *GoogleAuthenticator) Verify(r *http.Request) (string, error) {
	token, ok := bearerToken(r)
	if !ok {
		return "", errors.New("missing bearer token")
	}
	// idtoken.Validate treats an empty audience as "skip the audience
	// check" — accepting any validly Google-signed token regardless of
	// which OAuth client it was actually issued for. Fail closed here
	// rather than relying entirely on main.go's env-var validation to
	// ensure Audience is never empty.
	if g.Audience == "" {
		return "", errors.New("GoogleAuthenticator.Audience is empty — refusing to skip audience validation")
	}
	payload, err := idtoken.Validate(r.Context(), token, g.Audience)
	if err != nil {
		return "", fmt.Errorf("verify google id token: %w", err)
	}

	verified, _ := payload.Claims["email_verified"].(bool)
	if !verified {
		return "", errors.New("id token email is not verified")
	}
	email, ok := payload.Claims["email"].(string)
	if !ok || email == "" {
		return "", errors.New("id token missing email claim")
	}
	return email, nil
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// iapJWKSURL is Google's fixed public-key endpoint for verifying IAP's
// signed identity assertion — a different key set than the regular Google
// OAuth certs endpoint idtoken.Validate uses, since IAP signs with its own
// keys.
const iapJWKSURL = "https://www.gstatic.com/iap/verify/public_key-jwk"

// iapIssuer is the fixed issuer IAP signs its assertions as.
const iapIssuer = "https://cloud.google.com/iap"

// iapAssertionHeader is the header IAP attaches to every request it
// forwards, carrying the signed identity assertion.
const iapAssertionHeader = "X-Goog-IAP-JWT-Assertion" //nolint:gosec // a header name, not a credential

// IAPAuthenticator verifies the signed identity assertion Google's
// Identity-Aware Proxy attaches to every request it forwards. IAP itself
// already gates who can reach the service at all (via IAM); verifying the
// JWT here is defense-in-depth against a request that bypasses IAP (e.g.
// hitting the origin directly) forging that header — the pattern Google
// documents at https://cloud.google.com/iap/docs/signed-headers-howto.
type IAPAuthenticator struct {
	verifier *oidc.IDTokenVerifier
}

// NewIAPAuthenticator builds an IAPAuthenticator with its verifier already
// constructed (not lazily, which would race across concurrent requests) —
// safe for concurrent use across every request from construction on.
//
// audience is the expected aud claim. For IAP enabled directly on Cloud Run
// (no load balancer — the topology this deployment uses), this is
// "/projects/<PROJECT_NUMBER>/locations/<REGION>/services/<SERVICE_NAME>".
// (A fronting External HTTPS Load Balancer + Serverless NEG is a different,
// unused-here topology with its own format,
// "/projects/<PROJECT_NUMBER>/global/backendServices/<BACKEND_SERVICE_ID>" —
// see https://cloud.google.com/iap/docs/signed-headers-howto.)
func NewIAPAuthenticator(audience string) (*IAPAuthenticator, error) {
	if audience == "" {
		return nil, errors.New("IAP audience is empty — refusing to skip audience validation")
	}
	keySet := oidc.NewRemoteKeySet(context.Background(), iapJWKSURL)
	verifier := oidc.NewVerifier(iapIssuer, keySet, &oidc.Config{ClientID: audience})
	return &IAPAuthenticator{verifier: verifier}, nil
}

func (a *IAPAuthenticator) Verify(r *http.Request) (string, error) {
	assertion := r.Header.Get(iapAssertionHeader)
	if assertion == "" {
		return "", fmt.Errorf("missing %s header", iapAssertionHeader)
	}
	token, err := a.verifier.Verify(r.Context(), assertion)
	if err != nil {
		return "", fmt.Errorf("verify IAP jwt: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("decode IAP jwt claims: %w", err)
	}
	if claims.Email == "" {
		return "", errors.New("IAP jwt missing email claim")
	}
	return claims.Email, nil
}
