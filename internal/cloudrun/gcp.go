package cloudrun

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/runcd/runcd/internal/registry"
)

// GCPAdminClient is the real Cloud Run Admin API v2 implementation of
// AdminClient. Clients are regional (Cloud Run has no global endpoint), so
// one is lazily created per region and cached. Construction is coalesced
// per region via singleflight — the map mutex itself is only ever held for
// a plain lookup/insert, never across the network/credential-resolution
// call that creating a client can make, so a cold-start dial for one
// region can't stall lookups for every other region in the reconcile
// worker pool.
type GCPAdminClient struct {
	mu          sync.Mutex
	services    map[string]*run.ServicesClient
	workerPools map[string]*run.WorkerPoolsClient
	jobs        map[string]*run.JobsClient
	executions  map[string]*run.ExecutionsClient
	// registryClient resolves a live image that was deployed by tag rather
	// than digest (see resolveImageDigest) — one client for the whole
	// process, not per-region, since Artifact Registry has no per-region
	// endpoint the way Cloud Run does.
	registryClient *registry.Client

	servicesGroup    singleflight.Group
	workerPoolsGroup singleflight.Group
	jobsGroup        singleflight.Group
	executionsGroup  singleflight.Group
	registryGroup    singleflight.Group
}

func NewGCPAdminClient() *GCPAdminClient {
	return &GCPAdminClient{
		services:    make(map[string]*run.ServicesClient),
		workerPools: make(map[string]*run.WorkerPoolsClient),
		jobs:        make(map[string]*run.JobsClient),
		executions:  make(map[string]*run.ExecutionsClient),
	}
}

// Close releases every regional client created so far.
func (c *GCPAdminClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range c.services {
		_ = sc.Close()
	}
	for _, wc := range c.workerPools {
		_ = wc.Close()
	}
	for _, jc := range c.jobs {
		_ = jc.Close()
	}
	for _, ec := range c.executions {
		_ = ec.Close()
	}
	if c.registryClient != nil {
		_ = c.registryClient.Close()
	}
	return nil
}

func regionalEndpoint(region string) option.ClientOption {
	return option.WithEndpoint(fmt.Sprintf("%s-run.googleapis.com:443", region))
}

