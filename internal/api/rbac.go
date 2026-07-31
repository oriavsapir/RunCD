package api

import (
	"encoding/json"
	"net/http"
)

type rbacRoleView struct {
	Subject string   `json:"subject"`
	Role    string   `json:"role"`
	Scope   []string `json:"scope"`
}

// handleListRBAC serves the currently-loaded rbac.yaml roles, open to any
// authenticated caller like the other read views (§5.9) — it's who-can-do-
// what, not a secret, and the dashboard's settings view needs it to show
// the access model without duplicating rbac.yaml by hand.
func (h *Handler) handleListRBAC(w http.ResponseWriter, r *http.Request) {
	if _, ok := verify(w, r, h.Auth); !ok {
		return
	}

	cfg := h.RBAC.Get()
	var views []rbacRoleView
	if cfg != nil {
		views = make([]rbacRoleView, 0, len(cfg.Roles))
		for _, rule := range cfg.Roles {
			views = append(views, rbacRoleView{
				Subject: rule.Subject,
				Role:    string(rule.Role),
				Scope:   rule.Scope,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(views)
}
