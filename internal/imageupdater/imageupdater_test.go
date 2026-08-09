package imageupdater

import (
	"context"
	"errors"
	"testing"
)

type fakeResolver struct {
	tags []Tag
	err  error
}

func (f fakeResolver) ListTags(ctx context.Context, repository string) ([]Tag, error) {
	return f.tags, f.err
}

type fakeGitHub struct {
	content    []byte
	sha        string
	getErr     error
	putErr     error
	putCalls   int
	lastPut    []byte
	lastPutSHA string
	lastGetRef string
	lastPutRef string
}

func (f *fakeGitHub) GetFileWithSHA(ctx context.Context, repo, ref, path string) ([]byte, string, error) {
	f.lastGetRef = ref
	return f.content, f.sha, f.getErr
}

func (f *fakeGitHub) PutFile(ctx context.Context, repo, branch, path, message string, content []byte, sha string) error {
	f.putCalls++
	f.lastPut = content
	f.lastPutSHA = sha
	f.lastPutRef = branch
	return f.putErr
}

const digestA = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const digestB = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func manifestYAML() []byte {
	return []byte("image:\n  digest: " + digestA + "\n  track: stable\n  repository: us-central1-docker.pkg.dev/proj/repo/image\n")
}

func TestUpdate_NoTrackOrVersionIsNoOp(t *testing.T) {
	gh := &fakeGitHub{content: []byte("image:\n  digest: " + digestA + "\n"), sha: "blobsha"}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	got, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got != "" {
		t.Fatalf("expected no-op, got digest %q", got)
	}
	if gh.putCalls != 0 {
		t.Fatalf("expected no commit, got %d", gh.putCalls)
	}
}

func TestUpdate_DigestUnchangedIsNoOp(t *testing.T) {
	gh := &fakeGitHub{content: manifestYAML(), sha: "blobsha"}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestA}}}

	got, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got != "" {
		t.Fatalf("expected no-op when resolved digest matches committed digest, got %q", got)
	}
	if gh.putCalls != 0 {
		t.Fatalf("expected no commit, got %d", gh.putCalls)
	}
}

func TestUpdate_CommitsResolvedDigestChange(t *testing.T) {
	gh := &fakeGitHub{content: manifestYAML(), sha: "blobsha"}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	got, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got != digestB {
		t.Fatalf("got %q, want %q", got, digestB)
	}
	if gh.putCalls != 1 {
		t.Fatalf("expected exactly one commit, got %d", gh.putCalls)
	}
	if gh.lastPutSHA != "blobsha" {
		t.Fatalf("expected PutFile to carry the fetched blob sha, got %q", gh.lastPutSHA)
	}
	if string(gh.lastPut) == string(manifestYAML()) {
		t.Fatal("expected committed content to differ from the original")
	}
}

// TestUpdate_UsesManifestBranchForBothReadAndWrite checks that a non-default
// Manifest.Branch reaches both GetFileWithSHA's ref and PutFile's branch —
// reading from one branch but committing to another would silently diverge
// the two.
func TestUpdate_UsesManifestBranchForBothReadAndWrite(t *testing.T) {
	gh := &fakeGitHub{content: manifestYAML(), sha: "blobsha"}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	if _, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml", Branch: "staging"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gh.lastGetRef != "staging" {
		t.Fatalf("expected GetFileWithSHA ref=staging, got %q", gh.lastGetRef)
	}
	if gh.lastPutRef != "staging" {
		t.Fatalf("expected PutFile branch=staging, got %q", gh.lastPutRef)
	}
}

func TestUpdate_ResolveErrorSurfaced(t *testing.T) {
	gh := &fakeGitHub{content: manifestYAML(), sha: "blobsha"}
	resolver := fakeResolver{tags: nil} // no tag named "stable"

	if _, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"}); err == nil {
		t.Fatal("expected error when the tracked tag can't be resolved")
	}
	if gh.putCalls != 0 {
		t.Fatalf("expected no commit on resolve failure, got %d", gh.putCalls)
	}
}

// TestUpdate_FetchErrorSurfaced covers GetFileWithSHA itself failing (e.g. a
// transient GitHub API error, or the manifest having been deleted).
func TestUpdate_FetchErrorSurfaced(t *testing.T) {
	gh := &fakeGitHub{getErr: errors.New("boom")}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	if _, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"}); err == nil {
		t.Fatal("expected error when the manifest fetch fails")
	}
	if gh.putCalls != 0 {
		t.Fatalf("expected no commit on fetch failure, got %d", gh.putCalls)
	}
}

// TestUpdate_UnparsableManifestSurfacesError covers a manifest that fails
// manifest.Parse (malformed YAML, or one that fails §5.1's validation) —
// Update must not attempt to resolve or commit against it.
func TestUpdate_UnparsableManifestSurfacesError(t *testing.T) {
	gh := &fakeGitHub{content: []byte("not: [valid"), sha: "blobsha"}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	if _, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"}); err == nil {
		t.Fatal("expected error for a manifest that fails to parse")
	}
	if gh.putCalls != 0 {
		t.Fatalf("expected no commit for an unparsable manifest, got %d", gh.putCalls)
	}
}

// TestUpdate_CommitErrorSurfaced covers PutFile failing — the in-scope half
// of "Contents:write permission missing should fail per-manifest, not
// crash": Update must return the wrapped error rather than panicking or
// silently swallowing it. The "logged, not crash" half (the caller not
// aborting the whole reconcile pass) lives in cmd/controller, out of scope
// for this package.
func TestUpdate_CommitErrorSurfaced(t *testing.T) {
	gh := &fakeGitHub{content: manifestYAML(), sha: "blobsha", putErr: errors.New("403: Resource not accessible by integration")}
	resolver := fakeResolver{tags: []Tag{{Name: "stable", Digest: digestB}}}

	_, err := Update(context.Background(), gh, resolver, Manifest{Repo: "acme/deploy", Path: "app/service.yaml"})
	if err == nil {
		t.Fatal("expected error when PutFile fails")
	}
	if gh.putCalls != 1 {
		t.Fatalf("expected exactly one attempted commit, got %d", gh.putCalls)
	}
}
