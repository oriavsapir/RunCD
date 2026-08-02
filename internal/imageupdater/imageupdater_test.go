package imageupdater

import (
	"context"
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
}

func (f *fakeGitHub) GetFileWithSHA(ctx context.Context, repo, ref, path string) ([]byte, string, error) {
	return f.content, f.sha, f.getErr
}

func (f *fakeGitHub) PutFile(ctx context.Context, repo, branch, path, message string, content []byte, sha string) error {
	f.putCalls++
	f.lastPut = content
	f.lastPutSHA = sha
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
