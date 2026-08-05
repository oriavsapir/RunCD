package imageupdater

import (
	"fmt"
	"regexp"

	"github.com/runcd/runcd/internal/manifest"
)

// digestLine matches a service.yaml's "digest: sha256:<64 hex>" line,
// capturing the surrounding whitespace/quoting so replacement only ever
// touches the hex value — never re-marshaling the YAML (which would reorder
// keys, drop comments, and restyle quoting, turning a one-line digest bump
// into a whole-file diff that defeats the whole point of writing back a
// resolved digest instead of a floating tag). Allows either quote style
// manifest.Parse itself accepts (YAML permits single- or double-quoted
// scalars interchangeably) and an optional trailing "# comment", so a
// manifest using either doesn't silently stop resolving forever — RE2 (this
// package's regexp) has no backreferences, so the opening/closing quote
// groups aren't required to match each other; a genuine mismatch there is
// still caught below by the re-parse round-trip check.
var digestLine = regexp.MustCompile(`(?m)^(\s*digest:\s*["']?)sha256:[0-9a-f]{64}(["']?\s*(?:#.*)?)$`)

// rewriteDigest replaces the single digest: value in data with newDigest,
// then re-parses the result and asserts it round-trips to exactly
// newDigest — the guard against the regex having matched the wrong line (or
// not matched at all).
func rewriteDigest(data []byte, newDigest string) ([]byte, error) {
	matches := digestLine.FindAllIndex(data, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected exactly one digest: line, found %d", len(matches))
	}
	rewritten := digestLine.ReplaceAll(data, []byte("${1}"+newDigest+"${2}"))

	sd, err := manifest.Parse(rewritten)
	if err != nil {
		return nil, fmt.Errorf("rewritten manifest failed to parse: %w", err)
	}
	if sd.Image.Digest != newDigest {
		return nil, fmt.Errorf("rewritten manifest has digest %q, expected %q", sd.Image.Digest, newDigest)
	}
	return rewritten, nil
}
