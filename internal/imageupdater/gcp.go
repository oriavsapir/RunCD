package imageupdater

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/api/iterator"
)

// GCPResolver is the real Resolver implementation, backed by the Artifact
// Registry Admin API.
type GCPResolver struct {
	client *artifactregistry.Client
}

// NewGCPResolver constructs a GCPResolver using application default
// credentials — no additional configuration beyond what cloudrun.GCPAdminClient
// and precondition.GCPChecker already require of the runtime environment.
func NewGCPResolver(ctx context.Context) (*GCPResolver, error) {
	c, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("build artifact registry client: %w", err)
	}
	return &GCPResolver{client: c}, nil
}

func (r *GCPResolver) Close() error {
	return r.client.Close()
}

// repositoryPattern matches an Artifact Registry docker image reference of
// the form "LOCATION-docker.pkg.dev/PROJECT/REPOSITORY/IMAGE" — the same
// format Cloud Run's own live container image URIs use (§image.repository's
// doc comment), so it's copy-pasteable straight from `gcloud` output.
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

// ListTags implements Resolver. Artifact Registry has no server-side filter
// for a single image within a repository (ListDockerImagesRequest only
// takes a parent), so this lists every image in the repository and keeps
// only the one matching image's tags.
func (r *GCPResolver) ListTags(ctx context.Context, repository string) ([]Tag, error) {
	parent, wantPath, err := parseRepository(repository)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	it := r.client.ListDockerImages(ctx, &artifactregistrypb.ListDockerImagesRequest{Parent: parent})
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