func (c *GCPAdminClient) servicesClient(ctx context.Context, region string) (*run.ServicesClient, error) {
	c.mu.Lock()
	if sc, ok := c.services[region]; ok {
		c.mu.Unlock()
		return sc, nil
	}
	c.mu.Unlock()

	v, err, _ := c.servicesGroup.Do(region, func() (any, error) {
		c.mu.Lock()
		if sc, ok := c.services[region]; ok {
			c.mu.Unlock()
			return sc, nil
		}
		c.mu.Unlock()

		// context.WithoutCancel, deliberately with no additional deadline:
		// shared via singleflight across every concurrent caller for this
		// region, not just the one that triggered it (see the identical
		// note in precondition/gcp.go) — AND this client is cached for the
		// life of the process, so whatever context we hand NewServicesClient
		// here is retained by its resolved credentials for every future
		// token refresh, not just this construction call. For JSON-key/
		// authorized-user ADC (golang.org/x/oauth2/jwt's jwtSource captures
		// exactly the context TokenSource(ctx) is given), a context.WithTimeout
		// here — even with cancel() only deferred, never explicitly fired —
		// still expires on its own deadline and permanently breaks every
		// later refresh on this cached client. A hung construction call is
		// the lesser risk: it only ever affects the region that's actually
		// wedged, not every previously-constructed client for every region.
		sc, err := run.NewServicesClient(context.WithoutCancel(ctx), regionalEndpoint(region))
		if err != nil {
			return nil, fmt.Errorf("create Cloud Run services client for %s: %w", region, err)
		}
		c.mu.Lock()
		c.services[region] = sc
		c.mu.Unlock()
		return sc, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*run.ServicesClient), nil
}

func (c *GCPAdminClient) workerPoolsClient(ctx context.Context, region string) (*run.WorkerPoolsClient, error) {
	c.mu.Lock()
	if wc, ok := c.workerPools[region]; ok {
		c.mu.Unlock()
		return wc, nil
	}
	c.mu.Unlock()

	v, err, _ := c.workerPoolsGroup.Do(region, func() (any, error) {
		c.mu.Lock()
		if wc, ok := c.workerPools[region]; ok {
			c.mu.Unlock()
			return wc, nil
		}
		c.mu.Unlock()

		// Same singleflight/context.WithoutCancel rationale as servicesClient above.
		wc, err := run.NewWorkerPoolsClient(context.WithoutCancel(ctx), regionalEndpoint(region))
		if err != nil {
			return nil, fmt.Errorf("create Cloud Run workerPools client for %s: %w", region, err)
		}
		c.mu.Lock()
		c.workerPools[region] = wc
		c.mu.Unlock()
		return wc, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*run.WorkerPoolsClient), nil
}

func (c *GCPAdminClient) jobsClient(ctx context.Context, region string) (*run.JobsClient, error) {
	c.mu.Lock()
	if jc, ok := c.jobs[region]; ok {
		c.mu.Unlock()
		return jc, nil
	}
	c.mu.Unlock()

	v, err, _ := c.jobsGroup.Do(region, func() (any, error) {
		c.mu.Lock()
		if jc, ok := c.jobs[region]; ok {
			c.mu.Unlock()
			return jc, nil
		}
		c.mu.Unlock()

		// Same singleflight/context.WithoutCancel rationale as servicesClient above.
		jc, err := run.NewJobsClient(context.WithoutCancel(ctx), regionalEndpoint(region))
		if err != nil {
			return nil, fmt.Errorf("create Cloud Run jobs client for %s: %w", region, err)
		}
		c.mu.Lock()
		c.jobs[region] = jc
		c.mu.Unlock()
		return jc, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*run.JobsClient), nil
}

func (c *GCPAdminClient) executionsClient(ctx context.Context, region string) (*run.ExecutionsClient, error) {
	c.mu.Lock()
	if ec, ok := c.executions[region]; ok {
		c.mu.Unlock()
		return ec, nil
	}
	c.mu.Unlock()

	v, err, _ := c.executionsGroup.Do(region, func() (any, error) {
		c.mu.Lock()
		if ec, ok := c.executions[region]; ok {
			c.mu.Unlock()
			return ec, nil
		}
		c.mu.Unlock()

		// Same singleflight/context.WithoutCancel rationale as servicesClient above.
		ec, err := run.NewExecutionsClient(context.WithoutCancel(ctx), regionalEndpoint(region))
		if err != nil {
			return nil, fmt.Errorf("create Cloud Run executions client for %s: %w", region, err)
		}
		c.mu.Lock()
		c.executions[region] = ec
		c.mu.Unlock()
		return ec, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*run.ExecutionsClient), nil
}

// registryClientOrInit lazily constructs the Artifact Registry client used
// to resolve a tag-referenced live image — same singleflight-coalesced,
// context.WithoutCancel construction as the Cloud Run clients above, keyed
// by a constant since (unlike Cloud Run) there's only ever one of these.
func (c *GCPAdminClient) registryClientOrInit(ctx context.Context) (*registry.Client, error) {
	c.mu.Lock()
	if rc := c.registryClient; rc != nil {
		c.mu.Unlock()
		return rc, nil
	}
	c.mu.Unlock()

	v, err, _ := c.registryGroup.Do("", func() (any, error) {
		c.mu.Lock()
		if rc := c.registryClient; rc != nil {
			c.mu.Unlock()
			return rc, nil
		}
		c.mu.Unlock()

		rc, err := registry.NewClient(context.WithoutCancel(ctx))
		if err != nil {
			return nil, fmt.Errorf("create artifact registry client: %w", err)
		}
		c.mu.Lock()
		c.registryClient = rc
		c.mu.Unlock()
		return rc, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*registry.Client), nil
}

func serviceName(project, region, name string) string {
	return fmt.Sprintf("projects/%s/locations/%s/services/%s", project, region, name)
}

func workerPoolName(project, region, name string) string {
	return fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, region, name)
}

func jobName(project, region, name string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, region, name)
}

// apiCallTimeout bounds every AdminClient entry point's total Cloud Run API
// time. Without this, a call inherits whatever the caller's own context
// is — in the reconcile loop's auto path, that's the current leadership
// term's context (leadershipContext), which can live far longer than any
// single API call reasonably should. A hung call would otherwise occupy a
// reconcile worker-pool slot until leadership itself changes, not until
// the call actually finishes or fails.
const apiCallTimeout = 30 * time.Second

// GetService implements AdminClient.GetService. The interface doesn't carry
// resourceType (it covers both service and workerPool, §5.7), so this tries
// the Services API first and falls back to WorkerPools on NotFound —
// ponytail: one extra round-trip for workerPool units, add resourceType to
// the interface if that cost ever matters.
func (c *GCPAdminClient) GetService(ctx context.Context, project, region, name, desiredDigest string) (*LiveService, error) {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	sc, err := c.servicesClient(ctx, region)
	if err != nil {
		return nil, err
	}
	svc, err := sc.GetService(ctx, &runpb.GetServiceRequest{Name: serviceName(project, region, name)})
	if err == nil {
		return c.liveServiceFromService(ctx, svc, desiredDigest)
	}
	if status.Code(err) != codes.NotFound {
		return nil, fmt.Errorf("get service %s: %w", name, err)
	}

	wc, err := c.workerPoolsClient(ctx, region)
	if err != nil {
		return nil, err
	}
	wp, err := wc.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: workerPoolName(project, region, name)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotProvisioned
		}
		return nil, fmt.Errorf("get workerPool %s: %w", name, err)
	}
	return c.liveServiceFromWorkerPool(ctx, wp, desiredDigest)
}

// ListServiceNames implements AdminClient.ListServiceNames — services only
// (not workerPools/jobs): a first, deliberately narrower cut at prune's
// "flag what's orphaned" gap, not full coverage of every resource type.
func (c *GCPAdminClient) ListServiceNames(ctx context.Context, project, region string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	sc, err := c.servicesClient(ctx, region)
	if err != nil {
		return nil, err
	}
	it := sc.ListServices(ctx, &runpb.ListServicesRequest{
		Parent: fmt.Sprintf("projects/%s/locations/%s", project, region),
	})
	var names []string
	for {
		svc, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list services in projects/%s/locations/%s: %w", project, region, err)
		}
		names = append(names, lastPathSegment(svc.GetName()))
	}
	return names, nil
}

