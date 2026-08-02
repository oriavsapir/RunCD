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
// resolved digest instead of a floating tag).
var digestLine = regexp.MustCompile(`(?m)^(\s*digest:\s*"?)sha256:[0-9a-f]{64}("?\s*)$`)

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
