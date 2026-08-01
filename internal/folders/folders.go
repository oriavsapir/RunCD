// Package folders resolves a GCP folder ID to its direct child project
// IDs — the live-API step behind environments[env].folders (config) and
// rbac.yaml's "folder:<id>" scope, both of which let an operator reference
// a real GCP folder instead of listing every project under it by hand.
package folders

import (
	"context"
	"errors"
	"fmt"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"google.golang.org/api/iterator"
)

// Resolver lists the active project IDs directly under a GCP folder. Only
// direct children — a folder's own sub-folders are not recursed into,
// matching the Cloud Resource Manager ListProjects call this is built on
// ("descendants are not listed").
type Resolver interface {
	ProjectsInFolder(ctx context.Context, folderID string) ([]string, error)
}

// GCPResolver is the real Cloud Resource Manager v3 implementation. A
// single client suffices — unlike internal/cloudrun's regional clients,
// Resource Manager has one global endpoint.
type GCPResolver struct {
	client *resourcemanager.ProjectsClient
}

func NewGCPResolver(ctx context.Context) (*GCPResolver, error) {
	client, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create resource manager projects client: %w", err)
	}
	return &GCPResolver{client: client}, nil
}

func (r *GCPResolver) Close() error { return r.client.Close() }

func (r *GCPResolver) ProjectsInFolder(ctx context.Context, folderID string) ([]string, error) {
	it := r.client.ListProjects(ctx, &resourcemanagerpb.ListProjectsRequest{
		Parent: "folders/" + folderID,
	})
	var ids []string
	for {
		p, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list projects in folders/%s: %w", folderID, err)
		}
		// DELETE_REQUESTED/DELETED projects can linger in list results —
		// only an ACTIVE project is a real sync/RBAC target.
		if p.GetState() == resourcemanagerpb.Project_ACTIVE {
			ids = append(ids, p.GetProjectId())
		}
	}
	return ids, nil
}
