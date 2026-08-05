package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExamplesParse guards examples/*/runcd.yaml against silent rot: a
// schema change (new required field, renamed key, tightened validation)
// that breaks one of these must fail CI here, not wait for a user to
// notice the docs no longer match the code.
func TestExamplesParse(t *testing.T) {
	matches, err := filepath.Glob("../../examples/*/runcd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no examples/*/runcd.yaml files found — did the glob path change?")
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
