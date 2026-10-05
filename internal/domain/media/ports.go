// Ports for the MediaAsset aggregate. Hexagonal: domain owns interfaces;
// adapters (in-memory, GCS, Cloud SQL) implement them.
package media

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical sentinel for a missing-or-soft-deleted asset.
var ErrNotFound = errors.New("media asset not found")

// Repository is the persistence port.
type Repository interface {
	// Save persists the aggregate (upsert by AssetID).
	Save(ctx context.Context, m *MediaAsset) error

	// Get returns the asset for (tenantID, assetID) iff it exists, belongs to
	// tenantID, and is not soft-deleted. Returns ErrNotFound otherwise.
	Get(ctx context.Context, tenantID, assetID string) (*MediaAsset, error)

	// ListByAtom returns active assets for a tenant scoped to a single atom.
	ListByAtom(ctx context.Context, tenantID, atomID string) ([]*MediaAsset, error)
}

// SignedURLSigner is the upload-URL minting port. Adapters: gcsadapter
// (Cloud Storage V4 sign — production), inmem (stub URL — tests/dev).
type SignedURLSigner interface {
	// UploadURL returns a one-shot URL the client can PUT the object to,
	// scoped to the supplied content_type + size_bytes. Implementations are
	// free to encode tenancy + idempotency however suits the backing store.
	UploadURL(ctx context.Context, m *MediaAsset, ttlSeconds int) (string, error)
}
