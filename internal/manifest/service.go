// Package manifest parses service definitions (§5.1): one YAML file per
// service, environment-agnostic. The controller/diff engine only ever reads
// image.digest — track/version are resolver metadata, never a live mutable
// reference the reconcile loop has to track (NFR2).
package manifest

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ResourceType mirrors Cloud Run's service/job/workerPool distinction (§5.7).
type ResourceType string

const (
	ResourceService    ResourceType = "service"
	ResourceJob        ResourceType = "job"
	ResourceWorkerPool ResourceType = "workerPool"
)

type Image struct {
	Digest  string `yaml:"digest"`
	Track   string `yaml:"track,omitempty"`
	Version string `yaml:"version,omitempty"`
}

type Traffic struct {
	LatestRevisionPercent *int `yaml:"latestRevisionPercent,omitempty"`
}

type Precondition struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
}

// SecretRef exposes a Secret Manager secret+version as one environment
// variable — the same env var namespace as Env, so a name can't appear in
// both (checked at parse time). Version defaults to "latest" when omitted.
type SecretRef struct {
	Name    string `yaml:"name"`
	Secret  string `yaml:"secret"`
	Version string `yaml:"version,omitempty"`
}

// ServiceDefinition is one service's app.yaml (§5.1). No env, project, or
// region — those are bound in the root runcd.yaml's apps[]/environments.
type ServiceDefinition struct {
	ResourceType ResourceType `yaml:"resourceType,omitempty"`
	Image        Image        `yaml:"image"`
	Traffic      *Traffic     `yaml:"traffic,omitempty"`
	// Env/Secrets are only compared/deployed when "env" is in
	// managedFields (§5.7) — together they're this app's full desired
	// container environment; Cloud Run itself has no separate concept for
	// "plain" vs "secret-sourced" env vars, they're both just entries in
	// one list, so runcd manages them as a single managed field even
	// though the manifest splits them into two sections for readability.
	Env      map[string]string `yaml:"env,omitempty"`
	Secrets  []SecretRef       `yaml:"secrets,omitempty"`
	Requires []Precondition    `yaml:"requires,omitempty"`
}

// digestPattern matches a resolved OCI digest reference, e.g.
// sha256:3f8a1c...; the algorithm prefix is fixed to sha256 for v1.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Parse decodes and validates a service definition. It rejects anything
// where image isn't digest-pinned (NFR2): digest missing/malformed, or both
// track and version present at once (§5.1: "Present at most one of them").
func Parse(data []byte) (*ServiceDefinition, error) {
	var sd ServiceDefinition
	if err := yaml.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("parse service definition: %w", err)
	}

	if sd.ResourceType == "" {
		sd.ResourceType = ResourceService
	}
	switch sd.ResourceType {
	case ResourceService, ResourceJob, ResourceWorkerPool:
	default:
		return nil, fmt.Errorf("resourceType %q is not one of service, job, workerPool", sd.ResourceType)
	}

	if err := validateImage(sd.Image); err != nil {
		return nil, err
	}
	if err := validateTraffic(sd.Traffic); err != nil {
		return nil, err
	}
	if err := validateSecrets(sd.Env, sd.Secrets); err != nil {
		return nil, err
	}

	return &sd, nil
}

// validateSecrets rejects a malformed secret ref (empty name/secret) and an
// env var name declared in both env and secrets — Cloud Run has one env
// var namespace, so declaring "FOO" both ways is a real config mistake,
// not a meaningful override of one by the other.
func validateSecrets(env map[string]string, secrets []SecretRef) error {
	if _, ok := env[""]; ok {
		return fmt.Errorf("env: an entry has an empty name")
	}
	seen := make(map[string]bool, len(secrets))
	for _, s := range secrets {
		if s.Name == "" {
			return fmt.Errorf("secrets: an entry is missing name")
		}
		if s.Secret == "" {
			return fmt.Errorf("secrets: %q is missing secret", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("secrets: %q is listed more than once", s.Name)
		}
		seen[s.Name] = true
		if _, ok := env[s.Name]; ok {
			return fmt.Errorf("%q is declared in both env and secrets — Cloud Run has one env var namespace, pick one", s.Name)
		}
	}
	return nil
}

// validateTraffic rejects anything but a full cutover to the latest
// revision (100) — matching cloudrun.GCPAdminClient's validatedPercent
// exactly, not just the wider [0,100] range this field's own type allows.
// Accepting, say, 50 here would parse fine and diff cleanly, then fail
// every single deploy attempt forever (a new failed sync_events row every
// reconcile tick) once a real deploy call actually tries it — v1's traffic
// model has no way to say where the remaining percent should go, so this
// has to fail loudly at config-load time, not repeatedly at deploy time.
func validateTraffic(t *Traffic) error {
	if t == nil || t.LatestRevisionPercent == nil {
		return nil
	}
	if p := *t.LatestRevisionPercent; p != 100 {
		return fmt.Errorf("traffic.latestRevisionPercent %d is not supported — v1 only manages a full cutover to the latest revision (100)", p)
	}
	return nil
}

func validateImage(img Image) error {
	if img.Digest == "" {
		return fmt.Errorf("image.digest is required — runcd never accepts a floating tag (NFR2)")
	}
	if !digestPattern.MatchString(img.Digest) {
		return fmt.Errorf("image.digest %q is not a valid digest reference (expected sha256:<64 hex chars>)", img.Digest)
	}
	if img.Track != "" && img.Version != "" {
		return fmt.Errorf("image may set track or version, not both (§5.1)")
	}
	return nil
}
