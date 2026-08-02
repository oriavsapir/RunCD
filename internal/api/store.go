package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// statusDB is the subset of *sql.DB PostgresStatusStore needs — read-only,
// separate from reconcile.Reconciler's DB access, which only ever writes.
type statusDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ApplicationRow is the last-persisted reconcile result for a sync unit.
type ApplicationRow struct {
	App              string
	Project          string
	DesiredImage     string
	LiveImage        string
	Status           string
	Health           string
	LastReconciledAt time.Time
}

// SyncEvent is a single deploy attempt's audit record.
type SyncEvent struct {
	ID         int64
	Trigger    string
	Actor      string
	FromImage  string
	ToImage    string
	StartedAt  time.Time
	FinishedAt *time.Time
	Result     string
	Error      string
}

// SyncEventCount is one (trigger, result) bucket's total from sync_events —
// the /metrics endpoint's source for a cumulative counter, aggregated in
// SQL rather than fetching the full (never-pruned, §5.2) audit trail into
// memory just to group it in Go.
type SyncEventCount struct {
	Trigger string
	Result  string
	Count   int64
}

// StatusStore reads persisted reconcile state for the dashboard's
// read-only views.
type StatusStore interface {
	ListApplications(ctx context.Context) ([]ApplicationRow, error)
	GetApplication(ctx context.Context, app, project string) (row ApplicationRow, found bool, err error)
	SyncHistory(ctx context.Context, app, project string, limit int) ([]SyncEvent, error)
	SyncEventCounts(ctx context.Context) ([]SyncEventCount, error)
}

// PostgresStatusStore is the real StatusStore, backed by the same
// Postgres database the controller writes to.
type PostgresStatusStore struct {
	DB statusDB
}

func (s *PostgresStatusStore) ListApplications(ctx context.Context) ([]ApplicationRow, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT name, target_gcp_project, desired_image, COALESCE(live_image, ''), status, health, last_reconciled_at
		FROM applications`)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ApplicationRow
	for rows.Next() {
		var r ApplicationRow
		if err := rows.Scan(&r.App, &r.Project, &r.DesiredImage, &r.LiveImage, &r.Status, &r.Health, &r.LastReconciledAt); err != nil {
			return nil, fmt.Errorf("scan application row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	return out, nil
}

func (s *PostgresStatusStore) GetApplication(ctx context.Context, app, project string) (ApplicationRow, bool, error) {
	var r ApplicationRow
	err := s.DB.QueryRowContext(ctx, `
		SELECT name, target_gcp_project, desired_image, COALESCE(live_image, ''), status, health, last_reconciled_at
		FROM applications WHERE name = $1 AND target_gcp_project = $2`, app, project,
	).Scan(&r.App, &r.Project, &r.DesiredImage, &r.LiveImage, &r.Status, &r.Health, &r.LastReconciledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplicationRow{}, false, nil
	}
	if err != nil {
		return ApplicationRow{}, false, fmt.Errorf("get application %s/%s: %w", app, project, err)
	}
	return r, true, nil
}

func (s *PostgresStatusStore) SyncHistory(ctx context.Context, app, project string, limit int) ([]SyncEvent, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, trigger, COALESCE(actor, ''), COALESCE(from_image, ''), to_image, started_at, finished_at, result, COALESCE(error, '')
		FROM sync_events
		WHERE application = $1 AND target_gcp_project = $2
		ORDER BY started_at DESC
		LIMIT $3`, app, project, limit)
	if err != nil {
		return nil, fmt.Errorf("sync history for %s/%s: %w", app, project, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SyncEvent
	for rows.Next() {
		var e SyncEvent
		if err := rows.Scan(&e.ID, &e.Trigger, &e.Actor, &e.FromImage, &e.ToImage, &e.StartedAt, &e.FinishedAt, &e.Result, &e.Error); err != nil {
			return nil, fmt.Errorf("scan sync event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync history for %s/%s: %w", app, project, err)
	}
	return out, nil
}

func (s *PostgresStatusStore) SyncEventCounts(ctx context.Context) ([]SyncEventCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT trigger, result, count(*) FROM sync_events GROUP BY trigger, result`)
	if err != nil {
		return nil, fmt.Errorf("sync event counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SyncEventCount
	for rows.Next() {
		var c SyncEventCount
		if err := rows.Scan(&c.Trigger, &c.Result, &c.Count); err != nil {
			return nil, fmt.Errorf("scan sync event count: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync event counts: %w", err)
	}
	return out, nil
}
