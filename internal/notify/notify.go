// Package notify implements §5.8's rule engine: syncFailed, healthDegraded,
// and outOfSyncGated, each debounced per (sync unit, rule) in Postgres so a
// sustained failure doesn't spam the sink. v1 ships one sink (Slack); the
// engine itself is sink-agnostic.
package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/reconcile"
)

// DefaultDebounceInterval matches §5.8's default: at most once per hour per
// (sync unit, rule).
const DefaultDebounceInterval = time.Hour

// claimTTL bounds how long an in-flight "claimed but not yet confirmed
// sent" notification blocks a retry — generous relative to Sink.Send's own
// timeout (10s for SlackSink), so a live attempt is never pre-empted, but
// short enough that a crash or a lost connection mid-send only delays the
// next real attempt by claimTTL, not the full DebounceInterval.
const claimTTL = 30 * time.Second

// Sink delivers a rendered notification message. v1 has one implementation
// (SlackSink); additional sinks are additive, not a redesign (§5.8).
type Sink interface {
	Send(ctx context.Context, message string) error
}

// db is the subset of *sql.DB the evaluator needs — an interface so tests
// can use a real Postgres without pulling in *sql.DB directly.
type db interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Evaluator implements reconcile.Notifier: evaluated once per reconcile
// pass per sync unit.
type Evaluator struct {
	DB               db
	Sink             Sink
	Rules            []config.NotifyRule
	DebounceInterval time.Duration // 0 means DefaultDebounceInterval
	ClaimTTL         time.Duration // 0 means claimTTL
}

