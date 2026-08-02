package config

import "time"

var syncWindowWeekday = map[time.Weekday]string{
	time.Sunday:    "Sun",
	time.Monday:    "Mon",
	time.Tuesday:   "Tue",
	time.Wednesday: "Wed",
	time.Thursday:  "Thu",
	time.Friday:    "Fri",
	time.Saturday:  "Sat",
}

func (w SyncWindow) dayMatches(day time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	name := syncWindowWeekday[day]
	for _, d := range w.Days {
		if d == name {
			return true
		}
	}
	return false
}

func (w SyncWindow) matches(t time.Time) bool {
	if w.StartHour == w.EndHour {
		return w.dayMatches(t.Weekday())
	}
	hour := t.Hour()
	if w.StartHour < w.EndHour {
		return w.dayMatches(t.Weekday()) && hour >= w.StartHour && hour < w.EndHour
	}
	// Wraps past midnight: {Days: ["Fri"], Start: 22, End: 6} spans
	// [22:00,24:00) on Friday and continues into [00:00,6:00) on Saturday —
	// the early-hours portion must check *yesterday's* weekday, not today's,
	// or the window silently stops matching the instant the clock crosses
	// midnight into Saturday. An hour in neither half (e.g. noon) is outside
	// the window regardless of day.
	if hour >= w.StartHour {
		return w.dayMatches(t.Weekday())
	}
	if hour < w.EndHour {
		return w.dayMatches(t.AddDate(0, 0, -1).Weekday())
	}
	return false
}

// WindowsAllow reports whether auto-sync is permitted at t (evaluated in
// UTC) given windows. Any matching deny window blocks regardless of allow
// windows. With no windows at all, or only deny windows that don't match,
// auto-sync is permitted. Once at least one allow window is declared,
// auto-sync is permitted only inside a matching allow window.
func WindowsAllow(windows []SyncWindow, t time.Time) bool {
	t = t.UTC()
	hasAllow, allowMatched := false, false
	for _, w := range windows {
		switch w.Kind {
		case SyncWindowDeny:
			if w.matches(t) {
				return false
			}
		case SyncWindowAllow:
			hasAllow = true
			if w.matches(t) {
				allowMatched = true
			}
		}
	}
	if hasAllow {
		return allowMatched
	}
	return true
}
