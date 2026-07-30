// Package config parses the root argorun.yaml (§5.1): the fan-out source of
// truth binding apps[] to environments[env].projects.
package config

import (
	"fmt"
	"net/url"

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
	Auto     *bool        `yaml:"auto,omitempty"`
	Interval *int         `yaml:"interval,omitempty"`
	Retry    *RetryPolicy `yaml:"retry,omitempty"`
	SelfHeal *bool        `yaml:"selfHeal,omitempty"`
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
	return merged
}

type Environment struct {
	Projects []string   `yaml:"projects"`
	Region   string     `yaml:"region,omitempty"`
	Sync     SyncPolicy `yaml:"sync,omitempty"`
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
var validManagedFields = map[string]bool{"image": true, "traffic": true}

// Parse decodes argorun.yaml and rejects any app whose env doesn't exist, or
// any defaults.managedFields entry the diff engine doesn't know how to
// compare — both must fail loudly at parse time (§5.1), not be silently
// ignored later.
func Parse(data []byte) (*Root, error) {
	var root Root
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse argorun.yaml: %w", err)
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
	}
	for _, f := range root.Defaults.ManagedFields {
		if !validManagedFields[f] {
			return nil, fmt.Errorf("defaults.managedFields: %q is not a field argorun knows how to manage (image, traffic)", f)
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
