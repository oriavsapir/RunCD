// Package reconcile runs one read-only reconcile pass over a set of sync
// units (§5.4): fetch live state, check preconditions, diff against
// managedFields, assess health, and persist the result to the applications
// table. No deploy — that's a later phase.
package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/argorun/argorun/internal/cloudrun"
	"github.com/argorun/argorun/internal/diff"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/health"
	"github.com/argorun/argorun/internal/manifest"
	"github.com/argorun/argorun/internal/precondition"
)

// DefaultWorkers matches §5.4's default bounded worker pool size.
const DefaultWorkers = 16

// ManifestSource supplies a sync unit's service definition. Fetching it from
// git is a separate concern (not built yet); this interface keeps the
// reconcile loop testable without one.
type ManifestSource interface {
	Get(ctx context.Context, unit expander.SyncUnit) ([]byte, error)
}

// Status values a sync unit's applications row can land on. Invalid covers
// both a non-digest-pinned manifest (§7) and a failed precondition — in
// both cases the unit can never sync until something outside argorun is
// fixed, so it's surfaced the same way rather than as ordinary drift.
const (
	StatusInvalid = "Invalid"
	StatusMissing = "Missing"
)

type Result struct {
	Unit         expander.SyncUnit
	DesiredImage string
	LiveImage    string // empty when live state couldn't be read
	Status       string
	Health       string
	// Err is set for a unit that couldn't be assessed normally (manifest
	// parse failure, unsupported resourceType, precondition failure, or an
	// unprovisioned target resource). Not persisted anywhere in Phase 1 —
	// sync_events (Phase 2) is where failures get a durable record.
	Err error
}

type Reconciler struct {
	DB            *sql.DB
	CloudRun      cloudrun.AdminClient
	Preconditions precondition.Checker
	Manifests     ManifestSource
	ManagedFields []string
	Workers       int
}

// RunOnce reconciles every unit concurrently, bounded to r.Workers (default
// DefaultWorkers), and upserts each result into the applications table.
func (r *Reconciler) RunOnce(ctx context.Context, units []expander.SyncUnit) ([]Result, error) {
	workers := r.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}

	results := make([]Result, len(units))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	for i, unit := range units {
		g.Go(func() error {
			res := r.reconcileOne(ctx, unit)
			results[i] = res
			return r.upsert(ctx, res)
		})
	}

	if err := g.Wait(); err != nil {
		return results, err
	}
	return results, nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, unit expander.SyncUnit) Result {
	res := Result{Unit: unit}

	raw, err := r.Manifests.Get(ctx, unit)
	if err != nil {
		res.Status, res.Health, res.Err = StatusInvalid, StatusInvalid, fmt.Errorf("fetch manifest: %w", err)
		return res
	}
	sd, err := manifest.Parse(raw)
	if err != nil {
		res.Status, res.Health, res.Err = StatusInvalid, StatusInvalid, fmt.Errorf("invalid service definition: %w", err)
		return res
	}
	res.DesiredImage = sd.Image.Digest

	if err := precondition.Check(ctx, r.Preconditions, unit.Project, sd.Requires); err != nil {
		res.Status, res.Err = StatusInvalid, err
	}

	desired := cloudrun.ServiceState{ImageDigest: sd.Image.Digest}
	trafficManaged := false
	for _, f := range r.ManagedFields {
		if f == "traffic" {
			trafficManaged = true
		}
	}
	if trafficManaged && sd.Traffic != nil {
		desired.TrafficLatestRevisionPercent = sd.Traffic.LatestRevisionPercent
	}

	// Per §5.7: service and workerPool are both revision-based (workerPool
	// just has no traffic concept); job is execution-based. Only this
	// per-resourceType dispatch differs — the rest of the loop is shared.
	switch sd.ResourceType {
	case manifest.ResourceService:
		live, err := r.CloudRun.GetService(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
		return r.applyLiveState(res, desired, err, func() (cloudrun.ServiceState, string) {
			return live.ServiceState, string(health.AssessService(desired, *live, trafficManaged))
		}, string(sd.ResourceType))
	case manifest.ResourceWorkerPool:
		live, err := r.CloudRun.GetService(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
		return r.applyLiveState(res, desired, err, func() (cloudrun.ServiceState, string) {
			return live.ServiceState, string(health.AssessWorkerPool(*live))
		}, string(sd.ResourceType))
	case manifest.ResourceJob:
		live, err := r.CloudRun.GetJob(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
		return r.applyLiveState(res, desired, err, func() (cloudrun.ServiceState, string) {
			return live.ServiceState, string(health.AssessJob(*live))
		}, string(sd.ResourceType))
	default:
		res.Status, res.Health, res.Err = StatusInvalid, StatusInvalid, fmt.Errorf("unknown resourceType %q", sd.ResourceType)
		return res
	}
}

// applyLiveState folds a live-state fetch (success or failure) into res:
// ErrNotProvisioned -> Missing, any other error -> Invalid (both without
// overwriting a Status a precondition failure already set), otherwise runs
// assess to get the live ServiceState (for diffing) and health.
func (r *Reconciler) applyLiveState(res Result, desired cloudrun.ServiceState, liveErr error, assess func() (cloudrun.ServiceState, string), resourceType string) Result {
	if errors.Is(liveErr, cloudrun.ErrNotProvisioned) {
		res.Health = StatusMissing
		if res.Status == "" {
			res.Status = StatusMissing
		}
		if res.Err == nil {
			res.Err = liveErr
		}
		return res
	}
	if liveErr != nil {
		res.Health = StatusInvalid
		if res.Status == "" {
			res.Status = StatusInvalid
		}
		if res.Err == nil {
			res.Err = fmt.Errorf("get live state: %w", liveErr)
		}
		return res
	}

	live, healthStatus := assess()
	res.LiveImage = live.ImageDigest
	res.Health = healthStatus
	if res.Status == "" {
		res.Status = string(diff.Compute(desired, live, r.ManagedFields, resourceType))
	}
	return res
}

func (r *Reconciler) upsert(ctx context.Context, res Result) error {
	var liveImage sql.NullString
	if res.LiveImage != "" {
		liveImage = sql.NullString{String: res.LiveImage, Valid: true}
	}
	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO applications (name, target_gcp_project, desired_image, live_image, status, health, last_reconciled_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (name, target_gcp_project) DO UPDATE SET
			desired_image = EXCLUDED.desired_image,
			live_image = EXCLUDED.live_image,
			status = EXCLUDED.status,
			health = EXCLUDED.health,
			last_reconciled_at = now()`,
		res.Unit.App, res.Unit.Project, res.DesiredImage, liveImage, res.Status, res.Health)
	return err
}
