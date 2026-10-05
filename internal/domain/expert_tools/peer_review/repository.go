// Repository — peer-review submission persistence port.
//
// Hexagonal: domain owns the interface; adapters (in-memory, Cloud SQL via
// pgx) implement it. The domain MUST NOT import any adapter package.
package peer_review

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical sentinel for a missing submission.
var ErrNotFound = errors.New("review submission not found")

// ListFilter narrows the queue listing to a particular status (or all when
// empty). Pagination caps at 200 to stay within the chora-content database
// query budget per database-postgresql skill.
type ListFilter struct {
	Status Status // empty = all
	Limit  int    // 0 = default (50); cap 200
	Offset int
}

// Repository is the ReviewSubmission persistence port.
type Repository interface {
	// Save persists the aggregate. Acts as upsert by ReviewID.
	Save(ctx context.Context, r *ReviewSubmission) error
	// Get returns the submission for (tenantID, reviewID). ErrNotFound when
	// the submission does not exist or belongs to another tenant.
	Get(ctx context.Context, tenantID, reviewID string) (*ReviewSubmission, error)
	// List returns submissions for a tenant, optionally filtered by status.
	List(ctx context.Context, tenantID string, f ListFilter) ([]*ReviewSubmission, error)
}
