// service_crud_test.go — coverage for the Collection CRUD paths that WS-6a left
// untested (Update / RemoveAtom / List happy paths + their event emissions) and
// that ADR-233 now re-touches: Update carries the audience through the new
// value object, and every write path shares the ErrNotFound / ErrForbidden
// authorization spine that D8 hardened.
package collection_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// -----------------------------------------------------------------------------
// UpdateParams.ChangedFields
// -----------------------------------------------------------------------------

func TestChangedFields(t *testing.T) {
	t.Parallel()

	title, desc := "t", "d"
	aud := audience.Tenant

	if got := (collection.UpdateParams{}).ChangedFields(); len(got) != 0 {
		t.Errorf("empty params ChangedFields = %v; want none", got)
	}
	got := collection.UpdateParams{Title: &title, Description: &desc, Visibility: &aud}.ChangedFields()
	want := []string{"title", "description", "visibility"}
	if len(got) != len(want) {
		t.Fatalf("ChangedFields = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ChangedFields[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// -----------------------------------------------------------------------------
// Update
// -----------------------------------------------------------------------------

func TestService_Update_AppliesAndPublishesChangedFields(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)

	newTitle := "Renamed"
	newAud := audience.Tenant
	updated, err := f.svc.Update(context.Background(), collection.UpdateInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		Params:       collection.UpdateParams{Title: &newTitle, Visibility: &newAud},
		TraceParent:  "00-trace-span-01",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Renamed" {
		t.Errorf("Title = %q; want Renamed", updated.Title)
	}
	if updated.Visibility != audience.Tenant {
		t.Errorf("Visibility = %q; want tenant", updated.Visibility)
	}

	if len(f.pub.events) != 1 {
		t.Fatalf("events = %d; want 1 (updated.v1)", len(f.pub.events))
	}
	ev := f.pub.events[0]
	if ev.Type != collection.EventTypeCollectionUpdated {
		t.Errorf("event type = %q; want updated.v1", ev.Type)
	}
	if ev.Visibility != audience.Tenant {
		t.Errorf("event Visibility = %q; want tenant", ev.Visibility)
	}
	if len(ev.ChangedFields) != 2 || ev.ChangedFields[0] != "title" || ev.ChangedFields[1] != "visibility" {
		t.Errorf("event ChangedFields = %v; want [title visibility]", ev.ChangedFields)
	}
	if ev.TraceParent != "00-trace-span-01" {
		t.Errorf("TraceParent not propagated: %q", ev.TraceParent)
	}
}

func TestService_Update_MissingCollectionIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	title := "x"
	_, err := f.svc.Update(context.Background(), collection.UpdateInput{
		TenantID:     tenantA,
		CollectionID: "01970000-0000-7000-7000-00000000dead",
		OwnerGcid:    ownerA,
		Params:       collection.UpdateParams{Title: &title},
	})
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_Update_RejectsInvalidAudience(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	bogus := audience.Audience("PUBLIC") // the retired vocabulary must NOT be coerced
	_, err := f.svc.Update(context.Background(), collection.UpdateInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		Params:       collection.UpdateParams{Visibility: &bogus},
	})
	if err == nil {
		t.Fatalf("expected an error for the retired PUBLIC value")
	}
	if len(f.pub.events) != 0 {
		t.Errorf("published an event for a rejected update")
	}
}

// -----------------------------------------------------------------------------
// RemoveAtom
// -----------------------------------------------------------------------------

func TestService_RemoveAtom_RemovesAndPublishes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	a0, a1 := makeAtomID(t, 1), makeAtomID(t, 2)
	c := f.seed(t, audience.Private, a0, a1)

	updated, err := f.svc.RemoveAtom(context.Background(), collection.RemoveAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		AtomID:       a0,
	})
	if err != nil {
		t.Fatalf("RemoveAtom: %v", err)
	}
	if len(updated.Atoms) != 1 || updated.Atoms[0].AtomID != a1 {
		t.Fatalf("Atoms = %+v; want only %s", updated.Atoms, a1)
	}
	if updated.Atoms[0].Position != 0 {
		t.Errorf("Position = %d; want 0 (compacted)", updated.Atoms[0].Position)
	}
	if len(f.pub.events) != 1 {
		t.Fatalf("events = %d; want 1 (atom_removed.v1)", len(f.pub.events))
	}
	if f.pub.events[0].Type != collection.EventTypeCollectionAtomRemoved {
		t.Errorf("event type = %q; want atom_removed.v1", f.pub.events[0].Type)
	}
	if f.pub.events[0].AtomID != a0 {
		t.Errorf("event AtomID = %q; want %q", f.pub.events[0].AtomID, a0)
	}
}

