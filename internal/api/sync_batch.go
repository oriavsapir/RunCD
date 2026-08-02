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

	var candidates []expander.SyncUnit
	for _, u := range lister.List() {
		if projectFilter != "" && u.Project != projectFilter {
			continue
		}
		candidates = append(candidates, u)
	}
	if onlyOutOfSync {
		candidates = filterOutOfSync(r.Context(), h.Status, candidates)
	}

	results := make([]batchSyncResult, len(candidates))
	var g errgroup.Group
	g.SetLimit(batchSyncWorkers)
	for i, unit := range candidates {
		g.Go(func() error {
			results[i] = h.syncOneForBatch(r.Context(), email, unit)
			return nil
		})
	}
	// syncOneForBatch never returns an error itself — every outcome,
	// including infra failures, is captured per-result instead, so g.Wait()
	// here can't actually fail.
	_ = g.Wait()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// filterOutOfSync keeps only units whose last-known status isn't Synced —
// a unit with no persisted row yet (never reconciled) is kept too, since
// "unknown" isn't "confirmed synced." Best-effort: a unit whose status
// lookup itself errors is kept rather than silently dropped from the
// batch — for a bulk sync a human explicitly asked for, "unknown, so try
// it" is a safer default than "unknown, so skip it."
func filterOutOfSync(ctx context.Context, status StatusStore, units []expander.SyncUnit) []expander.SyncUnit {
	var out []expander.SyncUnit
	for _, u := range units {
		row, found, err := status.GetApplication(ctx, u.App, u.Project)
		if err != nil {
			slog.Error("sync-batch: get application", "app", u.App, "project", u.Project, "error", err)
			out = append(out, u)
			continue
		}
		// "Synced" mirrors diff.Synced's string value — not imported
		// directly to avoid a new internal/api -> internal/diff dependency
		// for a single string comparison.
		if !found || row.Status != "Synced" {
			out = append(out, u)
		}
	}
	return out
}

func (h *Handler) syncOneForBatch(ctx context.Context, email string, unit expander.SyncUnit) batchSyncResult {
	result := batchSyncResult{App: unit.App, Project: unit.Project}

	// CanSyncFolders, not plain CanSync, so a rule scoped via "folder:<id>"
	// is honored too (§5.9) — same as handleSync.
	if !rbac.CanSyncFolders(h.RBAC.Get(), h.RBAC.FolderMembership(), email, unit) {
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
	return result
}
