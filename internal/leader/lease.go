// Package leader implements the Postgres-backed leader-election lease from
// design spec §5.3: one dependency (Postgres, already required for the audit
// log) instead of a second coordination system.
package leader

import (
	"context"
	"database/sql"
	"time"
)

const (
	// TTL is how long a claimed lease is valid before another replica may take it.
	TTL = 30 * time.Second
	// RenewInterval is how often the holder renews, well inside TTL.
	RenewInterval = 10 * time.Second
	// queryTimeout bounds a single Claim call — well under RenewInterval
	// and TTL, so a wedged connection (network partition, half-dead TCP)
	// surfaces as an error (triggering leading(false)) instead of blocking
	// forever while Postgres lets the lease expire and another replica
	// claims it, leaving this replica's in-memory state stuck believing
	// it's still leader.
	queryTimeout = 5 * time.Second
)

// db is the subset of *sql.DB Lease needs — kept as an interface so tests
// can inject a wrapper that fails on demand for one specific call.
type db interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Lease claims and renews the single-row leader_lease table.
type Lease struct {
	db       db
	holderID string
	ttl      time.Duration
}

func New(db *sql.DB, holderID string) *Lease {
	return &Lease{db: db, holderID: holderID, ttl: TTL}
}

// Claim attempts to become (or remain) leader. It succeeds if no one else
// holds a live lease, or if this holder already does (renewal). Returns
// whether this replica is leader after the attempt.
func (l *Lease) Claim(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// $2 is a numeric second count multiplied by a literal 1-second
	// interval, not l.ttl.String() cast to ::interval — Go's
	// time.Duration.String() only happens to produce Postgres-parseable
	// text for whole-second/millisecond values; a sub-millisecond TTL would
	// format with a unit ("µs", "ns") Postgres's interval parser rejects.
	res, err := l.db.ExecContext(ctx, `
		UPDATE leader_lease
		SET holder_id = $1, expires_at = now() + ($2 * interval '1 second')
		WHERE id = 1 AND (holder_id = $1 OR expires_at < now())`,
		l.holderID, l.ttl.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Run claims the lease and then renews it every RenewInterval until ctx is
// cancelled or a claim/renew fails. leading is called each time the leader
// status changes (true on becoming leader, false on losing it or on error).
func (l *Lease) Run(ctx context.Context, leading func(bool)) error {
	return l.runWithInterval(ctx, RenewInterval, leading)
}

func (l *Lease) runWithInterval(ctx context.Context, interval time.Duration, leading func(bool)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	wasLeader := false
	attempt := func() error {
		ok, err := l.Claim(ctx)
		if err != nil {
			// A failed claim/renewal means this replica can no longer
			// vouch for its own leadership — a caller that keeps deploying
			// on the last-known leading(true) after this point risks
			// running concurrently with whoever claims the lease next.
			if wasLeader {
				wasLeader = false
				leading(false)
			}
			return err
		}
		if ok != wasLeader {
			leading(ok)
			wasLeader = ok
		}
		return nil
	}

	if err := attempt(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := attempt(); err != nil {
				return err
			}
		}
	}
}
