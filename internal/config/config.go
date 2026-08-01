// Package config parses the root runcd.yaml (§5.1): the fan-out source of
// truth binding apps[] to environments[env].projects.
package config

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

type RetryPolicy struct {
	Limit          int `yaml:"limit"`
	BackoffSeconds int `yaml:"backoffSeconds"`
}

// SyncPolicy is deliberately mergeable field-by-field: Auto/Interval are set
// per environment, Retry/SelfHeal are inherited from defaults unless
// overridden (§5.1).
type SyncPolicy struct {
	Auto        *bool        `yaml:"auto,omitempty"`
	Interval    *int         `yaml:"interval,omitempty"`
	Retry       *RetryPolicy `yaml:"retry,omitempty"`
	SelfHeal    *bool        `yaml:"selfHeal,omitempty"`
	SyncWindows []SyncWindow `yaml:"syncWindows,omitempty"`
}

// Merge returns a copy of base with any field set on override replacing it.
func (base SyncPolicy) Merge(override SyncPolicy) SyncPolicy {
	merged := base
	if override.Auto != nil {
		merged.Auto = override.Auto
	}
	if override.Interval != nil {
		merged.Interval = override.Interval
	}
	if override.Retry != nil {
		merged.Retry = override.Retry
	}
	if override.SelfHeal != nil {
		merged.SelfHeal = override.SelfHeal
	}
	if override.SyncWindows != nil {
		merged.SyncWindows = override.SyncWindows
	}
	return merged
}

type SyncWindowKind string

const (
	SyncWindowAllow SyncWindowKind = "allow"
	SyncWindowDeny  SyncWindowKind = "deny"
)

// SyncWindow gates auto-sync (never a manual/forced sync — §5.10-style
// "force always deploys") to specific days and UTC hours, ArgoCD's
// AppProject.syncWindows without a cron dependency. Deny always wins over
// allow; if no allow window is declared at all, auto-sync is permitted
// everywhere a deny window doesn't match.
type SyncWindow struct {
	Kind SyncWindowKind `yaml:"kind"`
	// Days is a subset of Mon/Tue/Wed/Thu/Fri/Sat/Sun; empty means every day.
	Days []string `yaml:"days,omitempty"`
	// StartHour/EndHour are UTC hours in [0,24]. Equal (including the zero
	// value) means "all day". StartHour > EndHour wraps past midnight, e.g.
	// 22/6 covers 22:00-06:00 UTC.
	StartHour int `yaml:"startHour,omitempty"`
	EndHour   int `yaml:"endHour,omitempty"`
}

var validSyncWindowDays = map[string]bool{
	"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true,
}

func (w SyncWindow) validate() error {
	if w.Kind != SyncWindowAllow && w.Kind != SyncWindowDeny {
		return fmt.Errorf("syncWindows: kind %q must be \"allow\" or \"deny\"", w.Kind)
	}
	for _, d := range w.Days {
		if !validSyncWindowDays[d] {
			return fmt.Errorf("syncWindows: %q is not a valid day (Mon..Sun)", d)
		}
	}
	if w.StartHour < 0 || w.StartHour > 24 || w.EndHour < 0 || w.EndHour > 24 {
		return fmt.Errorf("syncWindows: startHour/endHour must be within [0,24], got %d/%d", w.StartHour, w.EndHour)
	}
	// Equal start/end means "all day" (the zero-value default) — but a
	// non-zero equal pair, e.g. startHour:5/endHour:5, is almost always a
	// typo for a narrow window, not a deliberate all-day one, and silently
	// producing an all-day allow/deny from it is exactly the kind of
	// surprising blast radius worth failing loudly on instead.
	if w.StartHour == w.EndHour && w.StartHour != 0 {
		return fmt.Errorf("syncWindows: startHour and endHour are both %d — write 0/0 for an explicit all-day window, or pick different hours", w.StartHour)
	}
	return nil
}

