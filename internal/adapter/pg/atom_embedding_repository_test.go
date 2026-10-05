// atom_embedding_repository_test.go — Epic-1b W4: the atom_embeddings pgvector
// repo (writer + semantic search). SQL-shape + RLS-tx contract verified
// against in-memory stubs (no live pgvector); production atomicity under
// integration_test.go (build tag `integration`).
package pg

import (
	"context"
	"strings"
	"testing"
)

// aeStubRows is a minimal Rows for the Search scan path.
type aeStubRows struct {
	rows [][]any
	idx  int
}

func (m *aeStubRows) Next() bool {
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}
func (m *aeStubRows) Scan(dest ...any) error {
	row := m.rows[m.idx-1]
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			tgt2, _ := row[i].(string)
			*tgt = tgt2
		case *float32:
			tgt2, _ := row[i].(float32)
			*tgt = tgt2
		case *float64:
			tgt2, _ := row[i].(float64)
			*tgt = tgt2
		}
	}
	return nil
}
func (m *aeStubRows) Close() error { return nil }
func (m *aeStubRows) Err() error   { return nil }

type aeStubQuerier struct {
	execSQL   string
	execArgs  []any
	querySQL  string
	queryArgs []any
	rows      *aeStubRows
}

func (s *aeStubQuerier) Exec(_ context.Context, sql string, args ...any) error {
	s.execSQL = sql
	s.execArgs = args
	return nil
}
func (s *aeStubQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row { return nil }
func (s *aeStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	s.querySQL = sql
	s.queryArgs = args
	if s.rows == nil {
		return &aeStubRows{}, nil
	}
	return s.rows, nil
}

// aeStubTxQuerier records RunInTenantTx tenants and runs fn over the inner stub.
type aeStubTxQuerier struct {
	inner   aeStubQuerier
	tenants []string
}

func (s *aeStubTxQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, Tx) error) error {
	s.tenants = append(s.tenants, tenantID)
	return fn(ctx, &s.inner) // aeStubQuerier satisfies the Tx surface
}

func TestAtomEmbeddingUpsert_SQLShapeAndRLS(t *testing.T) {
	tq := &aeStubTxQuerier{}
	repo := NewAtomEmbeddingRepositoryFromTxQuerier(tq)

	err := repo.Upsert(context.Background(), "tnt-1", "atom-1", []float32{0.5, -0.25}, "text-embedding-004")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != "tnt-1" {
		t.Fatalf("RunInTenantTx tenants = %v, want [tnt-1] (RLS)", tq.tenants)
	}
	sql := tq.inner.execSQL
	if !strings.Contains(sql, "INSERT INTO atom_embeddings") || !strings.Contains(sql, "ON CONFLICT (atom_id) DO UPDATE") {
		t.Fatalf("upsert SQL = %q", sql)
	}
	if got := tq.inner.execArgs[2]; got != "[0.5,-0.25]" {
		t.Fatalf("vector literal = %v, want [0.5,-0.25]", got)
	}
}

func TestAtomEmbeddingUpsert_Validates(t *testing.T) {
	repo := NewAtomEmbeddingRepositoryFromTxQuerier(&aeStubTxQuerier{})
	if err := repo.Upsert(context.Background(), "tnt-1", "atom-1", nil, "m"); err == nil {
		t.Fatal("empty embedding must error")
	}
	if err := repo.Upsert(context.Background(), "", "atom-1", []float32{0.1}, "m"); err == nil {
		t.Fatal("missing tenant must error")
	}
}

func TestAtomEmbeddingSearch_PublishedOnlyOrderedByDistance(t *testing.T) {
	tq := &aeStubTxQuerier{inner: aeStubQuerier{rows: &aeStubRows{rows: [][]any{
		{"atom-1", float64(0.12), "Fractions I"},
		{"atom-2", float64(0.34), "Fractions II"},
	}}}}
	repo := NewAtomEmbeddingRepositoryFromTxQuerier(tq)

	matches, err := repo.Search(context.Background(), "tnt-1", []float32{0.5}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 || matches[0].AtomID != "atom-1" || matches[0].Title != "Fractions I" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].CosineDistance != 0.12 {
		t.Fatalf("distance = %v", matches[0].CosineDistance)
	}
	sql := tq.inner.querySQL
	for _, want := range []string{"atom_embeddings", "status = 'published'", "deleted_at IS NULL", "<=>", "LIMIT"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("search SQL missing %q: %s", want, sql)
		}
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != "tnt-1" {
		t.Fatalf("RLS tenants = %v", tq.tenants)
	}
}

func TestAtomEmbeddingSearch_LimitDefaultsAndCaps(t *testing.T) {
	tq := &aeStubTxQuerier{}
	repo := NewAtomEmbeddingRepositoryFromTxQuerier(tq)

	if _, err := repo.Search(context.Background(), "tnt-1", []float32{0.5}, 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := tq.inner.queryArgs[1]; got != 5 {
		t.Fatalf("default limit arg = %v, want 5", got)
	}
	if _, err := repo.Search(context.Background(), "tnt-1", []float32{0.5}, 99); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := tq.inner.queryArgs[1]; got != 20 {
		t.Fatalf("capped limit arg = %v, want 20", got)
	}
}

func TestAtomEmbeddingListMissing_SQLShape(t *testing.T) {
	tq := &aeStubTxQuerier{inner: aeStubQuerier{rows: &aeStubRows{rows: [][]any{
		{"atom-9", "Title 9", "Body 9", "Stem 9", "author-9"},
	}}}}
	repo := NewAtomEmbeddingRepositoryFromTxQuerier(tq)

	missing, err := repo.ListPublishedMissingEmbedding(context.Background(), "tnt-1", 50)
	if err != nil {
		t.Fatalf("ListPublishedMissingEmbedding: %v", err)
	}
	if len(missing) != 1 || missing[0].AtomID != "atom-9" || missing[0].Title != "Title 9" {
		t.Fatalf("missing = %+v", missing)
	}
	// G1' gap 1: the author gcid must ride the candidate row so the backfill
	// can attribute each embedding on the gateway ledger.
	if missing[0].AuthorGCID != "author-9" {
		t.Fatalf("AuthorGCID = %q, want author-9", missing[0].AuthorGCID)
	}
	sql := tq.inner.querySQL
	for _, want := range []string{"LEFT JOIN atom_embeddings", "status = 'published'", "e.atom_id IS NULL", "a.gcid"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing-SQL lacks %q: %s", want, sql)
		}
	}
}
