// question_bank_list_facets_test.go — RED-first coverage that the in-memory
// QuestionBank repo honours the listMyQuestionBanks facets (CHO-1899 BE gap #2):
// name-search (Q), tag-filter (Tags, OR-any), and sort. The inmem repo backs the
// HTTP handler test suite, so it MUST mirror the pg adapter's filter/sort
// behaviour or the end-to-end handler tests would be vacuous.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

func TestInmemQuestionBank_List_QFilter(t *testing.T) {
	t.Parallel()
	repo := inmem.NewQuestionBankRepository()
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "Algebra pool", questionbank.VisibilityPrivate))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "Geometry pool", questionbank.VisibilityPrivate))

	got, err := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA, Q: "alg"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Algebra pool" {
		t.Errorf("Q=alg → %+v; want 1 'Algebra pool'", got)
	}
}

func TestInmemQuestionBank_List_TagFilterORAny(t *testing.T) {
	t.Parallel()
	repo := inmem.NewQuestionBankRepository()
	a := mkBank(t, ibTenantA, ibOwnerA, "a", questionbank.VisibilityPrivate)
	a.Tags = []string{"exam-2026"}
	b := mkBank(t, ibTenantA, ibOwnerA, "b", questionbank.VisibilityPrivate)
	b.Tags = []string{"warmup"}
	_ = repo.Save(context.Background(), a)
	_ = repo.Save(context.Background(), b)

	got, err := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{
		OwnerGCID: ibOwnerA, Tags: []string{"exam-2026", "review"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("Tags=[exam-2026,review] → %+v; want 1 'a'", got)
	}
}

func TestInmemQuestionBank_List_SortNameAsc(t *testing.T) {
	t.Parallel()
	repo := inmem.NewQuestionBankRepository()
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "Charlie", questionbank.VisibilityPrivate))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "Alpha", questionbank.VisibilityPrivate))
	_ = repo.Save(context.Background(), mkBank(t, ibTenantA, ibOwnerA, "Bravo", questionbank.VisibilityPrivate))

	got, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{
		OwnerGCID: ibOwnerA, Sorts: []questionbank.Sort{{Field: "name", Direction: "asc"}},
	})
	if len(got) != 3 || got[0].Name != "Alpha" || got[1].Name != "Bravo" || got[2].Name != "Charlie" {
		t.Errorf("sort name:asc → %v; want Alpha,Bravo,Charlie", names(got))
	}
}

func TestInmemQuestionBank_List_DefaultSortCreatedAtDesc(t *testing.T) {
	t.Parallel()
	repo := inmem.NewQuestionBankRepository()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	old := mkBank(t, ibTenantA, ibOwnerA, "old", questionbank.VisibilityPrivate)
	old.CreatedAt = base
	mid := mkBank(t, ibTenantA, ibOwnerA, "mid", questionbank.VisibilityPrivate)
	mid.CreatedAt = base.Add(time.Hour)
	fresh := mkBank(t, ibTenantA, ibOwnerA, "new", questionbank.VisibilityPrivate)
	fresh.CreatedAt = base.Add(2 * time.Hour)
	_ = repo.Save(context.Background(), old)
	_ = repo.Save(context.Background(), fresh)
	_ = repo.Save(context.Background(), mid)

	got, _ := repo.List(context.Background(), ibTenantA, questionbank.ListFilter{OwnerGCID: ibOwnerA})
	if len(got) != 3 || got[0].Name != "new" || got[1].Name != "mid" || got[2].Name != "old" {
		t.Errorf("default sort → %v; want new,mid,old (created_at DESC)", names(got))
	}
}

func names(banks []*questionbank.QuestionBank) []string {
	out := make([]string, len(banks))
	for i, b := range banks {
		out[i] = b.Name
	}
	return out
}
