package cloudrun

import (
	"testing"
	"time"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidatedPercent_FullCutoverAccepted(t *testing.T) {
	for _, p := range []int{0, 100} {
		got, err := validatedPercent(p)
		if err != nil {
			t.Fatalf("percent=%d: unexpected error: %v", p, err)
		}
		if int(got) != p {
			t.Fatalf("percent=%d: got %d", p, got)
		}
	}
}

func TestValidatedPercent_PartialRejected(t *testing.T) {
	for _, p := range []int{1, 50, 99} {
		if _, err := validatedPercent(p); err == nil {
			t.Fatalf("percent=%d: expected error — v1 only supports a full cutover", p)
		}
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
