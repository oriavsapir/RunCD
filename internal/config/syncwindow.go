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

func (w SyncWindow) matches(t time.Time) bool {
	if len(w.Days) > 0 {
		day := syncWindowWeekday[t.Weekday()]
		found := false
		for _, d := range w.Days {
			if d == day {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if w.StartHour == w.EndHour {
		return true
	}
	hour := t.Hour()
	if w.StartHour < w.EndHour {
		return hour >= w.StartHour && hour < w.EndHour
	}
	return hour >= w.StartHour || hour < w.EndHour // wraps past midnight
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
