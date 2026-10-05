// Package inmem — in-memory adapter for collection.Repository (WS-6a).
//
// Used by the legacy HTTP test suite and as the M10-skeleton default when
// CHORA_DB_DSN is unset. NOT safe for production — does not survive a
// restart.
package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// CollectionRepository is a goroutine-safe, map-backed Collection repository.
type CollectionRepository struct {
	mu          sync.RWMutex
	collections map[string]*collection.Collection // keyed by CollectionID
}

// NewCollectionRepository constructs an initialised repository.
func NewCollectionRepository() *CollectionRepository {
	return &CollectionRepository{collections: make(map[string]*collection.Collection)}
}

// Save persists the collection. Stores a deep-ish clone so callers cannot
// mutate the repo state via the returned reference.
func (r *CollectionRepository) Save(_ context.Context, c *collection.Collection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *c
	clone.Atoms = cloneAtoms(c.Atoms)
	r.collections[c.CollectionID] = &clone
	return nil
}

// Get returns the collection iff it belongs to tenantID + is not soft-deleted.
func (r *CollectionRepository) Get(_ context.Context, tenantID, id string) (*collection.Collection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.collections[id]
	if !ok || c.TenantID != tenantID || c.DeletedAt != nil {
		return nil, collection.ErrNotFound
	}
	clone := *c
	clone.Atoms = cloneAtoms(c.Atoms)
	return &clone, nil
}

// NB: GetVisible is GONE (ADR-233 D8). It used to return a collection from ANY
// tenant when visibility was PUBLIC — a cross-tenant read path that RLS made
// unreachable in pg anyway, and that PUBLIC's retirement removes entirely. The
// per-actor read predicate now lives in collection.Collection.VisibleTo, composed
// by collection.Service.GetVisible; the repository enforces the TENANT boundary
// only (Get).

// List returns active collections for the supplied tenant + filter.
func (r *CollectionRepository) List(_ context.Context, tenantID string, f collection.ListFilter) ([]*collection.Collection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*collection.Collection{}
	for _, c := range r.collections {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		if f.OwnerGcid != "" && c.OwnerGcid != f.OwnerGcid {
			continue
		}
		if f.Visibility != "" && c.Visibility != f.Visibility {
			continue
		}
		clone := *c
		clone.Atoms = cloneAtoms(c.Atoms)
		out = append(out, &clone)
	}
	if f.Limit > 0 && len(out) > f.Limit {
		end := f.Offset + f.Limit
		if end > len(out) {
			end = len(out)
		}
		if f.Offset > len(out) {
			f.Offset = len(out)
		}
		out = out[f.Offset:end]
	}
	return out, nil
}

func cloneAtoms(in []*collection.CollectionAtom) []*collection.CollectionAtom {
	if len(in) == 0 {
		return nil
	}
	out := make([]*collection.CollectionAtom, len(in))
	for i, a := range in {
		c := *a
		out[i] = &c
	}
	return out
}

// Compile-time check.
var _ collection.Repository = (*CollectionRepository)(nil)
