// Package config parses the root argorun.yaml (§5.1): the fan-out source of
// truth binding apps[] to environments[env].projects.
package config

import (
	"fmt"

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

type Root struct {
	Environments map[string]Environment `yaml:"environments"`
	Defaults     Defaults               `yaml:"defaults"`
	Apps         []App                  `yaml:"apps"`
}

// Parse decodes argorun.yaml and rejects any app whose env doesn't exist —
// a typo here must fail loudly at parse time (§5.1).
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
	return &root, nil
}
