// Package auth verifies the Google OAuth (Workspace SSO) identity behind an
// API request (§5.9). An interface, not just the real Google implementation,
// so the API layer can be tested without a live Google token.
package auth

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/idtoken"
)

// Authenticator verifies an OAuth ID token and returns the authenticated
// email — the subject rbac.CanSync checks against.
type Authenticator interface {
	Verify(ctx context.Context, idToken string) (email string, err error)
}

// GoogleAuthenticator validates a Google-issued ID token against Google's
// public keys and the configured OAuth client ID (audience).
type GoogleAuthenticator struct {
	Audience string
}

func (g *GoogleAuthenticator) Verify(ctx context.Context, idToken string) (string, error) {
	payload, err := idtoken.Validate(ctx, idToken, g.Audience)
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
