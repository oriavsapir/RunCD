// Package diff compares desired vs live Cloud Run state, but only on the
// fields listed in defaults.managedFields (§5.7, NFR8) — everything else is
// the Cloud Run equivalent of ArgoCD's ignoreDifferences.
package diff

import "github.com/runcd/runcd/internal/cloudrun"

type Status string

const (
	Synced    Status = "Synced"
	OutOfSync Status = "OutOfSync"
)

// Compute returns Synced only if every managed field matches between
// desired and live. traffic is ignored for job/workerPool even if present
// in managedFields, since it's meaningless there (§5.7). env is ignored
// for job — Cloud Run Jobs' env vars live on the task template vs. what a
// past execution actually ran, a distinction this v1 doesn't try to
// reconcile; scoped to service/workerPool only for now.
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
			// A manifest that manages traffic but omits the traffic block
			// (desired == nil) hasn't told runcd what to enforce — treat
			// that as nothing-to-diff rather than a permanent mismatch
			// against whatever percent Cloud Run happens to report live.
			if desired.TrafficLatestRevisionPercent == nil {
				continue
			}
			if !trafficEqual(desired.TrafficLatestRevisionPercent, live.TrafficLatestRevisionPercent) {
				return OutOfSync
			}
		case "env":
			if resourceType == "job" {
				continue
			}
			// Both nil means this manifest hasn't declared env as part of
			// its spec (env not actually managed for this unit) — same
			// nothing-to-diff convention as traffic above.
			if desired.EnvVars == nil && desired.SecretRefs == nil {
				continue
			}
			if !stringMapEqual(desired.EnvVars, live.EnvVars) || !secretMapEqual(desired.SecretRefs, live.SecretRefs) {
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

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		// bv, ok := b[k], not b[k] alone — a plain index would return the
		// zero value ("") for a key missing from b, indistinguishable from
		// a key that's actually present with value "". A renamed env var
		// with an empty value (a real, Cloud-Run-permitted case) would then
		// read as unchanged even though b has no such key at all.
		bv, ok := b[k]
		if !ok || bv != v {
			return false
		}
	}
	return true
}

func secretMapEqual(a, b map[string]cloudrun.SecretRef) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, ok := b[k]
		if !ok || bv != v {
			return false
		}
	}
	return true
}
