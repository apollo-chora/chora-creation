// question_bank_list_facets_test.go — RED-first SQL-shape coverage for the
// listMyQuestionBanks facets (CHO-1899 BE gap #2): name-search → name ILIKE,
// tag-filter → TEXT[] array-overlap (&&, GIN array_ops), and whitelisted sort.
// Asserts against the shared stubTx (querySQLs) without a live Postgres.
package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

func lastListSQL(t *testing.T, q *stubTxQuerier) string {
	t.Helper()
	if len(q.tx.querySQLs) == 0 {
		t.Fatalf("no Query() SQL captured")
	}
	return q.tx.querySQLs[0]
}

func TestQuestionBankRepository_List_QFilterEmitsNameILIKE(t *testing.T) {
	t.Parallel()
	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)
	if _, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{Q: "alg"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sql := lastListSQL(t, q); !strings.Contains(sql, "name ILIKE $") {
		t.Errorf("expected `name ILIKE $N` predicate; got %q", sql)
	}
}

func TestQuestionBankRepository_List_TagFilterEmitsArrayOverlap(t *testing.T) {
	t.Parallel()
	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)
	if _, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{Tags: []string{"exam-2026"}}); err != nil {
		t.Fatalf("List: %v", err)
	}
	sql := lastListSQL(t, q)
	// TEXT[] overlap (OR-any), GIN-indexable via array_ops — NOT the JSONB `?|`.
	if !strings.Contains(sql, "tags && $") {
		t.Errorf("expected `tags && $N` array-overlap predicate; got %q", sql)
	}
	if !strings.Contains(sql, "::text[]") {
		t.Errorf("expected `::text[]` cast on the tag param; got %q", sql)
	}
}

func TestQuestionBankRepository_List_SortWhitelistedColumn(t *testing.T) {
	t.Parallel()
	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)
	if _, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{
		Sorts: []questionbank.Sort{{Field: "name", Direction: "asc"}},
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sql := lastListSQL(t, q); !strings.Contains(sql, "ORDER BY name ASC, question_bank_id DESC") {
		t.Errorf("expected ORDER BY name ASC, question_bank_id DESC; got %q", sql)
	}
}

func TestQuestionBankRepository_List_DefaultSortCreatedAtDescWithTiebreak(t *testing.T) {
	t.Parallel()
	q := newStubTxQuerier()
	repo := NewQuestionBankRepositoryFromTxQuerier(q)
	if _, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sql := lastListSQL(t, q); !strings.Contains(sql, "ORDER BY created_at DESC, question_bank_id DESC") {
		t.Errorf("expected default ORDER BY created_at DESC, question_bank_id DESC; got %q", sql)
	}
}
