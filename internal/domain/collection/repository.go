// Repository + gate ports — the Collection hexagonal boundary (WS-6a;
// ADR-233 WS-4).
//
// The DOMAIN owns these interfaces; adapters (in-memory, Cloud SQL via pgx,
// chora-sharing gRPC) implement them. The domain MUST NOT import any adapter
// package.
package collection

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// Repository is the Collection persistence port. Production wires a
// pgx-backed implementation against chora_creation; tests use an in-memory
// implementation.
//
// All methods operate inside a per-tenant RLS transaction at the adapter
// layer (SET LOCAL chora.tenant_id) — see internal/adapter/pg/runtime.go
// for the canonical pattern. The repository receives an explicit tenantID
// argument so the adapter can apply RLS before the user query.
//
// NB there is deliberately NO `GetVisible` here. It used to exist and was a
// pure passthrough to Get — no owner check, no visibility check — which is
// precisely how the horizontal-authz defect in ADR-233 Context §6 shipped: the
// service docstring CLAIMED the check existed, so it passed review. The
// visibility predicate now lives where it can be tested in isolation and cannot
// be silently skipped: Collection.VisibleTo (ADR-233 D8), composed by
// Service.GetVisible.
type Repository interface {
	// Save persists the aggregate (UPSERT on collection_id). Child
	// CollectionAtom rows are reconciled against the in-memory Atoms slice:
	// adds and removes between the existing DB row and the in-memory state
	// are applied. Cascade-soft-delete of children when the parent is
	// soft-deleted lives here (per ddd-enforcement Aggregate Invariant #8).
	Save(ctx context.Context, c *Collection) error

	// Get returns the collection for (tenantID, collectionID) iff it
	// exists, belongs to tenantID, and is not soft-deleted. Returns
	// ErrNotFound otherwise.
	//
	// Get enforces the TENANT boundary only. The per-actor READ predicate
	// (owner ∪ tenant ∪ friends) is enforced above it, in Service.GetVisible →
	// Collection.VisibleTo.
	Get(ctx context.Context, tenantID, collectionID string) (*Collection, error)

	// List returns active (non-deleted) collections owned by the supplied
	// tenant + filter (visibility, owner_gcid, pagination).
	List(ctx context.Context, tenantID string, filter ListFilter) ([]*Collection, error)
}

// NB there is deliberately NO `AtomLookup` port either. It existed to answer two
// questions for AddAtom — "does this atom exist?" and "which tenant owns it?" —
// and ADR-233 subsumed both. Existence is now answered by AtomReuseFactLookup
// below (a TENANT-SCOPED read: an atom missing from the map does not resolve, and
// reuseconsent reports ATOM_NOT_FOUND); the owning tenant was only ever needed to
// feed the BP-01 cross-tenant rule that ADR-233 D7 retired.
//
// Deleting it was a BUG FIX, not a tidy-up. Its pg adapter ran `SELECT tenant_id
// FROM learning_atoms WHERE atom_id = $1` with no tenant filter, on the bare pool
// rather than inside RunInTenantTx — an untenanted read of an RLS-protected table.
// learning_atoms' policy is `tenant_id = (current_setting('chora.tenant_id',
// true))::uuid`, so on a pooled connection whose GUC had reset to the empty
// string it threw `invalid input syntax for type uuid: ""` (22P02). Every
// POST /api/v1/collections/{id}/atoms in production 500'd on it; collection_atoms
// never held a row. A port whose contract REQUIRES reading across the tenant
// boundary cannot be implemented against an RLS-protected table — the contract
// was the bug.
//
// -----------------------------------------------------------------------------
// ADR-229 / ADR-233 D10 — the reuse-consent gate ports
//
// Two gates, one predicate (reuseconsent.Evaluate):
//
//	add-time     — creation-local read of the audience flag. No grant, no
//	               charge: adding to a collection is CURATION, not licensing
//	               (FR-030 / T068).
//	convert-time — re-evaluates the full disjunct at the moment atoms cross into
//	               chora_consumption, and mints the GRANT_SCOPE_COLLECTION audit
//	               record.
//
// Both gates are needed, or neither works (ADR-233 D10b): a collection hold
// mints no grant, so it is invisible to chora-sharing's stranding detector — an
// author's post-curation withdrawal can never reach a curated-but-unconverted
// atom. Conversion is the moment a weak (bookmark) hold becomes a strong
// (licensed, orphan-protected) one.
// -----------------------------------------------------------------------------

// AtomReuseFactLookup reads the locally-owned half of the disjunct — the author,
// the audience flag and the publish state — for a batch of atoms, in ONE query.
// chora_creation owns these facts (ADR-229 D1), so this is an intra-DB
// cross-aggregate read, never a cross-DB one.
//
// Atoms absent from the returned map are atoms that no longer resolve (a
// collection holds FK-less refs, so a curated atom can be soft-deleted out from
// under it). reuseconsent.EvaluateAll handles that as ATOM_NOT_FOUND rather than
// crashing the conversion.
type AtomReuseFactLookup interface {
	ReuseFacts(ctx context.Context, tenantID string, atomIDs []string) (map[string]reuseconsent.AtomFact, error)
}

// ConsentContextFetcher reads the caller-scoped half — the friend set and the
// active-grant atom ids — from chora-sharing over gRPC (never a cross-DB read).
//
// Implementations MUST fail loud. An error means the gate cannot be evaluated,
// so the operation is REFUSED: an empty context would silently NARROW the gate,
// and an absent one would blow it wide open.
type ConsentContextFetcher interface {
	ConsentContext(ctx context.Context, actorGCID, tenantID string) (reuseconsent.Context, error)
}

// CollectionUseAuthorizer writes the ADR-229 D2 audit record for an allowed
// NON-OWNER reuse crossing into chora_consumption: an idempotent AtomUsageGrant
// with scope GRANT_SCOPE_COLLECTION (v1 free-license — a grant records reuse,
// and mints no charge).
//
// ADR-233 D10a makes convert-to-study-list the THIRD grant-write site alongside
// the test-set snapshot and the quiz/duel arm, and activates a GrantScope enum
// value that had been dead in the contract until now.
//
// A write failure REFUSES the conversion: the audit record is not optional.
type CollectionUseAuthorizer interface {
	AuthorizeCollectionUse(ctx context.Context, tenantID, granteeGCID, atomID string) error
}
