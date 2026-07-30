package diff

import (
	"testing"

	"github.com/argorun/argorun/internal/cloudrun"
)

func intPtr(v int) *int { return &v }

func TestCompute_ImageMatchIsSynced(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	if got := Compute(desired, live, []string{"image"}, "service"); got != Synced {
		t.Fatalf("expected Synced, got %s", got)
	}
}

func TestCompute_ImageMismatchIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:def"}
	if got := Compute(desired, live, []string{"image"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync, got %s", got)
	}
}

func TestCompute_UnmanagedFieldsIgnored(t *testing.T) {
	// traffic differs but isn't in managedFields — must not affect status.
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(50)}
	if got := Compute(desired, live, []string{"image"}, "service"); got != Synced {
		t.Fatalf("expected Synced (traffic not managed), got %s", got)
	}
}

func TestCompute_TrafficManagedAndMismatchedIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(50)}
	if got := Compute(desired, live, []string{"image", "traffic"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync, got %s", got)
	}
}

func TestCompute_TrafficIgnoredForWorkerPoolEvenIfManaged(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(50)}
	if got := Compute(desired, live, []string{"image", "traffic"}, "workerPool"); got != Synced {
		t.Fatalf("expected Synced (traffic meaningless for workerPool), got %s", got)
	}
}

func TestCompute_TrafficIgnoredForJobEvenIfManaged(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(50)}
	if got := Compute(desired, live, []string{"image", "traffic"}, "job"); got != Synced {
		t.Fatalf("expected Synced (traffic meaningless for job), got %s", got)
	}
}

func TestCompute_JobImageMismatchIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:def"}
	if got := Compute(desired, live, []string{"image"}, "job"); got != OutOfSync {
		t.Fatalf("expected OutOfSync, got %s", got)
	}
}
