// Package collection is the personal-Collection aggregate of the Content
// Creation domain (WS-6a, 2026-05-26; ADR-233 WS-4, 2026-07-14).
//
// A Collection is a CURATION aggregate: it tracks ordered atom_id references
// to LearningAtoms but DOES NOT own atom content. LearningAtom remains the
// PRIMARY AGGREGATE ROOT of the platform per .claude/rules/ddd-enforcement.md
// (Aggregate Invariants #1) and CLAUDE.md §1 — "collections in other domains
// query atoms; they never own them".
//
// Cross-aggregate references (CollectionAtom.AtomID → LearningAtom.AtomID)
// are UUIDs without FK constraint per ddd-enforcement Aggregate Invariant #3.
// The CollectionService (see service.go) validates existence against the
// chora_creation.learning_atoms table via the reuse-consent gate's TENANT-SCOPED
// batch read (AtomReuseFactLookup) before the repository persists the membership
// row: an atom absent from that read does not resolve in the caller's tenant, and
// is refused as ErrAtomDoesNotExist. (An untenanted AtomLookup port used to do
// this and is deleted — ADR-233 D7; see collection/service.go for why.)
//
// Soft-delete only (deleted_at) per Aggregate Invariant #5 + #6. Cascade
// soft-delete CollectionAtom children with the parent Collection per
// Aggregate Invariant #8 — handled at the repository boundary.
//
// HARD INVARIANTS enforced by this package:
//  1. Title required (1..200 chars after trim).
//  2. Visibility is an audience.Audience ∈ {private, friends, tenant}; default
//     audience.Default (private). ADR-233 D7 — ONE audience vocabulary, shared
//     with learning_atoms.reuse_visibility.
//  3. At most MaxAtomsPerCollection (500) atoms per collection.
//  4. Cross-tenant atom inclusion is ALWAYS refused. (ADR-233 D7 retired the
//     BP-01 "unless PUBLIC" exception along with PUBLIC itself: PUBLIC could
//     never actually cross a tenant — RLS on `collections` is
//     `tenant_id = current_setting('chora.tenant_id')`, so the row was
//     invisible cross-tenant regardless — and cross-tenant distribution is a
//     locked *syndication* concern (ADR-229 fork (a)), never a visibility
//     level. One enum value was doing two incoherent jobs, neither of which
//     worked.)
//  5. No mutation of soft-deleted collections (returns ErrCollectionDeleted).
//  6. Append-only positions: a freshly added atom takes the next free slot;
//     removal compacts positions.
//  7. VisibleTo is the READ predicate (ADR-233 D8) — owner ∪ tenant ∪
//     (friends ∧ owner ∈ actor's friend set). Nothing else may read the row.
//
// HEXAGONAL: this package is dependency-free w.r.t. infrastructure. Only
// stdlib + google/uuid + the two pure-domain value packages (audience,
// reuseconsent) are imported. The repository / event-publisher ports live
// alongside in repository.go + events.go.
package collection

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
)

// -----------------------------------------------------------------------------
// Constants
// -----------------------------------------------------------------------------

// MaxAtomsPerCollection is the hard cap on the number of atoms a single
// Collection may contain. Per WS-6a invariant; downstream UI assumes this
// when sizing per-collection paginated reads.
const MaxAtomsPerCollection = 500

// MaxTitleLength is the cap on Collection.Title (per WS-6a).
const MaxTitleLength = 200

// MaxDescriptionLength is the cap on Collection.Description; chosen so the
// FE textarea can hold a healthy paragraph without bloating the row.
const MaxDescriptionLength = 2000

// -----------------------------------------------------------------------------
// Sentinel errors
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// Whose fault is it? (CHO-2175)
//
// Every error this package returns must answer one question before it reaches a
// caller: is this THEIR fault, or OURS? Before these three sentinels existed,
// writeCollectionError had a single default arm that answered "theirs" — 400
// CREATION_COLLECTION_INVALID — for a database fault, a context timeout, and an
// unwired consent port alike, with the raw internal error pasted into the
// message. That is how CHO-2173 hid for months: AddAtom had NEVER worked in
// prod, and every failure came back as a 400 saying the caller was holding it
// wrong, so no alert fired and no dashboard reddened.
//
// A 4xx is a promise that the caller can fix it by sending something different.
// Do not make that promise on our behalf.
// -----------------------------------------------------------------------------

