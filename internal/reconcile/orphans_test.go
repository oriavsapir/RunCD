package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/expander"
)

func TestDetectOrphans_FlagsLiveServiceAbsentFromUnits(t *testing.T) {
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
		"example-prod-us/leftover":   {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
	}}
	r := &Reconciler{CloudRun: cr}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us", Region: "us-central1"}}
	orphans, err := r.DetectOrphans(context.Background(), units)
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].App != "leftover" {
		t.Fatalf("expected exactly one orphan (leftover), got %+v", orphans)
	}
	if orphans[0].Project != "example-prod-us" || orphans[0].Region != "us-central1" {
		t.Fatalf("unexpected orphan project/region: %+v", orphans[0])
	}
}

func TestDetectOrphans_NoOrphansWhenEveryLiveServiceIsDeclared(t *testing.T) {
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
	}}
	r := &Reconciler{CloudRun: cr}

	units := []expander.SyncUnit{{App: "widget-api", Project: "example-prod-us", Region: "us-central1"}}
	orphans, err := r.DetectOrphans(context.Background(), units)
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("expected no orphans, got %+v", orphans)
	}
}

func TestDetectOrphans_ScansEachDistinctProjectRegionOnce(t *testing.T) {
	cr := &fakeCloudRun{services: map[string]*cloudrun.LiveService{
		"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
		"example-prod-eu/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
		"example-prod-eu/orphaned":   {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
	}}
	r := &Reconciler{CloudRun: cr}

	units := []expander.SyncUnit{
		{App: "widget-api", Project: "example-prod-us", Region: "us-central1"},
		{App: "widget-api", Project: "example-prod-eu", Region: "europe-west1"},
	}
	orphans, err := r.DetectOrphans(context.Background(), units)
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].App != "orphaned" || orphans[0].Project != "example-prod-eu" {
		t.Fatalf("expected exactly one orphan in example-prod-eu, got %+v", orphans)
	}
}

// TestDetectOrphans_OneProjectFailureDoesNotDiscardOthers guards against
// aborting a fleet-wide scan over one bad project — a real error scanning
// example-prod-us must not discard the orphan correctly found in
// example-prod-eu, and both scopes' results should still be reported.
func TestDetectOrphans_OneProjectFailureDoesNotDiscardOthers(t *testing.T) {
	boom := errors.New("boom")
	cr := &fakeCloudRun{
		services: map[string]*cloudrun.LiveService{
			"example-prod-us/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
			"example-prod-eu/widget-api": {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
			"example-prod-eu/orphaned":   {ServiceState: cloudrun.ServiceState{ImageDigest: validDigest}},
		},
		listServiceNamesErr: map[string]error{"example-prod-us": boom},
	}
	r := &Reconciler{CloudRun: cr}

	units := []expander.SyncUnit{
		{App: "widget-api", Project: "example-prod-us", Region: "us-central1"},
		{App: "widget-api", Project: "example-prod-eu", Region: "europe-west1"},
	}
	orphans, err := r.DetectOrphans(context.Background(), units)
	if err == nil {
		t.Fatal("expected the example-prod-us failure to be reported")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the underlying error to be wrapped, got %v", err)
	}
	if len(orphans) != 1 || orphans[0].App != "orphaned" || orphans[0].Project != "example-prod-eu" {
		t.Fatalf("expected the example-prod-eu orphan to still be reported despite the other project's failure, got %+v", orphans)
	}
}
