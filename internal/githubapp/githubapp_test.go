package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string]struct {
		owner, name string
	}{
		"acme-org/deployment":                        {"acme-org", "deployment"},
		"git@github.com:acme-org/deployment.git":     {"acme-org", "deployment"},
		"https://github.com/acme-org/deployment.git": {"acme-org", "deployment"},
		"github.com/acme-org/deployment":             {"acme-org", "deployment"},
	}
	for in, want := range cases {
		owner, name, err := parseOwnerRepo(in)
		if err != nil {
			t.Fatalf("parseOwnerRepo(%q): %v", in, err)
		}
		if owner != want.owner || name != want.name {
			t.Fatalf("parseOwnerRepo(%q) = %q/%q, want %q/%q", in, owner, name, want.owner, want.name)
		}
	}
}

func TestParseOwnerRepo_Invalid(t *testing.T) {
	if _, _, err := parseOwnerRepo("not-a-valid-repo"); err == nil {
		t.Fatal("expected error for a string with no owner/repo separator")
	}
}

func TestParseOwnerRepo_ExtraSegmentRejected(t *testing.T) {
	if _, _, err := parseOwnerRepo("acme-org/deployment/extra"); err == nil {
		t.Fatal("expected error for a repo string with an extra path segment")
	}
}

func TestNewClient_RejectsGarbagePEM(t *testing.T) {
	if _, err := NewClient("123", []byte("not a pem file")); err == nil {
		t.Fatal("expected error for non-PEM input")
	}
}

// TestNewClient_NormalizesLiteralNewlineEscapes regression-tests a common
// deployment footgun: a PEM passed through a Cloud Run secret-as-env-var
// can arrive with literal `\n` two-character sequences instead of real
// newlines (e.g. from a naive single-line copy-paste), which pem.Decode
// otherwise silently fails to parse.
func TestNewClient_NormalizesLiteralNewlineEscapes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	realPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	mangled := []byte(strings.ReplaceAll(string(realPEM), "\n", `\n`))

	if _, err := NewClient("123456", mangled); err != nil {
		t.Fatalf("expected literal \\n escapes to be normalized, got error: %v", err)
	}
}

func TestAppJWT_IsWellFormedAndSignedWithConfiguredKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	client, err := NewClient("123456", pemBytes)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	token, err := client.appJWT()
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Iss != "123456" {
		t.Fatalf("iss = %q, want 123456", claims.Iss)
	}
	if claims.Exp <= claims.Iat {
		t.Fatalf("exp (%d) must be after iat (%d)", claims.Exp, claims.Iat)
	}
}