// ErrInvalid — a genuinely-malformed request. THE ONLY class that may be a 4xx
// on the caller's own input. Explicit, so that the handler's default arm is free
// to be a loud 5xx: an unrecognised error is our bug, not the caller's typo.
var ErrInvalid = errors.New("invalid collection request")

// ErrRepository — a LOCAL persistence failure: chora_creation's own datastore
// could not be read or written (a dead pool, an RLS-blocked write, a 22P02 on a
// pooled connection). Maps to HTTP 500 CREATION_COLLECTION_REPO_ERROR.
//
// Deliberately NOT 502: that status is reserved for a failing UPSTREAM SERVICE
// (ErrSharingUnavailable). Collapsing the two would destroy the answer to "which
// dependency broke?" and send an operator to debug chora-sharing while our own
// database is the thing on fire.
//
// When this fires on a consent-gate read, the operation is REFUSED — a gate that
// cannot be evaluated must never degrade open.
var ErrRepository = errors.New("collection repository failure")

// ErrGateNotWired — a port the consent gate depends on is absent. A DEPLOYMENT
// defect, not a request defect. The service already refuses on it ("an unwired
// gate must never pass"); this sentinel makes the refusal legible as HTTP 500
// CREATION_GATE_NOT_WIRED so a misconfigured rollout pages a human instead of
// reading as a user typing badly.
var ErrGateNotWired = errors.New("a required consent-gate port is not wired")

// ErrNotFound is the canonical sentinel for a missing-or-soft-deleted
// Collection at the repository boundary. It is ALSO what a non-entitled read
// returns (ADR-233 D8) — a 403 would confirm the existence of a collection the
// caller may not see.
var ErrNotFound = errors.New("collection not found")

// ErrCollectionDeleted is returned by mutators when the aggregate is in a
// soft-deleted state.
var ErrCollectionDeleted = errors.New("collection is soft-deleted; cannot mutate")

// ErrDuplicateAtom is returned by AddAtom when the supplied atom_id is
// already a member of this collection.
var ErrDuplicateAtom = errors.New("atom already in collection")

// ErrAtomCapExceeded is returned by AddAtom when MaxAtomsPerCollection has
// already been reached.
var ErrAtomCapExceeded = errors.New("collection has reached the 500-atom cap")

// ErrAtomNotInCollection is returned by RemoveAtom when the supplied
// atom_id is not a member of this collection, and by ConvertToStudyList when
// an entitled id does not correspond to a curated membership row.
var ErrAtomNotInCollection = errors.New("atom not in collection")

// ErrCrossTenantAtomNotPermitted is returned by AddAtom when an atom owned by a
// different tenant is added to any collection. ADR-233 D7 retired the PUBLIC
// exception; the sentinel survives, the exception does not.
var ErrCrossTenantAtomNotPermitted = errors.New("cross-tenant atom inclusion is not permitted")

// ErrAtomDoesNotExist is returned by the domain service when the supplied
// atom_id does not resolve to any active learning_atoms row. Repository
// layer maps to HTTP 404 with a clear envelope.
var ErrAtomDoesNotExist = errors.New("referenced atom does not exist")

// ErrNoEntitledAtoms is returned by ConvertToStudyList when NOTHING survives the
// ADR-229 consent gate. ADR-233 D11: never emit an empty study list — that
// would be fabricating a success. The handler maps this to 409.
var ErrNoEntitledAtoms = errors.New("no entitled atoms in collection; nothing to convert")

// ErrAtomNotReusable is returned by the add-time gate (ADR-229 D4.4 / ADR-233
// D10b) when the author has not consented to the actor reusing this atom. A
// non-entitled atom cannot enter a collection at all.
var ErrAtomNotReusable = errors.New("atom is not reusable by this actor")

// -----------------------------------------------------------------------------
// Reuse-gate sentinels — chora-sharing refusals, translated AT THE ADAPTER
// (CHO-2174; the idiom CHO-2139 gave chora-delivery)
//
// Both halves of the gate cross a gRPC boundary into chora-sharing
// (ConsentContextFetcher → Sharing.GetReuseContext; CollectionUseAuthorizer →
// Sharing.AuthorizeAtomUse). gRPC MUST NOT leak past the client adapter — the
// domain has no business knowing the transport exists.
//
// Before CHO-2174 it did leak: a consent refusal arrived as an opaque
//
//	rpc error: code = FailedPrecondition desc = atom not published or withdrawn (412)
//
// fell through writeCollectionError's default arm, and reached A+ as
// 400 CREATION_COLLECTION_INVALID — the wrong status (a refusal is not a
// malformed request), the internal call chain exposed, and nothing the convert
// dialog could turn into a human sentence.
//
// The adapter now translates the upstream status into one of the four sentinels
// below; writeCollectionError maps each to a status + an actionable code.
// -----------------------------------------------------------------------------

