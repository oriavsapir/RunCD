package cloudrun

import (
	"testing"
	"time"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
