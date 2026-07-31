package api

import (
	"encoding/json"
	"net/http"
)

// RuntimeInfo is the operational configuration that never changes after
// boot (unlike ManagedFields/Notifier on the Reconciler, which hot-reload —
// see cmd/controller/main.go's reconcileLoop) — set once at Handler
// construction.
type RuntimeInfo struct {
	ConfigRepo               string
	ConfigBranch             string
	ConfigPath               string
	RBACPath                 string
	ReconcileIntervalSeconds int
}

type configView struct {
	ConfigRepo               string   `json:"configRepo"`
	ConfigBranch             string   `json:"configBranch"`
	ConfigPath               string   `json:"configPath"`
	RBACPath                 string   `json:"rbacPath"`
	ReconcileIntervalSeconds int      `json:"reconcileIntervalSeconds"`
	ManagedFields            []string `json:"managedFields"`
	NotificationsEnabled     bool     `json:"notificationsEnabled"`
}

// handleConfig serves operational configuration — where runcd.yaml/
// rbac.yaml come from, the poll interval, which fields are managed, and
// whether Slack notifications are configured. Never the webhook URL
// itself (a secret), just whether one's set. Open to any authenticated
// caller, same posture as every other read view (§5.9).
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := verify(w, r, h.Auth); !ok {
		return
	}

	reconciler := h.Reconciler.Load()
	view := configView{
		ConfigRepo:               h.RuntimeInfo.ConfigRepo,
		ConfigBranch:             h.RuntimeInfo.ConfigBranch,
		ConfigPath:               h.RuntimeInfo.ConfigPath,
		RBACPath:                 h.RuntimeInfo.RBACPath,
		ReconcileIntervalSeconds: h.RuntimeInfo.ReconcileIntervalSeconds,
		ManagedFields:            reconciler.ManagedFields,
		NotificationsEnabled:     reconciler.Notifier != nil,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}
