// Package api serves the manual (gated) sync request path (§5.9/FR4):
// verify the caller's OAuth identity, check RBAC, then hand off to
// reconcile.Reconciler.ManualSync.
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

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
	Error   string `json:"error,omitempty"`
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	email, err := h.Auth.Verify(r.Context(), token)
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
		// This is an infra-level failure (e.g. the applications-table
		// write itself failed) — distinct from res.Err, which is a
		// business-level outcome (precondition failure, etc.) that's
		// already deliberately surfaced in the response body below. Log
		// the real error server-side; don't echo it to the caller, who
		// only needed RBAC scope to reach this far, not visibility into
		// internal error text (DB errors, GCP error detail).
		// %q (not %s) on the request-controlled fields: app/project come
		// from the URL path and email from a verified ID token, but none
		// of the three are sanitized against CRLF, so %q neutralizes a
		// log-injection attempt by escaping control characters into a
		// literal "\n" rather than an actual newline byte. gosec's taint
		// check flags any tainted value reaching Printf regardless of verb
		// — it can't see that %q already defeats the injection.
		log.Printf("manual sync %q/%q by %q: %v", app, project, email, err) //nolint:gosec
		http.Error(w, "sync failed", http.StatusInternalServerError)
		return
	}

	resp := syncResponse{App: app, Project: project, Status: res.Status, Health: res.Health}
	if res.Err != nil {
		resp.Error = res.Err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}