// ErrAtomNotShareable — chora-sharing holds no reusable projection for the atom
// (upstream codes.FailedPrecondition: "atom not published or withdrawn (412)",
// or a reuse_visibility narrowed below 'tenant'). No license can be resolved, so
// no D2 audit grant can be minted, so the conversion is REFUSED.
//
// TERMINAL for this attempt — retrying changes nothing until the author
// (re-)publishes or re-widens the atom. Maps to HTTP 409
// CREATION_ATOM_NOT_SHAREABLE.
var ErrAtomNotShareable = errors.New("atom is not shareable: chora-sharing holds no reusable projection for it (unpublished, withdrawn, or narrowed)")

// ErrAtomReuseDenied — chora-sharing REFUSED this actor's reuse of the atom
// (upstream codes.PermissionDenied).
//
// Deliberately distinct from ErrAtomNotReusable, which is the LOCAL disjunct's
// verdict — this one is the upstream authority's. Collapsing them would destroy
// the answer to "which gate refused, and on whose facts?". Maps to HTTP 403
// CREATION_ATOM_REUSE_DENIED.
var ErrAtomReuseDenied = errors.New("atom reuse denied by chora-sharing for this actor")

// ErrSharingUnavailable — the reuse-consent gate could not be EVALUATED (the
// consent context) or RECORDED (the D2 grant) because chora-sharing is
// unreachable, failing, or unwired (upstream codes.Unavailable /
// DeadlineExceeded / Internal / Unimplemented, or a non-status transport error).
//
// FAIL LOUD: the operation is REFUSED, never degraded open. A conversion that
// proceeds without minting the grant is reuse nobody recorded — and a gate that
// degrades open is worse than no gate at all (the fail-loud contract stated at
// the top of reuse_context_client.go and in the collection Service package doc).
// Maps to HTTP 502 CREATION_SHARING_UNAVAILABLE — a retryable upstream failure,
// NOT a 4xx that blames the caller for an outage they did not cause.
var ErrSharingUnavailable = errors.New("chora-sharing is unavailable; the reuse-consent gate could not be evaluated (refusing rather than degrading the gate open)")

// ErrSharingRejectedRequest — chora-sharing rejected the request chora-creation
// CONSTRUCTED (upstream codes.InvalidArgument: an unspecified scope, a blank
// idempotency key, an unparseable id).
//
// That is a creation-side defect, never the caller's fault, so it must never
// surface as a 4xx blaming the learner — and it must not masquerade as an
// outage either, because retrying it will fail identically. Maps to HTTP 502
// CREATION_SHARING_GATE_ERROR, logged loudly server-side.
var ErrSharingRejectedRequest = errors.New("chora-sharing rejected the reuse-gate request chora-creation constructed (creation-side defect)")

// -----------------------------------------------------------------------------
// Aggregate root + child entity
// -----------------------------------------------------------------------------

// Collection is the curation aggregate root. Mutations go through methods
// on this type; never expose mutator helpers from sibling packages.
type Collection struct {
	CollectionID string            `json:"collection_id"`
	TenantID     string            `json:"tenant_id"`
	OwnerGcid    string            `json:"owner_gcid"`
	Title        string            `json:"title"`
	Description  string            `json:"description,omitempty"`
	Visibility   audience.Audience `json:"visibility"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`

	// Atoms is the ordered membership list (CollectionAtom child entities).
	// Accessed ONLY through Collection methods (AddAtom/RemoveAtom) per
	// ddd-enforcement Aggregate Invariant #2.
	Atoms []*CollectionAtom `json:"atoms"`
}

// CollectionAtom is a child entity: a single atom membership row. The
// AtomID is a cross-aggregate reference to LearningAtom; the membership
// row does NOT own atom content.
type CollectionAtom struct {
	CollectionID string    `json:"collection_id"`
	AtomID       string    `json:"atom_id"`
	Position     int       `json:"position"`
	AddedAt      time.Time `json:"added_at"`
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID    string
	OwnerGcid   string
	Title       string
	Description string
	// Visibility is optional; empty defaults to audience.Default (private).
	Visibility audience.Audience
}