func (e *Evaluator) Evaluate(ctx context.Context, res reconcile.Result) error {
	var errs []error
	for _, rule := range e.Rules {
		var err error
		switch rule.On {
		case "syncFailed":
			err = e.evalSyncFailed(ctx, res, rule)
		case "healthDegraded":
			err = e.evalHealthDegraded(ctx, res, rule)
		case "outOfSyncGated":
			err = e.evalOutOfSyncGated(ctx, res, rule)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Evaluator) evalSyncFailed(ctx context.Context, res reconcile.Result, _ config.NotifyRule) error {
	if !res.DeployFailed {
		return nil
	}
	msg := fmt.Sprintf("runcd: sync failed for %s in %s: %s", res.Unit.App, res.Unit.Project, res.FailureMessage)
	return e.maybeNotify(ctx, res, "syncFailed", msg)
}

func (e *Evaluator) evalHealthDegraded(ctx context.Context, res reconcile.Result, rule config.NotifyRule) error {
	if res.Health != "Degraded" || rule.ForMinutes == nil || res.HealthSince.IsZero() {
		return nil
	}
	if time.Since(res.HealthSince) < time.Duration(*rule.ForMinutes)*time.Minute {
		return nil
	}
	msg := fmt.Sprintf("runcd: %s in %s has been Degraded since %s", res.Unit.App, res.Unit.Project, res.HealthSince.Format(time.RFC3339))
	// Fold the threshold into the debounce key: two healthDegraded rules
	// with different forMinutes (e.g. an early warning plus an escalation)
	// must debounce independently, not collide on one "healthDegraded" row.
	return e.maybeNotify(ctx, res, fmt.Sprintf("healthDegraded:%d", *rule.ForMinutes), msg)
}

func (e *Evaluator) evalOutOfSyncGated(ctx context.Context, res reconcile.Result, rule config.NotifyRule) error {
	if res.Status != "OutOfSync" || autoSyncEnabled(res.Unit.Sync) || rule.ForHours == nil || res.StatusSince.IsZero() {
		return nil
	}
	if time.Since(res.StatusSince) < time.Duration(*rule.ForHours)*time.Hour {
		return nil
	}
	msg := fmt.Sprintf("runcd: gated sync unit %s in %s has been OutOfSync since %s", res.Unit.App, res.Unit.Project, res.StatusSince.Format(time.RFC3339))
	return e.maybeNotify(ctx, res, fmt.Sprintf("outOfSyncGated:%d", *rule.ForHours), msg)
}

func autoSyncEnabled(sync config.SyncPolicy) bool {
	return sync.Auto != nil && *sync.Auto
}

// maybeNotify claims, sends, then confirms — three separate statements, not
// one transaction held open across Sink.Send. Holding a pooled Postgres
// connection for however long a webhook call takes (up to Sink's own
// timeout) starved every other consumer of that same pool under load,
// including internal/leader's Claim(), in a real production incident.
//
// The claim (claim_expires_at, a short TTL, same idea as sync_locks) is
// deliberately separate from the debounce marker itself (last_notified_at,
// only ever set after Send actually succeeds): a single "hold the claim
// open until Send returns, then commit-or-rollback" step would need a
// transaction (the exact thing removed above), and a "commit the claim
// first, revert on failure" design — this function's earlier version —
// left a real gap: a crash (or a lost connection) between the claim
// committing and either Send returning or the revert succeeding left the
// claim stuck, silently dropping a real failure notification for up to the
// full DebounceInterval with nothing to retry it. Splitting the claim's own
// expiry from the debounce window means any interruption — a crash, a
// failed revert, anything — self-heals within claimTTL instead of
// depending on any single follow-up write succeeding.
func (e *Evaluator) maybeNotify(ctx context.Context, res reconcile.Result, rule, message string) error {
	interval := e.DebounceInterval
	if interval <= 0 {
		interval = DefaultDebounceInterval
	}
	ttl := e.ClaimTTL
	if ttl <= 0 {
		ttl = claimTTL
	}

	// $4/$5 are numeric second counts multiplied by a literal 1-second
	// interval, not Duration.String() cast to ::interval — Go's
	// time.Duration.String() only happens to produce Postgres-parseable
	// text for whole-second/millisecond values; a sub-millisecond duration
	// would format with a unit ("µs", "ns") Postgres's interval parser
	// rejects (same bug class as internal/leader/lease.go). 'epoch' as the
	// initial last_notified_at on a brand-new row is always more than
	// interval ago, so a never-notified (unit, rule) is immediately
	// eligible, same as before.
	var claimed bool
	err := e.DB.QueryRowContext(ctx, `
		INSERT INTO notification_debounce (application, target_gcp_project, rule, last_notified_at, claim_expires_at)
		VALUES ($1, $2, $3, 'epoch', now() + ($5 * interval '1 second'))
		ON CONFLICT (application, target_gcp_project, rule) DO UPDATE
		  SET claim_expires_at = now() + ($5 * interval '1 second')
		WHERE notification_debounce.last_notified_at < now() - ($4 * interval '1 second')
		  AND (notification_debounce.claim_expires_at IS NULL OR notification_debounce.claim_expires_at < now())
		RETURNING true`,
		res.Unit.App, res.Unit.Project, rule, interval.Seconds(), ttl.Seconds(),
	).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // debounced, or another attempt currently holds the claim
	}
	if err != nil {
		return fmt.Errorf("debounce claim for %s/%s/%s: %w", res.Unit.App, res.Unit.Project, rule, err)
	}

	if err := e.Sink.Send(ctx, message); err != nil {
		// Best-effort, immediate revert so a retry doesn't have to wait out
		// claimTTL — but this is an optimization, not the safety net: even
		// if this Exec itself fails, the claim expires on its own.
		revertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = e.DB.ExecContext(revertCtx, `
			UPDATE notification_debounce SET claim_expires_at = NULL
			WHERE application = $1 AND target_gcp_project = $2 AND rule = $3`,
			res.Unit.App, res.Unit.Project, rule)
		return fmt.Errorf("send notification for %s/%s/%s: %w", res.Unit.App, res.Unit.Project, rule, err)
	}

	if _, err := e.DB.ExecContext(ctx, `
		UPDATE notification_debounce SET last_notified_at = now(), claim_expires_at = NULL
		WHERE application = $1 AND target_gcp_project = $2 AND rule = $3`,
		res.Unit.App, res.Unit.Project, rule,
	); err != nil {
		return fmt.Errorf("confirm sent notification for %s/%s/%s: %w", res.Unit.App, res.Unit.Project, rule, err)
	}
	return nil
}
