// AtomRevision — append-only revision history of a LearningAtom.
//
// Per .claude/rules/ddd-enforcement.md aggregate invariant #4, AtomRevision is
// APPEND-ONLY: never UPDATE or DELETE in place. revision_number is monotonically
// increasing within an atom. The full revision history is replayable for audit.
//
// This skeleton ships the type only — persistence + the publishAtomRevision
// endpoint (POST /atoms/{atom_id}/revisions) are deferred until the Cloud SQL
// schema lands (Tier 2). The in-memory repo embeds the current revision on
// the LearningAtom itself and does not track history yet.
package atom

import "time"

// AtomRevision is an immutable, append-only content snapshot.
type AtomRevision struct {
	RevisionID     string    `json:"revision_id"`
	AtomID         string    `json:"atom_id"`
	RevisionNumber int       `json:"revision_number"`
	ContentHash    string    `json:"content_hash"`
	PublishedBy    string    `json:"published_by_gcid"`
	PublishedAt    time.Time `json:"published_at"`
}
