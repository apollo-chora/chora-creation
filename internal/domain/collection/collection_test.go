// Package collection_test exercises the Collection aggregate invariants per
// WS-6a (Personal Collections BE aggregate, 2026-05-26).
//
// TDD RED phase: these tests assert the aggregate constructor, mutators,
// invariants (title required, atom-cap 500, visibility-gated cross-tenant
// rules), and soft-delete semantics before any implementation lands. They
// stay pure-domain — no infrastructure imports.
//
// Source-of-truth: WS-6a prompt + .claude/rules/ddd-enforcement.md +
// CLAUDE.md §1 (LearningAtom primary aggregate root; collections query
// atoms, never own them) + §5 (BP-01 learner-ownership).
package collection_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	ownerA  = "01970000-0000-7000-9000-000000000001"
)

// -----------------------------------------------------------------------------
// Constructor invariants
// -----------------------------------------------------------------------------

func TestNew_AssignsUUIDv7AndDraftDefaults(t *testing.T) {
	t.Parallel()

	c, err := collection.New(collection.NewParams{
		TenantID:  tenantA,
		OwnerGcid: ownerA,
		Title:     "Photosynthesis essentials",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.CollectionID == "" {
		t.Errorf("CollectionID empty; want UUIDv7")
	}
	if c.Visibility != audience.Private {
		t.Errorf("default visibility = %q; want PRIVATE", c.Visibility)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", c.CreatedAt, c.UpdatedAt)
	}
	if c.DeletedAt != nil {
		t.Errorf("DeletedAt = %v; want nil", c.DeletedAt)
	}
	if len(c.Atoms) != 0 {
		t.Errorf("Atoms = %v; want empty", c.Atoms)
	}
}

func TestNew_RejectsMissingTenantID(t *testing.T) {
	t.Parallel()

	_, err := collection.New(collection.NewParams{
		OwnerGcid: ownerA,
		Title:     "x",
	})
	if err == nil {
		t.Fatalf("expected error for missing tenant_id")
	}
}

func TestNew_RejectsMissingOwnerGcid(t *testing.T) {
	t.Parallel()

	_, err := collection.New(collection.NewParams{
		TenantID: tenantA,
		Title:    "x",
	})
	if err == nil {
		t.Fatalf("expected error for missing owner_gcid")
	}
}

func TestNew_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()

	_, err := collection.New(collection.NewParams{
		TenantID:  tenantA,
		OwnerGcid: ownerA,
		Title:     "   ",
	})
	if err == nil {
		t.Fatalf("expected error for empty title")
	}
}

func TestNew_RejectsTitleOver200Chars(t *testing.T) {
	t.Parallel()

	_, err := collection.New(collection.NewParams{
		TenantID:  tenantA,
		OwnerGcid: ownerA,
		Title:     strings.Repeat("a", 201),
	})
	if err == nil {
		t.Fatalf("expected error for >200-char title")
	}
}

func TestNew_AcceptsExplicitVisibility(t *testing.T) {
	t.Parallel()

	c, err := collection.New(collection.NewParams{
		TenantID:   tenantA,
		OwnerGcid:  ownerA,
		Title:      "Public set",
		Visibility: audience.Tenant,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Visibility != audience.Tenant {
		t.Errorf("Visibility = %q; want PUBLIC", c.Visibility)
	}
}

func TestNew_RejectsInvalidVisibility(t *testing.T) {
	t.Parallel()

	_, err := collection.New(collection.NewParams{
		TenantID:   tenantA,
		OwnerGcid:  ownerA,
		Title:      "x",
		Visibility: audience.Audience("nonsense"),
	})
	if err == nil {
		t.Fatalf("expected error for invalid visibility")
	}
}

// -----------------------------------------------------------------------------
// ApplyUpdate semantics
// -----------------------------------------------------------------------------

func TestApplyUpdate_ChangesTitleAndBumpsUpdatedAt(t *testing.T) {
	t.Parallel()

	c := newC(t)
	prev := c.UpdatedAt

	newTitle := "Updated title"
	if err := c.ApplyUpdate(collection.UpdateParams{Title: &newTitle}); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if c.Title != "Updated title" {
		t.Errorf("Title = %q; want updated", c.Title)
	}
	if !c.UpdatedAt.After(prev) {
		t.Errorf("UpdatedAt = %v; want after %v", c.UpdatedAt, prev)
	}
}

func TestApplyUpdate_RejectsBlankTitle(t *testing.T) {
	t.Parallel()

	c := newC(t)
	blank := "   "
	if err := c.ApplyUpdate(collection.UpdateParams{Title: &blank}); err == nil {
		t.Fatalf("expected error for blank title")
	}
}

func TestApplyUpdate_ChangesVisibility(t *testing.T) {
	t.Parallel()

	c := newC(t)
	v := audience.Tenant
	if err := c.ApplyUpdate(collection.UpdateParams{Visibility: &v}); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if c.Visibility != audience.Tenant {
		t.Errorf("Visibility = %q; want TENANT_INTERNAL", c.Visibility)
	}
}

func TestApplyUpdate_RejectsInvalidVisibility(t *testing.T) {
	t.Parallel()

	c := newC(t)
	v := audience.Audience("nonsense")
	if err := c.ApplyUpdate(collection.UpdateParams{Visibility: &v}); err == nil {
		t.Fatalf("expected error for invalid visibility")
	}
}

func TestApplyUpdate_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	title := "x"
	if err := c.ApplyUpdate(collection.UpdateParams{Title: &title}); err == nil {
		t.Fatalf("expected error updating soft-deleted collection")
	}
}

// -----------------------------------------------------------------------------
// AddAtom invariants (atom-cap 500, visibility-gated cross-tenant)
// -----------------------------------------------------------------------------

func TestAddAtom_AppendsAtCorrectPosition(t *testing.T) {
	t.Parallel()

	c := newC(t)
	atomID := "01970000-0000-7000-a000-000000000001"

	if err := c.AddAtom(atomID, collection.AtomContext{TenantID: tenantA}); err != nil {
		t.Fatalf("AddAtom: %v", err)
	}
	if len(c.Atoms) != 1 {
		t.Fatalf("len(Atoms) = %d; want 1", len(c.Atoms))
	}
	if c.Atoms[0].AtomID != atomID {
		t.Errorf("Atoms[0].AtomID = %q; want %q", c.Atoms[0].AtomID, atomID)
	}
	if c.Atoms[0].Position != 0 {
		t.Errorf("Atoms[0].Position = %d; want 0", c.Atoms[0].Position)
	}
}

func TestAddAtom_AssignsIncrementingPositions(t *testing.T) {
	t.Parallel()

	c := newC(t)
	for i := 0; i < 5; i++ {
		id := makeAtomID(t, i)
		if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
			t.Fatalf("AddAtom[%d]: %v", i, err)
		}
		if c.Atoms[i].Position != i {
			t.Errorf("Atoms[%d].Position = %d; want %d", i, c.Atoms[i].Position, i)
		}
	}
}

func TestAddAtom_RejectsDuplicateAtomID(t *testing.T) {
	t.Parallel()

	c := newC(t)
	id := "01970000-0000-7000-a000-000000000001"
	if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
		t.Fatalf("AddAtom: %v", err)
	}
	err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA})
	if err == nil {
		t.Fatalf("expected duplicate-atom error")
	}
	if !errors.Is(err, collection.ErrDuplicateAtom) {
		t.Errorf("err = %v; want ErrDuplicateAtom", err)
	}
}

