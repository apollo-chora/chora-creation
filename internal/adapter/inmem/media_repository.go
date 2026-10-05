package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

// MediaRepository is a goroutine-safe map-backed implementation of
// media.Repository. Used for tests + the M11 skeleton; the Cloud SQL
// pgx adapter lands at M12 alongside the real GCS signer.
type MediaRepository struct {
	mu     sync.RWMutex
	assets map[string]*media.MediaAsset // keyed by AssetID
}

// NewMediaRepository constructs an empty repository.
func NewMediaRepository() *MediaRepository {
	return &MediaRepository{assets: make(map[string]*media.MediaAsset)}
}

// Save persists the asset (upsert by AssetID).
func (r *MediaRepository) Save(_ context.Context, m *media.MediaAsset) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *m
	r.assets[m.AssetID] = &clone
	return nil
}

// Get returns the asset iff it exists, belongs to tenantID, and is not
// soft-deleted.
func (r *MediaRepository) Get(_ context.Context, tenantID, assetID string) (*media.MediaAsset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.assets[assetID]
	if !ok {
		return nil, media.ErrNotFound
	}
	if m.TenantID != tenantID {
		return nil, media.ErrNotFound // tenant-isolation: opaque 404
	}
	if m.DeletedAt != nil {
		return nil, media.ErrNotFound
	}
	clone := *m
	return &clone, nil
}

// ListByAtom returns active assets for a tenant scoped to a single atom.
func (r *MediaRepository) ListByAtom(_ context.Context, tenantID, atomID string) ([]*media.MediaAsset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*media.MediaAsset, 0, len(r.assets))
	for _, m := range r.assets {
		if m.TenantID != tenantID {
			continue
		}
		if m.AtomID != atomID {
			continue
		}
		if m.DeletedAt != nil {
			continue
		}
		clone := *m
		out = append(out, &clone)
	}
	return out, nil
}
