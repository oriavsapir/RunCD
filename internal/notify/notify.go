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

	"github.com/argorun/argorun/internal/config"
	"github.com/argorun/argorun/internal/reconcile"
)

// DefaultDebounceInterval matches §5.8's default: at most once per hour per
// (sync unit, rule).
const DefaultDebounceInterval = time.Hour

// Sink delivers a rendered notification message. v1 has one implementation
// (SlackSink); additional sinks are additive, not a redesign (§5.8).
type Sink interface {
	Send(ctx context.Context, message string) error
}

// db is the subset of *sql.DB the evaluator needs — an interface so tests
// can use a real Postgres without pulling in *sql.DB directly.
type db interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Evaluator implements reconcile.Notifier: evaluated once per reconcile
// pass per sync unit.
type Evaluator struct {
	DB               db
	Sink             Sink
	Rules            []config.NotifyRule
	DebounceInterval time.Duration // 0 means DefaultDebounceInterval
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
	msg := fmt.Sprintf("argorun: sync failed for %s in %s: %s", res.Unit.App, res.Unit.Project, res.FailureMessage)
	return e.maybeNotify(ctx, res, "syncFailed", msg)
}

func (e *Evaluator) evalHealthDegraded(ctx context.Context, res reconcile.Result, rule config.NotifyRule) error {
	if res.Health != "Degraded" || rule.ForMinutes == nil || res.HealthSince.IsZero() {
		return nil
	}
	if time.Since(res.HealthSince) < time.Duration(*rule.ForMinutes)*time.Minute {
		return nil
	}
	msg := fmt.Sprintf("argorun: %s in %s has been Degraded since %s", res.Unit.App, res.Unit.Project, res.HealthSince.Format(time.RFC3339))
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
	msg := fmt.Sprintf("argorun: gated sync unit %s in %s has been OutOfSync since %s", res.Unit.App, res.Unit.Project, res.StatusSince.Format(time.RFC3339))
	return e.maybeNotify(ctx, res, fmt.Sprintf("outOfSyncGated:%d", *rule.ForHours), msg)
}

func autoSyncEnabled(sync config.SyncPolicy) bool {
	return sync.Auto != nil && *sync.Auto
}

// maybeNotify atomically checks-and-updates the debounce row in one
// statement: the conditional DO UPDATE only applies (and only then does
// RETURNING produce a row) if the last notification for this (unit, rule)
// was outside the debounce window, so concurrent callers can't double-send.
func (e *Evaluator) maybeNotify(ctx context.Context, res reconcile.Result, rule, message string) error {
	interval := e.DebounceInterval
	if interval <= 0 {
		interval = DefaultDebounceInterval
	}

	var fired bool
	err := e.DB.QueryRowContext(ctx, `
		INSERT INTO notification_debounce (application, target_gcp_project, rule, last_notified_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (application, target_gcp_project, rule) DO UPDATE SET last_notified_at = now()
		WHERE notification_debounce.last_notified_at < now() - $4::interval
		RETURNING true`,
		res.Unit.App, res.Unit.Project, rule, interval.String(),
	).Scan(&fired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // debounced — not an error, just not time yet
	}
	if err != nil {
		return fmt.Errorf("debounce check for %s/%s/%s: %w", res.Unit.App, res.Unit.Project, rule, err)
	}

	return e.Sink.Send(ctx, message)
}
