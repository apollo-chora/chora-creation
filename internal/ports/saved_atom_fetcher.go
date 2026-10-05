// saved_atom_fetcher.go — port for fetching the caller's saved-atom IDs from
// chora-sharing (C+ bookmark surface). Used by the question picker's `saved`
// disjunct per docs/design/ux_unified_atom_picker.md.
package ports

import "context"

// SavedAtomIDFetcher returns the atom IDs the caller has bookmarked (saved)
// in chora-sharing. The IDs are then hydrated from chora_creation.learning_atoms.
// Callers MUST treat the result as best-effort — a failure or timeout returns
// an empty list + the partial flag so the picker can warn.
type SavedAtomIDFetcher interface {
	ListSavedAtomIDs(ctx context.Context, gcid, tenantID string) (ids []string, truncated bool, err error)
}
