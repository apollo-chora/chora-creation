// Repository — golden anchor persistence port.
//
// Hexagonal: domain owns the interface; adapters implement it. The domain
// MUST NOT import any adapter package.
package golden_set

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical sentinel for a missing anchor.
var ErrNotFound = errors.New("golden anchor not found")

// Repository is the Anchor persistence port.
type Repository interface {
	// Save persists the aggregate. Acts as upsert by AnchorID.
	Save(ctx context.Context, a *Anchor) error
	// Get returns the anchor for (tenantID, anchorID) iff active + tenant-scoped.
	Get(ctx context.Context, tenantID, anchorID string) (*Anchor, error)
	// List returns active (non-deleted) anchors for a tenant.
	List(ctx context.Context, tenantID string) ([]*Anchor, error)
}
