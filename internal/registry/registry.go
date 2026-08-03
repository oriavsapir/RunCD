// Package registry lists Artifact Registry docker image tags — shared by
// internal/imageupdater (resolving image.track/image.version) and
// internal/cloudrun (resolving a live image that was deployed by tag rather
// than digest, e.g. by something other than RunCD). One implementation, one
// tested Artifact Registry List call, instead of two.
package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/iterator"
)

// Tag is one tag/digest pair for an Artifact Registry image.
type Tag struct {
	Name   string
	Digest string // "sha256:..."
}

// DefaultTagsCacheTTL caps how long a ListTags result is reused. Short
// relative to precondition.DefaultExistsCacheTTL — unlike topic/subscription
// existence, tags genuinely change on every merge — but still long enough to
// coalesce the handful of callers that share one image within a single
// reconcile pass (e.g. task-manager/task-manager-sweep) instead of each
// re-listing the whole repository.
const DefaultTagsCacheTTL = 30 * time.Second

type tagsCacheEntry struct {
	tags      []Tag
	fetchedAt time.Time
}

// Client lists tags for Artifact Registry docker images, backed by the real
// Admin API. One client for the whole process — unlike cloudrun's Cloud Run
// clients, Artifact Registry has no per-region endpoint to juggle.
type Client struct {
	client *artifactregistry.Client

	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]tagsCacheEntry
	group singleflight.Group
}

// NewClient constructs a Client using application default credentials — no
// additional configuration beyond what cloudrun.GCPAdminClient and
// precondition.GCPChecker already require of the runtime environment.
func NewClient(ctx context.Context) (*Client, error) {
	c, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("build artifact registry client: %w", err)
	}
	return &Client{client: c, cache: make(map[string]tagsCacheEntry)}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

// repositoryPattern matches an Artifact Registry docker image reference of
// the form "LOCATION-docker.pkg.dev/PROJECT/REPOSITORY/IMAGE" — the same
// format Cloud Run's own live container image URIs use, so it's
// copy-pasteable straight from `gcloud` output.
var repositoryPattern = regexp.MustCompile(`^([a-z0-9-]+)-docker\.pkg\.dev/([^/]+)/([^/]+)/(.+)$`)

// parseRepository splits repository into the Artifact Registry parent
// resource name (for the List call) and the "project/repo/image" path
// portion of a DockerImage's Uri (for filtering the results — a repository
// holds many images).
func parseRepository(repository string) (parent, imagePath string, err error) {
	m := repositoryPattern.FindStringSubmatch(repository)
	if m == nil {
		return "", "", fmt.Errorf("repository %q is not a valid Artifact Registry docker path (expected LOCATION-docker.pkg.dev/PROJECT/REPO/IMAGE)", repository)
	}
	location, project, repo, image := m[1], m[2], m[3], m[4]
	parent = fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repo)
	imagePath = fmt.Sprintf("%s/%s/%s", project, repo, image)
	return parent, imagePath, nil
}

// splitDigest splits a full image URI ("LOCATION-docker.pkg.dev/PROJECT/REPO/IMAGE@sha256:...")
// into its digest and its "PROJECT/REPO/IMAGE"-style path (everything after
// the registry host, before "@").
func splitDigest(uri string) (digest, path string, ok bool) {
	at := strings.LastIndex(uri, "@")
	if at < 0 {
		return "", "", false
	}
	digest = uri[at+1:]
	rest := uri[:at]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	return digest, rest[slash+1:], true
}

// ListTags lists every tag on the given Artifact Registry docker image.
// Results are cached for cacheTTL (DefaultTagsCacheTTL if unset) and
// construction/fetch is coalesced per repository via singleflight — only a
// successful fetch is cached, so a transient API error can't wedge a
// repository's tags stale past the point the underlying issue clears.
func (c *Client) ListTags(ctx context.Context, repository string) ([]Tag, error) {
	ttl := c.cacheTTL
	if ttl == 0 {
		ttl = DefaultTagsCacheTTL
	}

	c.mu.Lock()
	if e, ok := c.cache[repository]; ok && time.Since(e.fetchedAt) < ttl {
		c.mu.Unlock()
		return e.tags, nil
	}
	c.mu.Unlock()

	v, err, _ := c.group.Do(repository, func() (any, error) {
		c.mu.Lock()
		if e, ok := c.cache[repository]; ok && time.Since(e.fetchedAt) < ttl {
			c.mu.Unlock()
			return e.tags, nil
		}
		c.mu.Unlock()

		tags, err := c.listTagsUncached(ctx, repository)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.cache[repository] = tagsCacheEntry{tags: tags, fetchedAt: time.Now()}
		c.mu.Unlock()
		return tags, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]Tag), nil
}

// listTagsUncached does the real Artifact Registry call. There's no
// server-side filter for a single image within a repository
// (ListDockerImagesRequest only takes a parent), so this lists every image
// in the repository and keeps only the one matching image's tags.
func (c *Client) listTagsUncached(ctx context.Context, repository string) ([]Tag, error) {
	parent, wantPath, err := parseRepository(repository)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	it := c.client.ListDockerImages(ctx, &artifactregistrypb.ListDockerImagesRequest{Parent: parent})
	for {
		img, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list docker images in %s: %w", parent, err)
		}
		digest, path, ok := splitDigest(img.GetUri())
		if !ok || path != wantPath {
			continue
		}
		for _, name := range img.GetTags() {
			tags = append(tags, Tag{Name: name, Digest: digest})
		}
	}
	return tags, nil
}
