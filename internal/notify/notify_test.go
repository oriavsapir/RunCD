package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/reconcile"
	"github.com/runcd/runcd/internal/testutil"
)

type fakeSink struct {
	messages []string
}

func (f *fakeSink) Send(_ context.Context, message string) error {
	f.messages = append(f.messages, message)
	return nil
}

// failNTimesSink fails its first N sends, then succeeds — for testing that
// a failed send doesn't consume the debounce window.
type failNTimesSink struct {
	failures int
	messages []string
}

func (f *failNTimesSink) Send(_ context.Context, message string) error {
	if f.failures > 0 {
		f.failures--
		return errors.New("simulated webhook failure")
	}
	f.messages = append(f.messages, message)
	return nil
}

func intPtr(v int) *int { return &v }

func TestEvaluate_SyncFailedFiresImmediately(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "syncFailed"}}}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "quota exceeded",
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %v", len(sink.messages), sink.messages)
	}
}

func TestEvaluate_SyncFailedDebouncedOnRepeat(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "syncFailed"}}, DebounceInterval: time.Hour}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "quota exceeded",
	}
	for i := 0; i < 3; i++ {
		if err := e.Evaluate(context.Background(), res); err != nil {
			t.Fatalf("Evaluate #%d: %v", i, err)
		}
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected exactly 1 message despite 3 failures within the debounce window, got %d", len(sink.messages))
	}
}

func TestEvaluate_SyncFailedFiresAgainAfterDebounceWindow(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "syncFailed"}}, DebounceInterval: 50 * time.Millisecond}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "quota exceeded",
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected 2 messages once the debounce window passed, got %d", len(sink.messages))
	}
}

func TestEvaluate_NoSyncFailedRuleMeansNoNotification(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: nil}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "quota exceeded",
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no messages with no configured rules, got %v", sink.messages)
	}
}

func TestEvaluate_HealthDegradedFiresPastThreshold(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "healthDegraded", ForMinutes: intPtr(10)}}}

	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health:      "Degraded",
		HealthSince: time.Now().Add(-15 * time.Minute),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sink.messages))
	}
}

func TestEvaluate_HealthDegradedNotYetPastThreshold(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "healthDegraded", ForMinutes: intPtr(10)}}}

	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health:      "Degraded",
		HealthSince: time.Now().Add(-2 * time.Minute),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no message before the threshold, got %v", sink.messages)
	}
}

func TestEvaluate_HealthyNeverFiresHealthDegraded(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "healthDegraded", ForMinutes: intPtr(10)}}}

	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health:      "Healthy",
		HealthSince: time.Now().Add(-time.Hour),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no message for Healthy, got %v", sink.messages)
	}
}

func TestEvaluate_OutOfSyncGatedFiresForGatedUnitPastThreshold(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "outOfSyncGated", ForHours: intPtr(4)}}}

	falseVal := false
	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Sync: config.SyncPolicy{Auto: &falseVal}},
		Status:      "OutOfSync",
		StatusSince: time.Now().Add(-5 * time.Hour),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sink.messages))
	}
}

func TestEvaluate_OutOfSyncGatedNeverFiresForAutoSyncUnit(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "outOfSyncGated", ForHours: intPtr(4)}}}

	trueVal := true
	res := reconcile.Result{
		// auto=true means this unit isn't "gated" — this rule is
		// specifically about targets waiting on a human (§5.8).
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-dev-01", Sync: config.SyncPolicy{Auto: &trueVal}},
		Status:      "OutOfSync",
		StatusSince: time.Now().Add(-24 * time.Hour),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no message for an auto-sync unit, got %v", sink.messages)
	}
}

func TestEvaluate_OutOfSyncGatedNotYetPastThreshold(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "outOfSyncGated", ForHours: intPtr(4)}}}

	falseVal := false
	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-eu", Sync: config.SyncPolicy{Auto: &falseVal}},
		Status:      "OutOfSync",
		StatusSince: time.Now().Add(-1 * time.Hour),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no message before the threshold, got %v", sink.messages)
	}
}

