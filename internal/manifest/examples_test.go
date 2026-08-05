package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExamplesParse guards examples/full/services/*/service.yaml against
// silent rot — see internal/config's TestExamplesParse for the equivalent
// on examples/*/runcd.yaml.
func TestExamplesParse(t *testing.T) {
	matches, err := filepath.Glob("../../examples/full/services/*/service.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no examples/full/services/*/service.yaml files found — did the glob path change?")
	}
	for _, path := range matches {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			data, err := os.ReadFile(path) // #nosec G304 -- path comes from filepath.Glob above, not external input
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(data); err != nil {
				t.Fatalf("Parse(%s): %v", path, err)
			}
		})
	}
}
