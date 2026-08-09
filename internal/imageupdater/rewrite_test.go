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

// TestRewriteDigest_MultipleDigestLinesErrors covers the len(matches) > 1
// branch — regexp.FindAllIndex's "expected exactly one" guard, not just the
// zero-match case already covered above.
func TestRewriteDigest_MultipleDigestLinesErrors(t *testing.T) {
	data := []byte("image:\n  digest: " + oldDigest + "\nother:\n  digest: " + oldDigest + "\n")
	if _, err := rewriteDigest(data, newDigest); err == nil {
		t.Fatal("expected error when more than one digest: line is present")
	}
}

func TestRewriteDigest_DoubleQuotedDigestPreservesQuoting(t *testing.T) {
	data := []byte(`image:
  digest: "` + oldDigest + `"
  track: stable
  repository: us-central1-docker.pkg.dev/proj/repo/image
`)
	got, err := rewriteDigest(data, newDigest)
	if err != nil {
		t.Fatalf("rewriteDigest: %v", err)
	}
	want := `image:
  digest: "` + newDigest + `"
  track: stable
  repository: us-central1-docker.pkg.dev/proj/repo/image
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteDigest_SingleQuotedDigestPreservesQuoting(t *testing.T) {
	data := []byte("image:\n  digest: '" + oldDigest + "'\n")
	got, err := rewriteDigest(data, newDigest)
	if err != nil {
		t.Fatalf("rewriteDigest: %v", err)
	}
	want := "image:\n  digest: '" + newDigest + "'\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteDigest_TrailingCommentPreserved(t *testing.T) {
	data := []byte("image:\n  digest: " + oldDigest + " # pinned by imageupdater\n")
	got, err := rewriteDigest(data, newDigest)
	if err != nil {
		t.Fatalf("rewriteDigest: %v", err)
	}
	want := "image:\n  digest: " + newDigest + " # pinned by imageupdater\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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
