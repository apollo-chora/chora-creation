package inmem

import (
	"context"
	"sync"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

// VariantRepository persists Variant value objects keyed by VariantID.
// Each save is append-only conceptually — variants are immutable by design;
// re-publishing creates a new VariantID.
type VariantRepository struct {
	mu       sync.RWMutex
	variants map[string]av.Variant
}

// NewVariantRepository constructs an empty repository.
func NewVariantRepository() *VariantRepository {
	return &VariantRepository{variants: make(map[string]av.Variant)}
}

// Save persists v keyed by its VariantID. Returns no error.
func (r *VariantRepository) Save(_ context.Context, v av.Variant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch typed := v.(type) {
	case *av.Flashcard:
		r.variants[typed.VariantID] = typed
	case *av.CodeEditor:
		r.variants[typed.VariantID] = typed
	case *av.DragDrop:
		r.variants[typed.VariantID] = typed
	case *av.Multimedia:
		r.variants[typed.VariantID] = typed
	default:
		// Unknown concrete type — store as-is by underlying interface.
		r.variants[v.GetAtomID()+":"+string(v.Type())] = v
	}
	return nil
}

// Count returns the number of stored variants. Used by tests to assert
// persistence side-effects.
func (r *VariantRepository) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.variants)
}
