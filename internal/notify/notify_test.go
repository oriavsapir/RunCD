package notify

import (
	"context"
	"errors"
	"strings"
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

// failOnceForSubstringSink fails exactly once, the first time Send is
// called with a message containing substr — used to isolate a failure to
// one specific notification (e.g. the "recovered" message) without also
// failing the unrelated "degraded" message that necessarily precedes it in
// a recovery test.
type failOnceForSubstringSink struct {
	substr   string
	failed   bool
	messages []string
}

func (f *failOnceForSubstringSink) Send(_ context.Context, message string) error {
	if !f.failed && strings.Contains(message, f.substr) {
		f.failed = true
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

// TestEvaluate_HealthRecoveredFiresOnlyAfterANotifiedDegradation is the core
// gap this rule closes: a unit that crossed the healthDegraded threshold
// (and actually notified) gets a "recovered" message once Health leaves
// Degraded.
func TestEvaluate_HealthRecoveredFiresOnlyAfterANotifiedDegradation(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(10)},
		{On: "healthRecovered"},
	}}

	degraded := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health:      "Degraded",
		HealthSince: time.Now().Add(-15 * time.Minute),
	}
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded): %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected the healthDegraded notification, got %d: %v", len(sink.messages), sink.messages)
	}

	recovered := degraded
	recovered.Health = "Healthy"
	if err := e.Evaluate(context.Background(), recovered); err != nil {
		t.Fatalf("Evaluate (recovered): %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected a recovered notification too, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_HealthRecoveredNeverFiresWithoutAPriorNotification covers a
// unit that was Degraded but never crossed the threshold (never actually
// notified) — recovering from that must stay silent.
func TestEvaluate_HealthRecoveredNeverFiresWithoutAPriorNotification(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(10)},
		{On: "healthRecovered"},
	}}

	res := reconcile.Result{
		Unit:   expander.SyncUnit{App: "widget-api", Project: "example-prod-us"},
		Health: "Healthy",
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected no recovered message with no prior degraded notification, got %v", sink.messages)
	}
}

