// study_list_test.go — ADR-233 aggregate slice (WS-4), RED-first.
//
// Covers the three new aggregate behaviours:
//
//   - D8  VisibleTo            — the read predicate that closes the live
//     horizontal-authz defect (Context §6). owner ∪ tenant ∪
//     (friends ∧ owner ∈ actor's friend set).
//   - D9  ConvertToStudyList   — validate + re-sort into curated order,
//     mutating NOTHING (conversion is per-(collection, actor) state).
//   - D11 AtomIDsInOrder       — the curated order the disjunct is evaluated
//     over and the proto pins.
//
// Plus the converted_to_study_list.v1 event builder.
package collection_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

const (
	friendGCID  = "01970000-0000-7000-9000-0000000000f1"
	strangerGCD = "01970000-0000-7000-9000-0000000000f2"
)

// mkCollection builds a collection with the supplied audience + atoms already
// curated (positions 0..n-1).
func mkCollection(t *testing.T, aud audience.Audience, atomIDs ...string) *collection.Collection {
	t.Helper()
	c, err := collection.New(collection.NewParams{
		TenantID:   tenantA,
		OwnerGcid:  ownerA,
		Title:      "Study set",
		Visibility: aud,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, id := range atomIDs {
		if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
			t.Fatalf("AddAtom(%s): %v", id, err)
		}
	}
	return c
}

// -----------------------------------------------------------------------------
// D8 — VisibleTo (the security fix)
// -----------------------------------------------------------------------------

func TestVisibleTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		aud     audience.Audience
		actor   string
		friends []string
		want    bool
	}{
		{"owner sees own private", audience.Private, ownerA, nil, true},
		{"stranger CANNOT see private", audience.Private, strangerGCD, nil, false},
		{"friend CANNOT see private", audience.Private, friendGCID, []string{ownerA}, false},
		{"stranger sees tenant", audience.Tenant, strangerGCD, nil, true},
		{"owner sees own tenant", audience.Tenant, ownerA, nil, true},
		{"friend sees friends-audience", audience.Friends, friendGCID, []string{ownerA}, true},
		{"non-friend CANNOT see friends-audience", audience.Friends, strangerGCD, []string{"someone-else"}, false},
		{"empty friend set CANNOT see friends-audience", audience.Friends, strangerGCD, nil, false},
		{"owner sees own friends-audience with no friends", audience.Friends, ownerA, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := mkCollection(t, tc.aud)
			if got := c.VisibleTo(tc.actor, tc.friends); got != tc.want {
				t.Errorf("VisibleTo(%q, %v) with audience=%s = %v; want %v",
					tc.actor, tc.friends, tc.aud, got, tc.want)
			}
		})
	}
}

// An anonymous / identity-less caller must never see a non-tenant collection.
func TestVisibleTo_EmptyActorFailsClosedOnPrivate(t *testing.T) {
	t.Parallel()

	c := mkCollection(t, audience.Private)
	if c.VisibleTo("", nil) {
		t.Errorf("VisibleTo(\"\") = true on a private collection; want false (fail-closed)")
	}
}

// -----------------------------------------------------------------------------
// AtomIDsInOrder
// -----------------------------------------------------------------------------

