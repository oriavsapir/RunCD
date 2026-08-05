package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/rbac"
	"github.com/runcd/runcd/internal/reconcile"
)

// batchSyncWorkers bounds concurrent ManualSync calls from one bulk-sync
// request — matching reconcile.DefaultWorkers, since this fans out over
// the same GCP/DB-calling path RunOnce's own worker pool already does.
const batchSyncWorkers = reconcile.DefaultWorkers

type batchSyncResult struct {
	App     string `json:"app"`
	Project string `json:"project"`
	Status  string `json:"status,omitempty"`
	Health  string `json:"health,omitempty"`
	// Skipped explains why this unit was never actually deployed — one of
	// "forbidden" | "observing" | "inProgress" | "error". Empty means the
	// sync was attempted; Status/Health then reflect its outcome the same
	// way the single-unit sync response does.
	Skipped string `json:"skipped,omitempty"`
	// DeployFailed mirrors the single-unit sync response's 422: a deploy was
	// actually attempted and failed, as opposed to a blocked-before-deploy
	// outcome (bad manifest, failed precondition) that still reads as a
	// (business-level) 200.
	DeployFailed bool `json:"deployFailed,omitempty"`
}

// handleSyncBatch fans a manual sync out over every unit the caller's RBAC
// grants cover (§5.9), optionally narrowed by ?project= and
// ?filter=outOfSync — the "Sync All" / "sync what's out of sync"
// affordance ArgoCD has and this dashboard didn't, so a human isn't
// clicking Sync unit by unit across a whole environment. A unit an RBAC
// rule doesn't cover for this caller is skipped (reported, not silently
// dropped) rather than failing the whole batch — the same
// one-bad-unit-can't-take-down-the-rest posture reconcile.RunOnce already
// has for the auto loop.
func (h *Handler) handleSyncBatch(w http.ResponseWriter, r *http.Request) {
	email, ok := verify(w, r, h.Auth)
	if !ok {
		return
	}

	lister, ok := h.Units.(UnitLister)
	if !ok {
		http.Error(w, "unit listing not supported", http.StatusNotImplemented)
		return
	}

	projectFilter := r.URL.Query().Get("project")
	onlyOutOfSync := r.URL.Query().Get("filter") == "outOfSync"

	// Hoisted once, not read fresh per unit/goroutine — same reasoning as
	// handleListUnits: a hot-reload landing mid-batch must not let a grant
	// revoked partway through still authorize a later unit in this same
	// batch under a stale, pre-revocation snapshot.
	cfg := h.RBAC.Get()
	folderMembership := h.RBAC.FolderMembership()

	var candidates []expander.SyncUnit
	for _, u := range lister.List() {
		if projectFilter != "" && u.Project != projectFilter {
			continue
		}
		candidates = append(candidates, u)
	}
	// A caller with zero grants at all can't sync anything in the batch
	// regardless of out-of-sync status, so skip the (expensive, full-table)
	// scan below rather than let an unauthorized caller force it — same
	// HasAnyGrant guard handleOrphans already uses for the same reason.
	if onlyOutOfSync && rbac.HasAnyGrant(cfg, email) {
		candidates = filterOutOfSync(r.Context(), h.Status, candidates)
	}

	results := make([]batchSyncResult, len(candidates))
	var g errgroup.Group
	g.SetLimit(batchSyncWorkers)
	for i, unit := range candidates {
		g.Go(func() error {
			results[i] = h.syncOneForBatch(r.Context(), email, unit, cfg, folderMembership)
			return nil
		})
	}
	// syncOneForBatch never returns an error itself — every outcome,
	// including infra failures, is captured per-result instead, so g.Wait()
	// here can't actually fail.
	_ = g.Wait()

	deployFailed := false
	for _, res := range results {
		if res.DeployFailed {
			deployFailed = true
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if deployFailed {
		// Same posture as the single-unit sync response: a caller gating on
		// exit code/2xx (the CLI, CI) must not see an attempted-and-failed
		// deploy as success just because other units in the batch succeeded.
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_ = json.NewEncoder(w).Encode(results)
}

// filterOutOfSync keeps only units whose last-known status isn't Synced —
// a unit with no persisted row yet (never reconciled) is kept too, since
// "unknown" isn't "confirmed synced." Best-effort: if the status lookup
// itself errors, every unit is kept rather than silently dropped from the
// batch — for a bulk sync a human explicitly asked for, "unknown, so try
// it" is a safer default than "unknown, so skip it." One ListApplications
// call, not one GetApplication per unit — same batch-then-map pattern
// handleListUnits already uses, so a "sync all" over N units doesn't cost
// N sequential round trips just to decide which ones to include.
func filterOutOfSync(ctx context.Context, status StatusStore, units []expander.SyncUnit) []expander.SyncUnit {
	rows, err := status.ListApplications(ctx)
	if err != nil {
		slog.Error("sync-batch: list applications", "error", err)
		return units
	}
	byKey := make(map[string]ApplicationRow, len(rows))
	for _, row := range rows {
		byKey[row.App+"/"+row.Project] = row
	}

	var out []expander.SyncUnit
	for _, u := range units {
		row, found := byKey[u.App+"/"+u.Project]
		// "Synced" mirrors diff.Synced's string value — not imported
		// directly to avoid a new internal/api -> internal/diff dependency
		// for a single string comparison.
		if !found || row.Status != "Synced" {
			out = append(out, u)
		}
	}
	return out
}

// syncOneForBatch takes cfg/folderMembership as the caller's hoisted
// snapshot from handleSyncBatch rather than re-reading h.RBAC.Get() itself —
// a hot-reload landing mid-batch must not let different units in the same
// batch get evaluated against different RBAC snapshots.
func (h *Handler) syncOneForBatch(ctx context.Context, email string, unit expander.SyncUnit, cfg *rbac.Config, folderMembership map[string][]string) batchSyncResult {
	result := batchSyncResult{App: unit.App, Project: unit.Project}

	// CanSyncFolders, not plain CanSync, so a rule scoped via "folder:<id>"
	// is honored too (§5.9) — same as handleSync.
	if !rbac.CanSyncFolders(cfg, folderMembership, email, unit) {
		result.Skipped = "forbidden"
		return result
	}

	res, err := h.Reconciler.Load().ManualSync(ctx, unit, email)
	if err != nil {
		logSensitive(unit.App, unit.Project, email, err)
		result.Skipped = "error"
		return result
	}
	if errors.Is(res.Err, reconcile.ErrSyncInProgress) {
		result.Skipped = "inProgress"
		return result
	}
	if errors.Is(res.Err, reconcile.ErrObserveMode) {
		result.Skipped = "observing"
		return result
	}
	if res.Err != nil {
		// Same posture as handleSync: mixes business outcomes (failed
		// precondition) with genuine infra errors, so it's logged, not
		// echoed — result.Status already says Invalid/Missing.
		logSensitive(unit.App, unit.Project, email, res.Err)
	}
	result.Status, result.Health = res.Status, res.Health
	result.DeployFailed = res.DeployFailed
	return result
}
