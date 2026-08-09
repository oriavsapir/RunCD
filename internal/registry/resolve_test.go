package registry

import "testing"

func TestResolve_Track(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}, {Name: "stable", Digest: "sha256:bbb"}}
	got, err := Resolve(tags, "stable", "", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:bbb" {
		t.Fatalf("got %q, want sha256:bbb", got)
	}
}

func TestResolve_TrackNotFound(t *testing.T) {
	tags := []Tag{{Name: "latest", Digest: "sha256:aaa"}}
	if _, err := Resolve(tags, "stable", "", "myapp"); err == nil {
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
	got, err := Resolve(tags, "", "1.2", "myapp")
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
	got, err := Resolve(tags, "", "1", "myapp")
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
	got, err := Resolve(tags, "", "1.2.1", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:one" {
		t.Fatalf("got %q, want sha256:one", got)
	}
}

func TestResolve_VersionNoMatch(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := Resolve(tags, "", "2.0", "myapp"); err == nil {
		t.Fatal("expected error when no tag satisfies the constraint")
	}
}

func TestResolve_InvalidVersionConstraint(t *testing.T) {
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	if _, err := Resolve(tags, "", "not-a-version", "myapp"); err == nil {
		t.Fatal("expected error for a malformed version constraint")
	}
}

func TestResolve_PrefersPerServiceTagWhenConfirmedCurrent(t *testing.T) {
	// The per-service tag is trusted once its digest matches what the
	// bare/global tag resolves to — confirming it actually reflects the
	// image's current build, not just a higher-looking version number.
	tags := []Tag{
		{Name: "v0.333.0", Digest: "sha256:current"},
		{Name: "widget-service-v0.323.1", Digest: "sha256:current"},
	}
	got, err := Resolve(tags, "", "0", "widget-service")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:current" {
		t.Fatalf("got %q, want sha256:current", got)
	}
}

// TestResolve_FallsBackToBareTagWhenPrefixedTagIsStale regression-tests the
// actual production incident this guards against: a monorepo's per-service
// the-tagging-tool bump only fires on a commit touching that service's own path —
// a service that only changed via a shared lib gets rebuilt and re-tagged
// :latest/global, but its own prefixed tag doesn't move. Trusting the
// prefixed tag here would silently commit a real regression (this exact
// scenario rolled two services backward in production before being caught
// and reverted). The bare/global tag — which the global bump step re-tags
// onto every image's :latest on every merge, whether or not that image
// changed — is the one signal here that's actually still current.
func TestResolve_FallsBackToBareTagWhenPrefixedTagIsStale(t *testing.T) {
	tags := []Tag{
		{Name: "v0.333.0", Digest: "sha256:current"},            // re-tagged onto :latest every merge
		{Name: "widget-service-v0.1.0", Digest: "sha256:stale"}, // last real per-service bump, since superseded
	}
	got, err := Resolve(tags, "", "0", "widget-service")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:current" {
		t.Fatalf("got %q, want sha256:current (bare/global tag) — must not regress to the stale prefixed tag", got)
	}
}

func TestResolve_PerServiceTagPicksHighest(t *testing.T) {
	tags := []Tag{
		{Name: "widget-service-v0.323.1", Digest: "sha256:old"},
		{Name: "widget-service-v0.324.0", Digest: "sha256:new"},
		{Name: "silver-gold-to-shared-v9.9.9", Digest: "sha256:other"}, // different service, must not match
	}
	got, err := Resolve(tags, "", "0", "widget-service")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:new" {
		t.Fatalf("got %q, want sha256:new (0.324.0)", got)
	}
}

// TestResolve_TrustsPrefixedTagOutrightWhenNoBareTagExists covers the one
// case with nothing to compare a prefixed tag's digest against: an image
// that only ever gets its own per-service tag, with no repo-wide/global
// bump step touching it at all. There, a prefixed match is trusted outright
// rather than falling through to "no tag satisfies version" — a bare tag's
// absence isn't evidence the prefixed one is stale, just that this image
// isn't part of a monorepo's global tagging convention.
func TestResolve_TrustsPrefixedTagOutrightWhenNoBareTagExists(t *testing.T) {
	tags := []Tag{{Name: "widget-service-v0.323.1", Digest: "sha256:only"}}
	got, err := Resolve(tags, "", "0", "widget-service")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:only" {
		t.Fatalf("got %q, want sha256:only (trusted outright, no bare tag to compare against)", got)
	}
}

func TestResolve_FallsBackToBareTagsWhenNoPrefixedTagExists(t *testing.T) {
	// An app not using the per-service monorepo convention keeps resolving
	// bare version tags exactly as before.
	tags := []Tag{{Name: "1.2.1", Digest: "sha256:one"}}
	got, err := Resolve(tags, "", "1.2.1", "myapp")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "sha256:one" {
		t.Fatalf("got %q, want sha256:one", got)
	}
}
