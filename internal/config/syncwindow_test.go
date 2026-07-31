package config

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return tm
}

func TestWindowsAllow_NoWindowsAlwaysAllowed(t *testing.T) {
	if !WindowsAllow(nil, mustTime(t, "2026-08-01T12:00:00Z")) {
		t.Fatal("expected no windows to mean always allowed")
	}
}

func TestWindowsAllow_DenyBlocksOnlyWithinItsWindow(t *testing.T) {
	windows := []SyncWindow{{Kind: SyncWindowDeny, Days: []string{"Sat", "Sun"}}}
	// 2026-08-01 is a Saturday (UTC).
	if WindowsAllow(windows, mustTime(t, "2026-08-01T12:00:00Z")) {
		t.Fatal("expected Saturday to be denied")
	}
	// 2026-08-03 is a Monday.
	if !WindowsAllow(windows, mustTime(t, "2026-08-03T12:00:00Z")) {
		t.Fatal("expected Monday to be allowed (outside the deny window)")
	}
}

func TestWindowsAllow_AllowOnlyPermitsInsideItsWindow(t *testing.T) {
	windows := []SyncWindow{{Kind: SyncWindowAllow, Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, StartHour: 9, EndHour: 17}}
	// Monday 10:00 UTC — inside.
	if !WindowsAllow(windows, mustTime(t, "2026-08-03T10:00:00Z")) {
		t.Fatal("expected weekday business hours to be allowed")
	}
	// Monday 20:00 UTC — outside the hour range.
	if WindowsAllow(windows, mustTime(t, "2026-08-03T20:00:00Z")) {
		t.Fatal("expected outside business hours to be denied once an allow window exists")
	}
	// Saturday — outside the day range entirely.
	if WindowsAllow(windows, mustTime(t, "2026-08-01T10:00:00Z")) {
		t.Fatal("expected weekend to be denied once an allow window exists")
	}
}

func TestWindowsAllow_DenyWinsOverAllow(t *testing.T) {
	windows := []SyncWindow{
		{Kind: SyncWindowAllow, StartHour: 0, EndHour: 0}, // all day, every day
		{Kind: SyncWindowDeny, Days: []string{"Sat", "Sun"}},
	}
	if WindowsAllow(windows, mustTime(t, "2026-08-01T10:00:00Z")) {
		t.Fatal("expected deny to override an allow-all window")
	}
	if !WindowsAllow(windows, mustTime(t, "2026-08-03T10:00:00Z")) {
		t.Fatal("expected a weekday to still be allowed")
	}
}

func TestWindowsAllow_HourWrapsPastMidnight(t *testing.T) {
	windows := []SyncWindow{{Kind: SyncWindowDeny, StartHour: 22, EndHour: 6}}
	if !WindowsAllow(windows, mustTime(t, "2026-08-01T12:00:00Z")) {
		t.Fatal("expected midday to be outside an overnight 22-06 window")
	}
	if WindowsAllow(windows, mustTime(t, "2026-08-01T23:00:00Z")) {
		t.Fatal("expected 23:00 to be inside an overnight 22-06 window")
	}
	if WindowsAllow(windows, mustTime(t, "2026-08-01T02:00:00Z")) {
		t.Fatal("expected 02:00 to be inside an overnight 22-06 window")
	}
}
