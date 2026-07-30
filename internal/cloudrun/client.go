// Package cloudrun defines the controller's view of live Cloud Run state.
// No real Cloud Run Admin API calls are wired up yet (Phase 1 is read-only,
// diff/health only) — AdminClient is an interface so the reconcile loop can
// be built and tested against a fake today and a real client later.
package cloudrun

import (
	"context"
	"errors"
)

// ErrNotProvisioned means the target resource itself doesn't exist yet in
// the target project (Terraform hasn't created the shell) — a distinct
// condition from a Missing health status, which means "no revision with the
// desired digest on an existing resource" (§7).
var ErrNotProvisioned = errors.New("resource not provisioned: run Terraform first")

// ServiceState is the subset of Cloud Run service spec the diff engine and
// health assessment reason about — only the fields argorun ever manages
// (§5.7's managed-field set) plus what's needed to assess health.
type ServiceState struct {
	ImageDigest string
	// TrafficLatestRevisionPercent is nil when traffic isn't part of the
	// service spec being compared (e.g. not yet in defaults.managedFields).
	TrafficLatestRevisionPercent *int
}

// LiveService is what GetService returns: current spec plus the health
// signals from §5.7's table for resourceType: service and workerPool (both
// are revision-based; workerPool just has no traffic concept).
type LiveService struct {
	ServiceState
	// HasRevisionForDesiredDigest reports whether any existing revision of
	// this service runs the digest the caller asked about — "Missing" means
	// this is false.
	HasRevisionForDesiredDigest bool
	LatestRevisionReady         bool
	LatestRevisionCreating      bool
}

// ExecutionStatus is a Cloud Run Job execution's terminal/running state —
// jobs are assessed by execution outcome, not revision readiness (§5.7).
type ExecutionStatus string

const (
	ExecutionRunning   ExecutionStatus = "Running"
	ExecutionSucceeded ExecutionStatus = "Succeeded"
	ExecutionFailed    ExecutionStatus = "Failed"
)

// LiveJob is what GetJob returns for resourceType: job.
type LiveJob struct {
	ServiceState // ImageDigest is whatever image the latest execution ran.
	// HasExecutionForDesiredDigest reports whether any execution has ever
	// run the digest the caller asked about — "Missing" means this is false.
	HasExecutionForDesiredDigest bool
	LatestExecutionStatus        ExecutionStatus
}

// AdminClient is the controller's read access to Cloud Run Admin API state.
type AdminClient interface {
	// GetService returns live state for the named service or workerPool,
	// checked against desiredDigest to determine HasRevisionForDesiredDigest.
	// Returns ErrNotProvisioned if the resource doesn't exist in the
	// project at all.
	GetService(ctx context.Context, project, region, name, desiredDigest string) (*LiveService, error)

	// GetJob returns live state for the named job, checked against
	// desiredDigest to determine HasExecutionForDesiredDigest. Returns
	// ErrNotProvisioned if the resource doesn't exist in the project at all.
	GetJob(ctx context.Context, project, region, name, desiredDigest string) (*LiveJob, error)

	// DeployService applies desired's managed fields (image digest, and
	// traffic if managed) to the named service or workerPool as a new
	// revision. Returns ErrNotProvisioned if the resource shell doesn't
	// exist yet (§7 — Terraform hasn't provisioned it).
	DeployService(ctx context.Context, project, region, name string, desired ServiceState) error

	// DeployJob triggers an execution of the named job with desired's image
	// digest. Returns ErrNotProvisioned if the resource shell doesn't exist
	// yet.
	DeployJob(ctx context.Context, project, region, name string, desired ServiceState) error
}
