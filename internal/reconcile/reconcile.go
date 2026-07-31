// Package reconcile runs a reconcile pass over sync units (§5.4): fetch
// live state, check preconditions, diff against managedFields, assess
// health, deploy when appropriate (§5.3, §6), and persist the result plus a
// durable sync_events audit row (§5.2, §6) for every deploy attempt. Two
// entry points share this machinery: RunOnce (the poll loop, auto-sync only)
// and ManualSync (a single gated-sync request from a human, §5.9/FR4).
package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/diff"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/health"
	"github.com/runcd/runcd/internal/manifest"
	"github.com/runcd/runcd/internal/precondition"
)

// DefaultWorkers matches §5.4's default bounded worker pool size.
const DefaultWorkers = 16

// ErrSyncInProgress means another deploy attempt for this exact unit is
// already holding its lock — from a concurrent manual sync, the
// auto-reconcile loop, or both. Manual sync can run on any replica (not
// just the leader), so this can't be an in-process mutex; it's a row in
// the sync_locks table instead, checked/claimed atomically alongside the
// existing applications/sync_events writes.
var ErrSyncInProgress = errors.New("sync already in progress for this app/project")

// lockTTL bounds how long a held lock survives a crashed holder before a
// later attempt can reclaim it. Generous relative to the real work a lock
// is held for: DeployService/DeployJob submit the update and return
// without polling to readiness (no .Wait on the LRO), so a full deploy
// attempt — fetch, deploy call, one post-deploy fetch — is a handful of
// GCP API round-trips, not a multi-minute wait.
const lockTTL = 2 * time.Minute

// ManifestSource supplies a sync unit's service definition. Fetching it from
// git is a separate concern (not built yet); this interface keeps the
// reconcile loop testable without one.
type ManifestSource interface {
	Get(ctx context.Context, unit expander.SyncUnit) ([]byte, error)
}

// Status values a sync unit's applications row can land on. Invalid covers
// both a non-digest-pinned manifest (§7) and a failed precondition — in
// both cases the unit can never sync until something outside runcd is
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
	// StatusSince/HealthSince are when Status/Health last *changed* (not
	// merely last reconciled) — the Notifier's healthDegraded/
	// outOfSyncGated rules (§5.8) key off these. Only populated after a
	// successful upsert.
	StatusSince time.Time
	HealthSince time.Time
	// DeployFailed/FailureMessage are set when a deploy attempt this pass
	// resolved to sync_events.result=failed — the Notifier's syncFailed
	// rule fires on this, immediately, no debounce-worthy duration check.
	DeployFailed   bool
	FailureMessage string
	// Err is set for a unit that couldn't be assessed or synced normally
	// (manifest parse failure, unsupported resourceType, precondition
	// failure, unprovisioned target resource, or a failed deploy/audit
	// write). Not persisted to `applications` — sync_events is the durable
	// record for anything deploy-related; this is for the caller's own
	// logging.
	Err error
}

// db is the subset of *sql.DB the reconciler needs — kept as an interface
// so tests can inject a wrapper that fails on demand for one specific call.
type db interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Notifier is evaluated once per reconcile pass per sync unit (§5.8).
// Evaluation failures are logged by the caller, not fatal to the pass —
// a Slack outage shouldn't stop the controller from reconciling.
type Notifier interface {
	Evaluate(ctx context.Context, res Result) error
}

