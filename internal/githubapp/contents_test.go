package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// hostRewriteTransport redirects every request to target regardless of the
// request's own Host — GetFileWithSHA/PutFile hardcode "api.github.com",
// so tests point requests at an httptest.Server this way instead of
// standing up a real DNS/TLS endpoint.
type hostRewriteTransport struct {
	target *url.URL
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	c, err := NewClient("123456", pemBytes)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	c.HTTPClient = &http.Client{Transport: hostRewriteTransport{target: target}}
	return c
}

func (h hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = h.target.Scheme
	req.URL.Host = h.target.Host
	req.Host = h.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// fakeGitHub serves just enough of the real API for installationToken's two
// calls (installation lookup, token mint) plus one Contents API call.
func fakeGitHub(t *testing.T, contents http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/deploy/installation", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
	})
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "test-token", "expires_at": "2099-01-01T00:00:00Z"})
	})
	mux.HandleFunc("/repos/acme/deploy/contents/app/service.yaml", contents)
	return httptest.NewServer(mux)
}

func TestGetFileWithSHA_DecodesBase64ContentAndReturnsSHA(t *testing.T) {
	want := []byte("image:\n  digest: sha256:abc\n")
	server := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":      "blobsha123",
			"encoding": "base64",
			// GitHub chunks base64 with embedded newlines; assert that's tolerated.
			"content": base64.StdEncoding.EncodeToString(want)[:10] + "\n" + base64.StdEncoding.EncodeToString(want)[10:],
		})
	})
	defer server.Close()

	c := newTestClient(t, server)
	got, sha, err := c.GetFileWithSHA(context.Background(), "acme/deploy", "", "app/service.yaml")
	if err != nil {
		t.Fatalf("GetFileWithSHA: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if sha != "blobsha123" {
		t.Fatalf("sha = %q, want blobsha123", sha)
	}
}

func TestPutFile_SendsBase64ContentAndSHA(t *testing.T) {
	newContent := []byte("image:\n  digest: sha256:def\n")
	var gotBody map[string]any
	server := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"sha": "newcommit"}})
	})
	defer server.Close()

	c := newTestClient(t, server)
	if err := c.PutFile(context.Background(), "acme/deploy", "", "app/service.yaml", "bump digest", newContent, "blobsha123"); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	if gotBody["sha"] != "blobsha123" {
		t.Fatalf("request sha = %v, want blobsha123", gotBody["sha"])
	}
	decoded, err := base64.StdEncoding.DecodeString(gotBody["content"].(string))
	if err != nil {
		t.Fatalf("decode sent content: %v", err)
	}
	if string(decoded) != string(newContent) {
		t.Fatalf("sent content = %q, want %q", decoded, newContent)
	}
}

func TestPutFile_ErrorResponseSurfaced(t *testing.T) {
	server := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sha mismatch", http.StatusConflict)
	})
	defer server.Close()

	c := newTestClient(t, server)
	err := c.PutFile(context.Background(), "acme/deploy", "", "app/service.yaml", "bump digest", []byte("x"), "stale-sha")
	if err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}