// lastPathSegment: "projects/p/locations/r/services/name" -> "name".
func lastPathSegment(fullName string) string {
	i := strings.LastIndex(fullName, "/")
	if i < 0 {
		return fullName
	}
	return fullName[i+1:]
}

// DeployService implements AdminClient.DeployService, with the same
// service-then-workerPool fallback as GetService.
func (c *GCPAdminClient) DeployService(ctx context.Context, project, region, name string, desired ServiceState) error {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	sc, err := c.servicesClient(ctx, region)
	if err != nil {
		return err
	}
	svc, err := sc.GetService(ctx, &runpb.GetServiceRequest{Name: serviceName(project, region, name)})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return fmt.Errorf("get service %s: %w", name, err)
		}
		return c.deployWorkerPool(ctx, project, region, name, desired)
	}

	if len(svc.GetTemplate().GetContainers()) == 0 {
		return fmt.Errorf("service %s has no containers in its revision template", name)
	}
	svc.Template.Containers[0].Image = withDigest(svc.Template.Containers[0].Image, desired.ImageDigest)
	if desired.TrafficLatestRevisionPercent != nil {
		percent, err := validatedPercent(*desired.TrafficLatestRevisionPercent)
		if err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		svc.Traffic = []*runpb.TrafficTarget{{
			Type:    runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST,
			Percent: percent,
		}}
	}
	// nil EnvVars/SecretRefs (env unmanaged) leaves Env untouched here, same
	// don't-touch-what-isn't-managed behavior traffic gets above.
	if desired.EnvVars != nil || desired.SecretRefs != nil {
		svc.Template.Containers[0].Env = buildEnvVars(desired.EnvVars, desired.SecretRefs)
	}
	if _, err := sc.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: svc}); err != nil {
		return fmt.Errorf("update service %s: %w", name, err)
	}
	return nil
}

