// Package api serves the dashboard's read views (unit list, unit detail,
// sync history) and the manual (gated) sync request path (§5.9/FR4/§5.11):
// verify the caller's OAuth identity, check RBAC, then hand off to
// reconcile.Reconciler.ManualSync.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/runcd/runcd/internal/auth"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/rbac"
	"github.com/runcd/runcd/internal/reconcile"
)

// UnitLookup resolves an (app, project) pair to the sync unit currently
// known for it. The real implementation (cmd/controller's dynamicUnits) is
// refreshed each reconcile pass.
type UnitLookup interface {
	Find(app, project string) (expander.SyncUnit, bool)
}

// StaticUnits is the simplest UnitLookup: a fixed map, keyed "app/project".
// Used in tests.
type StaticUnits map[string]expander.SyncUnit

func (m StaticUnits) Find(app, project string) (expander.SyncUnit, bool) {
	u, ok := m[app+"/"+project]
	return u, ok
}

func (m StaticUnits) List() []expander.SyncUnit {
	out := make([]expander.SyncUnit, 0, len(m))
	for _, u := range m {
		out = append(out, u)
	}
	return out
}

// Handler wires auth -> RBAC -> ManualSync for the gated sync endpoint, and
// auth -> StatusStore for the dashboard's read-only views.
type Handler struct {
	Auth   auth.Authenticator
	RBAC   *rbac.Store
	Units  UnitLookup
	Status StatusStore
	// Reconciler is an atomic pointer, not a plain *reconcile.Reconciler,
	// so the controller can hot-swap it (new Notifier/ManagedFields after a
	// config reload — see cmd/controller/main.go's reconcileLoop) without a
	// data race against a manual sync reading it concurrently.
	Reconciler  *atomic.Pointer[reconcile.Reconciler]
	RuntimeInfo RuntimeInfo
	// Metrics is optional — nil skips registering GET /metrics entirely
	// (e.g. a test fixture that has no need for it). Build one via
	// NewMetricsHandler.
	Metrics http.Handler
}

// NewMux registers the API's routes on a fresh http.ServeMux.
func NewMux(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/units", h.handleListUnits)
	mux.HandleFunc("GET /api/units/{project}/{app}", h.handleUnitDetail)
	mux.HandleFunc("GET /api/units/{project}/{app}/history", h.handleUnitHistory)
	mux.HandleFunc("GET /api/units/{project}/{app}/dry-run", h.handleDryRun)
	mux.HandleFunc("GET /api/rbac", h.handleListRBAC)
	mux.HandleFunc("GET /api/config", h.handleConfig)
	mux.HandleFunc("GET /api/orphans", h.handleOrphans)
	mux.HandleFunc("POST /api/sync/{project}/{app}", h.handleSync)
	if h.Metrics != nil {
		mux.Handle("GET /metrics", h.Metrics)
	}
	return mux
}

type syncResponse struct {
	App     string `json:"app"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Health  string `json:"health"`
}

// verify authenticates the caller and logs the reason on failure — the
// underlying error (bad audience, expired token, malformed header, ...)
// never reaches the response body, so without this it's otherwise
// impossible to tell why a caller got a 401.
func verify(w http.ResponseWriter, r *http.Request, a auth.Authenticator) (string, bool) {
	email, err := a.Verify(r)
	if err != nil {
		slog.Error("auth", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return "", false
	}
	return email, true
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request) {
	email, ok := verify(w, r, h.Auth)
	if !ok {
		return
	}

	project := r.PathValue("project")
	app := r.PathValue("app")
	unit, ok := h.Units.Find(app, project)
	if !ok {
		http.Error(w, "unknown app/project", http.StatusNotFound)
		return
	}

	// CanSyncFolders, not plain CanSync, so a rule scoped via "folder:<id>"
	// is honored too (§5.9).
	if !rbac.CanSyncFolders(h.RBAC.Get(), h.RBAC.FolderMembership(), email, unit) {
		http.Error(w, "forbidden: no role grants sync access to this app/project", http.StatusForbidden)
		return
	}

	res, err := h.Reconciler.Load().ManualSync(r.Context(), unit, email)
	if err != nil {
		// The applications-table write itself failed — an infra error, not
		// business-level. Log it server-side; don't echo it to the caller.
		logSensitive(app, project, email, err)
		http.Error(w, "sync failed", http.StatusInternalServerError)
		return
	}
	if errors.Is(res.Err, reconcile.ErrSyncInProgress) {
		// The one res.Err case worth telling the caller about specifically:
		// unlike a failed precondition or a raw GCP/DB error (see the
		// general case below), "someone else is already syncing this" is
		// unambiguous and actionable — retry shortly, don't treat it as a
		// failed sync.
		http.Error(w, "sync already in progress for this app/project", http.StatusConflict)
		return
	}
	if errors.Is(res.Err, reconcile.ErrObserveMode) {
		// Another unambiguous, actionable case: the caller asked for a real
		// deploy but this unit's SyncPolicy has observe mode on, so nothing
		// happened — worth saying explicitly rather than a 200 that looks
		// like a no-op success.
		http.Error(w, "sync disabled: this app/environment is in observe mode (sync.observe)", http.StatusConflict)
		return
	}
	if res.Err != nil {
		// res.Err mixes business-level outcomes (failed precondition) with
		// genuine infra errors (raw wrapped GCP/DB error, see reconcile.go)
		// with no reliable way to tell which from here, so none of it goes
		// in the response body — res.Status/res.Health say enough
		// (Invalid/Missing), the detail lives in sync_events and the log.
		logSensitive(app, project, email, res.Err)
	}

	resp := syncResponse{App: app, Project: project, Status: res.Status, Health: res.Health}
	w.Header().Set("Content-Type", "application/json")
	if res.DeployFailed {
		// A blocked-before-deploy sync (bad manifest, failed precondition)
		// still gets 200 (res.Status already says Invalid). A deploy that
		// was actually attempted and failed must not read as success to a
		// caller gating on exit code/2xx (the CLI, CI).
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// logSensitive logs a manual-sync failure server-side only. app/project
// come from the URL path and email from a verified ID token — none of the
// three are sanitized against CRLF, but slog's JSON handler encodes every
// field as a JSON string value, which already neutralizes any log
// injection attempt (unlike a plain Printf format string).
func logSensitive(app, project, email string, err error) {
	slog.Error("manual sync", "app", app, "project", project, "email", email, "error", err)
}
