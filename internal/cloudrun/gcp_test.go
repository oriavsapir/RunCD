package cloudrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/runcd/runcd/internal/registry"
)

var errBoom = errors.New("boom")

func TestValidatedPercent_FullCutoverAccepted(t *testing.T) {
	got, err := validatedPercent(100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 100 {
		t.Fatalf("got %d", got)
	}
}

// TestValidatedPercent_ZeroRejected regression-tests a bug where 0 was
// accepted as a "full cutover" alongside 100, but a single TrafficTarget at
// 0% doesn't sum to the 100% Cloud Run requires — it's just as invalid as
// any other partial percent, for the same reason (no way to say where the
// rest of the traffic goes).
func TestValidatedPercent_ZeroRejected(t *testing.T) {
	if _, err := validatedPercent(0); err == nil {
		t.Fatal("expected error for percent=0 — a lone 0% target doesn't sum to 100")
	}
}

func TestValidatedPercent_PartialRejected(t *testing.T) {
	for _, p := range []int{1, 50, 99} {
		if _, err := validatedPercent(p); err == nil {
			t.Fatalf("percent=%d: expected error — v1 only supports a full cutover", p)
		}
	}
}

// TestDigestSuffix_ExtractsFromFullImageReference regression-tests the
// bare-digest-vs-full-image-reference bug: manifest digests are always
// bare (sha256:...), but a real Cloud Run container's Image field is a
// full reference (repo@sha256:...) — comparing them directly never
// matches, so ServiceState.ImageDigest must always hold just the suffix.
func TestDigestSuffix_ExtractsFromFullImageReference(t *testing.T) {
	full := "us-docker.pkg.dev/proj/repo/svc@sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"
	got := digestSuffix(full)
	want := "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDigestSuffix_BareDigestPassesThrough(t *testing.T) {
	bare := "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"
	if got := digestSuffix(bare); got != bare {
		t.Fatalf("got %q, want %q", got, bare)
	}
}

func TestWithDigest_PreservesRepoPrefix(t *testing.T) {
	existing := "us-docker.pkg.dev/proj/repo/svc@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := withDigest(existing, newDigest)
	want := "us-docker.pkg.dev/proj/repo/svc@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestWithDigest_StripsExistingTagInsteadOfAppending regression-tests a real
// bug: a resource last deployed by something other than RunCD (e.g. an
// external CI pipeline via `gcloud run deploy --image foo:v1`) has an
// existing image that's tag-referenced, not digest-referenced. Splicing the
// desired digest on without stripping that tag produced a malformed
// "repo:tag@sha256:..." reference instead of "repo@sha256:...".
func TestWithDigest_StripsExistingTagInsteadOfAppending(t *testing.T) {
	existing := "us-docker.pkg.dev/proj/repo/svc:v0.332.0"
	newDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := withDigest(existing, newDigest)
	want := "us-docker.pkg.dev/proj/repo/svc@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWithDigest_NoTagOrDigestUsesImageAsIs(t *testing.T) {
	existing := "us-docker.pkg.dev/proj/repo/svc"
	newDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := withDigest(existing, newDigest)
	want := "us-docker.pkg.dev/proj/repo/svc@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsDigest_ValidBareDigest(t *testing.T) {
	if !isDigest("sha256:" + hex64) {
		t.Fatal("expected a valid bare sha256 digest to be recognized")
	}
}

func TestIsDigest_RejectsTagLookingString(t *testing.T) {
	for _, s := range []string{
		"v1.2.3",
		"us-docker.pkg.dev/proj/repo/svc:v1.2.3",
		"sha256:tooshort",
		"",
	} {
		if isDigest(s) {
			t.Fatalf("isDigest(%q) = true, want false", s)
		}
	}
}

const hex64 = "3f8a1c0000000000000000000000000000000000000000000000000000000000"

func TestSplitImageRef_TagAfterLastSlash(t *testing.T) {
	repo, tag := splitImageRef("us-docker.pkg.dev/proj/repo/svc:v1.2.3")
	if repo != "us-docker.pkg.dev/proj/repo/svc" || tag != "v1.2.3" {
		t.Fatalf("got repo=%q tag=%q", repo, tag)
	}
}

func TestSplitImageRef_NoTagDefaultsToLatest(t *testing.T) {
	repo, tag := splitImageRef("us-docker.pkg.dev/proj/repo/svc")
	if repo != "us-docker.pkg.dev/proj/repo/svc" || tag != "latest" {
		t.Fatalf("got repo=%q tag=%q", repo, tag)
	}
}

func TestSplitImageRef_ColonBeforeLastSlashIsNotATag(t *testing.T) {
	// A registry host:port, not a tag separator — the colon precedes the
	// last "/", so the whole string is the repository and tag is "latest".
	repo, tag := splitImageRef("localhost:5000/repo/svc")
	if repo != "localhost:5000/repo/svc" || tag != "latest" {
		t.Fatalf("got repo=%q tag=%q", repo, tag)
	}
}

type fakeTagResolver struct {
	tags []registry.Tag
	err  error
}

func (f *fakeTagResolver) ListTags(ctx context.Context, repository string) ([]registry.Tag, error) {
	return f.tags, f.err
}

func TestResolveTag_FindsMatchingTag(t *testing.T) {
	r := &fakeTagResolver{tags: []registry.Tag{
		{Name: "v1.2.2", Digest: "sha256:old"},
		{Name: "v1.2.3", Digest: "sha256:new"},
	}}
	got, err := resolveTag(context.Background(), r, "us-docker.pkg.dev/proj/repo/svc:v1.2.3")
	if err != nil {
		t.Fatalf("resolveTag: %v", err)
	}
	if got != "sha256:new" {
		t.Fatalf("got %q, want sha256:new", got)
	}
}

func TestResolveTag_NoMatchingTagErrors(t *testing.T) {
	r := &fakeTagResolver{tags: []registry.Tag{{Name: "v1.2.2", Digest: "sha256:old"}}}
	if _, err := resolveTag(context.Background(), r, "us-docker.pkg.dev/proj/repo/svc:v9.9.9"); err == nil {
		t.Fatal("expected error when no tag matches")
	}
}

func TestResolveTag_ListErrorPropagates(t *testing.T) {
	r := &fakeTagResolver{err: errBoom}
	if _, err := resolveTag(context.Background(), r, "us-docker.pkg.dev/proj/repo/svc:v1"); err == nil {
		t.Fatal("expected the underlying list error to propagate")
	}
}

func TestExecutionStatus_StillRunningWhenNoCompletionTime(t *testing.T) {
	exec := &runpb.Execution{}
	if got := executionStatus(exec); got != ExecutionRunning {
		t.Fatalf("expected ExecutionRunning, got %s", got)
	}
}

func TestExecutionStatus_FailedWhenCompletedWithFailures(t *testing.T) {
	exec := &runpb.Execution{
		CompletionTime: timestamppb.New(time.Now()),
		FailedCount:    1,
	}
	if got := executionStatus(exec); got != ExecutionFailed {
		t.Fatalf("expected ExecutionFailed, got %s", got)
	}
}

// TestExecutionStatus_CancelledIsFailed regression-tests a bug where a
// cancelled execution (CompletionTime set, FailedCount == 0,
// CancelledCount > 0) fell through the FailedCount check straight to
// ExecutionSucceeded, misreporting a cancelled job as healthy.
func TestExecutionStatus_CancelledIsFailed(t *testing.T) {
	exec := &runpb.Execution{
		CompletionTime: timestamppb.New(time.Now()),
		CancelledCount: 1,
	}
	if got := executionStatus(exec); got != ExecutionFailed {
		t.Fatalf("expected ExecutionFailed for a cancelled execution, got %s", got)
	}
}

func TestExecutionStatus_SucceededWhenCompletedWithNoFailures(t *testing.T) {
	exec := &runpb.Execution{
		CompletionTime: timestamppb.New(time.Now()),
		SucceededCount: 1,
	}
	if got := executionStatus(exec); got != ExecutionSucceeded {
		t.Fatalf("expected ExecutionSucceeded, got %s", got)
	}
}

func TestConditionState_ReconcilingFlagWinsOverCondition(t *testing.T) {
	cond := &runpb.Condition{State: runpb.Condition_CONDITION_SUCCEEDED}
	ready, creating := conditionState(cond, true)
	if ready || !creating {
		t.Fatalf("expected creating=true, ready=false when reconciling, got ready=%v creating=%v", ready, creating)
	}
}

func TestConditionState_Succeeded(t *testing.T) {
	cond := &runpb.Condition{State: runpb.Condition_CONDITION_SUCCEEDED}
	ready, creating := conditionState(cond, false)
	if !ready || creating {
		t.Fatalf("expected ready=true, creating=false, got ready=%v creating=%v", ready, creating)
	}
}

func TestConditionState_Failed(t *testing.T) {
	cond := &runpb.Condition{State: runpb.Condition_CONDITION_FAILED}
	ready, creating := conditionState(cond, false)
	if ready || creating {
		t.Fatalf("expected ready=false, creating=false for a failed condition, got ready=%v creating=%v", ready, creating)
	}
}

func TestLatestRevisionPercent_SumsOnlyLatestTypeTargets(t *testing.T) {
	targets := []*runpb.TrafficTarget{
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST, Percent: 40},
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Percent: 60},
	}
	if got := latestRevisionPercent(targets); got != 40 {
		t.Fatalf("expected 40, got %d", got)
	}
}