func (c *GCPAdminClient) deployWorkerPool(ctx context.Context, project, region, name string, desired ServiceState) error {
	wc, err := c.workerPoolsClient(ctx, region)
	if err != nil {
		return err
	}
	wp, err := wc.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: workerPoolName(project, region, name)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrNotProvisioned
		}
		return fmt.Errorf("get workerPool %s: %w", name, err)
	}
	if len(wp.GetTemplate().GetContainers()) == 0 {
		return fmt.Errorf("workerPool %s has no containers in its revision template", name)
	}
	wp.Template.Containers[0].Image = withDigest(wp.Template.Containers[0].Image, desired.ImageDigest)
	if desired.EnvVars != nil || desired.SecretRefs != nil {
		wp.Template.Containers[0].Env = buildEnvVars(desired.EnvVars, desired.SecretRefs)
	}
	if _, err := wc.UpdateWorkerPool(ctx, &runpb.UpdateWorkerPoolRequest{WorkerPool: wp}); err != nil {
		return fmt.Errorf("update workerPool %s: %w", name, err)
	}
	return nil
}

// fetchJob fetches the raw Job proto via jc, translating NotFound into
// ErrNotProvisioned. Shared by GetJob and DeployJob so a deploy doesn't
// have to fetch the same job twice — once to decide idempotency, again to
// build the update payload.
func (c *GCPAdminClient) fetchJob(ctx context.Context, jc *run.JobsClient, project, region, name string) (*runpb.Job, error) {
	job, err := jc.GetJob(ctx, &runpb.GetJobRequest{Name: jobName(project, region, name)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotProvisioned
		}
		return nil, fmt.Errorf("get job %s: %w", name, err)
	}
	return job, nil
}

// GetJob implements AdminClient.GetJob. Right after a deploy, the job's
// spec template already reflects the new desired image but
// LatestCreatedExecution can still be the *previous* execution — comparing
// against the spec template (or trusting ExecutionReference's own
// completion status) would report an unrelated execution's outcome for the
// digest that hasn't actually run yet. Fetching the real Execution and
// reading its own container image avoids that conflation.
func (c *GCPAdminClient) GetJob(ctx context.Context, project, region, name, desiredDigest string) (*LiveJob, error) {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	jc, err := c.jobsClient(ctx, region)
	if err != nil {
		return nil, err
	}
	job, err := c.fetchJob(ctx, jc, project, region, name)
	if err != nil {
		return nil, err
	}

	ref := job.GetLatestCreatedExecution()
	if ref == nil {
		return &LiveJob{}, nil
	}

	exec, err := c.getExecution(ctx, project, region, name, ref)
	if err != nil {
		return nil, err
	}
	digest, err := c.resolveImageDigest(ctx, containerImage(exec.GetTemplate().GetContainers()))
	if err != nil {
		return nil, fmt.Errorf("resolve live image digest for job %s: %w", name, err)
	}
	return &LiveJob{
		ServiceState:                 ServiceState{ImageDigest: digest},
		HasExecutionForDesiredDigest: desiredDigest == "" || digest == desiredDigest,
		LatestExecutionStatus:        executionStatus(exec),
	}, nil
}

// getExecution fetches one job execution. ExecutionReference.Name (as
// actually returned by the real Cloud Run Admin API — confirmed against a
// live job, not just the proto docs) is only the short execution ID (e.g.
// "my-job-abcde"), not a fully-qualified resource name — passing it
// straight through as GetExecutionRequest.Name makes the API try to parse
// that short ID as if it were itself a resource path segment, producing a
// confusing "Permission denied on resource project <execution-id>" error
// instead of a clean 404/success. project/region/job (all already known to
// both callers) are needed to reconstruct the real resource name.
func (c *GCPAdminClient) getExecution(ctx context.Context, project, region, job string, ref *runpb.ExecutionReference) (*runpb.Execution, error) {
	ec, err := c.executionsClient(ctx, region)
	if err != nil {
		return nil, err
	}
	name := jobName(project, region, job) + "/executions/" + ref.GetName()
	exec, err := ec.GetExecution(ctx, &runpb.GetExecutionRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("get execution %s: %w", name, err)
	}
	return exec, nil
}

