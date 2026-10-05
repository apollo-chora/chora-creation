// Package inmem_test exercises the in-memory CollectionRepository implementation.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

const (
	collectionTenantA = "01970000-0000-7000-8000-000000000001"
	collectionTenantB = "01970000-0000-7000-8000-000000000002"
	collectionGcidA   = "01970000-0000-7000-9000-000000000001"
	collectionGcidB   = "01970000-0000-7000-9000-000000000002"
)

func mustNewCollection(t *testing.T, tenant, gcid, title string, visibility audience.Audience) *collection.Collection {
	t.Helper()
	c, err := collection.New(collection.NewParams{
		TenantID: tenant, OwnerGcid: gcid, Title: title, Visibility: visibility,
	})
	if err != nil {
		t.Fatalf("collection.New: %v", err)
	}
	return c
}

func TestCollectionRepository_SaveAndGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewCollectionRepository()
	c := mustNewCollection(t, collectionTenantA, collectionGcidA, "x", "")
	if err := c.AddAtom("atom-1", collection.AtomContext{TenantID: collectionTenantA}); err != nil {
		t.Fatalf("AddAtom: %v", err)
	}
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := r.Get(ctx, collectionTenantA, c.CollectionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CollectionID != c.CollectionID {
		t.Errorf("CollectionID mismatch: got %s want %s", got.CollectionID, c.CollectionID)
	}
	if len(got.Atoms) != 1 || got.Atoms[0].AtomID != "atom-1" {
		t.Errorf("Atoms round-trip: got %+v", got.Atoms)
	}

	// Mutation defence: mutating the returned membership list must not leak
	// into the repo's stored aggregate.
	got.Atoms[0].AtomID = "MUTATED"
	got2, _ := r.Get(ctx, collectionTenantA, c.CollectionID)
	if got2.Atoms[0].AtomID == "MUTATED" {
		t.Errorf("repo leaked CollectionAtom (must clone)")
	}
}

func TestCollectionRepository_Get_NotFound(t *testing.T) {
	t.Parallel()

	r := inmem.NewCollectionRepository()
	if _, err := r.Get(context.Background(), collectionTenantA, "nope"); err != collection.ErrNotFound {
		t.Errorf("Get nonexistent: err=%v; want ErrNotFound", err)
	}
}

func TestCollectionRepository_Get_TenantIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewCollectionRepository()
	c := mustNewCollection(t, collectionTenantA, collectionGcidA, "x", "")
	_ = r.Save(ctx, c)
	if _, err := r.Get(ctx, collectionTenantB, c.CollectionID); err != collection.ErrNotFound {
		t.Errorf("cross-tenant Get: err=%v; want ErrNotFound", err)
	}
}

func TestCollectionRepository_Get_HidesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewCollectionRepository()
	c := mustNewCollection(t, collectionTenantA, collectionGcidA, "x", "")
	if err := c.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	_ = r.Save(ctx, c)
	if _, err := r.Get(ctx, collectionTenantA, c.CollectionID); err != collection.ErrNotFound {
		t.Errorf("Get of soft-deleted: err=%v; want ErrNotFound", err)
	}
}

func TestCollectionRepository_List_FiltersAndHidesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewCollectionRepository()
	c1 := mustNewCollection(t, collectionTenantA, collectionGcidA, "a1", audience.Private)
	c2 := mustNewCollection(t, collectionTenantA, collectionGcidB, "a2", audience.Tenant)
	c3 := mustNewCollection(t, collectionTenantA, collectionGcidA, "a3-deleted", audience.Private)
	_ = c3.SoftDelete()
	c4 := mustNewCollection(t, collectionTenantB, collectionGcidA, "b1", audience.Private)

	for _, c := range []*collection.Collection{c1, c2, c3, c4} {
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := r.List(ctx, collectionTenantA, collection.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List size = %d; want 2 (c1, c2)", len(got))
	}

	got, _ = r.List(ctx, collectionTenantA, collection.ListFilter{OwnerGcid: collectionGcidA})
	if len(got) != 1 || got[0].CollectionID != c1.CollectionID {
		t.Errorf("List(OwnerGcid=A) = %+v; want c1 only", got)
	}

	got, _ = r.List(ctx, collectionTenantA, collection.ListFilter{Visibility: audience.Tenant})
	if len(got) != 1 || got[0].CollectionID != c2.CollectionID {
		t.Errorf("List(Visibility=tenant) = %+v; want c2 only", got)
	}
}

func TestCollectionRepository_List_LimitOffset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewCollectionRepository()
	for _, title := range []string{"c1", "c2", "c3"} {
		c := mustNewCollection(t, collectionTenantA, collectionGcidA, title, "")
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := r.List(ctx, collectionTenantA, collection.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List(limit=2) size = %d; want 2", len(got))
	}

	got, _ = r.List(ctx, collectionTenantA, collection.ListFilter{Limit: 2, Offset: 2})
	if len(got) != 1 {
		t.Errorf("List(limit=2 offset=2) size = %d; want 1", len(got))
	}

	got, _ = r.List(ctx, collectionTenantA, collection.ListFilter{Limit: 2, Offset: 99})
	if len(got) != 0 {
		t.Errorf("List(offset beyond end) size = %d; want 0", len(got))
	}
}