func TestAddAtom_EnforcesAtomCap500(t *testing.T) {
	t.Parallel()

	c := newC(t)
	for i := 0; i < collection.MaxAtomsPerCollection; i++ {
		id := makeAtomID(t, i)
		if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
			t.Fatalf("AddAtom[%d]: %v", i, err)
		}
	}
	overflow := makeAtomID(t, collection.MaxAtomsPerCollection)
	err := c.AddAtom(overflow, collection.AtomContext{TenantID: tenantA})
	if err == nil {
		t.Fatalf("expected cap-exceeded error at %d", collection.MaxAtomsPerCollection+1)
	}
	if !errors.Is(err, collection.ErrAtomCapExceeded) {
		t.Errorf("err = %v; want ErrAtomCapExceeded", err)
	}
}

func TestAddAtom_RejectsCrossTenantWhenNotPublic(t *testing.T) {
	t.Parallel()

	// Private collection. Atom resolves to tenant B (cross-tenant). Reject.
	c := newC(t) // visibility=PRIVATE by default
	atomID := "01970000-0000-7000-a000-000000000001"
	err := c.AddAtom(atomID, collection.AtomContext{TenantID: tenantB})
	if err == nil {
		t.Fatalf("expected cross-tenant error on PRIVATE collection")
	}
	if !errors.Is(err, collection.ErrCrossTenantAtomNotPermitted) {
		t.Errorf("err = %v; want ErrCrossTenantAtomNotPermitted", err)
	}
}