// New constructs a fresh Collection. Returns an error if inputs violate
// aggregate invariants.
// Every rejection below wraps ErrInvalid (CHO-2175): these are the caller's to
// fix, and they are the ONLY errors in this package that may reach them as a
// 4xx. Anything else that goes wrong is ours, and must not masquerade as their
// mistake.
func New(p NewParams) (*Collection, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", ErrInvalid)
	}
	if strings.TrimSpace(p.OwnerGcid) == "" {
		return nil, fmt.Errorf("%w: owner_gcid is required", ErrInvalid)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if len(title) > MaxTitleLength {
		return nil, fmt.Errorf("%w: title too long: %d > %d", ErrInvalid, len(title), MaxTitleLength)
	}
	description := strings.TrimSpace(p.Description)
	if len(description) > MaxDescriptionLength {
		return nil, fmt.Errorf("%w: description too long: %d > %d", ErrInvalid, len(description), MaxDescriptionLength)
	}
	visibility := p.Visibility
	if visibility == "" {
		visibility = audience.Default
	}
	if !visibility.Valid() {
		return nil, fmt.Errorf("%w: invalid visibility: %q (want private|friends|tenant)", ErrInvalid, string(visibility))
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &Collection{
		CollectionID: id.String(),
		TenantID:     p.TenantID,
		OwnerGcid:    p.OwnerGcid,
		Title:        title,
		Description:  description,
		Visibility:   visibility,
		CreatedAt:    now,
		UpdatedAt:    now,
		Atoms:        nil,
	}, nil
}

// -----------------------------------------------------------------------------
// Mutators
// -----------------------------------------------------------------------------

// UpdateParams is the partial-update payload (PATCH semantics).
type UpdateParams struct {
	Title       *string
	Description *string
	Visibility  *audience.Audience
}

// ChangedFields returns the names of fields modified by ApplyUpdate. Used
// by the event publisher to populate CollectionUpdated.changed_fields.
func (p UpdateParams) ChangedFields() []string {
	out := make([]string, 0, 3)
	if p.Title != nil {
		out = append(out, "title")
	}
	if p.Description != nil {
		out = append(out, "description")
	}
	if p.Visibility != nil {
		out = append(out, "visibility")
	}
	return out
}

// ApplyUpdate mutates the collection per UpdateParams + bumps UpdatedAt.
// Refuses to touch a soft-deleted collection.
//
// Narrowing the audience (e.g. tenant → private) needs no atom scrub: with
// PUBLIC retired, cross-tenant membership is refused at EVERY audience, so no
// tightening can retroactively violate an invariant. (Pre-ADR-233 this method
// carried a caveat about PUBLIC → PRIVATE stranding cross-tenant atoms; the
// hole it guarded is now closed at the source.)
func (c *Collection) ApplyUpdate(p UpdateParams) error {
	if c.DeletedAt != nil {
		return ErrCollectionDeleted
	}
	// As in New: every rejection here wraps ErrInvalid (CHO-2175) — the caller's
	// to fix, and the only class permitted to reach them as a 4xx.
	if p.Title != nil {
		title := strings.TrimSpace(*p.Title)
		if title == "" {
			return fmt.Errorf("%w: title cannot be empty", ErrInvalid)
		}
		if len(title) > MaxTitleLength {
			return fmt.Errorf("%w: title too long: %d > %d", ErrInvalid, len(title), MaxTitleLength)
		}
		c.Title = title
	}
	if p.Description != nil {
		desc := strings.TrimSpace(*p.Description)
		if len(desc) > MaxDescriptionLength {
			return fmt.Errorf("%w: description too long: %d > %d", ErrInvalid, len(desc), MaxDescriptionLength)
		}
		c.Description = desc
	}
	if p.Visibility != nil {
		if !p.Visibility.Valid() {
			return fmt.Errorf("%w: invalid visibility: %q (want private|friends|tenant)", ErrInvalid, string(*p.Visibility))
		}
		c.Visibility = *p.Visibility
	}
	c.UpdatedAt = time.Now().UTC().Add(time.Nanosecond) // ensure strict monotonicity for tests
	return nil
}

// AtomContext is the per-add context the domain needs to validate
// cross-tenant inclusion. The adapter (repository / domain service)
// resolves AtomID → atom row and supplies TenantID. We deliberately do
// NOT pass the LearningAtom struct itself — the domain stays decoupled
// from atom-package internals.
type AtomContext struct {
	// TenantID is the tenant that owns the referenced LearningAtom.
	// MUST match Collection.TenantID — always (ADR-233 D7).
	TenantID string
}

// AddAtom appends an atom to the collection. Enforces the cap + duplicate
// + cross-tenant invariants. Position is the next free slot (len(c.Atoms)).
//
// The ADR-229 reuse-consent gate is composed at the SERVICE layer (see
// Service.AddAtom) — it needs the sharing mesh, which the aggregate must not
// know about.
func (c *Collection) AddAtom(atomID string, ctx AtomContext) error {
	if c.DeletedAt != nil {
		return ErrCollectionDeleted
	}
	atomID = strings.TrimSpace(atomID)
	if atomID == "" {
		return errors.New("atom_id is required")
	}
	if len(c.Atoms) >= MaxAtomsPerCollection {
		return fmt.Errorf("%w (cap=%d)", ErrAtomCapExceeded, MaxAtomsPerCollection)
	}
	for _, a := range c.Atoms {
		if a.AtomID == atomID {
			return fmt.Errorf("%w: %s", ErrDuplicateAtom, atomID)
		}
	}
	// ADR-233 D7: a cross-tenant atom is ALWAYS refused. The BP-01 "unless
	// PUBLIC" exception died with PUBLIC — see the package doc, invariant 4.
	if ctx.TenantID != "" && ctx.TenantID != c.TenantID {
		return fmt.Errorf("%w (collection tenant=%s, atom tenant=%s)",
			ErrCrossTenantAtomNotPermitted, c.TenantID, ctx.TenantID)
	}
	now := time.Now().UTC()
	c.Atoms = append(c.Atoms, &CollectionAtom{
		CollectionID: c.CollectionID,
		AtomID:       atomID,
		Position:     len(c.Atoms),
		AddedAt:      now,
	})
	c.UpdatedAt = now
	return nil
}

// RemoveAtom drops an atom from the collection + compacts positions.
// Returns ErrAtomNotInCollection if the atom is not a member.
func (c *Collection) RemoveAtom(atomID string) error {
	if c.DeletedAt != nil {
		return ErrCollectionDeleted
	}
	atomID = strings.TrimSpace(atomID)
	if atomID == "" {
		return errors.New("atom_id is required")
	}
	idx := -1
	for i, a := range c.Atoms {
		if a.AtomID == atomID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %s", ErrAtomNotInCollection, atomID)
	}
	c.Atoms = append(c.Atoms[:idx], c.Atoms[idx+1:]...)
	for i, a := range c.Atoms {
		a.Position = i
	}
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// SoftDelete sets DeletedAt. Idempotent: a no-op on already-deleted
// collections. Per ddd-enforcement Aggregate Invariant #5, this is a soft
// delete (hard delete reserved for crypto-shred / cold archive).
//
// Child CollectionAtom rows are cascade-soft-deleted at the repository
// boundary per Aggregate Invariant #8; the aggregate itself does NOT
// mutate the child rows here (preserves in-memory state for the event
// publisher to read final positions).
func (c *Collection) SoftDelete() error {
	if c.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
	c.UpdatedAt = now
	return nil
}

// IsActive returns true iff the collection is not soft-deleted.
func (c *Collection) IsActive() bool { return c.DeletedAt == nil }

// -----------------------------------------------------------------------------
// ADR-233 D8 — the read predicate  🔴 security fix
// -----------------------------------------------------------------------------

// VisibleTo reports whether actorGCID may READ this collection:
//
//	owner ∪ tenant ∪ (friends ∧ owner ∈ actor's friend set)
//
// friendGCIDs is the ACTOR's friend set (from chora-sharing's GetReuseContext,
// ADR-230) — so the `friends` leg asks "is the owner someone I am friends
// with?". Friendship is mutual, so the direction is immaterial today; asking it
// from the actor's side keeps the predicate honest if it ever stops being.
//
// This closes the horizontal-authz defect in ADR-233 Context §6: before this,
// nothing anywhere read `collections.visibility`, and any authenticated member
// of a tenant could read any collection in that tenant by ID — including
// another learner's PRIVATE collection. A `visibility` column that nothing
// reads is not a feature, it is a liability.
//
// Callers map `false` to ErrNotFound (404), NEVER ErrForbidden (403) — a 403
// confirms the collection exists to someone who may not see it.
//
// Note the tenant boundary itself is NOT checked here: the repository read is
// already tenant-scoped (RLS + an explicit tenant_id predicate), so by the time
// an aggregate exists, same-tenant is established.
func (c *Collection) VisibleTo(actorGCID string, friendGCIDs []string) bool {
	if actorGCID != "" && actorGCID == c.OwnerGcid {
		return true
	}
	switch c.Visibility {
	case audience.Tenant:
		return true
	case audience.Friends:
		for _, f := range friendGCIDs {
			if f != "" && f == c.OwnerGcid {
				return true
			}
		}
		return false
	default:
		// private — and any unrecognised value, which must fail CLOSED.
		return false
	}
}

// -----------------------------------------------------------------------------
// ADR-233 D9/D11 — conversion to a study list
// -----------------------------------------------------------------------------

// AtomIDsInOrder returns the ids of the collection's active atoms in curated
// (Position) order. This is the order the ADR-229 disjunct is evaluated over
// and the order the converted_to_study_list proto pins — a study list that
// silently reshuffled the learner's sequence would be its own small betrayal.
//
// Sorted by Position rather than trusting slice order: the aggregate can be
// rehydrated by any adapter, and Position is the authority.
func (c *Collection) AtomIDsInOrder() []string {
	if len(c.Atoms) == 0 {
		return nil
	}
	ordered := make([]*CollectionAtom, len(c.Atoms))
	copy(ordered, c.Atoms)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })

	out := make([]string, 0, len(ordered))
	for _, a := range ordered {
		out = append(out, a.AtomID)
	}
	return out
}

