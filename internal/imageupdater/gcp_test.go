package imageupdater

import "testing"

func TestParseRepository(t *testing.T) {
	parent, imagePath, err := parseRepository("us-central1-docker.pkg.dev/proj/repo/myimage")
	if err != nil {
		t.Fatalf("parseRepository: %v", err)
	}
	if parent != "projects/proj/locations/us-central1/repositories/repo" {
		t.Fatalf("parent = %q", parent)
	}
	if imagePath != "proj/repo/myimage" {
		t.Fatalf("imagePath = %q", imagePath)
	}
}

func TestParseRepository_NestedImagePath(t *testing.T) {
	_, imagePath, err := parseRepository("us-central1-docker.pkg.dev/proj/repo/team/myimage")
	if err != nil {
		t.Fatalf("parseRepository: %v", err)
	}
	if imagePath != "proj/repo/team/myimage" {
		t.Fatalf("imagePath = %q", imagePath)
	}
}

func TestParseRepository_Invalid(t *testing.T) {
	if _, _, err := parseRepository("not-a-valid-repository"); err == nil {
		t.Fatal("expected error for a malformed repository string")
	}
}

func TestSplitDigest(t *testing.T) {
	digest, path, ok := splitDigest("us-central1-docker.pkg.dev/proj/repo/myimage@sha256:abc")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if digest != "sha256:abc" {
		t.Fatalf("digest = %q", digest)
	}
	if path != "proj/repo/myimage" {
		t.Fatalf("path = %q", path)
	}
}

func TestSplitDigest_NoAtSign(t *testing.T) {
	if _, _, ok := splitDigest("us-central1-docker.pkg.dev/proj/repo/myimage"); ok {
		t.Fatal("expected ok=false when the URI has no digest")
	}
}
