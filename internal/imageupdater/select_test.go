package imageupdater

import "testing"

func TestResolve_Track(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}, {Name: "stable", Digest: "sha256:bbb"}}
	got, err := resolve(tags, "stable", "", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:bbb" {
		t.Fatalf("got %q, want sha256:bbb", got)
	}
}

func TestResolve_TrackNotFound(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}}
	if _, err := resolve(tags, "stable", "", "myapp"); err == nil {
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
	got, err := resolve(tags, "", "1.2", "myapp")
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
	got, err := resolve(tags, "", "1", "myapp")
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
	got, err := resolve(tags, "", "1.2.1", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:one" {
		t.Fatalf("got %q, want sha256:one", got)
	}
}

func TestResolve_VersionNoMatch(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := resolve(tags, "", "2.0", "myapp"); err == nil {
		t.Fatal("expected error when no tag satisfies the constraint")
	}
}

func TestResolve_InvalidVersionConstraint(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := resolve(tags, "", "not-a-version", "myapp"); err == nil {
		t.Fatal("expected error for a malformed version constraint")
	}
}

func TestResolve_PrefersPerServiceTagOverGlobalTag(t *testing.T) {
	// A global cog-bump step tags every image's :latest with the repo-wide
	// release version on every merge, regardless of whether that particular
	// service changed — it must never outrank a real per-service tag.
	tags := []Tag{
		{Name: "v0.333.0", Digest: "sha256:global"},                   // global tag, climbs every merge
		{Name: "a-real-etl-job-v0.323.1", Digest: "sha256:service"}, // real per-service tag, lower number
	}
	got, err := resolve(tags, "", "0", "a-real-etl-job")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:service" {
		t.Fatalf("got %q, want sha256:service (the prefixed per-service tag)", got)
	}
}

func TestResolve_PerServiceTagPicksHighest(t *testing.T) {
	tags := []Tag{
		{Name: "a-real-etl-job-v0.323.1", Digest: "sha256:old"},
		{Name: "a-real-etl-job-v0.324.0", Digest: "sha256:new"},
		{Name: "silver-gold-to-shared-v9.9.9", Digest: "sha256:other"}, // different service, must not match
	}
	got, err := resolve(tags, "", "0", "a-real-etl-job")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:new" {
		t.Fatalf("got %q, want sha256:new (0.324.0)", got)
	}
}

func TestResolve_FallsBackToBareTagsWhenNoPrefixedTagExists(t *testing.T) {
	// An app not using the per-service monorepo convention keeps resolving
	// bare version tags exactly as before.
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	got, err := resolve(tags, "", "1.2.1", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:one" {
		t.Fatalf("got %q, want sha256:one", got)
	}
}
