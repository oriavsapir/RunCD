// Package folders resolves a GCP folder ID to its direct child project
// IDs — the live-API step behind environments[env].folders (config) and
// rbac.yaml's "folder:<id>" scope, both of which let an operator reference
// a real GCP folder instead of listing every project under it by hand.
package folders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/iterator"
)

// Resolver lists the active project IDs directly under a GCP folder. Only
// direct children — a folder's own sub-folders are not recursed into,
// matching the Cloud Resource Manager ListProjects call this is built on
// ("descendants are not listed").
type Resolver interface {
	ProjectsInFolder(ctx context.Context, folderID string) ([]string, error)
}

// DefaultCacheTTL caps how long a ProjectsInFolder result is reused. A
// folder's membership changes when someone moves a project in the GCP
// console, not every reconcile tick — without this, every tick would cost
// a live Resource Manager call per folder, and the same folder ID
// referenced from both environments[env].folders (config resolution) and
// an rbac.yaml "folder:<id>" scope (membership resolution) would be
// resolved twice per tick for identical, still-fresh data.
const DefaultCacheTTL = 60 * time.Second

type folderCacheEntry struct {
	projects  []string
	fetchedAt time.Time
}

// GCPResolver is the real Cloud Resource Manager v3 implementation. A
// single client suffices — unlike internal/cloudrun's regional clients,
// Resource Manager has one global endpoint. Concurrent calls for the same
// folder ID (e.g. config resolution and RBAC membership resolution racing
// each other in the same tick) coalesce via singleflight rather than each
// making their own API call.
type GCPResolver struct {
	// CacheTTL overrides DefaultCacheTTL; zero means use the default.
	CacheTTL time.Duration

	client *resourcemanager.ProjectsClient

	mu    sync.Mutex
	cache map[string]folderCacheEntry
	group singleflight.Group
}

func NewGCPResolver(ctx context.Context) (*GCPResolver, error) {
	client, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create resource manager projects client: %w", err)
	}
	return &GCPResolver{client: client, cache: make(map[string]folderCacheEntry)}, nil
}

func (r *GCPResolver) Close() error { return r.client.Close() }

func (r *GCPResolver) ProjectsInFolder(ctx context.Context, folderID string) ([]string, error) {
	ttl := r.CacheTTL
	if ttl == 0 {
		ttl = DefaultCacheTTL
	}

	r.mu.Lock()
	if entry, ok := r.cache[folderID]; ok && time.Since(entry.fetchedAt) < ttl {
		r.mu.Unlock()
		return entry.projects, nil
	}
	r.mu.Unlock()

	v, err, _ := r.group.Do(folderID, func() (any, error) {
		r.mu.Lock()
		if entry, ok := r.cache[folderID]; ok && time.Since(entry.fetchedAt) < ttl {
			r.mu.Unlock()
			return entry.projects, nil
		}
		r.mu.Unlock()

		ids, err := r.listProjectsInFolder(context.WithoutCancel(ctx), folderID)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.cache[folderID] = folderCacheEntry{projects: ids, fetchedAt: time.Now()}
		r.mu.Unlock()
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}

func (r *GCPResolver) listProjectsInFolder(ctx context.Context, folderID string) ([]string, error) {
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
