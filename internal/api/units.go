package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/rbac"
)

// UnitLister exposes every currently-configured sync unit, not just ones
// that have already been reconciled at least once — the dashboard's list
// view needs the full config-derived set (including a unit pending its
// first reconcile pass), which the applications table alone can't give it.
type UnitLister interface {
	List() []expander.SyncUnit
}

// unitView is the dashboard's read model for one sync unit: its declared
// config plus whatever's been persisted about it, if anything yet.
type unitView struct {
	App              string     `json:"app"`
	Project          string     `json:"project"`
	Env              string     `json:"env"`
	Region           string     `json:"region"`
	Auto             bool       `json:"auto"`
	DesiredImage     string     `json:"desiredImage,omitempty"`
	LiveImage        string     `json:"liveImage,omitempty"`
	Status           string     `json:"status"`
	Health           string     `json:"health"`
	LastReconciledAt *time.Time `json:"lastReconciledAt,omitempty"`
	// CanSync is computed server-side from the caller's own RBAC scope
	// (§5.9) — the dashboard has no way to evaluate rbac.CanSync itself,
	// so it needs this to decide whether the Sync button is enabled for
	// *this* unit, not just whether the unit exists.
	CanSync bool `json:"canSync"`
}

// pendingStatus/pendingHealth mark a unit that's in the current config but
// has never been through a reconcile pass — distinct from any real status
// enum value, so the dashboard can render it as "not yet synced" rather
// than a stale/incorrect real status.
const (
	pendingStatus = "Pending"
	pendingHealth = "Pending"
)

func unitViewFrom(u expander.SyncUnit, rbacCfg *rbac.Config, email string) unitView {
	return unitView{
		App:     u.App,
		Project: u.Project,
		Env:     u.Env,
		Region:  u.Region,
		Auto:    u.Sync.Auto != nil && *u.Sync.Auto,
		Status:  pendingStatus,
		Health:  pendingHealth,
		CanSync: rbac.CanSync(rbacCfg, email, u),
	}
}

func applyRow(v *unitView, row ApplicationRow) {
	v.DesiredImage = row.DesiredImage
	v.LiveImage = row.LiveImage
	v.Status = row.Status
	v.Health = row.Health
	t := row.LastReconciledAt
	v.LastReconciledAt = &t
}

