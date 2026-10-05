// Repository — quality-score persistence port (append-only).
//
// Append-only per .claude/rules/ddd-enforcement.md aggregate invariant #4.
// Re-scoring an atom emits a NEW QualityScore (new ScoreID); the previous
// records remain queryable for audit replay.
package quality_score

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical sentinel for a missing score record.
var ErrNotFound = errors.New("quality score not found")

// Repository is the QualityScore persistence port.
type Repository interface {
	// Append persists a fresh QualityScore record. Implementations must
	// reject UPDATEs in place — re-scoring uses a new ScoreID.
	Append(ctx context.Context, q *QualityScore) error
	// Latest returns the most recent score for (tenantID, atomID), or
	// ErrNotFound when none exists.
	Latest(ctx context.Context, tenantID, atomID string) (*QualityScore, error)
	// History returns the full append-only history (descending by ComputedAt).
	History(ctx context.Context, tenantID, atomID string) ([]*QualityScore, error)
}
