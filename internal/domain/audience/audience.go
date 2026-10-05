// Package audience is the ADR-233 D7 audience value object — the ONE
// vocabulary the platform uses to express "who may see / reuse this":
//
//	private | friends | tenant
//
// Before ADR-233 the same domain concept was expressed two incompatible ways:
// atoms carried `reuse_visibility TEXT CHECK (private|friends|tenant)` while
// collections carried a PG enum `collection_visibility (PRIVATE|
// TENANT_INTERNAL|PUBLIC)` — a different set, a different type and a different
// case for one idea. This package collapses them.
//
// PUBLIC is RETIRED. A collection could never actually be public: RLS on
// `collections` is `tenant_id = current_setting('chora.tenant_id')`, so the row
// is invisible cross-tenant regardless, and ADR-229 fork (a) locks "NO
// cross-tenant machinery — future = syndication ADR (clone + provenance), never
// RLS widening". A visibility level that silently means the same as the one
// below it is a trap for the next reader.
//
// HEXAGONAL: stdlib only. No infrastructure, no other domain packages.
package audience

import "fmt"

// Audience names who a thing is exposed to. The subject differs by usage —
// for a LearningAtom it governs REUSE (may you build with it), for a Collection
// it governs READ (may you see it) — but the vocabulary, and the friend set it
// resolves against, are the same.
type Audience string

const (
	// Private — owner only.
	Private Audience = "private"
	// Friends — owner + the owner's explicit friend set (ADR-230 friendships
	// minus blocks, resolved via chora-sharing's GetReuseContext).
	Friends Audience = "friends"
	// Tenant — every member of the owning tenant. This is the WIDEST audience
	// the platform has; there is nothing above it (see the PUBLIC note above).
	Tenant Audience = "tenant"
)

// Default is the consent-safe default (ADR-229 D1): absent an explicit choice,
// a thing is exposed to NOBODY. Widening is always a deliberate author act.
const Default = Private

// Valid reports whether a is one of the three canonical audiences. The retired
// collection vocabulary (PRIVATE / TENANT_INTERNAL / PUBLIC) does NOT validate —
// migration 0031 rewrites those rows, and a straggler must fail loud rather
// than be silently coerced.
func (a Audience) Valid() bool {
	switch a {
	case Private, Friends, Tenant:
		return true
	}
	return false
}

func (a Audience) String() string { return string(a) }

// ParseLegacyCollectionVisibility maps the retired `collection_visibility` enum
// onto the canonical vocabulary. Used by migration 0031 and by any lingering
// legacy payload.
//
// PUBLIC collapses to Tenant — not a downgrade, a truth-telling: RLS already
// capped it at the tenant, so PUBLIC and TENANT_INTERNAL were the same thing
// wearing different names.
func ParseLegacyCollectionVisibility(s string) (Audience, error) {
	switch s {
	case "PRIVATE":
		return Private, nil
	case "TENANT_INTERNAL":
		return Tenant, nil
	case "PUBLIC":
		return Tenant, nil
	}
	return "", fmt.Errorf("audience: unrecognised legacy collection visibility %q (want PRIVATE|TENANT_INTERNAL|PUBLIC)", s)
}
