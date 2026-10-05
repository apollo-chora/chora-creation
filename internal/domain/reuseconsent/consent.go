// Package reuseconsent is the single canonical statement of the ADR-229
// reuse-consent disjunct:
//
//	own ∪ (tenant-visible ∧ published) ∪ (friends-visible ∧ published ∧
//	author ∈ my friend set) ∪ granted
//
// It is a PURE predicate over locally-owned atom facts (chora_creation owns the
// audience flag + publish state) plus the caller-scoped consent context that
// arrives from chora-sharing over gRPC (friend set + active-grant atom ids).
// Never a cross-DB read.
//
// WHY IT LIVES HERE (ADR-233 D10/D11): the disjunct is enforced at four
// chokepoints — the picker (SQL, question_search.go), the test-set snapshot
// (SnapshotQuestionByID), collection-ADD, and collection-CONVERT. The first two
// predate this package. The two collection gates share this one function so a
// convert and an add can never silently diverge on what a learner may reuse.
//
// GRANTS. A grant is the ADR-229 D2 audit record of reusing SOMEONE ELSE's
// atom — free-license in v1, no charge. `NeedsGrant` is therefore true on
// exactly the legs where a non-owner is newly reusing under an audience rule
// (tenant / friends), and false when the reuse is already covered (own, or an
// existing grant). Minting a grant against yourself would be nonsense; re-minting
// an existing one would be noise.
//
// HEXAGONAL: stdlib + the audience value object. No infrastructure.
package reuseconsent

import "github.com/apollo-chora/chora-creation/internal/domain/audience"

// Reason is the machine-readable D4.2 taxonomy returned to the caller for every
// EXCLUDED atom. It is surfaced verbatim to A+ so the learner is told what was
// left out and why — ADR-233 D11 forbids dropping an atom silently.
type Reason string

const (
	// ReasonNarrowed — the atom is private to its author and the actor holds no
	// grant. Covers both "the author withdrew it after you curated it" and
	// "you never had rights to it"; the two are indistinguishable without an
	// audience-change history, and the remedy is identical.
	ReasonNarrowed Reason = "REUSE_VISIBILITY_NARROWED"
	// ReasonNotPublished — the audience would permit reuse, but the atom is not
	// published (draft or archived). Reuse rides on the published revision.
	ReasonNotPublished Reason = "ATOM_NOT_PUBLISHED"
	// ReasonNotInFriendSet — friends-visible, but the author is not in the
	// actor's friend set.
	ReasonNotInFriendSet Reason = "NOT_IN_FRIEND_SET"
	// ReasonAtomNotFound — the atom_id does not resolve to an active row. A
	// collection holds FK-less cross-aggregate refs, so a curated atom can be
	// soft-deleted out from under it.
	ReasonAtomNotFound Reason = "ATOM_NOT_FOUND"
)

// AtomFact is the locally-owned half of the disjunct: everything chora_creation
// knows about an atom's reusability, read in ONE batch query.
type AtomFact struct {
	AtomID     string
	AuthorGCID string
	Audience   audience.Audience
	Published  bool
}

// Context is the caller-scoped half, sourced from chora-sharing's
// GetReuseContext RPC (ADR-229 WS-0). A fetch failure must refuse the operation
// upstream — an empty Context here would silently narrow the gate, and an absent
// one would blow it wide open.
type Context struct {
	ActorGCID      string
	FriendGCIDs    []string
	GrantedAtomIDs []string
}

// Decision is the per-atom verdict.
type Decision struct {
	AtomID     string
	Entitled   bool
	NeedsGrant bool
	Reason     Reason // set iff !Entitled
}

// Result partitions a whole collection. EntitledAtomIDs preserves the
// collection's curated order — the proto pins ordering, and a study list that
// silently reshuffled the learner's sequence would be its own small betrayal.
type Result struct {
	EntitledAtomIDs  []string
	NeedGrantAtomIDs []string
	Excluded         []Decision
}

// Evaluate resolves the disjunct for one atom.
//
// Order matters. An active grant is checked FIRST and is dispositive: orphan
// editions (ADR-229 A1.1) are minted audience=private and are never 'published',
// reachable ONLY through a repointed grant. Checking audience first would
// exclude exactly the atoms consumed-continuity exists to protect.
func Evaluate(f AtomFact, c Context) Decision {
	d := Decision{AtomID: f.AtomID}

	// granted — dispositive, outranks audience and publish state.
	if contains(c.GrantedAtomIDs, f.AtomID) {
		d.Entitled = true
		return d
	}

	// own — you may always reuse your own work, at any audience, published or
	// not. No grant: a grant records reuse of someone else's atom.
	if f.AuthorGCID != "" && f.AuthorGCID == c.ActorGCID {
		d.Entitled = true
		return d
	}

	switch f.Audience {
	case audience.Tenant:
		if !f.Published {
			d.Reason = ReasonNotPublished
			return d
		}
		d.Entitled = true
		d.NeedsGrant = true
		return d

	case audience.Friends:
		// Inert until the A+ audience un-hides (ADR-229 A1.4 / ADR-230
		// B-lite.2) — no atom carries audience=friends yet. Implemented anyway,
		// so the gate is correct the day it does, rather than silently refusing
		// atoms it should admit.
		if !f.Published {
			d.Reason = ReasonNotPublished
			return d
		}
		if !contains(c.FriendGCIDs, f.AuthorGCID) {
			d.Reason = ReasonNotInFriendSet
			return d
		}
		d.Entitled = true
		d.NeedsGrant = true
		return d

	default:
		// private — and any unrecognised value, which must fail CLOSED.
		d.Reason = ReasonNarrowed
		return d
	}
}

// EvaluateAll resolves the disjunct across a collection's curated atom order.
//
// `facts` is the batch read keyed by atom_id; an atom_id absent from the map is
// an atom that no longer resolves (soft-deleted) and is excluded as
// ReasonAtomNotFound rather than crashing the conversion.
func EvaluateAll(order []string, facts map[string]AtomFact, c Context) Result {
	var res Result
	for _, atomID := range order {
		f, ok := facts[atomID]
		if !ok {
			res.Excluded = append(res.Excluded, Decision{
				AtomID: atomID,
				Reason: ReasonAtomNotFound,
			})
			continue
		}
		d := Evaluate(f, c)
		if !d.Entitled {
			res.Excluded = append(res.Excluded, d)
			continue
		}
		res.EntitledAtomIDs = append(res.EntitledAtomIDs, atomID)
		if d.NeedsGrant {
			res.NeedGrantAtomIDs = append(res.NeedGrantAtomIDs, atomID)
		}
	}
	return res
}

func contains(haystack []string, needle string) bool {
	if needle == "" {
		return false
	}
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