// ADR-233 D7 — the BP-01 PUBLIC exception is RETIRED along with PUBLIC itself.
//
// This test previously asserted the inverse ("AllowsCrossTenantOnPublic"): that a
// PUBLIC collection MAY hold another tenant's atoms. That rule never worked —
// RLS on `collections` is `tenant_id = current_setting('chora.tenant_id')`, so a
// PUBLIC collection was never readable cross-tenant anyway, and cross-tenant
// distribution is a locked *syndication* concern (ADR-229 fork (a)), never a
// visibility level. The rule is now inverted, at EVERY audience: a cross-tenant
// atom is always refused.
func TestAddAtom_RefusesCrossTenantAtEveryAudience(t *testing.T) {
	t.Parallel()

	for _, aud := range []audience.Audience{audience.Private, audience.Friends, audience.Tenant} {
		t.Run(string(aud), func(t *testing.T) {
			t.Parallel()

			c, err := collection.New(collection.NewParams{
				TenantID:   tenantA,
				OwnerGcid:  ownerA,
				Title:      "T",
				Visibility: aud,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			atomID := "01970000-0000-7000-a000-000000000001"
			err = c.AddAtom(atomID, collection.AtomContext{TenantID: tenantB})
			if !errors.Is(err, collection.ErrCrossTenantAtomNotPermitted) {
				t.Errorf("audience=%s: err = %v; want ErrCrossTenantAtomNotPermitted", aud, err)
			}
			if len(c.Atoms) != 0 {
				t.Errorf("audience=%s: cross-tenant atom was admitted", aud)
			}
		})
	}
}

func TestAddAtom_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	err := c.AddAtom("01970000-0000-7000-a000-000000000001",
		collection.AtomContext{TenantID: tenantA})
	if err == nil {
		t.Fatalf("expected error adding atom to soft-deleted collection")
	}
}

func TestAddAtom_RejectsEmptyAtomID(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if err := c.AddAtom("", collection.AtomContext{TenantID: tenantA}); err == nil {
		t.Fatalf("expected error for empty atom_id")
	}
}

// -----------------------------------------------------------------------------
// RemoveAtom invariants
// -----------------------------------------------------------------------------

func TestRemoveAtom_RemovesAndReindexesPositions(t *testing.T) {
	t.Parallel()

	c := newC(t)
	ids := []string{
		makeAtomID(t, 0),
		makeAtomID(t, 1),
		makeAtomID(t, 2),
	}
	for _, id := range ids {
		if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
			t.Fatalf("AddAtom: %v", err)
		}
	}
	// Remove middle atom.
	if err := c.RemoveAtom(ids[1]); err != nil {
		t.Fatalf("RemoveAtom: %v", err)
	}
	if len(c.Atoms) != 2 {
		t.Fatalf("len(Atoms) = %d; want 2", len(c.Atoms))
	}
	if c.Atoms[0].AtomID != ids[0] || c.Atoms[0].Position != 0 {
		t.Errorf("Atoms[0] = %+v; want id=%s pos=0", c.Atoms[0], ids[0])
	}
	if c.Atoms[1].AtomID != ids[2] || c.Atoms[1].Position != 1 {
		t.Errorf("Atoms[1] = %+v; want id=%s pos=1", c.Atoms[1], ids[2])
	}
}

func TestRemoveAtom_ReturnsNotFoundForMissingAtom(t *testing.T) {
	t.Parallel()

	c := newC(t)
	err := c.RemoveAtom("01970000-0000-7000-a000-000000000999")
	if err == nil {
		t.Fatalf("expected error for missing atom_id")
	}
	if !errors.Is(err, collection.ErrAtomNotInCollection) {
		t.Errorf("err = %v; want ErrAtomNotInCollection", err)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete semantics (idempotent; cascade-flagged)
// -----------------------------------------------------------------------------

func TestSoftDelete_SetsDeletedAtAndIsIdempotent(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if c.DeletedAt == nil {
		t.Fatalf("DeletedAt nil after SoftDelete")
	}
	first := *c.DeletedAt

	// Second call must be no-op (idempotent).
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete (2nd): %v", err)
	}
	if !c.DeletedAt.Equal(first) {
		t.Errorf("DeletedAt mutated on 2nd SoftDelete (was %v, now %v)", first, *c.DeletedAt)
	}
}

func TestIsActive_ReflectsSoftDelete(t *testing.T) {
	t.Parallel()

	c := newC(t)
	if !c.IsActive() {
		t.Errorf("IsActive = false; want true on fresh collection")
	}
	_ = c.SoftDelete()
	if c.IsActive() {
		t.Errorf("IsActive = true; want false after SoftDelete")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newC(t *testing.T) *collection.Collection {
	t.Helper()
	c, err := collection.New(collection.NewParams{
		TenantID:  tenantA,
		OwnerGcid: ownerA,
		Title:     "Test collection",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func makeAtomID(t *testing.T, i int) string {
	t.Helper()
	// Synthetic UUID-ish — collection's domain doesn't validate UUID format,
	// only repository / handler do. We use a deterministic pattern so the
	// duplicate-detection assertion stays meaningful.
	//
	// Init MUST be all-zeros: the right-to-left decimal fill leaves higher
	// positions untouched, so a trailing "1" would make i=0 (loop skipped) and
	// i=1 collide on "...0001" — which the new ErrDuplicateAtom check then
	// (correctly) rejects, breaking the position/cap tests with distinct i.
	suffix := []byte("0000000000000000")
	idx := len(suffix) - 1
	v := i
	for idx >= 0 && v > 0 {
		suffix[idx] = byte('0' + v%10)
		v /= 10
		idx--
	}
	return "01970000-0000-7000-a000-" + string(suffix)
}
