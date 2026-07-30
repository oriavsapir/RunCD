package leader

import (
	"context"
	"testing"
	"time"

	"github.com/argorun/argorun/internal/testutil"
)

func TestClaim_FirstReplicaWins(t *testing.T) {
	db := testutil.NewPostgres(t)
	l := New(db, "replica-a")

	ok, err := l.Claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("expected first claim to succeed")
	}
}

func TestClaim_SecondReplicaBlockedWhileLeaseLive(t *testing.T) {
	db := testutil.NewPostgres(t)
	a := New(db, "replica-a")
	b := New(db, "replica-b")

	ok, err := a.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("replica-a claim: ok=%v err=%v", ok, err)
	}

	ok, err = b.Claim(context.Background())
	if err != nil {
		t.Fatalf("replica-b claim: %v", err)
	}
	if ok {
		t.Fatal("replica-b should not be able to claim a live lease held by replica-a")
	}
}

func TestClaim_RenewByExistingHolderExtendsLease(t *testing.T) {
	db := testutil.NewPostgres(t)
	a := New(db, "replica-a")

	if ok, err := a.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}

	var firstExpiry time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT expires_at FROM leader_lease WHERE id = 1`).Scan(&firstExpiry); err != nil {
		t.Fatalf("read expires_at: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	if ok, err := a.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("renew claim: ok=%v err=%v", ok, err)
	}

	var secondExpiry time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT expires_at FROM leader_lease WHERE id = 1`).Scan(&secondExpiry); err != nil {
		t.Fatalf("read expires_at: %v", err)
	}

	if !secondExpiry.After(firstExpiry) {
		t.Fatalf("expected renew to push expires_at forward: first=%v second=%v", firstExpiry, secondExpiry)
	}
}

func TestClaim_CrashTakeoverAfterExpiry(t *testing.T) {
	db := testutil.NewPostgres(t)
	a := New(db, "replica-a")

	if ok, err := a.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("replica-a claim: ok=%v err=%v", ok, err)
	}

	// Simulate replica-a crashing: force its lease into the past, as if the
	// 30s TTL had elapsed with no renewal (§5.3).
	if _, err := db.ExecContext(context.Background(), `UPDATE leader_lease SET expires_at = now() - interval '1 second' WHERE id = 1`); err != nil {
		t.Fatalf("force-expire lease: %v", err)
	}

	b := New(db, "replica-b")
	ok, err := b.Claim(context.Background())
	if err != nil {
		t.Fatalf("replica-b takeover claim: %v", err)
	}
	if !ok {
		t.Fatal("replica-b should take over once replica-a's lease has expired")
	}

	// replica-a must no longer be able to renew — it lost the lease.
	ok, err = a.Claim(context.Background())
	if err != nil {
		t.Fatalf("replica-a post-takeover claim: %v", err)
	}
	if ok {
		t.Fatal("replica-a should not reclaim the lease after replica-b took over")
	}
}

func TestRun_ClaimsThenRenewsUntilCancelled(t *testing.T) {
	db := testutil.NewPostgres(t)
	l := New(db, "replica-a")
	l.ttl = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan bool, 4)
	done := make(chan error, 1)
	go func() {
		done <- l.runWithInterval(ctx, 20*time.Millisecond, func(leading bool) {
			events <- leading
		})
	}()

	select {
	case leading := <-events:
		if !leading {
			t.Fatal("expected first event to be becoming leader")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial leadership event")
	}

	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected Run to return context.Canceled")
	}
}

func TestRun_LosesLeadershipIfRenewalStops(t *testing.T) {
	db := testutil.NewPostgres(t)
	a := New(db, "replica-a")
	a.ttl = 60 * time.Millisecond

	if ok, err := a.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}

	// replica-a stops renewing (simulated crash); wait past its TTL.
	time.Sleep(100 * time.Millisecond)

	b := New(db, "replica-b")
	b.ttl = 60 * time.Millisecond
	ok, err := b.Claim(context.Background())
	if err != nil {
		t.Fatalf("replica-b claim: %v", err)
	}
	if !ok {
		t.Fatal("replica-b should claim leadership once replica-a stopped renewing past TTL")
	}
}
