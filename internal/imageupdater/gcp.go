package imageupdater

import (
	"context"
	"fmt"

	"github.com/runcd/runcd/internal/registry"
)

// GCPResolver is the real Resolver implementation, backed by
// internal/registry's Artifact Registry client.
type GCPResolver struct {
	client *registry.Client
}

// NewGCPResolver constructs a GCPResolver using application default
// credentials — no additional configuration beyond what cloudrun.GCPAdminClient
// and precondition.GCPChecker already require of the runtime environment.
func NewGCPResolver(ctx context.Context) (*GCPResolver, error) {
	c, err := registry.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("build artifact registry client: %w", err)
	}
	return &GCPResolver{client: c}, nil
}

func (r *GCPResolver) Close() error {
	return r.client.Close()
}

// ListTags implements Resolver.
func (r *GCPResolver) ListTags(ctx context.Context, repository string) ([]Tag, error) {
	tags, err := r.client.ListTags(ctx, repository)
	if err != nil {
		return nil, err
	}
	out := make([]Tag, len(tags))
	for i, t := range tags {
		out[i] = Tag{Name: t.Name, Digest: t.Digest}
	}
	return out, nil
}