// DeployJob implements AdminClient.DeployJob: point the job spec at the
// desired image, then trigger a new execution — unless one's already
// running or has already succeeded for this exact digest. Unlike
// UpdateService/UpdateWorkerPool (which Cloud Run itself no-ops when
// nothing actually changed), RunJob always creates a brand new Execution
// regardless of whether the image changed — so without this check,
// deploySyncUnit's documented idempotency invariant ("deploying an
// already-deployed digest is a no-op", §5.3/NFR6) wouldn't hold for jobs:
// a poll that re-issues a deploy call while still waiting for a prior
// deploy's convergence would trigger a genuine duplicate job execution.
func (c *GCPAdminClient) DeployJob(ctx context.Context, project, region, name string, desired ServiceState) error {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	jc, err := c.jobsClient(ctx, region)
	if err != nil {
		return err
	}
	job, err := c.fetchJob(ctx, jc, project, region, name)
	if err != nil {
		return err
	}

	if ref := job.GetLatestCreatedExecution(); ref != nil {
		exec, err := c.getExecution(ctx, project, region, name, ref)
		if err != nil {
			return err
		}
		// A resolve failure here just falls through to redeploying (the
		// zero-value digest can't equal desired.ImageDigest) rather than
		// failing the whole deploy attempt — unlike GetJob's live-state
		// fetch, an idempotency pre-check that can't confirm "already
		// running this digest" should err toward deploying, not blocking.
		digest, _ := c.resolveImageDigest(ctx, containerImage(exec.GetTemplate().GetContainers()))
		execStatus := executionStatus(exec)
		if digest == desired.ImageDigest && (execStatus == ExecutionRunning || execStatus == ExecutionSucceeded) {
			return nil
		}
	}

	containers := job.GetTemplate().GetTemplate().GetContainers()
	if len(containers) == 0 {
		return fmt.Errorf("job %s has no containers in its task template", name)
	}
	containers[0].Image = withDigest(containers[0].Image, desired.ImageDigest)
	if _, err := jc.UpdateJob(ctx, &runpb.UpdateJobRequest{Job: job}); err != nil {
		return fmt.Errorf("update job %s: %w", name, err)
	}
	if _, err := jc.RunJob(ctx, &runpb.RunJobRequest{Name: job.GetName()}); err != nil {
		return fmt.Errorf("run job %s: %w", name, err)
	}
	return nil
}

// validatedPercent rejects anything other than a full cutover to the
// latest revision (100). runcd's traffic model
// (manifest.Traffic.LatestRevisionPercent) has no way to say where the
// remaining traffic should go, so any other value — including 0, which
// would still produce a single TrafficTarget summing to 0% rather than the
// required 100% — would build a Cloud Run traffic spec the API rejects,
// deep inside the call instead of here with a clear reason.
func validatedPercent(p int) (int32, error) {
	if p != 100 {
		return 0, fmt.Errorf("traffic.latestRevisionPercent %d is not supported — v1 only manages a full cutover to the latest revision (100), since it has no way to express where the remaining traffic should go", p)
	}
	return int32(p), nil
}

func containerImage(containers []*runpb.Container) string {
	if len(containers) == 0 {
		return ""
	}
	return containers[0].GetImage()
}

// digestSuffix extracts the "sha256:..." portion of a full image reference
// like "us-docker.pkg.dev/proj/repo/svc@sha256:...". manifest.Image.Digest
// is always validated as a bare digest with no registry/repo prefix
// (manifest/service.go), so ServiceState.ImageDigest must be bare too, or
// it can never equal the manifest's desired digest and the diff engine
// would report every resource as perpetually out of sync.
func digestSuffix(image string) string {
	if i := strings.LastIndex(image, "@"); i >= 0 {
		return image[i+1:]
	}
	return image
}

// isDigest reports whether s is already a bare "sha256:<64 hex>" digest —
// guards the case of a live image field with no registry prefix at all
// (digestSuffix returns it unchanged, having found no "@" to split on) so it
// isn't mistaken for a tag reference needing resolution.
func isDigest(s string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(s, prefix) && len(s) == len(prefix)+64
}

