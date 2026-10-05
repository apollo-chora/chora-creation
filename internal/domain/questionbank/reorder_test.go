// reorder_test.go — RED-first unit tests for QuestionBank.ReorderItems
// (CHO-1899 Wave 2). Pure-domain: builds the aggregate via New + AddItem and
// asserts the permutation invariant + position reassignment.
package questionbank_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

func mkBankWithItems(t *testing.T, ids ...string) *questionbank.QuestionBank {
	t.Helper()
	b, err := questionbank.New(questionbank.NewParams{
		TenantID:  "01970000-0000-7000-8000-000000000001",
		OwnerGCID: "01970000-0000-7000-9000-000000000001",
		Name:      "Reorder pool",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, id := range ids {
		if err := b.AddItem(id); err != nil {
			t.Fatalf("AddItem(%s): %v", id, err)
		}
	}
	return b
}

func TestReorderItems_PermutationReassignsPositions(t *testing.T) {
	a, b, c := "q-aaa", "q-bbb", "q-ccc"
	bank := mkBankWithItems(t, a, b, c) // positions a=0,b=1,c=2

	if err := bank.ReorderItems([]string{c, a, b}); err != nil {
		t.Fatalf("ReorderItems: %v", err)
	}
	want := []struct {
		id  string
		pos int
	}{{c, 0}, {a, 1}, {b, 2}}
	if len(bank.Items) != 3 {
		t.Fatalf("items len = %d; want 3", len(bank.Items))
	}
	for i, w := range want {
		if bank.Items[i].QuestionID != w.id || bank.Items[i].Position != w.pos {
			t.Errorf("item[%d] = {%s,pos=%d}; want {%s,pos=%d}",
				i, bank.Items[i].QuestionID, bank.Items[i].Position, w.id, w.pos)
		}
	}
}

func TestReorderItems_RejectsNonPermutation(t *testing.T) {
	a, b, c := "q-aaa", "q-bbb", "q-ccc"
	cases := map[string][]string{
		"wrong count (missing)": {a, b},
		"unknown id":            {a, b, "q-zzz"},
		"duplicate id":          {a, a, b},
		"extra id":              {a, b, c, "q-zzz"},
	}
	for name, ids := range cases {
		bank := mkBankWithItems(t, a, b, c)
		if err := bank.ReorderItems(ids); !errors.Is(err, questionbank.ErrReorderMismatch) {
			t.Errorf("%s: err = %v; want ErrReorderMismatch", name, err)
		}
	}
}

func TestReorderItems_RejectsDeletedBank(t *testing.T) {
	bank := mkBankWithItems(t, "q-aaa", "q-bbb")
	_ = bank.Delete()
	if err := bank.ReorderItems([]string{"q-bbb", "q-aaa"}); !errors.Is(err, questionbank.ErrQuestionBankDeleted) {
		t.Errorf("err = %v; want ErrQuestionBankDeleted", err)
	}
}
