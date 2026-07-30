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
)

// Lease claims and renews the single-row leader_lease table.
type Lease struct {
	db       *sql.DB
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
	res, err := l.db.ExecContext(ctx, `
		UPDATE leader_lease
		SET holder_id = $1, expires_at = now() + $2::interval
		WHERE id = 1 AND (holder_id = $1 OR expires_at < now())`,
		l.holderID, l.ttl.String())
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
