package folders

import (
	"context"
	"errors"
	"testing"

	"github.com/runcd/runcd/internal/config"
)

// fakeResolver is a simple in-memory Resolver for tests.
type fakeResolver struct {
	byFolder map[string][]string
	err      map[string]error
}

func (f *fakeResolver) ProjectsInFolder(_ context.Context, folderID string) ([]string, error) {
	if err, ok := f.err[folderID]; ok {
		return nil, err
	}
	return f.byFolder[folderID], nil
}

func TestResolveConfig_MergesFolderProjectsIntoEnvironment(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"123": {"folder-project-a", "folder-project-b"},
	}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"explicit-project"}, Folders: []string{"123"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), resolver, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 3 {
		t.Fatalf("expected 3 projects (1 explicit + 2 from folder), got %+v", got)
	}
}

func TestResolveConfig_DedupesProjectListedBothExplicitlyAndViaFolder(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"123": {"shared-project", "folder-only-project"},
	}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"shared-project"}, Folders: []string{"123"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), resolver, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 2 {
		t.Fatalf("expected shared-project deduped, got %+v", got)
	}
}

func TestResolveConfig_NoFoldersLeavesEnvironmentUnchanged(t *testing.T) {
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Projects: []string{"explicit-project"}},
		},
	}
	resolved, err := ResolveConfig(context.Background(), &fakeResolver{}, root)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	got := resolved.Environments["prd"].Projects
	if len(got) != 1 || got[0] != "explicit-project" {
		t.Fatalf("expected unchanged project list, got %+v", got)
	}
}

func TestResolveConfig_ResolverErrorPropagatesWithEnvironmentContext(t *testing.T) {
	boom := errors.New("boom")
	resolver := &fakeResolver{err: map[string]error{"123": boom}}
	root := &config.Root{
		Environments: map[string]config.Environment{
			"prd": {Folders: []string{"123"}},
		},
	}
	_, err := ResolveConfig(context.Background(), resolver, root)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the underlying error wrapped, got %v", err)
	}
}

func TestResolveMembership_ResolvesEachDistinctFolder(t *testing.T) {
	resolver := &fakeResolver{byFolder: map[string][]string{
		"111": {"a", "b"},
		"222": {"c"},
	}}
	membership, err := ResolveMembership(context.Background(), resolver, []string{"111", "222", "111"})
	if err != nil {
		t.Fatalf("ResolveMembership: %v", err)
	}
	if len(membership) != 2 {
		t.Fatalf("expected 2 distinct folders resolved, got %+v", membership)
	}
	if len(membership["111"]) != 2 || len(membership["222"]) != 1 {
		t.Fatalf("unexpected membership: %+v", membership)
	}
}
