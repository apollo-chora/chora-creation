// Repository — the LearningAtom persistence port.
//
// This is a hexagonal port: the domain owns the interface; adapters
// (in-memory, Cloud SQL via pgx, etc.) implement it. The domain MUST NOT
// import any adapter package.
package atom

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical sentinel for a missing-or-soft-deleted atom.
var ErrNotFound = errors.New("atom not found")

// Repository is the LearningAtom persistence port.
type Repository interface {
	// Save persists the aggregate. Acts as upsert by AtomID.
	Save(ctx context.Context, a *LearningAtom) error

	// Get returns the atom for (tenantID, atomID) iff it exists, belongs to
	// tenantID, and is not soft-deleted. Returns ErrNotFound otherwise.
	Get(ctx context.Context, tenantID, atomID string) (*LearningAtom, error)

	// List returns active (non-deleted) atoms for a tenant, optionally
	// filtered by status.
	List(ctx context.Context, tenantID string, filter ListFilter) ([]*LearningAtom, error)

	// ListByCourse returns active (non-deleted) atoms for a tenant scoped to
	// a single course. Used by the Phyllis MVP /v1/courses/{course_id}/atoms
	// endpoint per docs/m13/phyllis-mvp-2026-05-08.md §5.3.
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*LearningAtom, error)
}