type Environment struct {
	Projects []string `yaml:"projects"`
	// Folders is a list of GCP folder IDs whose direct child projects are
	// resolved (via internal/folders, a live Cloud Resource Manager API
	// call — Parse itself never does I/O) and merged into Projects at load
	// time, deduped. Only direct children — a folder's own sub-folders are
	// not recursed into.
	Folders []string   `yaml:"folders,omitempty"`
	Region  string     `yaml:"region,omitempty"`
	Sync    SyncPolicy `yaml:"sync,omitempty"`
}

type Defaults struct {
	Region        string     `yaml:"region,omitempty"`
	ManagedFields []string   `yaml:"managedFields"`
	Sync          SyncPolicy `yaml:"sync,omitempty"`
}

type Override struct {
	Region string `yaml:"region,omitempty"`
}

type Source struct {
	Repo string `yaml:"repo"`
	Path string `yaml:"path"`
}

type App struct {
	Name      string              `yaml:"name"`
	Env       string              `yaml:"env"`
	Source    Source              `yaml:"source"`
	Overrides map[string]Override `yaml:"overrides,omitempty"`
	Exclude   []string            `yaml:"exclude,omitempty"`
	// IgnoreFields subtracts from defaults.managedFields for this app only
	// (ArgoCD's resource.exclusions, at field granularity — §7 already has
	// the allow-list, this is the per-app override on top of it).
	IgnoreFields []string `yaml:"ignoreFields,omitempty"`
	// IgnorePreconditions skips specific requires entries for this app
	// only, each named "type:name" (matching manifest.Precondition, e.g.
	// "pubsubTopic:orders-events") — an escape hatch for a precondition
	// that's legitimately not applicable to one app, not a way to
	// routinely bypass §5.10's gate.
	IgnorePreconditions []string `yaml:"ignorePreconditions,omitempty"`
}

// NotifyRule is one entry in notify.rules (§5.8): a rule fires when its
// condition (sync failed, health degraded, or a gated unit stuck
// OutOfSync) holds, subject to the named duration threshold.
type NotifyRule struct {
	On         string `yaml:"on"` // syncFailed | healthDegraded | outOfSyncGated
	ForMinutes *int   `yaml:"forMinutes,omitempty"`
	ForHours   *int   `yaml:"forHours,omitempty"`
}

type Notify struct {
	SlackWebhookURL string       `yaml:"slackWebhookUrl"`
	Rules           []NotifyRule `yaml:"rules,omitempty"`
}

type Root struct {
	Environments map[string]Environment `yaml:"environments"`
	Defaults     Defaults               `yaml:"defaults"`
	Apps         []App                  `yaml:"apps"`
	Notify       Notify                 `yaml:"notify,omitempty"`
}

// validManagedFields are the only fields the diff engine and deploy path
// know how to compare/apply (§5.7, NFR8). An unrecognized entry here would
// otherwise be silently ignored by the diff engine rather than rejected.
var validManagedFields = map[string]bool{"image": true, "traffic": true, "env": true}

// validPreconditionTypes mirrors internal/precondition.Check's own type
// switch (kept as a local literal, not imported, matching how
// validManagedFields is self-contained rather than importing internal/diff
// for its field names) — an app.IgnorePreconditions entry naming any other
// type would otherwise silently never match a real requires entry, leaving
// that precondition gating forever instead of failing loudly here.
var validPreconditionTypes = map[string]bool{"pubsubTopic": true, "pubsubSubscription": true}

// validateIgnorePrecondition checks one app.IgnorePreconditions entry —
// "type:name", where type is a precondition type runcd actually checks.
// name itself can't be validated further at parse time (no I/O here); a
// name that never matches any requires entry is harmless (it just ignores
// nothing), the type prefix is what's worth catching a typo in.
func validateIgnorePrecondition(entry string) error {
	typ, name, found := strings.Cut(entry, ":")
	if !found || name == "" {
		return fmt.Errorf("%q must be \"type:name\" (e.g. \"pubsubTopic:orders-events\")", entry)
	}
	if !validPreconditionTypes[typ] {
		return fmt.Errorf("%q: %q is not a precondition type runcd checks (pubsubTopic, pubsubSubscription)", entry, typ)
	}
	return nil
}