// handleListUnits serves every currently-configured sync unit with its
// last-known status/health, open to any authenticated caller — read
// visibility has no RBAC gate (§5.9); only Sync itself does.
func (h *Handler) handleListUnits(w http.ResponseWriter, r *http.Request) {
	email, ok := verify(w, r, h.Auth)
	if !ok {
		return
	}

	lister, ok := h.Units.(UnitLister)
	if !ok {
		http.Error(w, "unit listing not supported", http.StatusNotImplemented)
		return
	}
	units := lister.List()

	rows, err := h.Status.ListApplications(r.Context())
	if err != nil {
		slog.Error("list applications", "error", err)
		http.Error(w, "failed to list units", http.StatusInternalServerError)
		return
	}
	byKey := make(map[string]ApplicationRow, len(rows))
	for _, row := range rows {
		byKey[row.App+"/"+row.Project] = row
	}

	views := make([]unitView, 0, len(units))
	for _, u := range units {
		v := unitViewFrom(u, h.RBAC.Get(), email)
		if row, ok := byKey[u.App+"/"+u.Project]; ok {
			applyRow(&v, row)
		}
		views = append(views, v)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(views)
}

// handleUnitDetail serves one sync unit's full state — the same fields as
// the list view, used by the dashboard's diff view (desired vs live).
func (h *Handler) handleUnitDetail(w http.ResponseWriter, r *http.Request) {
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

	v := unitViewFrom(unit, h.RBAC.Get(), email)
	row, found, err := h.Status.GetApplication(r.Context(), app, project)
	if err != nil {
		slog.Error("get application", "app", app, "project", project, "error", err)
		http.Error(w, "failed to load unit", http.StatusInternalServerError)
		return
	}
	if found {
		applyRow(&v, row)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// dryRunView previews what a manual sync would compute — status, health,
// desired/live image — without any of it being persisted. Deliberately
// omits the reconcile error text itself: like handleSync, a precondition
// failure or a raw GCP/DB error isn't safe to echo verbatim to the client
// (see logSensitive in api.go); Status already surfaces Invalid/Missing.
type dryRunView struct {
	App          string `json:"app"`
	Project      string `json:"project"`
	Status       string `json:"status"`
	Health       string `json:"health"`
	DesiredImage string `json:"desiredImage,omitempty"`
	LiveImage    string `json:"liveImage,omitempty"`
}

// handleDryRun previews a manual sync (§ dry-run/diff preview): the same
// fetch/precondition/diff/health computation ManualSync does, but never
// deploys and never touches the DB — safe to call right before a real sync,
// or repeatedly, with no side effects.
//
// RBAC-checked like handleSync, unlike the rest of the read views: those
// only ever read Postgres, but a dry run makes the same real Cloud
// Run/Pub-Sub API calls a sync does — without this gate, any authenticated
// caller (not just one with sync access to this app/project) could burn GCP
// quota on demand with no rate limit.
func (h *Handler) handleDryRun(w http.ResponseWriter, r *http.Request) {
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

	if !rbac.CanSync(h.RBAC.Get(), email, unit) {
		http.Error(w, "forbidden: no role grants sync access to this app/project", http.StatusForbidden)
		return
	}

	res := h.Reconciler.Load().DryRun(r.Context(), unit)
	if res.Err != nil {
		slog.Error("dry run", "app", app, "project", project, "error", res.Err)
	}

	v := dryRunView{
		App:          app,
		Project:      project,
		Status:       res.Status,
		Health:       res.Health,
		DesiredImage: res.DesiredImage,
		LiveImage:    res.LiveImage,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// orphanView is one live Cloud Run service present in GCP but not declared
// by any current sync unit for that project/region (§ prune).
type orphanView struct {
	Project string `json:"project"`
	Region  string `json:"region"`
	App     string `json:"app"`
}

// handleOrphans runs a live prune/orphan-detection scan (§ prune) over
// every currently-configured unit's project/region and reports what's
// found — open to any authenticated caller, like every other read view;
// this only flags, it never deletes anything.
func (h *Handler) handleOrphans(w http.ResponseWriter, r *http.Request) {
	if _, ok := verify(w, r, h.Auth); !ok {
		return
	}

	lister, ok := h.Units.(UnitLister)
	if !ok {
		http.Error(w, "unit listing not supported", http.StatusNotImplemented)
		return
	}

	orphans, err := h.Reconciler.Load().DetectOrphans(r.Context(), lister.List())
	if err != nil {
		slog.Error("detect orphans", "error", err)
		http.Error(w, "failed to detect orphans", http.StatusInternalServerError)
		return
	}

	views := make([]orphanView, len(orphans))
	for i, o := range orphans {
		views[i] = orphanView{Project: o.Project, Region: o.Region, App: o.App}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(views)
}

// syncEventView is the JSON shape for one sync_events row.
type syncEventView struct {
	ID         int64      `json:"id"`
	Trigger    string     `json:"trigger"`
	Actor      string     `json:"actor,omitempty"`
	FromImage  string     `json:"fromImage,omitempty"`
	ToImage    string     `json:"toImage"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Result     string     `json:"result"`
	Error      string     `json:"error,omitempty"`
}

// defaultHistoryLimit caps how many sync_events rows the history view
// returns — sync_events is append-only and never pruned (§5.2), so an
// unbounded query would grow without limit over a unit's lifetime.
const defaultHistoryLimit = 50

func (h *Handler) handleUnitHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := verify(w, r, h.Auth); !ok {
		return
	}

	project := r.PathValue("project")
	app := r.PathValue("app")
	if _, ok := h.Units.Find(app, project); !ok {
		http.Error(w, "unknown app/project", http.StatusNotFound)
		return
	}

	events, err := h.Status.SyncHistory(r.Context(), app, project, defaultHistoryLimit)
	if err != nil {
		slog.Error("sync history", "app", app, "project", project, "error", err)
		http.Error(w, "failed to load sync history", http.StatusInternalServerError)
		return
	}

	views := make([]syncEventView, len(events))
	for i, e := range events {
		views[i] = syncEventView(e)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(views)
}
