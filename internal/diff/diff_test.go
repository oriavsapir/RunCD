package diff

import (
	"testing"

	"github.com/runcd/runcd/internal/cloudrun"
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

// TestCompute_TrafficManagedButManifestOmitsItIsSynced regression-tests a
// permanent-redeploy bug: a real Cloud Run client always reports a non-nil
// live traffic percent, but desired.TrafficLatestRevisionPercent is nil
// whenever the manifest simply doesn't set a traffic block — nil-vs-non-nil
// must not read as a mismatch.
func TestCompute_TrafficManagedButManifestOmitsItIsSynced(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", TrafficLatestRevisionPercent: intPtr(100)}
	if got := Compute(desired, live, []string{"image", "traffic"}, "service"); got != Synced {
		t.Fatalf("expected Synced (nothing to enforce on traffic), got %s", got)
	}
}

func TestCompute_JobImageMismatchIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:def"}
	if got := Compute(desired, live, []string{"image"}, "job"); got != OutOfSync {
		t.Fatalf("expected OutOfSync, got %s", got)
	}
}

func TestCompute_EnvNotManagedIsSynced(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "bar"}}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != Synced {
		t.Fatalf("expected Synced (env not part of the declared spec), got %s", got)
	}
}

func TestCompute_EnvManagedMismatchIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "bar"}}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "baz"}}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync, got %s", got)
	}
}

func TestCompute_EnvManagedMatchIsSynced(t *testing.T) {
	desired := cloudrun.ServiceState{
		ImageDigest: "sha256:abc",
		EnvVars:     map[string]string{"FOO": "bar"},
		SecretRefs:  map[string]cloudrun.SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "3"}},
	}
	live := cloudrun.ServiceState{
		ImageDigest: "sha256:abc",
		EnvVars:     map[string]string{"FOO": "bar"},
		SecretRefs:  map[string]cloudrun.SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "3"}},
	}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != Synced {
		t.Fatalf("expected Synced, got %s", got)
	}
}

func TestCompute_SecretVersionMismatchIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{
		ImageDigest: "sha256:abc",
		EnvVars:     map[string]string{},
		SecretRefs:  map[string]cloudrun.SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "3"}},
	}
	live := cloudrun.ServiceState{
		ImageDigest: "sha256:abc",
		EnvVars:     map[string]string{},
		SecretRefs:  map[string]cloudrun.SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "2"}},
	}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync on a secret version mismatch, got %s", got)
	}
}

func TestCompute_EnvIgnoredForJobEvenIfManaged(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "bar"}}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "different"}}
	if got := Compute(desired, live, []string{"image", "env"}, "job"); got != Synced {
		t.Fatalf("expected Synced (env not yet supported for job), got %s", got)
	}
}

// TestCompute_EnvVarRemovedFromLiveIsOutOfSync guards stringMapEqual's own
// documented reasoning: a plain b[k] index would return "" for a key
// missing from live entirely, indistinguishable from a key genuinely
// present with an empty value — this is the "key removed" case, distinct
// from the "value changed" case TestCompute_EnvManagedMismatchIsOutOfSync
// already covers.
func TestCompute_EnvVarRemovedFromLiveIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{"FOO": "bar"}}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{}}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync (env var missing from live entirely), got %s", got)
	}
}

func TestCompute_SecretRemovedFromLiveIsOutOfSync(t *testing.T) {
	desired := cloudrun.ServiceState{
		ImageDigest: "sha256:abc",
		SecretRefs:  map[string]cloudrun.SecretRef{"DB_PASSWORD": {Secret: "db-password", Version: "3"}},
	}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc", SecretRefs: map[string]cloudrun.SecretRef{}}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != OutOfSync {
		t.Fatalf("expected OutOfSync (secret ref missing from live entirely), got %s", got)
	}
}

func TestCompute_EnvManagedAndEmptyIsSyncedAgainstEmptyLive(t *testing.T) {
	desired := cloudrun.ServiceState{ImageDigest: "sha256:abc", EnvVars: map[string]string{}, SecretRefs: map[string]cloudrun.SecretRef{}}
	live := cloudrun.ServiceState{ImageDigest: "sha256:abc"}
	if got := Compute(desired, live, []string{"image", "env"}, "service"); got != Synced {
		t.Fatalf("expected Synced (nil live env == empty desired env), got %s", got)
	}
}
