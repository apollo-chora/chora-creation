// orphan.go — ADR-229 Amendment A1 (CHO-2132): the singleton orphan edition.
//
// When a reused atom is withdrawn (reuse-visibility narrowed, un-shared, or
// archived) while >=1 consumer holds an active AtomUsageGrant, chora-creation
// materialises exactly ONE immutable orphan edition per (atom,
// last-published-revision): a clone of that revision carrying orphaned_from /
// orphaned_source_revision / orphaned_at provenance. Stranded consumers'
// grants, and creation's own non-author collection entries, repoint to it —
// grants are NEVER revoked by a withdrawal (consumed-continuity is absolute).
//
// Invariants (enforced here + migration 0030 + the freeze guards in atom.go):
//   - owner stays the ORIGINAL author (attribution R1; the closure saga
//     pseudonymises it, the orphan itself persists — A1.2),
//   - status=published (consumers can still read/snapshot),
//   - reuse_visibility=private FOREVER (the cross-lane picker contract:
//     tenant/saved disjuncts exclude orphans purely via reuse_visibility;
//     orphans are reachable ONLY via repointed grants),
//   - FROZEN: no revisions, no audience changes, no publish, no soft-delete
//     (ErrOrphanFrozen) — evolving an orphan = fork via Clone(),
//   - its own aggregate root: the original's archive cascade never touches it.
package atom

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrOrphanFrozen is returned by every mutation attempted against an orphan
// edition. Handlers map it onto the 409 CREATION_ATOM_ORPHANED_FROZEN
// taxonomy (ADR-229 A1.2 — fail loud, never a silent no-op).
var ErrOrphanFrozen = errors.New("atom is a frozen orphan edition (ADR-229 A1); fork via clone to evolve it")

// Orphan trigger labels — the wire values carried on
// chora.sharing.atom_reuse.orphan_required.v1 (field 4) and
// chora.creation.atom.orphan_created.v1 (field 6).
//
// "unshared" is RESERVED for a future feed-unshare surface: today's two real
// triggers are a reuse-visibility narrowing and an archive-while-consumed.
const (
	OrphanTriggerNarrowed = "narrowed"
	OrphanTriggerUnshared = "unshared"
	OrphanTriggerArchived = "archived"
)

// ValidOrphanTrigger reports whether s is one of the canonical trigger
// labels (exact match — the wire contract is lowercase, untrimmed).
func ValidOrphanTrigger(s string) bool {
	switch s {
	case OrphanTriggerNarrowed, OrphanTriggerUnshared, OrphanTriggerArchived:
		return true
	}
	return false
}

// OrphanCloneParams is the input envelope for CloneOrphan.
type OrphanCloneParams struct {
	// Source is the withdrawn atom (loaded INCLUDING soft-deleted — the
	// archive trigger fires after the original was archived). Required.
	Source *LearningAtom
	// SourceRevisionID is the LAST-PUBLISHED revision the orphan pins — the
	// second half of the singleton key. The caller resolves it authoritatively
	// in-domain (the orphan_required event's revision_id is only a hint).
	SourceRevisionID string
	// Now is injectable for tests; zero means time.Now().UTC().
	Now time.Time
}

// CloneOrphan builds the immutable orphan edition from the withdrawn source
// atom. Unlike Clone (a caller-owned draft fork), the orphan:
//   - stays owned by the ORIGINAL author (attribution R1),
//   - keeps the source title verbatim (it is an edition, not a "Copy of"),
//   - is born PUBLISHED + reuse_visibility=private,
//   - carries the orphan markers (orphaned_from / source revision / at) that
//     freeze it and feed the DB singleton index (migration 0030).
//
// The caller persists it via the idempotent mint (INSERT .. ON CONFLICT on
// the partial unique index → return existing) and clones the source's
// question + latest revision onto it.
func CloneOrphan(p OrphanCloneParams) (*LearningAtom, error) {
	if p.Source == nil {
		return nil, errors.New("atom.CloneOrphan: source atom is required")
	}
	if p.Source.IsOrphan() {
		return nil, fmt.Errorf("atom.CloneOrphan: source %s is itself an orphan edition (orphan-of-orphan forbidden)", p.Source.AtomID)
	}
	rev := strings.TrimSpace(p.SourceRevisionID)
	if rev == "" {
		return nil, errors.New("atom.CloneOrphan: source revision id is required (singleton key)")
	}
	if strings.TrimSpace(p.Source.TenantID) == "" || strings.TrimSpace(p.Source.Gcid) == "" {
		return nil, errors.New("atom.CloneOrphan: source tenant_id + gcid required")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("atom.CloneOrphan: uuidv7: %w", err)
	}

	mode := p.Source.Mode
	if !mode.Valid() {
		mode = ModeStraightUp
	}

	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	orphanedAt := now

	return &LearningAtom{
		AtomID:   id.String(),
		TenantID: p.Source.TenantID,
		Gcid:     p.Source.Gcid, // the ORIGINAL author — attribution R1
		Title:    p.Source.Title,
		Body:     p.Source.Body,
		Tags:     append([]string(nil), p.Source.Tags...),
		Mode:     mode,
		Status:   StatusPublished, // consumers still read/snapshot it
		Revision: 1,
		// CourseID intentionally NOT inherited — the orphan is an
		// independent frozen edition, not a course member.
		QuestionType:      p.Source.QuestionType,
		Difficulty:        p.Source.Difficulty,
		Stem:              p.Source.Stem,
		Subject:           p.Source.Subject,
		CognitiveLevel:    p.Source.CognitiveLevel,
		ImdaDimensionTags: append([]ImdaDimTag(nil), p.Source.ImdaDimensionTags...),
		MediaAssets:       append([]MediaAsset(nil), p.Source.MediaAssets...),
		ClonedFromAtomID:  p.Source.AtomID, // clone-substrate provenance (mig 0027)
		ReuseVisibility:   ReusePrivate,    // FOREVER — cross-lane contract

		OrphanedFromAtomID:       p.Source.AtomID,
		OrphanedSourceRevisionID: rev,
		OrphanedAt:               &orphanedAt,

		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
