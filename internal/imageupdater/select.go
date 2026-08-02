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
// erroring, since a real image repo mixes both kinds of tags.
func resolve(tags []Tag, track, version string) (string, error) {
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

	var bestDigest, bestVersion string
	for _, t := range tags {
		v := canonicalSemver(t.Name)
		if !semver.IsValid(v) {
			continue
		}
		var prefix string
		switch depth {
		case 0:
			prefix = semver.Major(v)
		case 1:
			prefix = semver.MajorMinor(v)
		default:
			prefix = semver.Canonical(v)
		}
		if prefix != constraint {
			continue
		}
		if bestVersion == "" || semver.Compare(v, bestVersion) > 0 {
			bestVersion, bestDigest = v, t.Digest
		}
	}
	if bestDigest == "" {
		return "", fmt.Errorf("no tag satisfies version %q", version)
	}
	return bestDigest, nil
}

// canonicalSemver adds the "v" prefix golang.org/x/mod/semver requires —
// real image tags are overwhelmingly bare ("1.2.3"), not "v1.2.3".
func canonicalSemver(s string) string {
	if strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}
