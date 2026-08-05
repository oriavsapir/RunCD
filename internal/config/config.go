// Package config parses the root runcd.yaml (§5.1): the fan-out source of
// truth binding apps[] to environments[env].projects.
package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// validRuleName matches notification_debounce.rule's CHECK constraint
// (internal/store/migrations/00008_notify_rule_names.sql).
var validRuleName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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
	// Observe puts every unit under this policy into shadow mode: the
	// reconcile loop still fetches, diffs, and assesses health every tick
	// exactly as normal (so Status/Health reflect real drift from the first
	// tick), but never deploys — not on auto-sync, and not on a manual sync
	// request either, which is what distinguishes this from just leaving
	// Auto unset. Meant for onboarding a project/environment onto runcd
	// gradually: prove the desired state matches reality (or see exactly
	// where it doesn't) before granting runcd any authority to change
	// anything, without needing a separate on-demand dry-run for every
	// tick. Takes precedence over Auto and a manual sync's force — both
	// still no-op while Observe is true.
	Observe *bool `yaml:"observe,omitempty"`
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
	if override.Observe != nil {
		merged.Observe = override.Observe
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

// validateSyncNotImplemented rejects sync.retry / sync.selfHeal wherever
// they're set — neither is consumed anywhere in internal/reconcile (grep
// confirms zero references), so a config setting either would silently do
// nothing: no retry-with-backoff on a failed deploy, no auto-correction of
// manually-drifted live resources. Rejecting loudly at parse time matches
// this repo's established pattern for exactly this class of gap (see the
// traffic-percent and job+env validations) — better than a user believing
// selfHeal is protecting them from drift when it never was.
func validateSyncNotImplemented(sync SyncPolicy, path string) error {
	if sync.Retry != nil {
		return fmt.Errorf("%s.retry is not implemented yet — remove it (a deploy failure is retried on the next reconcile poll regardless, just not with per-app backoff/limit control)", path)
	}
	if sync.SelfHeal != nil {
		return fmt.Errorf("%s.selfHeal is not implemented yet — remove it (auto-sync already redeploys OutOfSync units; there's no separate self-heal behavior for manually-drifted live resources)", path)
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
	Folders []string       `yaml:"folders,omitempty"`
	Region  string         `yaml:"region,omitempty"`
	Sync    SyncPolicy     `yaml:"sync,omitempty"`
	Notify  NotifyOverride `yaml:"notify,omitempty"`
}

type Defaults struct {
	Region        string     `yaml:"region,omitempty"`
	ManagedFields []string   `yaml:"managedFields"`
	Sync          SyncPolicy `yaml:"sync,omitempty"`
}

type Override struct {
	Region string `yaml:"region,omitempty"`
	// Track/Version override the app's manifest-declared image.track/
	// image.version for this project only (mutually exclusive, mirroring
	// manifest.Image's own track/version pair) — resolved live against
	// Artifact Registry at reconcile time, not committed to the manifest,
	// so the same service.yaml can serve one project riding the manifest's
	// own digest and another pinned to a different version.
	Track   string `yaml:"track,omitempty"`
	Version string `yaml:"version,omitempty"`
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
// condition (sync failed, health degraded, a gated unit stuck OutOfSync, or
// health recovering from Degraded) holds, subject to the named duration
// threshold.
type NotifyRule struct {
	On string `yaml:"on"` // syncFailed | healthDegraded | outOfSyncGated | healthRecovered
	// Name disambiguates two rules sharing the same On (e.g. an early-warning
	// healthDegraded at 5 minutes and an escalation at 60) so
	// environments[].notify.rules can reference one without the other. Only
	// required when such a reference would otherwise be ambiguous — two
	// unnamed rules of the same type still debounce independently either way.
	Name       string `yaml:"name,omitempty"`
	ForMinutes *int   `yaml:"forMinutes,omitempty"`
	ForHours   *int   `yaml:"forHours,omitempty"`
}

// NotifyOverride narrows notify.rules and/or picks a non-default named Slack
// sink for one environment (ArgoCD's per-Application notification
// subscription, expressed as static config instead of an annotation since
// there's no CR here) — e.g. prod subscribes to every configured rule, dev
// only to syncFailed.
type NotifyOverride struct {
	Slack string `yaml:"slack,omitempty"`
	// Rules is a subset of notify.rules identifiers (name, or bare "on" when
	// unambiguous) this environment should actually notify on. Nil means
	// every configured rule applies, unchanged from pre-override behavior.
	Rules []string `yaml:"rules,omitempty"`
}

type Notify struct {
	// Slack is named webhook sinks (ArgoCD's named notification services) —
	// "default" is used by any environment that sets no notify.slack
	// override, and is required once Rules is non-empty.
	Slack map[string]string `yaml:"slack,omitempty"`
	Rules []NotifyRule      `yaml:"rules,omitempty"`
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
	// Sync units key on (app, project), not app alone (every Postgres table,
	// API route, and lookup map downstream already reflects that) — so the
	// only real collision to reject is two apps landing on the same
	// (app, project) pair after expansion, not merely sharing a name across
	// unrelated projects/environments. This only checks each environment's
	// explicitly-declared Projects — a folder-resolved addition (via
	// environments[env].folders, resolved later by folders.ResolveConfig,
	// not here) could in principle still introduce a collision Parse can't
	// see, since Parse does no I/O.
	seenAppProject := make(map[string]bool)
	for _, app := range root.Apps {
		excluded := make(map[string]bool, len(app.Exclude))
		for _, p := range app.Exclude {
			excluded[p] = true
		}
		for _, p := range root.Environments[app.Env].Projects {
			if excluded[p] {
				continue
			}
			key := app.Name + "/" + p
			if seenAppProject[key] {
				return nil, fmt.Errorf("app %q is declared more than once for project %q — sync units key on (app, project), so this would clobber one applications row", app.Name, p)
			}
			seenAppProject[key] = true
		}
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
		for project, o := range app.Overrides {
			if o.Track != "" && o.Version != "" {
				return nil, fmt.Errorf("app %q: overrides[%q] may set track or version, not both", app.Name, project)
			}
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
	if err := validateSyncNotImplemented(root.Defaults.Sync, "defaults.sync"); err != nil {
		return nil, err
	}
	for envName, env := range root.Environments {
		if err := validateSyncNotImplemented(env.Sync, fmt.Sprintf("environments[%q].sync", envName)); err != nil {
			return nil, err
		}
	}
	for _, rule := range root.Notify.Rules {
		// Name becomes a notification_debounce.rule value verbatim (see
		// ruleKey in internal/notify) — that column's CHECK constraint
		// (internal/store/migrations/00008_notify_rule_names.sql) restricts
		// it to this same charset, so reject anything wider here rather
		// than at insert time deep in a reconcile pass.
		if rule.Name != "" && !validRuleName.MatchString(rule.Name) {
			return nil, fmt.Errorf("notify.rules: name %q must match %s", rule.Name, validRuleName)
		}
		switch rule.On {
		case "syncFailed", "healthRecovered":
		case "healthDegraded":
			if rule.ForMinutes == nil {
				return nil, fmt.Errorf("notify.rules: %q requires forMinutes", rule.On)
			}
			// A non-positive value isn't just semantically odd — it embeds
			// into the notification_debounce.rule key as
			// "healthDegraded:<n>", which a DB CHECK constraint restricts to
			// digits only (internal/store/migrations/00002_notify.sql); a
			// negative n fails that constraint on every insert, so the rule
			// would never fire at all, silently.
			if *rule.ForMinutes <= 0 {
				return nil, fmt.Errorf("notify.rules: %q forMinutes must be positive, got %d", rule.On, *rule.ForMinutes)
			}
		case "outOfSyncGated":
			if rule.ForHours == nil {
				return nil, fmt.Errorf("notify.rules: %q requires forHours", rule.On)
			}
			if *rule.ForHours <= 0 {
				return nil, fmt.Errorf("notify.rules: %q forHours must be positive, got %d", rule.On, *rule.ForHours)
			}
		default:
			return nil, fmt.Errorf("notify.rules: %q is not a known rule (syncFailed, healthDegraded, outOfSyncGated, healthRecovered)", rule.On)
		}
	}
	if len(root.Notify.Rules) > 0 && root.Notify.Slack["default"] == "" {
		return nil, fmt.Errorf("notify.slack: requires a %q entry when notify.rules is non-empty", "default")
	}
	for name, raw := range root.Notify.Slack {
		if err := validateWebhookURL(raw); err != nil {
			return nil, fmt.Errorf("notify.slack[%q]: %w", name, err)
		}
	}
	// ruleIDs backs environments[].notify.rules lookups below: Name when
	// set, otherwise the bare On — ambiguous (matches >1 rule) only if some
	// environment actually references a bare On shared by more than one
	// rule, which is checked per-reference below rather than rejected here,
	// since two unnamed rules of the same On (e.g. two healthDegraded
	// thresholds with no environment override selecting between them) is a
	// perfectly valid, already-supported config.
	ruleIDs := make(map[string]int, len(root.Notify.Rules))
	for _, rule := range root.Notify.Rules {
		id := rule.Name
		if id == "" {
			id = rule.On
		}
		ruleIDs[id]++
	}
	for envName, env := range root.Environments {
		if env.Notify.Slack != "" {
			if _, ok := root.Notify.Slack[env.Notify.Slack]; !ok {
				return nil, fmt.Errorf("environments[%q].notify.slack: %q is not a name in notify.slack", envName, env.Notify.Slack)
			}
		}
		for _, r := range env.Notify.Rules {
			switch ruleIDs[r] {
			case 0:
				return nil, fmt.Errorf("environments[%q].notify.rules: %q is not a configured notify.rules identifier", envName, r)
			case 1:
			default:
				return nil, fmt.Errorf("environments[%q].notify.rules: %q matches more than one notify.rules entry — give the ones you mean a distinct name", envName, r)
			}
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
		return fmt.Errorf("must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not a valid http(s) URL", raw)
	}
	return nil
}
