// External test package (config_test, not config): expander imports
// config, so this check would be an import cycle from inside package
// config itself.
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/expander"
)

// TestExamplesExpand guards examples/*/runcd.yaml one level past parsing:
// each must also expand into at least one real sync unit with no error
// (region resolution, override/exclude collisions, unknown-env references,
// etc.) — a config that parses but can't expand is exactly the kind of
// break TestExamplesParse alone wouldn't catch.
func TestExamplesExpand(t *testing.T) {
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
			root, err := config.Parse(data)
			if err != nil {
				t.Fatalf("Parse(%s): %v", path, err)
			}
			units, err := expander.Expand(root)
			if err != nil {
				t.Fatalf("Expand(%s): %v", path, err)
			}
			if len(units) == 0 {
				t.Fatalf("Expand(%s): expected at least one sync unit", path)
			}
		})
	}
}