func TestEvaluate_DifferentRulesDebouncedIndependently(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "syncFailed"},
		{On: "healthDegraded", ForMinutes: intPtr(1)},
	}}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "boom",
		Health:         "Degraded",
		HealthSince:    time.Now().Add(-5 * time.Minute),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected both rules to fire independently, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_SameRuleTypeDifferentThresholdsDebounceIndependently
// regression-tests a debounce-key collision: an early-warning rule and an
// escalation rule of the same type (e.g. healthDegraded at 5 minutes and
// again at 60 minutes) must not share one debounce row — otherwise
// whichever fires first silently swallows the other until the full
// debounce interval elapses.
func TestEvaluate_SameRuleTypeDifferentThresholdsDebounceIndependently(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(5)},
		{On: "healthDegraded", ForMinutes: intPtr(60)},
	}}

	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health:      "Degraded",
		HealthSince: time.Now().Add(-90 * time.Minute), // past both thresholds
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected both the 5m and 60m healthDegraded rules to fire independently, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_FailedSendDoesNotConsumeDebounceWindow regression-tests a
// bug where last_notified_at was committed before Sink.Send ran: a
// failed/unreachable webhook would silently blackhole alerts for the full
// debounce window even though nothing was ever actually delivered. The fix
// only commits the debounce claim after Send succeeds, so a failed attempt
// can be retried on the very next Evaluate call.
func TestEvaluate_FailedSendDoesNotConsumeDebounceWindow(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &failNTimesSink{failures: 1}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{{On: "syncFailed"}}}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "boom",
	}

	if err := e.Evaluate(context.Background(), res); err == nil {
		t.Fatal("expected Evaluate to report the simulated send failure")
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no message delivered on the failed attempt, got %v", sink.messages)
	}

	// Retry immediately (no waiting out a debounce window) — must succeed,
	// proving the failed attempt above never claimed the debounce row.
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("retry Evaluate: %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected exactly 1 message after the retry succeeded, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_StuckClaimSelfHealsAfterTTL simulates a crash between the
// claim committing and Send even starting — a stuck claim_expires_at with
// no corresponding process still running. A later attempt must not wait
// out the full DebounceInterval for this; only claimTTL.
func TestEvaluate_StuckClaimSelfHealsAfterTTL(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{
		DB:               db,
		Sink:             sink,
		Rules:            []config.NotifyRule{{On: "syncFailed"}},
		DebounceInterval: time.Hour, // must not be what unblocks the retry
		ClaimTTL:         50 * time.Millisecond,
	}
	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "boom",
	}

	// Simulate a crashed prior attempt: claimed, never confirmed sent.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO notification_debounce (application, target_gcp_project, rule, last_notified_at, claim_expires_at)
		VALUES ('widget-api', 'example-prod-us', 'syncFailed', 'epoch', now() + interval '50 milliseconds')`,
	); err != nil {
		t.Fatalf("seed stuck claim: %v", err)
	}

	time.Sleep(100 * time.Millisecond) // let the stuck claim's TTL pass

	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate after stuck claim expired: %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected the notification to actually send once the stuck claim expired, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_LiveClaimBlocksConcurrentAttempt is the flip side: a claim
// that's genuinely still within its TTL (a real attempt actually in
// flight, not a crash) must block a concurrent attempt from also sending.
func TestEvaluate_LiveClaimBlocksConcurrentAttempt(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{
		DB:       db,
		Sink:     sink,
		Rules:    []config.NotifyRule{{On: "syncFailed"}},
		ClaimTTL: time.Minute,
	}
	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		DeployFailed:   true,
		FailureMessage: "boom",
	}

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO notification_debounce (application, target_gcp_project, rule, last_notified_at, claim_expires_at)
		VALUES ('widget-api', 'example-prod-us', 'syncFailed', 'epoch', now() + interval '1 minute')`,
	); err != nil {
		t.Fatalf("seed live claim: %v", err)
	}

	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no send while another attempt's claim is still live, got %v", sink.messages)
	}
}
