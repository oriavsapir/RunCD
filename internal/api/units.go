package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
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
	// Observing mirrors this unit's effective SyncPolicy.Observe — the
	// dashboard uses it to disable the Sync button and explain why, the
	// same way CanSync already does for an RBAC-denied unit, rather than
	// let a human hit Sync and get back a 409 they didn't expect.
	Observing bool `json:"observing"`
	// IgnoreFields/IgnorePreconditions surface this app's resource
	// exclusions (config.App) — without these, a unit whose Status
	// reflects a diff on an excluded field (e.g. OutOfSync from a traffic
	// mismatch when ignoreFields: [traffic]) has no way to explain that to
	// the dashboard, which otherwise only ever compares desiredImage vs
	// liveImage and would render something contradicting the badge above it.
	IgnoreFields        []string `json:"ignoreFields,omitempty"`
	IgnorePreconditions []string `json:"ignorePreconditions,omitempty"`
}

// pendingStatus/pendingHealth mark a unit that's in the current config but
// has never been through a reconcile pass — distinct from any real status
// enum value, so the dashboard can render it as "not yet synced" rather
// than a stale/incorrect real status.
const (
	pendingStatus = "Pending"
	pendingHealth = "Pending"
)

func unitViewFrom(u expander.SyncUnit, rbacCfg *rbac.Config, folderMembership map[string][]string, email string) unitView {
	return unitView{
		App:                 u.App,
		Project:             u.Project,
		Env:                 u.Env,
		Region:              u.Region,
		Auto:                u.Sync.Auto != nil && *u.Sync.Auto,
		Status:              pendingStatus,
		Health:              pendingHealth,
		CanSync:             rbac.CanSyncFolders(rbacCfg, folderMembership, email, u),
		Observing:           u.Sync.Observe != nil && *u.Sync.Observe,
		IgnoreFields:        u.IgnoreFields,
		IgnorePreconditions: u.IgnorePreconditions,
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

	// cfg/folderMembership are hoisted once, not read fresh per unit — a
	// hot-reload landing mid-loop would otherwise let different units in
	// the same response be evaluated against two different RBAC
	// snapshots (canSync inconsistent within one JSON response).
	cfg := h.RBAC.Get()
	folderMembership := h.RBAC.FolderMembership()
	views := make([]unitView, 0, len(units))
	for _, u := range units {
		v := unitViewFrom(u, cfg, folderMembership, email)
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

	v := unitViewFrom(unit, h.RBAC.Get(), h.RBAC.FolderMembership(), email)
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
	// Observing means this preview reflects real drift, but a real sync
	// would be blocked outright (shadow mode) — without this, a dry-run of
	// an observing unit looks identical to a normal preview of an
	// auto:false unit, silently hiding that even a forced manual sync
	// would go nowhere.
	Observing bool `json:"observing"`
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

	if !rbac.CanSyncFolders(h.RBAC.Get(), h.RBAC.FolderMembership(), email, unit) {
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
		Observing:    res.Observing,
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
// found. RBAC-checked like handleSync/handleDryRun, unlike the rest of the
// read views: it fans out real Cloud Run calls across every project/region
// the whole config touches, not just one unit, so there's no single unit
// to scope a check against — HasAnyGrant requires the caller to have some
// sync grant at all, rather than opening this to any authenticated caller.
func (h *Handler) handleOrphans(w http.ResponseWriter, r *http.Request) {
	email, ok := verify(w, r, h.Auth)
	if !ok {
		return
	}
	cfg := h.RBAC.Get()
	if !rbac.HasAnyGrant(cfg, email) {
		http.Error(w, "forbidden: no role grants sync access", http.StatusForbidden)
		return
	}

	lister, ok := h.Units.(UnitLister)
	if !ok {
		http.Error(w, "unit listing not supported", http.StatusNotImplemented)
		return
	}
	units := lister.List()

	// DetectOrphans returns partial results alongside a non-nil err when
	// only some project/region scans failed — serve what it found rather
	// than discarding a fleet-wide scan over one bad project (same "one bad
	// unit can't take down the fleet" principle RunOnce follows).
	orphans, err := h.Reconciler.Load().DetectOrphans(r.Context(), units)
	if err != nil {
		slog.Error("detect orphans", "error", err)
		if orphans == nil {
			http.Error(w, "failed to detect orphans", http.StatusInternalServerError)
			return
		}
	}

	// HasAnyGrant only proved the caller has *some* sync grant somewhere —
	// scanning is fleet-wide (no single unit to scope the check above
	// against), but the response must not leak every project's orphans to
	// a caller only scoped to, say, one env. A project counts as visible
	// here if the caller can sync at least one currently-configured unit in
	// it — the same set of projects DetectOrphans itself scanned.
	folderMembership := h.RBAC.FolderMembership()
	allowedProjects := make(map[string]bool)
	for _, u := range units {
		if rbac.CanSyncFolders(cfg, folderMembership, email, u) {
			allowedProjects[u.Project] = true
		}
	}

	views := make([]orphanView, 0, len(orphans))
	for _, o := range orphans {
		if !allowedProjects[o.Project] {
			continue
		}
		views = append(views, orphanView{Project: o.Project, Region: o.Region, App: o.App})
	}

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		// orphans != nil here (the err/nil-orphans case already returned
		// above) — some but not all project/region scans failed. 206 says
		// "this list may be incomplete" rather than a plain 200 a caller
		// would otherwise read as "these are definitively all the orphans."
		w.WriteHeader(http.StatusPartialContent)
	}
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
// returns by default — sync_events is append-only and never pruned (§5.2),
// so an unbounded query would grow without limit over a unit's lifetime.
// maxHistoryLimit bounds the "?limit=" override for the same reason.
const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 500
)

// handleUnitHistory is RBAC-checked like handleSync/handleDryRun, unlike
// the rest of the read views: sync_events.error is populated verbatim from
// a real deploy/DB error's Error() text (see reconcile.go's
// updateSyncEvent calls) — exactly the raw-infra-error-detail class
// handleSync's own response deliberately never echoes. Unlike that
// response, this IS the intended durable place to see the actual reason
// (§5.2/FR6's audit-trail requirement) — so rather than scrub it here too
// and leave no API path to it at all, visibility is restricted to callers
// who could also sync this unit.
func (h *Handler) handleUnitHistory(w http.ResponseWriter, r *http.Request) {
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
	if !rbac.CanSyncFolders(h.RBAC.Get(), h.RBAC.FolderMembership(), email, unit) {
		http.Error(w, "forbidden: no role grants sync access to this app/project", http.StatusForbidden)
		return
	}

	limit := defaultHistoryLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxHistoryLimit)
	}

	events, err := h.Status.SyncHistory(r.Context(), app, project, limit)
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
