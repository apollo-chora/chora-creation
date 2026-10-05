package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

const (
	mediaTenantA = "01970000-0000-7000-8000-000000000001"
	mediaTenantB = "01970000-0000-7000-8000-000000000002"
	mediaGcidA   = "01970000-0000-7000-9000-000000000001"
	mediaAtomA   = "01970000-0000-7000-aaaa-000000000001"
	mediaAtomB   = "01970000-0000-7000-aaaa-000000000002"
)

func newAsset(t *testing.T, tenant, atomID string) *media.MediaAsset {
	t.Helper()
	a, err := media.New(media.NewParams{
		TenantID: tenant, Gcid: mediaGcidA, AtomID: atomID,
		Filename: "x.mp4", ContentType: "video/mp4", SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestMediaRepository_SaveAndGet(t *testing.T) {
	t.Parallel()

	repo := inmem.NewMediaRepository()
	a := newAsset(t, mediaTenantA, mediaAtomA)
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := repo.Get(context.Background(), mediaTenantA, a.AssetID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AssetID != a.AssetID {
		t.Errorf("AssetID mismatch")
	}
}

func TestMediaRepository_Get_TenantIsolation(t *testing.T) {
	t.Parallel()

	repo := inmem.NewMediaRepository()
	a := newAsset(t, mediaTenantA, mediaAtomA)
	_ = repo.Save(context.Background(), a)
	_, err := repo.Get(context.Background(), mediaTenantB, a.AssetID)
	if err != media.ErrNotFound {
		t.Errorf("cross-tenant Get: err=%v; want ErrNotFound", err)
	}
}

func TestMediaRepository_Get_HidesSoftDeleted(t *testing.T) {
	t.Parallel()

	repo := inmem.NewMediaRepository()
	a := newAsset(t, mediaTenantA, mediaAtomA)
	_ = a.SoftDelete()
	_ = repo.Save(context.Background(), a)
	if _, err := repo.Get(context.Background(), mediaTenantA, a.AssetID); err != media.ErrNotFound {
		t.Errorf("Get of soft-deleted: err=%v; want ErrNotFound", err)
	}
}

func TestMediaRepository_Get_NotFound(t *testing.T) {
	t.Parallel()

	repo := inmem.NewMediaRepository()
	if _, err := repo.Get(context.Background(), mediaTenantA, "01970000-0000-7000-zzzz-000000000099"); err != media.ErrNotFound {
		t.Errorf("Get nonexistent: err=%v; want ErrNotFound", err)
	}
}

func TestMediaRepository_ListByAtom(t *testing.T) {
	t.Parallel()

	repo := inmem.NewMediaRepository()
	a1 := newAsset(t, mediaTenantA, mediaAtomA)
	a2 := newAsset(t, mediaTenantA, mediaAtomA)
	a3 := newAsset(t, mediaTenantA, mediaAtomB)
	a4 := newAsset(t, mediaTenantB, mediaAtomA) // different tenant
	a5 := newAsset(t, mediaTenantA, mediaAtomA)
	_ = a5.SoftDelete()

	for _, a := range []*media.MediaAsset{a1, a2, a3, a4, a5} {
		_ = repo.Save(context.Background(), a)
	}
	got, err := repo.ListByAtom(context.Background(), mediaTenantA, mediaAtomA)
	if err != nil {
		t.Fatalf("ListByAtom: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d; want 2 (a1, a2)", len(got))
	}
}