func TestAtomIDsInOrder_ReturnsCuratedOrder(t *testing.T) {
	t.Parallel()

	a0, a1, a2 := makeAtomID(t, 1), makeAtomID(t, 2), makeAtomID(t, 3)
	c := mkCollection(t, audience.Private, a0, a1, a2)

	got := c.AtomIDsInOrder()
	want := []string{a0, a1, a2}
	if len(got) != len(want) {
		t.Fatalf("AtomIDsInOrder() len = %d; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AtomIDsInOrder()[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// The slice may arrive from the repo in any order; Position is the truth.
func TestAtomIDsInOrder_SortsByPositionNotSliceOrder(t *testing.T) {
	t.Parallel()

	a0, a1, a2 := makeAtomID(t, 1), makeAtomID(t, 2), makeAtomID(t, 3)
	c := mkCollection(t, audience.Private, a0, a1, a2)
	// Scramble the in-memory slice while leaving Position intact.
	c.Atoms[0], c.Atoms[2] = c.Atoms[2], c.Atoms[0]

	got := c.AtomIDsInOrder()
	want := []string{a0, a1, a2}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AtomIDsInOrder()[%d] = %q; want %q (Position order, not slice order)", i, got[i], want[i])
		}
	}
}

func TestAtomIDsInOrder_EmptyCollection(t *testing.T) {
	t.Parallel()

	c := mkCollection(t, audience.Private)
	if got := c.AtomIDsInOrder(); len(got) != 0 {
		t.Errorf("AtomIDsInOrder() = %v; want empty", got)
	}
}

// -----------------------------------------------------------------------------
// D9 / D11 — ConvertToStudyList
// -----------------------------------------------------------------------------

func TestConvertToStudyList_ResortsEntitledIntoCuratedOrder(t *testing.T) {
	t.Parallel()

	a0, a1, a2 := makeAtomID(t, 1), makeAtomID(t, 2), makeAtomID(t, 3)
	c := mkCollection(t, audience.Private, a0, a1, a2)

	// The gate returns them out of order (a2 before a0); the aggregate MUST
	// re-sort into the curated Position order — the proto pins ordering.
	got, err := c.ConvertToStudyList([]string{a2, a0})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}
	want := []string{a0, a2}
	if len(got) != len(want) {
		t.Fatalf("len = %d; want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// ADR-233 D9: "No converted_at / last_study_list_event_id stamp on Collection."
// Conversion is per-(collection, actor) state — stamping it would mutate
// someone else's aggregate the moment a non-owner converts a shared collection.
func TestConvertToStudyList_MutatesNothing(t *testing.T) {
	t.Parallel()

	a0, a1 := makeAtomID(t, 1), makeAtomID(t, 2)
	c := mkCollection(t, audience.Private, a0, a1)

	beforeUpdatedAt := c.UpdatedAt
	beforeAtoms := len(c.Atoms)
	beforePos0 := c.Atoms[0].Position

	if _, err := c.ConvertToStudyList([]string{a0}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if !c.UpdatedAt.Equal(beforeUpdatedAt) {
		t.Errorf("UpdatedAt mutated: %v → %v; conversion must not touch the aggregate", beforeUpdatedAt, c.UpdatedAt)
	}
	if len(c.Atoms) != beforeAtoms {
		t.Errorf("Atoms mutated: %d → %d; the excluded atom must stay curated", beforeAtoms, len(c.Atoms))
	}
	if c.Atoms[0].Position != beforePos0 {
		t.Errorf("Position mutated: %d → %d", beforePos0, c.Atoms[0].Position)
	}
	if c.DeletedAt != nil {
		t.Errorf("DeletedAt set by conversion")
	}
}

// The returned slice must not alias the aggregate's internal state.
func TestConvertToStudyList_ReturnedSliceIsIndependent(t *testing.T) {
	t.Parallel()

	a0, a1 := makeAtomID(t, 1), makeAtomID(t, 2)
	c := mkCollection(t, audience.Private, a0, a1)

	got, err := c.ConvertToStudyList([]string{a0, a1})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}
	got[0] = "mutated"
	if c.Atoms[0].AtomID != a0 {
		t.Errorf("mutating the returned slice corrupted the aggregate: %q", c.Atoms[0].AtomID)
	}
}

func TestConvertToStudyList_RefusesSoftDeleted(t *testing.T) {
	t.Parallel()

	a0 := makeAtomID(t, 1)
	c := mkCollection(t, audience.Private, a0)
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	_, err := c.ConvertToStudyList([]string{a0})
	if !errors.Is(err, collection.ErrCollectionDeleted) {
		t.Errorf("err = %v; want ErrCollectionDeleted", err)
	}
}

// Zero survivors is a REFUSAL, never an empty study list — emitting one would
// be fabricating a success (ADR-233 D11 → 409).
func TestConvertToStudyList_ZeroEntitledIsErrNoEntitledAtoms(t *testing.T) {
	t.Parallel()

	c := mkCollection(t, audience.Private, makeAtomID(t, 1))
	_, err := c.ConvertToStudyList(nil)
	if !errors.Is(err, collection.ErrNoEntitledAtoms) {
		t.Errorf("err = %v; want ErrNoEntitledAtoms", err)
	}
}

func TestConvertToStudyList_EmptyCollectionIsErrNoEntitledAtoms(t *testing.T) {
	t.Parallel()

	c := mkCollection(t, audience.Private)
	_, err := c.ConvertToStudyList([]string{})
	if !errors.Is(err, collection.ErrNoEntitledAtoms) {
		t.Errorf("err = %v; want ErrNoEntitledAtoms", err)
	}
}

// An entitled id that is not a member is a caller bug — fail loud rather than
// smuggle a non-curated atom into the learner's study list.
func TestConvertToStudyList_RejectsNonMemberAtom(t *testing.T) {
	t.Parallel()

	a0 := makeAtomID(t, 1)
	stranger := makeAtomID(t, 99)
	c := mkCollection(t, audience.Private, a0)

	_, err := c.ConvertToStudyList([]string{a0, stranger})
	if err == nil {
		t.Fatalf("expected an error for a non-member atom id")
	}
	if !errors.Is(err, collection.ErrAtomNotInCollection) {
		t.Errorf("err = %v; want ErrAtomNotInCollection", err)
	}
}

// -----------------------------------------------------------------------------
// Event builder — chora.creation.collection.converted_to_study_list.v1
// -----------------------------------------------------------------------------

func TestNewCollectionConvertedToStudyListEvent(t *testing.T) {
	t.Parallel()

	a0, a1 := makeAtomID(t, 1), makeAtomID(t, 2)
	c := mkCollection(t, audience.Private, a0, a1)
	atomIDs := []string{a0, a1}

	// The ACTOR is an explicit input (CHO-2165). Deliberately pass someone who is
	// NOT the collection's owner: passing the owner is what made the old signature
	// look correct — with actor == owner there was no input that could tell
	// "whose list is this" apart from "whose collection is this", so the identity
	// fields were never asserted and stamped the wrong learner for years.
	const forker = "01970000-0000-7000-9000-0000000000f3"
	ev := collection.NewCollectionConvertedToStudyListEvent(c, forker, atomIDs, "00-trace-span-01", "vendor=x")

	if ev.Gcid != forker {
		t.Errorf("Gcid = %q; want the ACTOR %q — the derived study list belongs to whoever forked it", ev.Gcid, forker)
	}
	if ev.OwnerGcid != forker {
		t.Errorf("OwnerGcid = %q; want the ACTOR %q — consumption reads this into learning_paths.owner_gcid", ev.OwnerGcid, forker)
	}

	if ev.Type != collection.EventTypeCollectionConvertedToStudyList {
		t.Errorf("Type = %q; want converted_to_study_list.v1", ev.Type)
	}
	if ev.Type != "chora.creation.collection.converted_to_study_list.v1" {
		t.Errorf("topic = %q; want the canonical chora.creation.collection.converted_to_study_list.v1", ev.Type)
	}
	if ev.EventID == "" {
		t.Fatalf("EventID empty; want UUIDv7")
	}
	// The proto comment: study_list_event_id "= the event_id of this event by
	// default; consumption dedups on this".
	if ev.StudyListEventID != ev.EventID {
		t.Errorf("StudyListEventID = %q; want == EventID (%q)", ev.StudyListEventID, ev.EventID)
	}
	if ev.IdempotencyKey != ev.EventID {
		t.Errorf("IdempotencyKey = %q; want == EventID", ev.IdempotencyKey)
	}
	if ev.CollectionID != c.CollectionID {
		t.Errorf("CollectionID = %q; want %q", ev.CollectionID, c.CollectionID)
	}
	// NB: this used to assert Gcid == OwnerGcid == ownerA (the COLLECTION's owner)
	// and was green — because the constructor stamped both from the Collection.
	// That is the bug, encoded as the spec. The identity fields are asserted
	// against the ACTOR above; here only the tenant is a collection property.
	if ev.TenantID != tenantA {
		t.Errorf("TenantID = %q; want %q", ev.TenantID, tenantA)
	}
	if len(ev.AtomIDs) != 2 || ev.AtomIDs[0] != a0 || ev.AtomIDs[1] != a1 {
		t.Errorf("AtomIDs = %v; want %v", ev.AtomIDs, atomIDs)
	}
	if ev.TraceParent != "00-trace-span-01" || ev.TraceState != "vendor=x" {
		t.Errorf("trace context not propagated: %+v", ev)
	}
	if ev.SourceProject == "" || ev.SourceService == "" || ev.SchemaVersion == 0 {
		t.Errorf("envelope mandatory fields missing: %+v", ev)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.OccurredAt); err != nil {
		t.Errorf("OccurredAt %q not RFC3339Nano: %v", ev.OccurredAt, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.PublishedAt); err != nil {
		t.Errorf("PublishedAt %q not RFC3339Nano: %v", ev.PublishedAt, err)
	}

	// The event must not alias the caller's slice.
	atomIDs[0] = "mutated"
	if ev.AtomIDs[0] == "mutated" {
		t.Errorf("event AtomIDs aliases the caller's slice")
	}
}