type Reconciler struct {
	DB            db
	CloudRun      cloudrun.AdminClient
	Preconditions precondition.Checker
	Manifests     ManifestSource
	ManagedFields []string
	Workers       int
	// Notifier is optional; nil means no notifications are evaluated.
	Notifier Notifier
	// Now is optional; nil means time.Now. Overridden in tests so sync
	// window evaluation is deterministic instead of racing the real clock.
	Now func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// RunOnce reconciles every unit concurrently, bounded to r.Workers (default
// DefaultWorkers), and upserts each result into the applications table.
func (r *Reconciler) RunOnce(ctx context.Context, units []expander.SyncUnit) ([]Result, error) {
	workers := r.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}

	results := make([]Result, len(units))
	var g errgroup.Group
	g.SetLimit(workers)

	// Computed once for the whole pass, not per unit inside applyLiveState —
	// see syncOptions.now's doc comment.
	now := r.now()

	// Deliberately not errgroup.WithContext: that cancels every other
	// in-flight unit's context the instant any single unit's upsert fails,
	// which would discard perfectly good results for the rest of the fleet
	// over one transient write error — exactly what §7 says shouldn't
	// happen ("one bad file can't take down the fleet").
	for i, unit := range units {
		g.Go(func() error {
			res := r.reconcileOne(ctx, unit, now)
			if errors.Is(res.Err, ErrSyncInProgress) {
				// See the identical guard in ManualSync: a concurrent
				// manual sync elsewhere already owns (or will own) this
				// pass's applications row for this unit — upserting this
				// attempt's stale pre-lock result would be a last-write-
				// wins race against that write.
				results[i] = res
				r.notify(ctx, res)
				return nil
			}
			res, err := r.upsert(ctx, res)
			if err != nil && res.Err == nil {
				// The aggregate error g.Wait() returns below is just the
				// first of possibly several concurrent upsert failures,
				// with no unit attribution — attach it to this specific
				// Result so a caller inspecting results[i] can tell which
				// unit(s) actually failed to persist, not just that RunOnce
				// as a whole returned an error.
				res.Err = fmt.Errorf("upsert: %w", err)
			}
			results[i] = res
			// Notify regardless of the upsert outcome: a genuine deploy
			// failure (res.DeployFailed) already happened and was recorded
			// to sync_events before this write — an unrelated persistence
			// hiccup here shouldn't also suppress the alert about it.
			// notify() is already best-effort/error-swallowing, so this is
			// safe even when err != nil.
			r.notify(ctx, res)
			if err != nil {
				return err
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return results, err
	}
	return results, nil
}

// ManualSync runs a single gated sync request from an authenticated human
// (§5.9/FR4): the same precondition-check-then-deploy path as the auto
// loop, but always attempts the deploy (unless the unit is Invalid/Missing)
// regardless of the unit's auto flag, with trigger=manual and actor set to
// the caller's verified email.
func (r *Reconciler) ManualSync(ctx context.Context, unit expander.SyncUnit, actor string) (Result, error) {
	res := r.reconcile(ctx, unit, syncOptions{trigger: "manual", actor: actor, force: true, now: r.now()})
	if errors.Is(res.Err, ErrSyncInProgress) {
		// The winning attempt already owns (or will own) this pass's
		// applications row — this result reflects this attempt's own
		// pre-lock fetch, not the winner's outcome. Upserting it anyway
		// would be a last-write-wins race against the winner's own write:
		// if this write lands second, it clobbers the winner's fresher
		// status back to this stale one, and incorrectly resets
		// status_since/health_since (upsert's CASE only preserves them
		// when the new status matches the existing row) — which the
		// notifier's healthDegraded/outOfSyncGated duration rules key off.
		r.notify(ctx, res)
		return res, nil
	}
	res, err := r.upsert(ctx, res)
	// Notify regardless of the upsert outcome — see the identical note in
	// RunOnce above.
	r.notify(ctx, res)
	return res, err
}

func (r *Reconciler) notify(ctx context.Context, res Result) {
	if r.Notifier == nil {
		return
	}
	// Best-effort: a notification failure (e.g. Slack unreachable) must
	// never fail the reconcile pass itself.
	_ = r.Notifier.Evaluate(ctx, res)
}

type syncOptions struct {
	trigger string // "auto" | "manual"
	actor   string
	// force means "deploy the current desired state regardless of the
	// unit's auto flag or whether it's already Synced" — the manual Sync
	// button's semantics. Never overrides an Invalid/Missing status: a
	// failed precondition or unprovisioned resource blocks deploy
	// regardless of trigger (§5.10).
	force bool
	// dryRun computes fetch/precondition/diff/health exactly as a real sync
	// would, but never deploys — checked ahead of force in applyLiveState,
	// so a dry run never reaches deploySyncUnit and never takes a sync_locks
	// row.
	dryRun bool
	// now is when this attempt's sync-window check should be evaluated
	// against — computed once per pass (RunOnce/ManualSync/DryRun), not
	// inside applyLiveState per unit, so a single pass gives every unit a
	// consistent, reproducible answer even as it straddles a window
	// boundary. Only consulted on the auto (non-force, non-dryRun) path.
	now time.Time
}

// DryRun previews what a manual sync would do — the same fetch,
// precondition-check, diff, and health assessment as ManualSync — without
// deploying, without acquiring the per-unit sync lock, and without
// persisting anything (no applications upsert, no sync_events row). Safe to
// call concurrently with a real sync of the same unit.
func (r *Reconciler) DryRun(ctx context.Context, unit expander.SyncUnit) Result {
	return r.reconcile(ctx, unit, syncOptions{trigger: "manual", dryRun: true, now: r.now()})
}

func (r *Reconciler) reconcileOne(ctx context.Context, unit expander.SyncUnit, now time.Time) Result {
	return r.reconcile(ctx, unit, syncOptions{trigger: "auto", actor: "runcd-controller", now: now})
}

func (r *Reconciler) reconcile(ctx context.Context, unit expander.SyncUnit, opts syncOptions) Result {
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

	if err := precondition.Check(ctx, r.Preconditions, unit.Project, filterPreconditions(sd.Requires, unit.IgnorePreconditions)); err != nil {
		res.Status, res.Err = StatusInvalid, err
	}

	// Computed once here, not re-derived separately in applyLiveState and
	// deploySyncUnit — both the pre-deploy diff and the post-deploy re-diff
	// must agree on exactly the same effective field set, or a unit with
	// ignoreFields set could land on a wrong final status after deploy.
	managedFields := effectiveManagedFields(r.ManagedFields, unit.IgnoreFields)

	desired := cloudrun.ServiceState{ImageDigest: sd.Image.Digest}
	trafficManaged := fieldManaged(managedFields, "traffic")
	if trafficManaged && sd.Traffic != nil {
		desired.TrafficLatestRevisionPercent = sd.Traffic.LatestRevisionPercent
	}

	// Per §5.7: service and workerPool are both revision-based (workerPool
	// just has no traffic concept); job is execution-based. Only the
	// per-resourceType fetch/assess/deploy calls differ — the rest of the
	// loop (precondition check, diff, deploy-and-audit) is shared. fetch is
	// a real GetService/GetJob call each time it's invoked — deploySyncUnit
	// calls it again after a deploy to genuinely re-check Cloud Run, not to
	// re-read a stale pre-deploy snapshot.
	switch sd.ResourceType {
	case manifest.ResourceService:
		fetch := func(ctx context.Context) (cloudrun.ServiceState, string, error) {
			live, err := r.CloudRun.GetService(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
			if err != nil {
				return cloudrun.ServiceState{}, "", err
			}
			return live.ServiceState, string(health.AssessService(desired, *live, trafficManaged)), nil
		}
		deploy := func(ctx context.Context, d cloudrun.ServiceState) error {
			return r.CloudRun.DeployService(ctx, unit.Project, unit.Region, unit.App, d)
		}
		return r.applyLiveState(ctx, res, unit, desired, fetch, string(sd.ResourceType), deploy, opts, managedFields)
	case manifest.ResourceWorkerPool:
		fetch := func(ctx context.Context) (cloudrun.ServiceState, string, error) {
			live, err := r.CloudRun.GetService(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
			if err != nil {
				return cloudrun.ServiceState{}, "", err
			}
			return live.ServiceState, string(health.AssessWorkerPool(*live)), nil
		}
		deploy := func(ctx context.Context, d cloudrun.ServiceState) error {
			return r.CloudRun.DeployService(ctx, unit.Project, unit.Region, unit.App, d)
		}
		return r.applyLiveState(ctx, res, unit, desired, fetch, string(sd.ResourceType), deploy, opts, managedFields)
	case manifest.ResourceJob:
		fetch := func(ctx context.Context) (cloudrun.ServiceState, string, error) {
			live, err := r.CloudRun.GetJob(ctx, unit.Project, unit.Region, unit.App, sd.Image.Digest)
			if err != nil {
				return cloudrun.ServiceState{}, "", err
			}
			return live.ServiceState, string(health.AssessJob(*live)), nil
		}
		deploy := func(ctx context.Context, d cloudrun.ServiceState) error {
			return r.CloudRun.DeployJob(ctx, unit.Project, unit.Region, unit.App, d)
		}
		return r.applyLiveState(ctx, res, unit, desired, fetch, string(sd.ResourceType), deploy, opts, managedFields)
	default:
		res.Status, res.Health, res.Err = StatusInvalid, StatusInvalid, fmt.Errorf("unknown resourceType %q", sd.ResourceType)
		return res
	}
}

// applyLiveState folds a live-state fetch (success or failure) into res:
// ErrNotProvisioned -> Missing, any other error -> Invalid (both without
// overwriting a Status a precondition failure already set), otherwise diffs
// the fetched state and — unless already Invalid/Missing, and the unit is
// either forced (manual sync) or OutOfSync-and-auto-synced — deploys it
// (§6 steps 5-6).
func (r *Reconciler) applyLiveState(ctx context.Context, res Result, unit expander.SyncUnit, desired cloudrun.ServiceState, fetch func(context.Context) (cloudrun.ServiceState, string, error), resourceType string, deploy func(context.Context, cloudrun.ServiceState) error, opts syncOptions, managedFields []string) Result {
	live, healthStatus, err := fetch(ctx)
	if errors.Is(err, cloudrun.ErrNotProvisioned) {
		res.Health = StatusMissing
		if res.Status == "" {
			res.Status = StatusMissing
		}
		if res.Err == nil {
			res.Err = err
		}
		return res
	}
	if err != nil {
		res.Health = StatusInvalid
		if res.Status == "" {
			res.Status = StatusInvalid
		}
		if res.Err == nil {
			res.Err = fmt.Errorf("get live state: %w", err)
		}
		return res
	}

	res.LiveImage = live.ImageDigest
	res.Health = healthStatus
	if res.Status == "" {
		res.Status = string(diff.Compute(desired, live, managedFields, resourceType))
	}

	blocked := res.Status == StatusInvalid || res.Status == StatusMissing
	autoAllowed := autoSyncEnabled(unit.Sync) && config.WindowsAllow(unit.Sync.SyncWindows, opts.now)
	shouldDeploy := !opts.dryRun && !blocked && (opts.force || (res.Status == string(diff.OutOfSync) && autoAllowed))
	if shouldDeploy {
		res = r.deploySyncUnit(ctx, res, unit, desired, live, resourceType, deploy, fetch, opts, managedFields)
	}
	return res
}

func autoSyncEnabled(sync config.SyncPolicy) bool {
	return sync.Auto != nil && *sync.Auto
}

// fieldManaged reports whether name is in managedFields.
func fieldManaged(managedFields []string, name string) bool {
	for _, f := range managedFields {
		if f == name {
			return true
		}
	}
	return false
}

// effectiveManagedFields subtracts ignore from base, preserving base's
// order — the diff/traffic-managed logic downstream doesn't care about
// order, but a stable result makes this deterministic to test.
func effectiveManagedFields(base, ignore []string) []string {
	if len(ignore) == 0 {
		return base
	}
	skip := make(map[string]bool, len(ignore))
	for _, f := range ignore {
		skip[f] = true
	}
	out := make([]string, 0, len(base))
	for _, f := range base {
		if !skip[f] {
			out = append(out, f)
		}
	}
	return out
}

// filterPreconditions drops any requires entry named "type:name" in ignore
// — an app-level override for a precondition that's legitimately not
// applicable to one specific app (config.App.IgnorePreconditions).
func filterPreconditions(requires []manifest.Precondition, ignore []string) []manifest.Precondition {
	if len(ignore) == 0 {
		return requires
	}
	skip := make(map[string]bool, len(ignore))
	for _, s := range ignore {
		skip[s] = true
	}
	out := make([]manifest.Precondition, 0, len(requires))
	for _, req := range requires {
		if !skip[req.Type+":"+req.Name] {
			out = append(out, req)
		}
	}
	return out
}

// deploySyncUnit implements §6 steps 5-6 / §5.3's crash-safety contract: a
// sync_events row is written in_progress *before* the deploy call and
// updated after, so a controller crash between those two writes leaves a
// stale in_progress row that the next reconcile pass never trusts — it
// re-derives desired/live state fresh (via fetch) instead of reading this
// row at all.
//
// The post-deploy check calls fetch again — a real GetService/GetJob call,
// not a cached pre-deploy value — because Cloud Run itself, not anything in
// this process, is the only source of truth for whether the new revision
// has converged. If it hasn't yet (still creating, or eventually-consistent
// propagation lag), this pass still records the deploy call itself as
// succeeded but leaves res.Status honestly reflecting what's actually live;
// the next poll re-diffs from scratch. A poll that still sees OutOfSync
// after a deploy it doesn't know is still converging may issue another
// deploy call — accepted per NFR6/§5.3 ("deploying an already-deployed
// digest is a no-op"), which is why any real DeployService/DeployJob must
// itself be idempotent for an unchanged desired digest.
func (r *Reconciler) deploySyncUnit(ctx context.Context, res Result, unit expander.SyncUnit, desired, live cloudrun.ServiceState, resourceType string, deploy func(context.Context, cloudrun.ServiceState) error, fetch func(context.Context) (cloudrun.ServiceState, string, error), opts syncOptions, managedFields []string) Result {
	// Unlike traffic (whose "unmanaged" state is representable as nil and
	// DeployService/DeployJob skip touching it entirely), image has no such
	// representation — the real GCP client always writes desired.ImageDigest
	// into the container spec unconditionally. So when "image" isn't in
	// this unit's effective managedFields (config.App.IgnoreFields), the
	// only way to make the deploy call a genuine no-op for that field is to
	// substitute the *live* digest here — otherwise a force/manual sync (or
	// any deploy triggered by some other field's drift) would silently
	// redeploy the manifest's image despite the app opting out of managing
	// it.
	if !fieldManaged(managedFields, "image") {
		desired.ImageDigest = live.ImageDigest
	}

	// A per-attempt token, not r's holder identity: two concurrent attempts
	// from the very same replica must be just as mutually exclusive as two
	// from different replicas, so the lock can't be keyed on anything
	// shared across attempts.
	token := fmt.Sprintf("%d", time.Now().UnixNano())
	acquired, err := r.acquireLock(ctx, unit, token)
	if err != nil {
		res.Err = fmt.Errorf("acquire sync lock: %w", err)
		return res
	}
	if !acquired {
		res.Err = ErrSyncInProgress
		return res // res.Status stays whatever applyLiveState already computed.
	}
	defer func() {
		// A short-lived, un-cancellable-by-ctx context: if ctx was already
		// cancelled (e.g. leadership lost mid-deploy), the lock still needs
		// releasing so a legitimate later attempt isn't stuck waiting out
		// the full lockTTL — but it must still time out on its own rather
		// than risk hanging forever against a wedged connection.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r.releaseLock(releaseCtx, unit, token)
	}()

	// sync_events has an FK on (application, target_gcp_project) ->
	// applications, so a brand-new sync unit's very first reconcile pass —
	// already OutOfSync and auto-synced — needs that row to exist before a
	// sync_events row can reference it. Idempotent (ON CONFLICT DO UPDATE)
	// on every later pass, so this is a harmless extra write, not a
	// duplicate-row risk.
	if _, err := r.upsert(ctx, res); err != nil {
		res.Err = fmt.Errorf("upsert applications row before deploy: %w", err)
		return res
	}

	id, err := r.insertSyncEvent(ctx, unit, opts.trigger, opts.actor, live.ImageDigest, desired.ImageDigest)
	if err != nil {
		res.Err = fmt.Errorf("write sync_events(in_progress): %w", err)
		return res
	}

	if err := deploy(ctx, desired); err != nil {
		res.DeployFailed = true
		res.FailureMessage = err.Error()
		if updErr := r.updateSyncEvent(ctx, id, "failed", err.Error()); updErr != nil && res.Err == nil {
			res.Err = fmt.Errorf("deploy failed (%w) and updating sync_events also failed: %w", err, updErr)
		} else if res.Err == nil {
			res.Err = fmt.Errorf("deploy: %w", err)
		}
		return res // res.Status stays OutOfSync — correctly reflects reality, retried next poll.
	}

	postLive, postHealth, err := fetch(ctx)
	if err != nil {
		// The deploy call itself succeeded; we just couldn't confirm the
		// result. Leave res.Status/.Health as the pre-deploy values rather
		// than guessing — the next poll will check again.
		if updErr := r.updateSyncEvent(ctx, id, "succeeded", ""); updErr != nil && res.Err == nil {
			res.Err = fmt.Errorf("deploy succeeded but post-deploy check failed (%w) and updating sync_events also failed: %w", err, updErr)
		} else if res.Err == nil {
			res.Err = fmt.Errorf("deploy succeeded but post-deploy check failed: %w", err)
		}
		return res
	}

	res.LiveImage = postLive.ImageDigest
	res.Health = postHealth
	res.Status = string(diff.Compute(desired, postLive, managedFields, resourceType))

	if err := r.updateSyncEvent(ctx, id, "succeeded", ""); err != nil && res.Err == nil {
		res.Err = fmt.Errorf("deploy succeeded but updating sync_events failed: %w", err)
	}
	return res
}

// acquireLock claims unit's row in sync_locks for token, succeeding if no
// one else holds it or its previous holder's claim has expired — the same
// conditional-UPDATE-via-ON-CONFLICT idiom internal/leader uses for
// leader_lease, just per-unit and row-per-unit instead of a single
// pre-seeded row.
func (r *Reconciler) acquireLock(ctx context.Context, unit expander.SyncUnit, token string) (bool, error) {
	res, err := r.DB.ExecContext(ctx, `
		INSERT INTO sync_locks (application, target_gcp_project, holder, expires_at)
		VALUES ($1, $2, $3, now() + ($4 * interval '1 second'))
		ON CONFLICT (application, target_gcp_project) DO UPDATE
		  SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
		  WHERE sync_locks.expires_at < now()`,
		unit.App, unit.Project, token, lockTTL.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// releaseLock drops unit's lock row, but only if token is still the
// holder — guards against releasing a newer attempt's lock after this
// one's own claim already expired and someone else reclaimed it. Logging
// rather than returning the error: this runs from a defer after the real
// work is already done, and a stuck lock only costs a later attempt a
// wait of at most lockTTL, not correctness.
func (r *Reconciler) releaseLock(ctx context.Context, unit expander.SyncUnit, token string) {
	if _, err := r.DB.ExecContext(ctx, `
		DELETE FROM sync_locks WHERE application = $1 AND target_gcp_project = $2 AND holder = $3`,
		unit.App, unit.Project, token); err != nil {
		slog.Error("reconcile: release sync lock", "app", unit.App, "project", unit.Project, "error", err)
	}
}

func (r *Reconciler) insertSyncEvent(ctx context.Context, unit expander.SyncUnit, trigger, actor, fromImage, toImage string) (int64, error) {
	var id int64
	err := r.DB.QueryRowContext(ctx, `
		INSERT INTO sync_events (application, target_gcp_project, trigger, actor, from_image, to_image, started_at, result)
		VALUES ($1, $2, $3, $4, $5, $6, now(), 'in_progress')
		RETURNING id`,
		unit.App, unit.Project, trigger, actor, nullIfEmpty(fromImage), toImage).Scan(&id)
	return id, err
}

func (r *Reconciler) updateSyncEvent(ctx context.Context, id int64, result, errMsg string) error {
	_, err := r.DB.ExecContext(ctx, `
		UPDATE sync_events SET finished_at = now(), result = $1, error = $2 WHERE id = $3`,
		result, nullIfEmpty(errMsg), id)
	return err
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// upsert writes res into the applications table and returns res with
// StatusSince/HealthSince populated from the row (unchanged if this pass's
// status/health matches what was already there, reset to now() if not) —
// the Notifier's duration-based rules (§5.8) depend on these being accurate.
func (r *Reconciler) upsert(ctx context.Context, res Result) (Result, error) {
	err := r.DB.QueryRowContext(ctx, `
		INSERT INTO applications (name, target_gcp_project, desired_image, live_image, status, health, status_since, health_since, last_reconciled_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now(), now())
		ON CONFLICT (name, target_gcp_project) DO UPDATE SET
			-- A transient manifest-fetch failure leaves res.DesiredImage
			-- empty for that pass (reconcile() never reached the point of
			-- setting it) — don't let that blank out a previously-recorded
			-- desired_image; keep the last known-good value instead.
			desired_image = CASE WHEN EXCLUDED.desired_image = '' THEN applications.desired_image ELSE EXCLUDED.desired_image END,
			-- Same exposure as desired_image above: a transient live-state
			-- fetch failure leaves res.LiveImage empty (nullIfEmpty turns
			-- that into NULL) for that pass — don't let that blank out a
			-- previously-observed live_image.
			live_image = CASE WHEN EXCLUDED.live_image IS NULL THEN applications.live_image ELSE EXCLUDED.live_image END,
			status = EXCLUDED.status,
			health = EXCLUDED.health,
			status_since = CASE WHEN applications.status = EXCLUDED.status THEN applications.status_since ELSE now() END,
			health_since = CASE WHEN applications.health = EXCLUDED.health THEN applications.health_since ELSE now() END,
			last_reconciled_at = now()
		RETURNING status_since, health_since`,
		res.Unit.App, res.Unit.Project, res.DesiredImage, nullIfEmpty(res.LiveImage), res.Status, res.Health,
	).Scan(&res.StatusSince, &res.HealthSince)
	return res, err
}