// ConvertToStudyList validates the gate's surviving atom set against this
// collection and returns it re-sorted into curated (Position) order, ready for
// the converted_to_study_list.v1 event.
//
// It MUTATES NOTHING — deliberately (ADR-233 D9). There is no `converted_at` /
// `last_study_list_event_id` stamp on Collection, because conversion is
// per-(collection, actor) state, not collection state: stamping it would mutate
// someone ELSE's aggregate the moment a non-owner converts a shared collection.
// Conversion state lives on the derived LearningPath in chora_consumption, which
// already carries created_at and (per D2) source_id. Net effect: chora-creation
// needs NO migration for the convert transition itself.
//
// Excluded atoms stay curated for the same reason (D11) — if the author
// re-widens the audience, a later convert picks them up.
//
// Refuses:
//   - a soft-deleted collection            → ErrCollectionDeleted
//   - an entitled id that is not a member   → ErrAtomNotInCollection (caller bug;
//     fail loud rather than smuggle a non-curated atom into a study list)
//   - zero survivors                        → ErrNoEntitledAtoms (never emit an
//     empty study list — that would be fabricating a success)
func (c *Collection) ConvertToStudyList(entitledAtomIDs []string) ([]string, error) {
	if c.DeletedAt != nil {
		return nil, ErrCollectionDeleted
	}

	// Membership index: atom_id → Position.
	positions := make(map[string]int, len(c.Atoms))
	for _, a := range c.Atoms {
		positions[a.AtomID] = a.Position
	}

	type entry struct {
		atomID string
		pos    int
	}
	entries := make([]entry, 0, len(entitledAtomIDs))
	seen := make(map[string]bool, len(entitledAtomIDs))
	for _, id := range entitledAtomIDs {
		pos, ok := positions[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s is not curated in collection %s", ErrAtomNotInCollection, id, c.CollectionID)
		}
		if seen[id] {
			continue // defensive: a duplicate in the gate's output is not an error, just noise
		}
		seen[id] = true
		entries = append(entries, entry{atomID: id, pos: pos})
	}

	if len(entries) == 0 {
		return nil, ErrNoEntitledAtoms
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].pos < entries[j].pos })
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.atomID)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Read filters
// -----------------------------------------------------------------------------

// ListFilter is the query filter for repository List operations.
type ListFilter struct {
	// OwnerGcid filters to collections owned by this GCID. Required by the
	// /api/v1/me/collections endpoint.
	OwnerGcid string
	// Visibility narrows by audience when non-empty.
	Visibility audience.Audience
	// Limit caps result size; 0 means default (100).
	Limit int
	// Offset is the pagination offset.
	Offset int
}
