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

// SecretRef is one env var sourced from Secret Manager: name is the env var
// exposed in the container, Secret/Version identify the Secret Manager
// secret+version.
type SecretRef struct {
	Secret  string
	Version string
}

// ServiceState is the subset of Cloud Run service spec the diff engine and
// health assessment reason about — only the fields runcd ever manages
// (§5.7's managed-field set) plus what's needed to assess health.
type ServiceState struct {
	ImageDigest string
	// TrafficLatestRevisionPercent is nil when traffic isn't part of the
	// service spec being compared (e.g. not yet in defaults.managedFields).
	TrafficLatestRevisionPercent *int
	// EnvVars/SecretRefs are both nil when "env" isn't managed for this
	// unit — the single signal diff.Compute and the real deploy path use
	// to skip comparing/touching the container's environment at all, same
	// convention TrafficLatestRevisionPercent already uses. When managed,
	// both are non-nil (possibly empty maps, meaning "manage env and want
	// zero entries") — together they're the full desired/live container
	// environment; Cloud Run has one unified env var list, not a separate
	// "plain" vs "secret-sourced" concept.
	EnvVars    map[string]string
	SecretRefs map[string]SecretRef
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

	// DeployService applies desired's managed fields (image digest,
	// traffic if managed, env/secrets if managed) to the named service or
	// workerPool as a new revision. Returns ErrNotProvisioned if the
	// resource shell doesn't exist yet (§7 — Terraform hasn't provisioned
	// it).
	DeployService(ctx context.Context, project, region, name string, desired ServiceState) error

	// DeployJob triggers an execution of the named job with desired's image
	// digest. Returns ErrNotProvisioned if the resource shell doesn't exist
	// yet.
	DeployJob(ctx context.Context, project, region, name string, desired ServiceState) error

	// ListServiceNames returns the short name of every Cloud Run service
	// that exists in project/region — the prune/orphan-detection read path
	// (§ prune): services present in GCP but absent from runcd.yaml.
	ListServiceNames(ctx context.Context, project, region string) ([]string, error)
}
