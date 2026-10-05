// question_bank_repository_test.go — direct coverage of the in-memory
// QuestionBankRepository (W3.B.1). Black-box via the public questionbank API.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

const (
	ibTenantA = "01970000-0000-7000-8000-000000000001"
	ibTenantB = "01970000-0000-7000-8000-000000000002"
	ibOwnerA  = "01970000-0000-7000-9000-000000000001"
	ibOwnerB  = "01970000-0000-7000-9000-000000000002"
)

func mkBank(t *testing.T, tenant, owner, name string, vis questionbank.Visibility) *questionbank.QuestionBank {
	t.Helper()
	b, err := questionbank.New(questionbank.NewParams{TenantID: tenant, OwnerGCID: owner, Name: name, Visibility: vis})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestInmemQuestionBank_SaveGetRoundTrip(t *testing.T) {
	t.Parallel()

	repo := inmem.NewQuestionBankRepository()
	b := mkBank(t, ibTenantA, ibOwnerA, "pool", questionbank.VisibilityPrivate)
	if err := b.AddItem("01970000-0000-7000-b000-000000000001"); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := repo.Get(context.Background(), ibTenantA, b.QuestionBankID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "pool" || len(got.Items) != 1 {
		t.Errorf("got = %+v; want name=pool with 1 item", got)
	}
	// Mutating the returned clone must not affect repo state.
	got.Items[0].QuestionID = "tampered"
	again, _ := repo.Get(context.Background(), ibTenantA, b.QuestionBankID)
	if again.Items[0].QuestionID == "tampered" {
		t.Errorf("repo state mutated via returned reference")
	}
}

func TestInmemQuestionBank_GetWrongTenant(t *testing.T) {
	t.Parallel()

	repo := inmem.NewQuestionBankRepository()
	b := mkBank(t, ibTenantA, ibOwnerA, "x", questionbank.VisibilityPrivate)
	_ = repo.Save(context.Background(), b)
	if _, err := repo.Get(context.Background(), ibTenantB, b.QuestionBankID); err != questionbank.ErrNotFound {
		t.Errorf("err = %v; want ErrNotFound for wrong tenant", err)
	}
}

func TestInmemQuestionBank_GetSoftDeleted(t *testing.T) {
	t.Parallel()

	repo := inmem.NewQuestionBankRepository()
	b := mkBank(t, ibTenantA, ibOwnerA, "x", questionbank.VisibilityPrivate)
	_ = b.Delete()
	_ = repo.Save(context.Background(), b)
	if _, err := repo.Get(context.Background(), ibTenantA, b.QuestionBankID); err != questionbank.ErrNotFound {
		t.Errorf("err = %v; want ErrNotFound for soft-deleted", err)
	}
}

func TestInmemQuestionBank_GetVisible_SameTenantOnly(t *testing.T) {
	t.Parallel()

	repo := inmem.NewQuestionBankRepository()
	b := mkBank(t, ibTenantA, ibOwnerA, "x", questionbank.VisibilityTenantInternal)
	_ = repo.Save(context.Background(), b)
	if _, err := repo.GetVisible(context.Background(), ibTenantA, b.QuestionBankID); err != nil {
		t.Fatalf("GetVisible same-tenant: %v", err)
	}
	if _, err := repo.GetVisible(context.Background(), ibTenantB, b.QuestionBankID); err != questionbank.ErrNotFound {
		t.Errorf("GetVisible cross-tenant err = %v; want ErrNotFound", err)
	}
}

func TestInmemQuestionBank_ListFiltersAndPaginates(t *testing.T) {
	t.Parallel()

	repo := inmem.NewQuestionBankRepository()
	// 3 owned by A (1 TENANT_INTERNAL), 1 owned by B, 1 soft-deleted.
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "a1", questionbank.VisibilityPrivate))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "a2", questionbank.VisibilityTenantInternal))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "a3", questionbank.VisibilityPrivate))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerB, "b1", questionbank.VisibilityPrivate))
	del := mkBank(t, ibTenantA, ibOwnerA, "gone", questionbank.VisibilityPrivate)
	_ = del.Delete()
	_ = repo.Save(context.Background(), del)

	// Owner filter → 3 active for ownerA.
	mine, err := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mine) != 3 {
		t.Errorf("ownerA active banks = %d; want 3", len(mine))
	}

	// Visibility filter.
	internal, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{
		OwnerGCID: ibOwnerA, Visibility: questionbank.VisibilityTenantInternal,
	})
	if len(internal) != 1 || internal[0].Name != "a2" {
		t.Errorf("TENANT_INTERNAL filter = %+v; want 1 'a2'", internal)
	}

	// Pagination: limit 2 of ownerA's 3.
	page, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA, Limit: 2})
	if len(page) != 2 {
		t.Errorf("paginated len = %d; want 2", len(page))
	}

	// Offset within range: limit 2, offset 2 of 3 → 1 row.
	rest, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA, Limit: 2, Offset: 2})
	if len(rest) != 1 {
		t.Errorf("paginated offset=2 len = %d; want 1", len(rest))
	}

	// Offset beyond the result set is clamped → empty page.
	beyond, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA, Limit: 2, Offset: 99})
	if len(beyond) != 0 {
		t.Errorf("paginated offset=99 len = %d; want 0", len(beyond))
	}

	// Wrong tenant → none.
	none, _ := repo.List(context.Background(), ibTenantB, questionbank.ListFilter{})
	if len(none) != 0 {
		t.Errorf("cross-tenant list = %d; want 0", len(none))
	}
}
