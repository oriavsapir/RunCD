package imageupdater

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// resolve picks the digest track or version (exactly one is non-empty, per
// manifest.Parse's validation) selects among tags. track is a literal tag
// name to follow; version is a semver constraint ("1", "1.2", or "1.2.3")
// satisfied by the highest matching semver tag — tags that aren't valid
// semver (e.g. "latest", "main-abc123") are silently skipped rather than
// erroring, since a real image repo mixes both kinds of tags. imageName is
// the image's own last repository path segment (e.g. "a-real-etl-job"),
// used to prefer per-service monorepo tags (see below).
func resolve(tags []Tag, track, version, imageName string) (string, error) {
	if track != "" {
		for _, t := range tags {
			if t.Name == track {
				return t.Digest, nil
			}
		}
		return "", fmt.Errorf("no tag named %q found", track)
	}

	constraint := canonicalSemver(version)
	if !semver.IsValid(constraint) {
		return "", fmt.Errorf("version %q is not a valid semver constraint", version)
	}
	depth := strings.Count(strings.TrimPrefix(constraint, "v"), ".")
	if depth >= 2 {
		constraint = semver.Canonical(constraint) // normalize e.g. missing patch/build the same way tag comparisons are
	}

	// A monorepo CI commonly pushes per-service tags ("a-real-etl-job-v1.2.3")
	// alongside a repo-wide release tag ("v1.2.3", from a separate global bump
	// step) on the very same image, since the global step tags every image's
	// current :latest regardless of which service actually changed. If any tag
	// carries this image's own prefix, only those count — falling through to
	// bare tags here would let that unrelated, ever-climbing global tag quietly
	// outrank the real per-service version on every merge.
	if digest := bestMatch(tags, imageName+"-", depth, constraint); digest != "" {
		return digest, nil
	}
	if digest := bestMatch(tags, "", depth, constraint); digest != "" {
		return digest, nil
	}
	return "", fmt.Errorf("no tag satisfies version %q", version)
}

// bestMatch returns the digest of the highest tag matching constraint at the
// given depth, after stripping prefix from each tag name — tags that don't
// carry prefix (when non-empty) are skipped entirely, not merely deprioritized.
func bestMatch(tags []Tag, prefix string, depth int, constraint string) string {
	var bestDigest, bestVersion string
	for _, t := range tags {
		name := t.Name
		if prefix != "" {
			n, ok := strings.CutPrefix(name, prefix)
			if !ok {
				continue
			}
			name = n
		}
		v := canonicalSemver(name)
		if !semver.IsValid(v) {
			continue
		}
		var p string
		switch depth {
		case 0:
			p = semver.Major(v)
		case 1:
			p = semver.MajorMinor(v)
		default:
			p = semver.Canonical(v)
		}
		if p != constraint {
			continue
		}
		if bestVersion == "" || semver.Compare(v, bestVersion) > 0 {
			bestVersion, bestDigest = v, t.Digest
		}
	}
	return bestDigest
}

// canonicalSemver adds the "v" prefix golang.org/x/mod/semver requires —
// real image tags are overwhelmingly bare ("1.2.3"), not "v1.2.3".
func canonicalSemver(s string) string {
	if strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}
