// reuse_context.go — ports for the ADR-229 reuse-consent gate (WS-2,
// CHO-2133). chora-creation enforces the consent predicate at its two
// chokepoints (picker search D4.1 + SnapshotQuestionByID D4.2); the
// authorisation INPUTS (friend set, active-grant atom ids) live in
// chora-sharing and arrive per-request over gRPC — never a cross-DB read.
package ports

import "context"

// ReuseContext is the caller-scoped consent context returned by
// chora-sharing's GetReuseContext RPC (ADR-229 WS-0 / CHO-2102).
type ReuseContext struct {
	// FriendGCIDs — GCIDs the caller is friends with (ADR-230 explicit
	// friendship minus blocks). Amendment A1.4 seam: this field is carried
	// so the contract stays byte-identical, but the friends-visible picker
	// disjunct is DEFERRED until the A+ audience un-hides (ADR-230
	// B-lite.2) — no consumer reads it yet. Wire it into the pg search
	// builder's friends leg (see question_search.go seam note) when the
	// audience ships.
	FriendGCIDs []string

	// GrantedAtomIDs — atom ids covered by an ACTIVE AtomUsageGrant for the
	// caller (any scope). Orphan editions repointed by the singleton-orphan
	// machinery (ADR-229 A1.1) surface exclusively through this list — they
	// are minted reuse_visibility='private' forever.
	GrantedAtomIDs []string
}

// ReuseContextFetcher reads the caller's reuse-consent context from
// chora-sharing, once per picker search / snapshot check. Implementations
// MUST fail loud — a fetch error means the gate cannot be evaluated and the
// caller refuses the operation (5xx / PermissionDenied); it must NEVER be
// swallowed into an empty (silently-narrowed) or absent (wide-open) context.
type ReuseContextFetcher interface {
	GetReuseContext(ctx context.Context, gcid, tenantID string) (ReuseContext, error)
}

// AtomUseAuthorizer writes the ADR-229 D2 audit record: an idempotent
// AtomUsageGrant (scope TEST_SET, v1 free-license) minted at snapshot time
// for an allowed NON-OWNER reuse of a tenant-visible atom. Callers invoke it
// BEFORE serving the snapshot payload; a write failure refuses the snapshot
// (the audit record is not optional).
type AtomUseAuthorizer interface {
	AuthorizeTestSetUse(ctx context.Context, tenantID, granteeGCID, atomID string) error
}