// splitImageRef splits a tag-referenced image ("repo/path:tag", or bare
// "repo/path" implicitly "latest") into repository and tag — the same rule
// Docker itself uses, careful not to mistake a registry host's own port
// (rare here, but "host:5000/repo" is a valid reference) for a tag
// separator: only a colon after the last "/" counts.
func splitImageRef(image string) (repository, tag string) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, "latest"
}

// tagResolver is the minimal seam resolveTag needs — narrowed from
// *registry.Client to just this one method so it's unit-testable with a
// fake, the same interface+fake pattern as cloudrun.AdminClient itself.
type tagResolver interface {
	ListTags(ctx context.Context, repository string) ([]registry.Tag, error)
}

// resolveImageDigest returns the bare "sha256:..." digest for a live image
// reference. Something other than RunCD may have deployed this resource by
// tag (e.g. `gcloud run deploy --image foo:v1`) rather than digest, and
// Cloud Run reports the image reference exactly as it was deployed — a raw
// tag string can never equal a desired bare digest, so a tag-only reference
// is resolved against Artifact Registry first. Once RunCD itself deploys
// this resource, withDigest always writes an "@sha256:..." reference, so
// this only ever costs a real API call for resources some other deployer
// still owns — not on every reconcile pass, forever.
func (c *GCPAdminClient) resolveImageDigest(ctx context.Context, image string) (string, error) {
	if image == "" {
		return "", nil
	}
	if suffix := digestSuffix(image); suffix != image || isDigest(suffix) {
		return suffix, nil
	}
	rc, err := c.registryClientOrInit(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve tag for %s: %w", image, err)
	}
	return resolveTag(ctx, rc, image)
}

// resolveTag does the actual tag lookup, split out from resolveImageDigest
// so it's testable against a fake tagResolver without a live client.
func resolveTag(ctx context.Context, r tagResolver, image string) (string, error) {
	repo, tag := splitImageRef(image)
	tags, err := r.ListTags(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("list tags for %s: %w", repo, err)
	}
	for _, t := range tags {
		if t.Name == tag {
			return t.Digest, nil
		}
	}
	return "", fmt.Errorf("no tag %q found for %s", tag, repo)
}

// withDigest rebuilds a full image reference from an existing live image
// (to recover its registry/repo prefix — the manifest never carries one)
// and the desired bare digest. Deploying a resource that's never had any
// image set (no "@" to anchor on, e.g. a Terraform-provisioned shell with a
// placeholder image) isn't supported — §5.5 assumes Terraform provisions
// the shell already pointed at the right repo.
func withDigest(existingImage, digest string) string {
	repo := existingImage
	if i := strings.LastIndex(existingImage, "@"); i >= 0 {
		repo = existingImage[:i]
	}
	return repo + "@" + digest
}

func latestRevisionPercent(targets []*runpb.TrafficTarget) int {
	total := 0
	for _, t := range targets {
		if t.GetType() == runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST {
			total += int(t.GetPercent())
		}
	}
	return total
}

// conditionState maps a resource's terminal condition (plus its top-level
// reconciling flag, which flips true before a terminal condition even
// exists) to service/workerPool health per §5.7: ready, or still creating.
// Anything else (FAILED, unspecified) is neither — health.AssessService
// reports that as Degraded.
func conditionState(cond *runpb.Condition, reconciling bool) (ready, creating bool) {
	if reconciling {
		return false, true
	}
	switch cond.GetState() {
	case runpb.Condition_CONDITION_SUCCEEDED:
		return true, false
	case runpb.Condition_CONDITION_RECONCILING, runpb.Condition_CONDITION_PENDING:
		return false, true
	default:
		return false, false
	}
}

// executionStatus classifies a fetched Execution by its own completion
// state — not by trusting the parent Job's ExecutionReference, which only
// carries a completion status snapshot that can lag the actual Execution.
// A cancelled execution maps to ExecutionFailed: it didn't complete as
// desired, same as health.AssessJob's Degraded treatment of any non-success
// outcome.
func executionStatus(exec *runpb.Execution) ExecutionStatus {
	if exec.GetCompletionTime() == nil {
		return ExecutionRunning
	}
	if exec.GetFailedCount() > 0 || exec.GetCancelledCount() > 0 {
		return ExecutionFailed
	}
	return ExecutionSucceeded
}