// TestEvaluate_HealthRecoveredSurvivesAFailedSend regression-tests a bug
// where the sibling healthDegraded rule's debounce marker was cleared to
// 'epoch' before the recovered message's Send was attempted: if Send failed,
// the marker was already cleared, so the next Evaluate call found nothing
// to clear and the recovery notification was permanently dropped. The fix
// only clears the marker once Send actually confirms.
func TestEvaluate_HealthRecoveredSurvivesAFailedSend(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &failOnceForSubstringSink{substr: "recovered"}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(10)},
		{On: "healthRecovered"},
	}}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us"}
	degraded := reconcile.Result{Unit: unit, Health: "Degraded", HealthSince: time.Now().Add(-15 * time.Minute)}
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded): %v", err)
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected the healthDegraded notification, got %d: %v", len(sink.messages), sink.messages)
	}

	recovered := reconcile.Result{Unit: unit, Health: "Healthy"}
	if err := e.Evaluate(context.Background(), recovered); err == nil {
		t.Fatal("expected Evaluate to report the simulated send failure")
	}
	if len(sink.messages) != 1 {
		t.Fatalf("expected no recovered message delivered on the failed attempt, got %d: %v", len(sink.messages), sink.messages)
	}

	// Retry immediately — must actually deliver the recovered message,
	// proving the failed attempt above never cleared the sibling's marker.
	if err := e.Evaluate(context.Background(), recovered); err != nil {
		t.Fatalf("retry Evaluate: %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected the recovered message to be retried and delivered, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_HealthRecoveredDebouncedSendDoesNotClearSiblingMarker
// regression-tests a bug where maybeNotify's nil error was treated as "sent"
// even when the recovered notification was itself debounced (a unit
// flapping Degraded->Healthy twice inside one debounce window shares one
// "healthRecovered" debounce row across both recoveries) — the sibling
// healthDegraded rule's marker was cleared anyway, so a subsequent Degraded
// episode notified immediately despite no second "recovered" message having
// actually gone out for the flap that triggered the clear.
func TestEvaluate_HealthRecoveredDebouncedSendDoesNotClearSiblingMarker(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(10)},
		{On: "healthRecovered"},
	}, DebounceInterval: time.Hour}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us"}
	degraded := reconcile.Result{Unit: unit, Health: "Degraded", HealthSince: time.Now().Add(-15 * time.Minute)}
	recovered := reconcile.Result{Unit: unit, Health: "Healthy"}

	// Episode 1: degrade, then recover. Both notify; the recovery clears the
	// sibling's marker to epoch, which is expected and already covered by
	// TestEvaluate_HealthRecoveredResetsDebounceForTheNextEpisode.
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded #1): %v", err)
	}
	if err := e.Evaluate(context.Background(), recovered); err != nil {
		t.Fatalf("Evaluate (recovered #1): %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected degraded+recovered, got %d: %v", len(sink.messages), sink.messages)
	}

	// Episode 2: the sibling's epoch marker makes this degrade immediately
	// eligible again, setting last_notified_at to "now."
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded #2): %v", err)
	}
	if len(sink.messages) != 3 {
		t.Fatalf("expected the second degraded episode to notify, got %d: %v", len(sink.messages), sink.messages)
	}

	// The second "recovered" call shares the first's debounce row (same
	// unit/project/rule) and is well within DebounceInterval — it must be
	// silently debounced, not sent, and must NOT clear episode 2's
	// just-set sibling marker.
	if err := e.Evaluate(context.Background(), recovered); err != nil {
		t.Fatalf("Evaluate (recovered #2, debounced): %v", err)
	}
	if len(sink.messages) != 3 {
		t.Fatalf("expected the second recovered message to be debounced (no send), got %d: %v", len(sink.messages), sink.messages)
	}

	// The bug: episode 3's degrade would fire immediately here because the
	// debounced "recovered" call above incorrectly cleared the sibling's
	// marker anyway. Fixed behavior: still within DebounceInterval of
	// episode 2's degraded notification, so this must stay debounced.
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded #3): %v", err)
	}
	if len(sink.messages) != 3 {
		t.Fatalf("expected the third degraded episode to stay debounced (no matching recovered message was ever sent for episode 2's flap), got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_HealthRecoveredResetsDebounceForTheNextEpisode ensures a
// second, later Degraded episode notifies again immediately once it
// crosses the threshold, rather than waiting out the original debounce
// window — recovery clears the sibling rule's marker.
func TestEvaluate_HealthRecoveredResetsDebounceForTheNextEpisode(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{DB: db, Sink: sink, Rules: []config.NotifyRule{
		{On: "healthDegraded", ForMinutes: intPtr(10)},
		{On: "healthRecovered"},
	}, DebounceInterval: time.Hour}

	unit := expander.SyncUnit{App: "widget-api", Project: "example-prod-us"}
	degraded := reconcile.Result{Unit: unit, Health: "Degraded", HealthSince: time.Now().Add(-15 * time.Minute)}
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded #1): %v", err)
	}
	recovered := reconcile.Result{Unit: unit, Health: "Healthy"}
	if err := e.Evaluate(context.Background(), recovered); err != nil {
		t.Fatalf("Evaluate (recovered): %v", err)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("expected degraded+recovered, got %d: %v", len(sink.messages), sink.messages)
	}

	// A second episode, well within the original hour-long debounce window.
	if err := e.Evaluate(context.Background(), degraded); err != nil {
		t.Fatalf("Evaluate (degraded #2): %v", err)
	}
	if len(sink.messages) != 3 {
		t.Fatalf("expected the second degraded episode to notify despite being within the original debounce window, got %d: %v", len(sink.messages), sink.messages)
	}
}

// TestEvaluate_EnvironmentOverrideNarrowsRules covers "prod gets every
// event, dev only syncFailed" via config.NotifyOverride.Rules.
func TestEvaluate_EnvironmentOverrideNarrowsRules(t *testing.T) {
	db := testutil.NewPostgres(t)
	sink := &fakeSink{}
	e := &Evaluator{
		DB:   db,
		Sink: sink,
		Rules: []config.NotifyRule{
			{On: "syncFailed"},
			{On: "healthDegraded", ForMinutes: intPtr(10)},
		},
		Environments: map[string]config.Environment{
			"dev": {Notify: config.NotifyOverride{Rules: []string{"syncFailed"}}},
		},
	}

	res := reconcile.Result{
		Unit:        expander.SyncUnit{App: "widget-api", Project: "example-dev-01", Env: "dev"},
		Health:      "Degraded",
		HealthSince: time.Now().Add(-15 * time.Minute),
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(sink.messages) != 0 {
		t.Fatalf("expected healthDegraded to be filtered out for dev, got %v", sink.messages)
	}
}

// TestResolvedRules_NilOverrideMeansEveryRule covers the default case: an
// environment with no NotifyOverride.Rules set gets every configured rule,
// using the default sink name.
func TestResolvedRules_NilOverrideMeansEveryRule(t *testing.T) {
	e := &Evaluator{
		Rules: []config.NotifyRule{
			{On: "syncFailed"},
			{On: "healthDegraded", ForMinutes: intPtr(10)},
		},
		Environments: map[string]config.Environment{
			"prod": {},
		},
	}
	got := e.ResolvedRules("default")
	rules, ok := got["prod"]
	if !ok {
		t.Fatal("expected an entry for prod")
	}
	if rules.Sink != "default" {
		t.Fatalf("Sink = %q, want default", rules.Sink)
	}
	if len(rules.Rules) != 2 || rules.Rules[0] != "syncFailed" || rules.Rules[1] != "healthDegraded" {
		t.Fatalf("Rules = %v, want both configured rules in order", rules.Rules)
	}
}

// TestResolvedRules_OverrideNarrowsRulesAndSelectsNamedSink covers an
// environment override narrowing the rule subset and picking a non-default
// sink by name — the same resolution Evaluate itself does.
func TestResolvedRules_OverrideNarrowsRulesAndSelectsNamedSink(t *testing.T) {
	e := &Evaluator{
		Rules: []config.NotifyRule{
			{On: "syncFailed"},
			{On: "healthDegraded", ForMinutes: intPtr(10)},
		},
		Environments: map[string]config.Environment{
			"dev": {Notify: config.NotifyOverride{Slack: "dev-sink", Rules: []string{"syncFailed"}}},
		},
	}
	got := e.ResolvedRules("default")
	rules, ok := got["dev"]
	if !ok {
		t.Fatal("expected an entry for dev")
	}
	if rules.Sink != "dev-sink" {
		t.Fatalf("Sink = %q, want dev-sink", rules.Sink)
	}
	if len(rules.Rules) != 1 || rules.Rules[0] != "syncFailed" {
		t.Fatalf("Rules = %v, want only syncFailed", rules.Rules)
	}
}

// TestResolvedRules_NamedRuleUsesNameNotOn covers a named rule identified by
// its Name rather than its bare On (ruleID's own precedence).
func TestResolvedRules_NamedRuleUsesNameNotOn(t *testing.T) {
	e := &Evaluator{
		Rules: []config.NotifyRule{
			{On: "healthDegraded", Name: "early-warning", ForMinutes: intPtr(5)},
			{On: "healthDegraded", Name: "escalation", ForMinutes: intPtr(60)},
		},
		Environments: map[string]config.Environment{
			"prod": {Notify: config.NotifyOverride{Rules: []string{"escalation"}}},
		},
	}
	got := e.ResolvedRules("default")
	rules := got["prod"]
	if len(rules.Rules) != 1 || rules.Rules[0] != "escalation" {
		t.Fatalf("Rules = %v, want only escalation", rules.Rules)
	}
}

// TestEvaluate_EnvironmentOverrideSelectsNamedSink covers routing to a
// different Slack webhook per environment via config.NotifyOverride.Slack.
func TestEvaluate_EnvironmentOverrideSelectsNamedSink(t *testing.T) {
	db := testutil.NewPostgres(t)
	defaultSink := &fakeSink{}
	prodSink := &fakeSink{}
	e := &Evaluator{
		DB:    db,
		Sink:  defaultSink,
		Sinks: map[string]Sink{"default": defaultSink, "prod": prodSink},
		Rules: []config.NotifyRule{{On: "syncFailed"}},
		Environments: map[string]config.Environment{
			"prod": {Notify: config.NotifyOverride{Slack: "prod"}},
		},
	}

	res := reconcile.Result{
		Unit:           expander.SyncUnit{App: "widget-api", Project: "example-prod-us", Env: "prod"},
		DeployFailed:   true,
		FailureMessage: "boom",
	}
	if err := e.Evaluate(context.Background(), res); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(prodSink.messages) != 1 {
		t.Fatalf("expected the prod-named sink to receive the message, got %d", len(prodSink.messages))
	}
	if len(defaultSink.messages) != 0 {
		t.Fatalf("expected the default sink to receive nothing, got %d", len(defaultSink.messages))
	}
}
