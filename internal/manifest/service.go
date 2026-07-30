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

// ServiceDefinition is one service's app.yaml (§5.1). No env, project, or
// region — those are bound in the root argorun.yaml's apps[]/environments.
type ServiceDefinition struct {
	ResourceType ResourceType   `yaml:"resourceType,omitempty"`
	Image        Image          `yaml:"image"`
	Traffic      *Traffic       `yaml:"traffic,omitempty"`
	Requires     []Precondition `yaml:"requires,omitempty"`
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

	return &sd, nil
}

func validateTraffic(t *Traffic) error {
	if t == nil || t.LatestRevisionPercent == nil {
		return nil
	}
	if p := *t.LatestRevisionPercent; p < 0 || p > 100 {
		return fmt.Errorf("traffic.latestRevisionPercent %d must be between 0 and 100", p)
	}
	return nil
}

func validateImage(img Image) error {
	if img.Digest == "" {
		return fmt.Errorf("image.digest is required — argorun never accepts a floating tag (NFR2)")
	}
	if !digestPattern.MatchString(img.Digest) {
		return fmt.Errorf("image.digest %q is not a valid digest reference (expected sha256:<64 hex chars>)", img.Digest)
	}
	if img.Track != "" && img.Version != "" {
		return fmt.Errorf("image may set track or version, not both (§5.1)")
	}
	return nil
}
