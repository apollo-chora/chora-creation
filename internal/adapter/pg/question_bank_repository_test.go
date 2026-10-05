// question_bank_repository_test.go — TDD coverage of the pg.QuestionBankRepository
// adapter (W3.B.1, 2026-06-28).
//
// Reuses the stub TxQuerier surface defined in collection_repository_test.go
// (same package `pg`) to assert the SQL shape + RLS-tx wrap without a live
// Postgres. Cluster-level integration coverage is owned by integration_test.go.
package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

const testBankID = "01970000-0000-7000-7000-0000000000b1"

func TestQuestionBankRepository_Save_RunsInTenantTx(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)

	b, err := questionbank.New(questionbank.NewParams{
		TenantID:  testTenantID,
		OwnerGCID: testOwnerID,
		Name:      "Saved pool",
		Tags:      []string{"algebra"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if q.runCount != 1 {
		t.Errorf("RunInTenantTx count = %d; want 1", q.runCount)
	}
	if len(q.tenants) != 1 || q.tenants[0] != testTenantID {
		t.Errorf("tenants = %v; want [%s]", q.tenants, testTenantID)
	}
	hasInsert := false
	for _, s := range q.tx.execSQLs {
		if strings.Contains(s, "INSERT INTO question_banks") {
			hasInsert = true
		}
	}
	if !hasInsert {
		t.Errorf("expected INSERT INTO question_banks; got %v", q.tx.execSQLs)
	}
}

func TestQuestionBankRepository_Save_UpsertsActiveChildItems(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)

	b, _ := questionbank.New(questionbank.NewParams{TenantID: testTenantID, OwnerGCID: testOwnerID, Name: "X"})
	_ = b.AddItem("01970000-0000-7000-b000-000000000001")

	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	hasChildUpsert := false
	for _, s := range q.tx.execSQLs {
		if strings.Contains(s, "INSERT INTO question_bank_items") {
			hasChildUpsert = true
		}
	}
	if !hasChildUpsert {
		t.Errorf("expected INSERT INTO question_bank_items for active child; got %v", q.tx.execSQLs)
	}
}

func TestQuestionBankRepository_Save_CascadeSoftDeletesChildItemsOnDelete(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)

	b, _ := questionbank.New(questionbank.NewParams{TenantID: testTenantID, OwnerGCID: testOwnerID, Name: "X"})
	_ = b.AddItem("01970000-0000-7000-b000-000000000001")
	_ = b.Delete()

	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	hasChildSoftDelete := false
	hasChildResurrect := false
	for _, s := range q.tx.execSQLs {
		if strings.Contains(s, "UPDATE question_bank_items") && strings.Contains(s, "deleted_at") {
			hasChildSoftDelete = true
		}
		if strings.Contains(s, "INSERT INTO question_bank_items") {
			hasChildResurrect = true
		}
	}
	if !hasChildSoftDelete {
		t.Errorf("expected UPDATE question_bank_items ... deleted_at on cascade; got %v", q.tx.execSQLs)
	}
	if hasChildResurrect {
		t.Errorf("did not expect INSERT INTO question_bank_items on cascade; got %v", q.tx.execSQLs)
	}
}

func TestQuestionBankRepository_Get_NotFoundAppliesTenantTx(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)

	_, err := repo.Get(context.Background(), testTenantID, testBankID)
	if err == nil {
		t.Fatalf("expected ErrNotFound on empty stub")
	}
	if q.runCount != 1 {
		t.Errorf("RunInTenantTx count = %d; want 1", q.runCount)
	}
	if len(q.tenants) == 0 || q.tenants[0] != testTenantID {
		t.Errorf("tenants = %v; want first = %s", q.tenants, testTenantID)
	}
}

func TestQuestionBankRepository_List_RunsInTenantTx(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)

	out, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{
		OwnerGCID:  testOwnerID,
		Visibility: questionbank.VisibilityPrivate,
		Limit:      10,
		Offset:     5,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len(out) = %d; want 0 from empty stub", len(out))
	}
	if q.runCount != 1 {
		t.Errorf("RunInTenantTx count = %d; want 1", q.runCount)
	}
}

func TestQuestionBankRepository_Save_NilBank(t *testing.T) {
	t.Parallel()

	repo := NewQuestionBankRepositoryFromTxQuerier(newStubTxQuerier())
	if err := repo.Save(context.Background(), nil); err == nil {
		t.Fatalf("expected error on nil bank")
	}
}
