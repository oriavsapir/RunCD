package imageupdater

import (
	"strings"
	"testing"
)

const oldDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"
const newDigest = "sha256:4a9b2d1111111111111111111111111111111111111111111111111111111111"

func TestRewriteDigest_ReplacesOnlyTheDigestValue(t *testing.T) {
	data := []byte(`resourceType: service
image:
  digest: ` + oldDigest + `
  version: "1.2"
  repository: us-central1-docker.pkg.dev/proj/repo/image
env:
  FOO: bar
`)
	got, err := rewriteDigest(data, newDigest)
	if err != nil {
		t.Fatalf("rewriteDigest: %v", err)
	}
	if strings.Contains(string(got), oldDigest) {
		t.Fatalf("old digest still present: %s", got)
	}
	if !strings.Contains(string(got), newDigest) {
		t.Fatalf("new digest missing: %s", got)
	}
	// Everything else must be byte-identical.
	wantOther := []string{"resourceType: service", `version: "1.2"`, "repository: us-central1-docker.pkg.dev/proj/repo/image", "FOO: bar"}
	for _, line := range wantOther {
		if !strings.Contains(string(got), line) {
			t.Fatalf("expected unrelated line %q to survive rewrite, got:\n%s", line, got)
		}
	}
}

func TestRewriteDigest_NoDigestLineErrors(t *testing.T) {
	data := []byte("resourceType: service\n")
	if _, err := rewriteDigest(data, newDigest); err == nil {
		t.Fatal("expected error when no digest: line is present")
	}
}

func TestRewriteDigest_GuardCatchesMismatch(t *testing.T) {
	// A malformed newDigest (e.g. not matching manifest.Parse's digest
	// pattern) must be caught by the re-parse guard, not silently written.
	data := []byte("image:\n  digest: " + oldDigest + "\n")
	if _, err := rewriteDigest(data, "sha256:not-valid"); err == nil {
		t.Fatal("expected the re-parse guard to reject an invalid resulting digest")
	}
}