// Parse decodes runcd.yaml and rejects any app whose env doesn't exist, or
// any defaults.managedFields entry the diff engine doesn't know how to
// compare — both must fail loudly at parse time (§5.1), not be silently
// ignored later.
func Parse(data []byte) (*Root, error) {
	var root Root
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse runcd.yaml: %w", err)
	}
	for _, app := range root.Apps {
		if _, ok := root.Environments[app.Env]; !ok {
			return nil, fmt.Errorf("app %q references unknown environment %q", app.Name, app.Env)
		}
	}
	seenApps := make(map[string]bool, len(root.Apps))
	for _, app := range root.Apps {
		if seenApps[app.Name] {
			return nil, fmt.Errorf("app %q is listed more than once — sync units key on (app, project), so two apps sharing a name can clobber each other's applications row", app.Name)
		}
		seenApps[app.Name] = true
	}
	for envName, env := range root.Environments {
		seen := make(map[string]bool, len(env.Projects))
		for _, p := range env.Projects {
			if seen[p] {
				return nil, fmt.Errorf("environment %q: project %q is listed more than once", envName, p)
			}
			seen[p] = true
		}
		seenFolders := make(map[string]bool, len(env.Folders))
		for _, f := range env.Folders {
			if seenFolders[f] {
				return nil, fmt.Errorf("environment %q: folder %q is listed more than once", envName, f)
			}
			seenFolders[f] = true
		}
	}
	for _, f := range root.Defaults.ManagedFields {
		if !validManagedFields[f] {
			return nil, fmt.Errorf("defaults.managedFields: %q is not a field runcd knows how to manage (image, traffic, env)", f)
		}
	}
	for _, app := range root.Apps {
		for _, f := range app.IgnoreFields {
			if !validManagedFields[f] {
				return nil, fmt.Errorf("app %q: ignoreFields: %q is not a field runcd knows how to manage (image, traffic, env)", app.Name, f)
			}
		}
		for _, p := range app.IgnorePreconditions {
			if err := validateIgnorePrecondition(p); err != nil {
				return nil, fmt.Errorf("app %q: ignorePreconditions: %w", app.Name, err)
			}
		}
	}
	for _, w := range root.Defaults.Sync.SyncWindows {
		if err := w.validate(); err != nil {
			return nil, fmt.Errorf("defaults.sync: %w", err)
		}
	}
	for envName, env := range root.Environments {
		for _, w := range env.Sync.SyncWindows {
			if err := w.validate(); err != nil {
				return nil, fmt.Errorf("environments[%q].sync: %w", envName, err)
			}
		}
	}
	for _, rule := range root.Notify.Rules {
		switch rule.On {
		case "syncFailed":
		case "healthDegraded":
			if rule.ForMinutes == nil {
				return nil, fmt.Errorf("notify.rules: %q requires forMinutes", rule.On)
			}
		case "outOfSyncGated":
			if rule.ForHours == nil {
				return nil, fmt.Errorf("notify.rules: %q requires forHours", rule.On)
			}
		default:
			return nil, fmt.Errorf("notify.rules: %q is not a known rule (syncFailed, healthDegraded, outOfSyncGated)", rule.On)
		}
	}
	if len(root.Notify.Rules) > 0 {
		if err := validateWebhookURL(root.Notify.SlackWebhookURL); err != nil {
			return nil, fmt.Errorf("notify.slackWebhookUrl: %w", err)
		}
	}
	return &root, nil
}

// validateWebhookURL rejects an empty or malformed webhook URL at config
// load time — otherwise notify.rules silently never fires (empty URL) or
// every send fails at runtime (malformed URL) instead of failing loudly
// where the operator can actually see it.
func validateWebhookURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("required when notify.rules is non-empty")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not a valid http(s) URL", raw)
	}
	return nil
}
