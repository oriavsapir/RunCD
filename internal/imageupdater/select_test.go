package imageupdater

import "testing"

func TestResolve_Track(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}, {Name: "stable", Digest: "sha256:bbb"}}
	got, err := resolve(tags, "stable", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:bbb" {
		t.Fatalf("got %q, want sha256:bbb", got)
	}
}

func TestResolve_TrackNotFound(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}}
	if _, err := resolve(tags, "stable", ""); err == nil {
		t.Fatal("expected error for a track with no matching tag")
	}
}

func TestResolve_VersionPicksHighestPatch(t *testing.T) {
	tags := []Tag{
		{Name: "1.2.1", Digest: "sha256:one"},
		{Name: "1.2.9", Digest: "sha256:two"},
		{Name: "1.3.0", Digest: "sha256:three"}, // different minor, excluded by "1.2" constraint
		{Name: "latest", Digest: "sha256:four"}, // not semver, skipped
	}
	got, err := resolve(tags, "", "1.2")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:two" {
		t.Fatalf("got %q, want sha256:two (1.2.9)", got)
	}
}

func TestResolve_VersionMajorOnly(t *testing.T) {
	tags := []Tag{
		{Name: "1.2.1", Digest: "sha256:one"},
		{Name: "1.9.0", Digest: "sha256:two"},
		{Name: "2.0.0", Digest: "sha256:three"},
	}
	got, err := resolve(tags, "", "1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:two" {
		t.Fatalf("got %q, want sha256:two (1.9.0)", got)
	}
}

func TestResolve_VersionExact(t *testing.T) {
	tags := []Tag{
		{Name: "1.2.1", Digest: "sha256:one"},
		{Name: "1.2.9", Digest: "sha256:two"},
	}
	got, err := resolve(tags, "", "1.2.1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:one" {
		t.Fatalf("got %q, want sha256:one", got)
	}
}

func TestResolve_VersionNoMatch(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := resolve(tags, "", "2.0"); err == nil {
		t.Fatal("expected error when no tag satisfies the constraint")
	}
}

func TestResolve_InvalidVersionConstraint(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := resolve(tags, "", "not-a-version"); err == nil {
		t.Fatal("expected error for a malformed version constraint")
	}
}
