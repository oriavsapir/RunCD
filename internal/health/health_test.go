package health

import (
	"testing"

	"github.com/runcd/runcd/internal/cloudrun"
)

func intPtr(v int) *int { return &v }

func TestAssessService_Healthy(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	if got := AssessService(desired, live, true); got != Healthy {
		t.Fatalf("expected Healthy, got %s", got)
	}
}

func TestAssessService_HealthyIgnoresTrafficWhenNotManaged(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(50)},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	if got := AssessService(desired, live, false); got != Healthy {
		t.Fatalf("expected Healthy (traffic not managed), got %s", got)
	}
}

func TestAssessService_Missing(t *testing.T) {
	live := cloudrun.LiveService{HasRevisionForDesiredDigest: false}
	if got := AssessService(cloudrun.ServiceState{}, live, false); got != Missing {
		t.Fatalf("expected Missing, got %s", got)
	}
}

func TestAssessService_Progressing(t *testing.T) {
	live := cloudrun.LiveService{
		HasRevisionForDesiredDigest: true,
		LatestRevisionCreating:      true,
	}
	if got := AssessService(cloudrun.ServiceState{}, live, false); got != Progressing {
		t.Fatalf("expected Progressing, got %s", got)
	}
}

func TestAssessService_Degraded(t *testing.T) {
	live := cloudrun.LiveService{
		HasRevisionForDesiredDigest: true,
		LatestRevisionCreating:      false,
		LatestRevisionReady:         false,
	}
	if got := AssessService(cloudrun.ServiceState{}, live, false); got != Degraded {
		t.Fatalf("expected Degraded, got %s", got)
	}
}

func TestAssessService_TrafficMismatchWhileManagedIsProgressing(t *testing.T) {
	desired := cloudrun.ServiceState{TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{TrafficLatestRevisionPercent: intPtr(50)},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	if got := AssessService(desired, live, true); got != Progressing {
		t.Fatalf("expected Progressing (traffic split still catching up), got %s", got)
	}
}

// TestAssessService_HealthyWhenTrafficManagedButManifestOmitsIt
// regression-tests a permanent-Progressing bug: desired.TrafficLatestRevisionPercent
// is nil whenever the manifest manages traffic but doesn't set a traffic:
// block, while a real Cloud Run client's live percent is never nil —
// treating nil-vs-non-nil as a mismatch would report Progressing forever
// for an otherwise perfectly healthy service (diff.Compute already has
// this same guard).
func TestAssessService_HealthyWhenTrafficManagedButManifestOmitsIt(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"} // no TrafficLatestRevisionPercent
	live := cloudrun.LiveService{
		ServiceState:                cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	if got := AssessService(desired, live, true); got != Healthy {
		t.Fatalf("expected Healthy (nothing to enforce on traffic), got %s", got)
	}
}

func TestAssessWorkerPool_HealthyIgnoresTrafficEntirely(t *testing.T) {
	live := cloudrun.LiveService{
		// Traffic populated but must never be consulted for workerPool.
		ServiceState:                cloudrun.ServiceState{TrafficLatestRevisionPercent: intPtr(50)},
		HasRevisionForDesiredDigest: true,
		LatestRevisionReady:         true,
	}
	if got := AssessWorkerPool(live); got != Healthy {
		t.Fatalf("expected Healthy, got %s", got)
	}
}

func TestAssessWorkerPool_Missing(t *testing.T) {
	if got := AssessWorkerPool(cloudrun.LiveService{HasRevisionForDesiredDigest: false}); got != Missing {
		t.Fatalf("expected Missing, got %s", got)
	}
}

func TestAssessWorkerPool_Progressing(t *testing.T) {
	live := cloudrun.LiveService{HasRevisionForDesiredDigest: true, LatestRevisionCreating: true}
	if got := AssessWorkerPool(live); got != Progressing {
		t.Fatalf("expected Progressing, got %s", got)
	}
}

func TestAssessWorkerPool_Degraded(t *testing.T) {
	live := cloudrun.LiveService{HasRevisionForDesiredDigest: true, LatestRevisionReady: false}
	if got := AssessWorkerPool(live); got != Degraded {
		t.Fatalf("expected Degraded, got %s", got)
	}
}

func TestAssessJob_Healthy(t *testing.T) {
	live := cloudrun.LiveJob{HasExecutionForDesiredDigest: true, LatestExecutionStatus: cloudrun.ExecutionSucceeded}
	if got := AssessJob(live); got != Healthy {
		t.Fatalf("expected Healthy, got %s", got)
	}
}

func TestAssessJob_Progressing(t *testing.T) {
	live := cloudrun.LiveJob{HasExecutionForDesiredDigest: true, LatestExecutionStatus: cloudrun.ExecutionRunning}
	if got := AssessJob(live); got != Progressing {
		t.Fatalf("expected Progressing, got %s", got)
	}
}

func TestAssessJob_Degraded(t *testing.T) {
	live := cloudrun.LiveJob{HasExecutionForDesiredDigest: true, LatestExecutionStatus: cloudrun.ExecutionFailed}
	if got := AssessJob(live); got != Degraded {
		t.Fatalf("expected Degraded, got %s", got)
	}
}

func TestAssessJob_Missing(t *testing.T) {
	if got := AssessJob(cloudrun.LiveJob{HasExecutionForDesiredDigest: false}); got != Missing {
		t.Fatalf("expected Missing, got %s", got)
	}
}

func TestAssessJob_UnknownStatusIsDegradedNotSilentlyHealthy(t *testing.T) {
	live := cloudrun.LiveJob{HasExecutionForDesiredDigest: true, LatestExecutionStatus: "Pending"}
	if got := AssessJob(live); got != Degraded {
		t.Fatalf("expected Degraded for an unrecognized execution status, got %s", got)
	}
}
