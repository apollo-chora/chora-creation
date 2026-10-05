package ports

import "context"

// DisplayNameResolver resolves a GCID to a human-readable display name.
// Used by the publish handler to denormalise author_display_name onto the
// atom.published.v1 event so downstream consumers (chora-sharing feed cards)
// never need a cross-DB identity lookup.
type DisplayNameResolver interface {
	ResolveDisplayName(ctx context.Context, gcid string) (string, error)
}
