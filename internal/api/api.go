// Package api serves the manual (gated) sync request path (§5.9/FR4):
// verify the caller's OAuth identity, check RBAC, then hand off to
// reconcile.Reconciler.ManualSync.
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/argorun/argorun/internal/auth"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/rbac"
	"github.com/argorun/argorun/internal/reconcile"
)

// UnitLookup resolves an (app, project) pair to the sync unit currently
// known for it. A real implementation is refreshed each reconcile pass;
// nothing wires that up yet (no git polling exists yet either — see
// PROGRESS.md), so this stays an interface.
type UnitLookup interface {
	Find(app, project string) (expander.SyncUnit, bool)
}

// StaticUnits is the simplest UnitLookup: a fixed map, keyed "app/project".
// Useful for tests and as a placeholder until a real, live-refreshed lookup
// exists.
type StaticUnits map[string]expander.SyncUnit

func (m StaticUnits) Find(app, project string) (expander.SyncUnit, bool) {
	u, ok := m[app+"/"+project]
	return u, ok
}

// Handler wires auth -> RBAC -> ManualSync for the gated sync endpoint.
type Handler struct {
	Auth       auth.Authenticator
	RBAC       *rbac.Config
	Units      UnitLookup
	Reconciler *reconcile.Reconciler
}

// NewMux registers the API's routes on a fresh http.ServeMux.
func NewMux(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sync/{project}/{app}", h.handleSync)
	return mux
}

type syncResponse struct {
	App     string `json:"app"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Health  string `json:"health"`
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request) {
	email, err := h.Auth.Verify(r)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	project := r.PathValue("project")
	app := r.PathValue("app")
	unit, ok := h.Units.Find(app, project)
	if !ok {
		http.Error(w, "unknown app/project", http.StatusNotFound)
		return
	}

	// §5.9: RBAC-checked — everyone authenticated gets read-only by
	// default, only an admin/syncer rule whose scope covers this unit may
	// trigger a sync.
	if !rbac.CanSync(h.RBAC, email, unit) {
		http.Error(w, "forbidden: no role grants sync access to this app/project", http.StatusForbidden)
		return
	}

	res, err := h.Reconciler.ManualSync(r.Context(), unit, email)
	if err != nil {
		// The applications-table write itself failed — an infra error, not
		// business-level. Log it server-side; don't echo it to the caller.
		logSensitive(app, project, email, err)
		http.Error(w, "sync failed", http.StatusInternalServerError)
		return
	}
	if res.Err != nil {
		// res.Err mixes business-level outcomes (a failed precondition,
		// say) with genuine infra errors (a raw wrapped GCP/DB error from a
		// failed live-state fetch or deploy, see reconcile.go) — there's no
		// reliable way to tell which from here, so none of it goes in the
		// response body. res.Status/res.Health already tell the caller
		// something's wrong (Invalid/Missing); the specific reason lives in
		// sync_events and the server log, not the HTTP response.
		logSensitive(app, project, email, res.Err)
	}

	resp := syncResponse{App: app, Project: project, Status: res.Status, Health: res.Health}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// logSensitive logs a manual-sync failure server-side only. %q (not %s) on
// the request-controlled fields — app/project come from the URL path and
// email from a verified ID token, but none of the three are sanitized
// against CRLF — neutralizes a log-injection attempt by escaping control
// characters into a literal "\n" rather than an actual newline byte.
// gosec's taint check flags any tainted value reaching Printf regardless
// of verb — it can't see that %q already defeats the injection.
func logSensitive(app, project, email string, err error) {
	log.Printf("manual sync %q/%q by %q: %v", app, project, email, err) //nolint:gosec
}
