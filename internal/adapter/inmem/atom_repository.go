// Package inmem is an in-memory implementation of the LearningAtom Repository
// port. Used for tests and the M10 skeleton; Cloud SQL is deferred to Tier 2.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// AtomRepository is a goroutine-safe map-backed repository.
type AtomRepository struct {
	mu    sync.RWMutex
	atoms map[string]*atom.LearningAtom // keyed by AtomID
}

// NewAtomRepository constructs an initialised repository.
func NewAtomRepository() *AtomRepository {
	return &AtomRepository{atoms: make(map[string]*atom.LearningAtom)}
}

// Save persists the atom. Stored as a deep-ish copy (tags + revision history
// slices are copied; AppendOnlyRevisions are clone-ed because revisions are
// append-only and must not be observable as mutated by callers).
//
// ADR-156 Phase 1: also defensively copies the new slice fields
// (ImdaDimensionTags + MediaAssets) so callers cannot mutate the repo's
// stored aggregate via the returned reference.
func (r *AtomRepository) Save(_ context.Context, a *atom.LearningAtom) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *a
	clone.Tags = append([]string(nil), a.Tags...)
	clone.RevisionHistoryList = cloneRevisions(a.RevisionHistoryList)
	clone.ImdaDimensionTags = clonePhase1ImdaTags(a.ImdaDimensionTags)
	clone.MediaAssets = clonePhase1MediaAssets(a.MediaAssets)
	r.atoms[a.AtomID] = &clone
	return nil
}

// clonePhase1ImdaTags returns a fresh slice copy or nil. Keeps nil distinct
// from empty so the round-trip test can assert "no tags = nil".
func clonePhase1ImdaTags(in []atom.ImdaDimTag) []atom.ImdaDimTag {
	if in == nil {
		return nil
	}
	out := make([]atom.ImdaDimTag, len(in))
	copy(out, in)
	return out
}

// clonePhase1MediaAssets returns a fresh slice copy. MediaAsset itself is a
// value type (no inner pointers) so a shallow copy is sufficient.
func clonePhase1MediaAssets(in []atom.MediaAsset) []atom.MediaAsset {
	if in == nil {
		return nil
	}
	out := make([]atom.MediaAsset, len(in))
	copy(out, in)
	return out
}

// cloneRevisions defensively copies the slice + each revision (pointer +
// embedded SourceMetadata map) so internal mutations cannot leak.
func cloneRevisions(in []*atom.AppendOnlyRevision) []*atom.AppendOnlyRevision {
	if len(in) == 0 {
		return nil
	}
	out := make([]*atom.AppendOnlyRevision, len(in))
	for i, r := range in {
		c := *r
		if r.SourceMetadata != nil {
			c.SourceMetadata = make(map[string]string, len(r.SourceMetadata))
			for k, v := range r.SourceMetadata {
				c.SourceMetadata[k] = v
			}
		}
		out[i] = &c
	}
	return out
}

// Get returns the atom iff it exists, belongs to the tenant, and is not
// soft-deleted.
func (r *AtomRepository) Get(_ context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.atoms[atomID]
	if !ok {
		return nil, atom.ErrNotFound
	}
	if a.TenantID != tenantID {
		return nil, atom.ErrNotFound // tenant isolation: opaque 404
	}
	if a.DeletedAt != nil {
		return nil, atom.ErrNotFound // hide soft-deleted
	}
	return cloneAtomForOut(a), nil
}

// cloneAtomForOut produces a defensive copy of an aggregate for return-path
// callers (Get/List/ListByCourse). Copies every slice field so callers
// cannot mutate repo state via the returned reference.
func cloneAtomForOut(a *atom.LearningAtom) *atom.LearningAtom {
	clone := *a
	clone.Tags = append([]string(nil), a.Tags...)
	clone.RevisionHistoryList = cloneRevisions(a.RevisionHistoryList)
	clone.ImdaDimensionTags = clonePhase1ImdaTags(a.ImdaDimensionTags)
	clone.MediaAssets = clonePhase1MediaAssets(a.MediaAssets)
	return &clone
}

// ListByCourse returns active (non-deleted) atoms for a tenant scoped to a
// course. Sorted by CreatedAt ascending.
func (r *AtomRepository) ListByCourse(_ context.Context, tenantID, courseID string) ([]*atom.LearningAtom, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*atom.LearningAtom, 0, len(r.atoms))
	for _, a := range r.atoms {
		if a.TenantID != tenantID {
			continue
		}
		if a.DeletedAt != nil {
			continue
		}
		if a.CourseID != courseID {
			continue
		}
		out = append(out, cloneAtomForOut(a))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// List returns all non-deleted atoms for a tenant, sorted by CreatedAt asc.
func (r *AtomRepository) List(_ context.Context, tenantID string, f atom.ListFilter) ([]*atom.LearningAtom, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*atom.LearningAtom, 0, len(r.atoms))
	for _, a := range r.atoms {
		if a.TenantID != tenantID {
			continue
		}
		if a.DeletedAt != nil {
			continue
		}
		if f.Status != "" && a.Status != f.Status {
			continue
		}
		if f.Query != "" && !strings.Contains(strings.ToLower(a.Title), strings.ToLower(f.Query)) {
			continue
		}
		out = append(out, cloneAtomForOut(a))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if f.Offset > 0 && f.Offset < len(out) {
		out = out[f.Offset:]
	} else if f.Offset >= len(out) {
		out = nil
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}