func TestContainerImage_EmptyWhenNoContainers(t *testing.T) {
	if got := containerImage(nil); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestEnvStateFromContainers_NoContainersOrNoEnvReturnsNil(t *testing.T) {
	vars, secrets := envStateFromContainers(nil)
	if vars != nil || secrets != nil {
		t.Fatalf("expected nil/nil for no containers, got %+v/%+v", vars, secrets)
	}
	vars, secrets = envStateFromContainers([]*runpb.Container{{}})
	if vars != nil || secrets != nil {
		t.Fatalf("expected nil/nil for a container with no env, got %+v/%+v", vars, secrets)
	}
}

func TestEnvStateFromContainers_SplitsPlainAndSecretSourced(t *testing.T) {
	containers := []*runpb.Container{{
		Env: []*runpb.EnvVar{
			{Name: "LOG_LEVEL", Values: &runpb.EnvVar_Value{Value: "debug"}},
			{Name: "DB_PASSWORD", Values: &runpb.EnvVar_ValueSource{
				ValueSource: &runpb.EnvVarSource{
					SecretKeyRef: &runpb.SecretKeySelector{Secret: "db-password", Version: "3"},
				},
			}},
		},
	}}
	vars, secrets := envStateFromContainers(containers)
	if len(vars) != 1 || len(secrets) != 1 {
		t.Fatalf("expected exactly one plain var and one secret ref, got vars=%+v secrets=%+v", vars, secrets)
	}
	if vars["LOG_LEVEL"] != "debug" {
		t.Fatalf("expected plain var parsed, got %+v", vars)
	}
	if secrets["DB_PASSWORD"] != (SecretRef{Secret: "db-password", Version: "3"}) {
		t.Fatalf("expected secret ref parsed, got %+v", secrets)
	}
}

func TestBuildEnvVars_RoundTripsThroughEnvStateFromContainers(t *testing.T) {
	vars := map[string]string{"LOG_LEVEL": "debug"}
	secrets := map[string]SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "3"}}
	built := buildEnvVars(vars, secrets)
	if len(built) != 2 {
		t.Fatalf("expected 2 env vars built, got %d", len(built))
	}
	gotVars, gotSecrets := envStateFromContainers([]*runpb.Container{{Env: built}})
	if gotVars["LOG_LEVEL"] != "debug" {
		t.Fatalf("round-trip lost the plain var: %+v", gotVars)
	}
	if gotSecrets["DB_PASSWORD"] != (SecretRef{Secret: "db-password", Version: "3"}) {
		t.Fatalf("round-trip lost the secret ref: %+v", gotSecrets)
	}
}

