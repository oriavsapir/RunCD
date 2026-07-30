// Package diff compares desired vs live Cloud Run state, but only on the
// fields listed in defaults.managedFields (§5.7, NFR8) — everything else is
// the Cloud Run equivalent of ArgoCD's ignoreDifferences.
package diff

import "github.com/argorun/argorun/internal/cloudrun"

type Status string

const (
	Synced    Status = "Synced"
	OutOfSync Status = "OutOfSync"
)

// Compute returns Synced only if every managed field matches between
// desired and live. traffic is ignored for job/workerPool even if present
// in managedFields, since it's meaningless there (§5.7).
func Compute(desired, live cloudrun.ServiceState, managedFields []string, resourceType string) Status {
	for _, field := range managedFields {
		switch field {
		case "image":
			if desired.ImageDigest != live.ImageDigest {
				return OutOfSync
			}
		case "traffic":
			if resourceType != "service" {
				continue
			}
			if !trafficEqual(desired.TrafficLatestRevisionPercent, live.TrafficLatestRevisionPercent) {
				return OutOfSync
			}
		}
	}
	return Synced
}

func trafficEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