func TestService_RemoveAtom_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	a0 := makeAtomID(t, 1)
	c := f.seed(t, audience.Tenant, a0)

	_, err := f.svc.RemoveAtom(context.Background(), collection.RemoveAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    strangerGCD,
		AtomID:       a0,
	})
	if !errors.Is(err, collection.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
}

func TestService_RemoveAtom_UnknownAtomIsNotInCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private, makeAtomID(t, 1))

	_, err := f.svc.RemoveAtom(context.Background(), collection.RemoveAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		AtomID:       makeAtomID(t, 99),
	})
	if !errors.Is(err, collection.ErrAtomNotInCollection) {
		t.Errorf("err = %v; want ErrAtomNotInCollection", err)
	}
}

// -----------------------------------------------------------------------------
// Delete / List
// -----------------------------------------------------------------------------

func TestService_Delete_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Tenant)

	err := f.svc.Delete(context.Background(), collection.DeleteInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    strangerGCD,
	})
	if !errors.Is(err, collection.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
}

func TestService_List_FiltersByOwnerAndAudience(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.seed(t, audience.Private)
	f.seed(t, audience.Tenant)

	all, err := f.svc.List(context.Background(), tenantA, collection.ListFilter{OwnerGcid: ownerA})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List(owner) = %d; want 2", len(all))
	}

	tenantOnly, err := f.svc.List(context.Background(), tenantA, collection.ListFilter{
		OwnerGcid:  ownerA,
		Visibility: audience.Tenant,
	})
	if err != nil {
		t.Fatalf("List(tenant): %v", err)
	}
	if len(tenantOnly) != 1 {
		t.Errorf("List(visibility=tenant) = %d; want 1", len(tenantOnly))
	}

	none, err := f.svc.List(context.Background(), tenantA, collection.ListFilter{OwnerGcid: strangerGCD})
	if err != nil {
		t.Fatalf("List(stranger): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("List(other owner) = %d; want 0", len(none))
	}
}

// -----------------------------------------------------------------------------
// Aggregate edges left uncovered by WS-6a
// -----------------------------------------------------------------------------

func TestApplyUpdate_ChangesDescription(t *testing.T) {
	t.Parallel()

	c := newC(t)
	desc := "  a fuller description  "
	if err := c.ApplyUpdate(collection.UpdateParams{Description: &desc}); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if c.Description != "a fuller description" {
		t.Errorf("Description = %q; want trimmed", c.Description)
	}
}

func TestApplyUpdate_RejectsOverlongDescription(t *testing.T) {
	t.Parallel()

	c := newC(t)
	long := make([]byte, collection.MaxDescriptionLength+1)
	for i := range long {
		long[i] = 'a'
	}
	desc := string(long)
	if err := c.ApplyUpdate(collection.UpdateParams{Description: &desc}); err == nil {
		t.Fatalf("expected an error for an over-long description")
	}
}

func TestNew_RejectsOverlongDescription(t *testing.T) {
	t.Parallel()

	long := make([]byte, collection.MaxDescriptionLength+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := collection.New(collection.NewParams{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "x", Description: string(long),
	})
	if err == nil {
		t.Fatalf("expected an error for an over-long description")
	}
}

func TestRemoveAtom_RejectsEmptyAtomIDAndSoftDeleted(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if err := c.RemoveAtom("  "); err == nil {
		t.Errorf("expected an error for an empty atom_id")
	}
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := c.RemoveAtom(makeAtomID(t, 1)); !errors.Is(err, collection.ErrCollectionDeleted) {
		t.Errorf("err = %v; want ErrCollectionDeleted", err)
	}
}

// A duplicate in the gate's output is noise, not an error — the aggregate
// de-duplicates rather than emitting the atom twice into the study list.
func TestConvertToStudyList_DeduplicatesGateOutput(t *testing.T) {
	t.Parallel()

	a0 := makeAtomID(t, 1)
	c := mkCollection(t, audience.Private, a0)

	got, err := c.ConvertToStudyList([]string{a0, a0})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d; want 1 (deduplicated)", len(got))
	}
}