func TestBuildEnvVars_DeterministicOrder(t *testing.T) {
	vars := map[string]string{"ZEBRA": "1", "ALPHA": "2"}
	built := buildEnvVars(vars, nil)
	if len(built) != 2 || built[0].GetName() != "ALPHA" || built[1].GetName() != "ZEBRA" {
		t.Fatalf("expected alphabetical order for deterministic output, got %+v", built)
	}
}

func TestContainerImage_ReturnsFirstContainerImage(t *testing.T) {
	containers := []*runpb.Container{{Image: "us-docker.pkg.dev/proj/repo/svc@sha256:" + hex64}}
	if got := containerImage(containers); got != containers[0].Image {
		t.Fatalf("got %q, want %q", got, containers[0].Image)
	}
}

func TestConditionState_ReconcilingCondition(t *testing.T) {
	cond := &runpb.Condition{State: runpb.Condition_CONDITION_RECONCILING}
	ready, creating := conditionState(cond, false)
	if ready || !creating {
		t.Fatalf("expected ready=false, creating=true for a reconciling condition, got ready=%v creating=%v", ready, creating)
	}
}

func TestConditionState_PendingCondition(t *testing.T) {
	cond := &runpb.Condition{State: runpb.Condition_CONDITION_PENDING}
	ready, creating := conditionState(cond, false)
	if ready || !creating {
		t.Fatalf("expected ready=false, creating=true for a pending condition, got ready=%v creating=%v", ready, creating)
	}
}

