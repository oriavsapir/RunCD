// Package health assesses Cloud Run health per the §5.7 table: service and
// workerPool are revision-based, job is execution-based.
package health

import "github.com/argorun/argorun/internal/cloudrun"

type Status string

const (
	Healthy     Status = "Healthy"
	Progressing Status = "Progressing"
	Degraded    Status = "Degraded"
	Missing     Status = "Missing"
)

// AssessService implements the `service` row of §5.7's health table:
// Healthy requires the latest revision Ready and traffic matching desired
// (only when traffic is managed); Progressing while it's still creating;
// Degraded if Ready=False; Missing if no revision matches the desired
// digest at all.
func AssessService(desired cloudrun.ServiceState, live cloudrun.LiveService, trafficManaged bool) Status {
	if !live.HasRevisionForDesiredDigest {
		return Missing
	}
	if live.LatestRevisionCreating {
		return Progressing
	}
	if !live.LatestRevisionReady {
		return Degraded
	}
	// desired.TrafficLatestRevisionPercent is nil whenever the manifest
	// manages traffic but doesn't set a traffic: block — nothing to
	// enforce, so that's not a mismatch (same guard as diff.Compute; a
	// real Cloud Run client's live percent is never nil, so without this
	// a fully-healthy service would report Progressing forever).
	if trafficManaged && desired.TrafficLatestRevisionPercent != nil && !trafficEqual(desired.TrafficLatestRevisionPercent, live.TrafficLatestRevisionPercent) {
		return Progressing
	}
	return Healthy
}

// AssessWorkerPool implements the `workerPool` row of §5.7's table: same as
// service, but there's no traffic concept to check.
func AssessWorkerPool(live cloudrun.LiveService) Status {
	return AssessService(cloudrun.ServiceState{}, live, false)
}

// AssessJob implements the `job` row of §5.7's table: health follows the
// latest execution's outcome for the desired digest, not revision readiness.
func AssessJob(live cloudrun.LiveJob) Status {
	if !live.HasExecutionForDesiredDigest {
		return Missing
	}
	switch live.LatestExecutionStatus {
	case cloudrun.ExecutionSucceeded:
		return Healthy
	case cloudrun.ExecutionRunning:
		return Progressing
	case cloudrun.ExecutionFailed:
		return Degraded
	default:
		// A job execution always ends up in one of the three states above;
		// an unrecognized value is treated as a loud failure rather than
		// silently reporting Healthy.
		return Degraded
	}
}

func trafficEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
