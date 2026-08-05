package api

import (
	"log/slog"
	"net/http"
)

// handleImageEvent is the optional Eventarc add-on's entry point: a trigger
// on Cloud Audit Logs (serviceName=artifactregistry.googleapis.com,
// methodName=Docker-PutManifest — Artifact Registry has no native Eventarc
// event type of its own, only audit-log-based triggers) can point here to
// make the auto-reconcile loop react to a fresh image push within seconds
// instead of waiting out the rest of RECONCILE_INTERVAL.
//
// Deliberately ignores the event body/CloudEvent payload entirely — this
// isn't "find the one sync unit this image affects," it's "something
// changed, so run the loop's regular tick sooner." reconcile.RunOnce
// already only redeploys units that are actually OutOfSync (or Invalid,
// etc., where it does nothing), so an extra early pass over the whole
// fleet is harmless, just slightly wasteful at very large scale.
// ponytail: add per-unit targeting (parse the audit log's resourceName,
// match against declared image repos) if fleet size ever makes that waste
// worth avoiding — no evidence it does yet.
//
// h.ImageEvents nil means the feature was never configured (no env vars
// set in cmd/controller/main.go) — 404s unconditionally, the same "route
// exists, does nothing unless configured" shape as an unset
// notify.slackWebhookUrl. This keeps the route safe to leave registered
// even for deployments that never wire up an Eventarc trigger.
func (h *Handler) handleImageEvent(w http.ResponseWriter, r *http.Request) {
	if h.ImageEvents == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.ImageEvents.Verify(r); err != nil {
		// Same posture as verify() for the IAP/OAuth path: the underlying
		// reason (bad audience — see the RUNCD_IMAGE_EVENTS_AUDIENCE footgun
		// above — wrong service account, expired token) never reaches the
		// response body, but without logging it a misconfigured trigger
		// 403s forever with zero operator-visible signal.
		slog.Error("image-events auth", "error", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Any replica can receive the Eventarc push, but only the leader
	// actually reconciles — a non-leader nudging its own reconcile loop
	// would do nothing (RunOnce never runs there) and nudging some other
	// replica would need cross-replica messaging this deliberately skips.
	// A non-leader (or an unconfigured IsLeader/NudgeReconcile) just 202s;
	// the leader's own next RECONCILE_INTERVAL tick still catches it —
	// same fallback the polling loop always was, not a second mechanism.
	if h.IsLeader != nil && h.IsLeader() && h.NudgeReconcile != nil {
		h.NudgeReconcile()
	}
	w.WriteHeader(http.StatusAccepted)
}