func (c *GCPAdminClient) liveServiceFromService(ctx context.Context, svc *runpb.Service, desiredDigest string) (*LiveService, error) {
	digest, err := c.resolveImageDigest(ctx, containerImage(svc.GetTemplate().GetContainers()))
	if err != nil {
		return nil, fmt.Errorf("resolve live image digest for service %s: %w", svc.GetName(), err)
	}
	percent := latestRevisionPercent(svc.GetTraffic())
	ready, creating := conditionState(svc.GetTerminalCondition(), svc.GetReconciling())
	envVars, secretRefs := envStateFromContainers(svc.GetTemplate().GetContainers())
	return &LiveService{
		ServiceState: ServiceState{
			ImageDigest:                  digest,
			TrafficLatestRevisionPercent: &percent,
			EnvVars:                      envVars,
			SecretRefs:                   secretRefs,
		},
		HasRevisionForDesiredDigest: desiredDigest == "" || digest == desiredDigest,
		LatestRevisionReady:         ready,
		LatestRevisionCreating:      creating,
	}, nil
}

func (c *GCPAdminClient) liveServiceFromWorkerPool(ctx context.Context, wp *runpb.WorkerPool, desiredDigest string) (*LiveService, error) {
	digest, err := c.resolveImageDigest(ctx, containerImage(wp.GetTemplate().GetContainers()))
	if err != nil {
		return nil, fmt.Errorf("resolve live image digest for workerPool %s: %w", wp.GetName(), err)
	}
	ready, creating := conditionState(wp.GetTerminalCondition(), wp.GetReconciling())
	envVars, secretRefs := envStateFromContainers(wp.GetTemplate().GetContainers())
	return &LiveService{
		ServiceState: ServiceState{
			ImageDigest: digest,
			EnvVars:     envVars,
			SecretRefs:  secretRefs,
		},
		HasRevisionForDesiredDigest: desiredDigest == "" || digest == desiredDigest,
		LatestRevisionReady:         ready,
		LatestRevisionCreating:      creating,
	}, nil
}

// envStateFromContainers splits containers[0].Env into plain values and
// secret-sourced ones — the inverse of buildEnvVars. Both return values are
// nil (not empty maps) when there are no env vars at all, matching this
// package's "nil means nothing here" convention for live state.
func envStateFromContainers(containers []*runpb.Container) (map[string]string, map[string]SecretRef) {
	if len(containers) == 0 || len(containers[0].GetEnv()) == 0 {
		return nil, nil
	}
	var vars map[string]string
	var secrets map[string]SecretRef
	for _, e := range containers[0].GetEnv() {
		if ref := e.GetValueSource().GetSecretKeyRef(); ref != nil {
			if secrets == nil {
				secrets = make(map[string]SecretRef)
			}
			secrets[e.GetName()] = SecretRef{Secret: ref.GetSecret(), Version: ref.GetVersion()}
			continue
		}
		if vars == nil {
			vars = make(map[string]string)
		}
		vars[e.GetName()] = e.GetValue()
	}
	return vars, secrets
}

// buildEnvVars is the inverse of envStateFromContainers — used by the
// deploy path to replace a container's Env wholesale when "env" is
// managed. Sorted by name for deterministic output (map iteration order
// isn't stable), so re-deploying an unchanged desired env doesn't produce
// a spurious spec diff from reordering alone.
func buildEnvVars(vars map[string]string, secrets map[string]SecretRef) []*runpb.EnvVar {
	out := make([]*runpb.EnvVar, 0, len(vars)+len(secrets))
	for name, value := range vars {
		out = append(out, &runpb.EnvVar{Name: name, Values: &runpb.EnvVar_Value{Value: value}})
	}
	for name, ref := range secrets {
		out = append(out, &runpb.EnvVar{
			Name: name,
			Values: &runpb.EnvVar_ValueSource{
				ValueSource: &runpb.EnvVarSource{
					SecretKeyRef: &runpb.SecretKeySelector{Secret: ref.Secret, Version: ref.Version},
				},
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