// TestLastPathSegment_NoSlashReturnsWholeString guards the fallback branch —
// a malformed/unexpected resource name with no "/" must still return
// something usable rather than panicking on the slice index.
func TestLastPathSegment_NoSlashReturnsWholeString(t *testing.T) {
	if got := lastPathSegment("my-service"); got != "my-service" {
		t.Fatalf("got %q, want %q", got, "my-service")
	}
}

func TestResolveImageDigest_EmptyImageReturnsEmpty(t *testing.T) {
	c := &GCPAdminClient{}
	got, err := c.resolveImageDigest(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

func TestResolveImageDigest_FullDigestReferenceSkipsRegistryLookup(t *testing.T) {
	c := &GCPAdminClient{}
	image := "us-docker.pkg.dev/proj/repo/svc@sha256:" + hex64
	got, err := c.resolveImageDigest(context.Background(), image)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sha256:"+hex64 {
		t.Fatalf("got %q, want %q", got, "sha256:"+hex64)
	}
}

func TestResolveImageDigest_BareDigestWithNoAtSignSkipsRegistryLookup(t *testing.T) {
	c := &GCPAdminClient{}
	got, err := c.resolveImageDigest(context.Background(), "sha256:"+hex64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sha256:"+hex64 {
		t.Fatalf("got %q, want %q", got, "sha256:"+hex64)
	}
}

// TestLiveServiceFromService_DigestPinnedImageNeedsNoRegistryClient exercises
// liveServiceFromService end to end on a zero-value client — safe only
// because a digest-pinned image never reaches the Artifact Registry lookup
// path (see resolveImageDigest), so no live network/credentials are needed.
func TestLiveServiceFromService_DigestPinnedImageNeedsNoRegistryClient(t *testing.T) {
	c := &GCPAdminClient{}
	svc := &runpb.Service{
		Template: &runpb.RevisionTemplate{
			Containers: []*runpb.Container{{Image: "us-docker.pkg.dev/proj/repo/svc@sha256:" + hex64}},
		},
		Traffic: []*runpb.TrafficTarget{
			{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST, Percent: 100},
		},
		TerminalCondition: &runpb.Condition{State: runpb.Condition_CONDITION_SUCCEEDED},
	}
	live, err := c.liveServiceFromService(context.Background(), svc, "sha256:"+hex64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if live.ImageDigest != "sha256:"+hex64 {
		t.Fatalf("got digest %q", live.ImageDigest)
	}
	if !live.HasRevisionForDesiredDigest || !live.LatestRevisionReady || live.LatestRevisionCreating {
		t.Fatalf("unexpected live state: %+v", live)
	}
	if live.TrafficLatestRevisionPercent == nil || *live.TrafficLatestRevisionPercent != 100 {
		t.Fatalf("expected traffic percent 100, got %+v", live.TrafficLatestRevisionPercent)
	}
}

func TestLiveServiceFromService_DigestMismatchIsNotHasRevisionForDesiredDigest(t *testing.T) {
	c := &GCPAdminClient{}
	svc := &runpb.Service{
		Template: &runpb.RevisionTemplate{
			Containers: []*runpb.Container{{Image: "us-docker.pkg.dev/proj/repo/svc@sha256:" + hex64}},
		},
	}
	live, err := c.liveServiceFromService(context.Background(), svc, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if live.HasRevisionForDesiredDigest {
		t.Fatal("expected HasRevisionForDesiredDigest=false on a digest mismatch")
	}
}

func TestLiveServiceFromWorkerPool_DigestPinnedImageNeedsNoRegistryClient(t *testing.T) {
	c := &GCPAdminClient{}
	wp := &runpb.WorkerPool{
		Template: &runpb.WorkerPoolRevisionTemplate{
			Containers: []*runpb.Container{{Image: "us-docker.pkg.dev/proj/repo/svc@sha256:" + hex64}},
		},
		TerminalCondition: &runpb.Condition{State: runpb.Condition_CONDITION_SUCCEEDED},
	}
	live, err := c.liveServiceFromWorkerPool(context.Background(), wp, "sha256:"+hex64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !live.HasRevisionForDesiredDigest || !live.LatestRevisionReady {
		t.Fatalf("unexpected live state: %+v", live)
	}
	if live.TrafficLatestRevisionPercent != nil {
		t.Fatalf("expected no traffic concept for workerPool, got %+v", live.TrafficLatestRevisionPercent)
	}
}
