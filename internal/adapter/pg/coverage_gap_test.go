// coverage_gap_test.go — DB-free unit coverage of the pg adapter seams that
// the TDD suites left dark: runtime.go guards/helpers, the repository
// constructors (production wiring), and the happy-path scan/load helpers of
// CollectionRepository + QuestionBankRepository (Get/List/GetVisible/
// ListItemsPage).
//
// Uses its own substring-matching stub Tx (covTx) because the SQL under test
// is dynamically assembled (List / ListItemsPage build WHERE + ORDER BY at
// runtime), so the exact-SQL-keyed stubTx in collection_repository_test.go
// cannot preload rows for it.
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// -----------------------------------------------------------------------------
// Test doubles (substring-matching)
// -----------------------------------------------------------------------------

// covAssign copies v into dest — same idiom as assignPtr in
// collection_repository_test.go, plus the *[]string leg scanQuestionBank needs.
func covAssign(dest, v any) error {
	switch d := dest.(type) {
	case *string:
		if s, ok := v.(string); ok {
			*d = s
			return nil
		}
	case *int:
		if i, ok := v.(int); ok {
			*d = i
			return nil
		}
	case *bool:
		if b, ok := v.(bool); ok {
			*d = b
			return nil
		}
	case *time.Time:
		if t, ok := v.(time.Time); ok {
			*d = t
			return nil
		}
	case **time.Time:
		if v == nil {
			*d = nil
			return nil
		}
		if t, ok := v.(*time.Time); ok {
			*d = t
			return nil
		}
	case *[]string:
		if s, ok := v.([]string); ok {
			*d = s
			return nil
		}
	}
	return errors.New("covAssign: unsupported type")
}

type covRow struct {
	values []any
	noRows bool
}

func (r *covRow) Scan(dest ...any) error {
	if r.noRows {
		return ErrNoRows
	}
	if len(r.values) != len(dest) {
		return errors.New("covRow.Scan: arity mismatch")
	}
	for i, v := range r.values {
		if err := covAssign(dest[i], v); err != nil {
			return err
		}
	}
	return nil
}

type covRows struct {
	rows [][]any
	cur  int
	err  error
}

func (r *covRows) Next() bool {
	if r.cur < len(r.rows) {
		r.cur++
		return true
	}
	return false
}
func (r *covRows) Scan(dest ...any) error {
	row := r.rows[r.cur-1]
	if len(row) != len(dest) {
		return errors.New("covRows.Scan: arity mismatch")
	}
	for i, v := range row {
		if err := covAssign(dest[i], v); err != nil {
			return err
		}
	}
	return nil
}
func (r *covRows) Close() error { return nil }
func (r *covRows) Err() error   { return r.err }

// covTx resolves QueryRow/Query results by SQL substring so tests can pin
// rows for dynamically assembled statements.
type covTx struct {
	queryRowFn func(sql string) ([]any, bool)
	queryFn    func(sql string) ([][]any, bool)
	execErr    error
	execSQLs   []string
}

func (t *covTx) Exec(_ context.Context, sql string, _ ...any) error {
	t.execSQLs = append(t.execSQLs, sql)
	return t.execErr
}
func (t *covTx) QueryRow(_ context.Context, sql string, _ ...any) Row {
	if t.queryRowFn != nil {
		if values, ok := t.queryRowFn(sql); ok {
			return &covRow{values: values}
		}
	}
	return &covRow{noRows: true}
}
func (t *covTx) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	if t.queryFn != nil {
		if rows, ok := t.queryFn(sql); ok {
			return &covRows{rows: rows}, nil
		}
	}
	return &covRows{}, nil
}

type covTxQuerier struct {
	tx      *covTx
	tenants []string
	runErr  error
}

func (q *covTxQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, Tx) error) error {
	q.tenants = append(q.tenants, tenantID)
	if q.runErr != nil {
		return q.runErr
	}
	return fn(ctx, q.tx)
}

// -----------------------------------------------------------------------------
// Constructors — production wiring shape (nil pool is fine; no SQL runs)
// -----------------------------------------------------------------------------

func TestConstructors_ProductionWiringShape(t *testing.T) {
	t.Parallel()

	pq := NewPgxPoolQuerier(nil)
	if pq == nil {
		t.Fatal("NewPgxPoolQuerier = nil")
	}
	if pq.Pool() != nil {
		t.Errorf("Pool() = %v; want nil for nil-pool wrap", pq.Pool())
	}

	if NewAtomRepository(pq) == nil {
		t.Error("NewAtomRepository = nil")
	}
	if NewAtomRepositoryFromTx(nil) == nil {
		t.Error("NewAtomRepositoryFromTx = nil")
	}
	if NewCollectionRepository(pq) == nil {
		t.Error("NewCollectionRepository = nil")
	}
	if NewQuestionBankRepository(pq) == nil {
		t.Error("NewQuestionBankRepository = nil")
	}
	if NewQuestionJobRepository(pq) == nil {
		t.Error("NewQuestionJobRepository = nil")
	}
	if NewQuestionRepository(pq) == nil {
		t.Error("NewQuestionRepository = nil")
	}
	if NewAiAssistJobsRepository(pq) == nil {
		t.Error("NewAiAssistJobsRepository = nil")
	}
	if NewAtomEmbeddingRepository(pq) == nil {
		t.Error("NewAtomEmbeddingRepository = nil")
	}
	if NewSourceChunkRepository(pq) == nil {
		t.Error("NewSourceChunkRepository = nil")
	}
	if NewTopicRepository(pq) == nil {
		t.Error("NewTopicRepository = nil")
	}
	if NewAtomTagSource(pq) == nil {
		t.Error("NewAtomTagSource = nil")
	}
}

// -----------------------------------------------------------------------------
// runtime.go — tx guards + identifier/role validators (no live DB)
// -----------------------------------------------------------------------------

func TestRunTenantTx_Guards(t *testing.T) {
	t.Parallel()

	noop := func(context.Context, pgx.Tx) error { return nil }
	if err := RunTenantTx(context.Background(), nil, testTenantID, noop); err == nil ||
		!strings.Contains(err.Error(), "nil pool") {
		t.Errorf("nil-pool err = %v; want nil pool error", err)
	}
	if err := RunTenantTx(context.Background(), &pgxpool.Pool{}, "", noop); err == nil {
		t.Error("empty tenant err = nil; want error")
	}
}

func TestRunInTenantTx_Guards(t *testing.T) {
	t.Parallel()

	pq := NewPgxPoolQuerier(nil)
	noop := func(context.Context, Tx) error { return nil }

	if err := pq.RunInTenantTx(context.Background(), "", noop); err == nil {
		t.Error("empty tenant err = nil; want error")
	}
	if err := pq.RunInTenantTx(context.Background(), "tenant';DROP TABLE collections;--", noop); err == nil {
		t.Error("injection-shaped tenant err = nil; want error")
	}
	// Valid tenant shape + nil pool → nil-pool guard.
	if err := pq.RunInTenantTx(context.Background(), testTenantID, noop); err == nil ||
		!strings.Contains(err.Error(), "nil pool") {
		t.Errorf("nil-pool err = %v; want nil pool error", err)
	}
}

func TestValidateTenantID(t *testing.T) {
	t.Parallel()

	if err := validateTenantID(testTenantID); err != nil {
		t.Errorf("valid UUID err = %v; want nil", err)
	}
	if err := validateTenantID(""); err == nil {
		t.Error("empty err = nil; want error")
	}
	if err := validateTenantID("01970000-0000-7000-8000-00000000000g"); err == nil {
		t.Error("non-hex err = nil; want error")
	}
}

func TestValidateUserRoles(t *testing.T) {
	t.Parallel()

	if err := validateUserRoles("instructor,proctor_admin-2"); err != nil {
		t.Errorf("valid roles err = %v; want nil", err)
	}
	if err := validateUserRoles(""); err == nil {
		t.Error("empty err = nil; want error")
	}
	if err := validateUserRoles("proctor role"); err == nil {
		t.Error("whitespace err = nil; want error")
	}
}

func TestApplyUserRolesSession(t *testing.T) {
	t.Parallel()

	// No roles on ctx → exec never runs.
	called := false
	if err := applyUserRolesSession(context.Background(), func(string) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("no-roles err = %v; want nil", err)
	}
	if called {
		t.Error("exec called without roles; want no-op")
	}

	// Roles on ctx → SET LOCAL emitted verbatim.
	ctx := tracing.WithUserRoles(context.Background(), "proctor")
	var got string
	if err := applyUserRolesSession(ctx, func(sql string) error {
		got = sql
		return nil
	}); err != nil {
		t.Fatalf("roles err = %v; want nil", err)
	}
	if want := "SET LOCAL chora.user_roles = 'proctor'"; got != want {
		t.Errorf("exec sql = %q; want %q", got, want)
	}

	// Unsafe roles fail loud.
	bad := tracing.WithUserRoles(context.Background(), "proctor';--")
	if err := applyUserRolesSession(bad, func(string) error { return nil }); err == nil {
		t.Error("unsafe roles err = nil; want error")
	}

	// Exec failure is wrapped, not swallowed.
	boom := errors.New("boom")
	if err := applyUserRolesSession(ctx, func(string) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("exec err = %v; want wrapped boom", err)
	}
}

// -----------------------------------------------------------------------------
// question_job_repository.go — copyTimePtr
// -----------------------------------------------------------------------------

func TestCopyTimePtr(t *testing.T) {
	t.Parallel()

	if got := copyTimePtr(nil); got != nil {
		t.Errorf("copyTimePtr(nil) = %v; want nil", got)
	}
	in := time.Date(2026, 5, 26, 12, 0, 0, 0, time.FixedZone("X", 3600))
	got := copyTimePtr(&in)
	if got == nil || !got.Equal(in) {
		t.Fatalf("copyTimePtr = %v; want equal to %v", got, in)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %v; want UTC", got.Location())
	}
	if got == &in {
		t.Error("copyTimePtr aliased the input pointer; want a copy")
	}
}

// -----------------------------------------------------------------------------
// CollectionRepository — Get / List happy paths (scanCollection +
// loadCollectionAtoms full coverage)
// -----------------------------------------------------------------------------

func TestCollectionRepository_Get_HappyPath(t *testing.T) {
	t.Parallel()

	created := time.Now().UTC().Add(-time.Hour)
	updated := time.Now().UTC()
	added := time.Now().UTC().Add(-time.Minute)

	tx := &covTx{
		queryRowFn: func(sql string) ([]any, bool) {
			if strings.Contains(sql, "FROM collections") {
				return []any{testCollID, testTenantID, testOwnerID, "Title", "Desc", "tenant", created, updated, nil}, true
			}
			return nil, false
		},
		queryFn: func(sql string) ([][]any, bool) {
			if strings.Contains(sql, "FROM collection_atoms") {
				return [][]any{{testCollID, "01970000-0000-7000-a000-000000000001", 1, added}}, true
			}
			return nil, false
		},
	}
	repo := NewCollectionRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	c, err := repo.Get(context.Background(), testTenantID, testCollID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c.CollectionID != testCollID || c.TenantID != testTenantID || c.OwnerGcid != testOwnerID {
		t.Errorf("identity fields = %+v", c)
	}
	if c.Title != "Title" || c.Description != "Desc" {
		t.Errorf("title/description = %q/%q", c.Title, c.Description)
	}
	if c.Visibility != audience.Audience("tenant") {
		t.Errorf("visibility = %q; want tenant", c.Visibility)
	}
	if !c.CreatedAt.Equal(created) || !c.UpdatedAt.Equal(updated) || c.DeletedAt != nil {
		t.Errorf("timestamps = %v/%v/%v", c.CreatedAt, c.UpdatedAt, c.DeletedAt)
	}
	if len(c.Atoms) != 1 || c.Atoms[0].Position != 1 || !c.Atoms[0].AddedAt.Equal(added) {
		t.Errorf("atoms = %+v; want 1 loaded child", c.Atoms)
	}
}

func TestCollectionRepository_Get_NotFound(t *testing.T) {
	t.Parallel()

	repo := NewCollectionRepositoryFromTxQuerier(&covTxQuerier{tx: &covTx{}})
	_, err := repo.Get(context.Background(), testTenantID, testCollID)
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want collection.ErrNotFound", err)
	}
}

func TestCollectionRepository_Get_TxErrorWraps(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	repo := NewCollectionRepositoryFromTxQuerier(&covTxQuerier{tx: &covTx{}, runErr: boom})
	if _, err := repo.Get(context.Background(), testTenantID, testCollID); !errors.Is(err, boom) {
		t.Errorf("err = %v; want wrapped boom", err)
	}
}

func TestCollectionRepository_List_HappyPath(t *testing.T) {
	t.Parallel()

	created := time.Now().UTC()
	tx := &covTx{
		queryFn: func(sql string) ([][]any, bool) {
			switch {
			case strings.Contains(sql, "FROM collection_atoms"):
				return [][]any{}, true
			case strings.Contains(sql, "FROM collections"):
				return [][]any{{testCollID, testTenantID, testOwnerID, "T", "D", "private", created, created, nil}}, true
			}
			return nil, false
		},
	}
	repo := NewCollectionRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	out, err := repo.List(context.Background(), testTenantID, collection.ListFilter{
		OwnerGcid:  testOwnerID,
		Visibility: audience.Audience("private"),
		Limit:      10,
		Offset:     5,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].CollectionID != testCollID {
		t.Fatalf("List = %+v; want 1 collection", out)
	}
	if out[0].Atoms == nil {
		// empty-but-loaded child set is fine; nil would mean load never ran
		t.Log("atoms empty as stubbed")
	}
}

func TestCollectionRepository_List_ScanErrorPropagates(t *testing.T) {
	t.Parallel()

	// Row arity mismatch inside scanCollection must surface as an error.
	tx := &covTx{
		queryFn: func(sql string) ([][]any, bool) {
			if strings.Contains(sql, "FROM collections") {
				return [][]any{{testCollID}}, true
			}
			return [][]any{}, true
		},
	}
	repo := NewCollectionRepositoryFromTxQuerier(&covTxQuerier{tx: tx})
	if _, err := repo.List(context.Background(), testTenantID, collection.ListFilter{}); err == nil {
		t.Error("err = nil; want scan error")
	}
}

func TestCollectionRepository_NilTxQuerierGuards(t *testing.T) {
	t.Parallel()

	repo := &CollectionRepository{}
	if err := repo.Save(context.Background(), &collection.Collection{}); err == nil {
		t.Error("Save err = nil; want not-wired error")
	}
	if _, err := repo.Get(context.Background(), testTenantID, testCollID); err == nil {
		t.Error("Get err = nil; want not-wired error")
	}
	if _, err := repo.List(context.Background(), testTenantID, collection.ListFilter{}); err == nil {
		t.Error("List err = nil; want not-wired error")
	}
	if _, err := repo.ReuseFacts(context.Background(), testTenantID, []string{"a"}); err == nil {
		t.Error("ReuseFacts err = nil; want not-wired error")
	}
	if err := repo.Save(context.Background(), nil); err == nil {
		t.Error("Save(nil) err = nil; want nil-collection error")
	}
}

// -----------------------------------------------------------------------------
// QuestionBankRepository — Get / GetVisible / ListItemsPage happy paths
// (scanQuestionBank + loadQuestionBankItems full coverage)
// -----------------------------------------------------------------------------

func TestQuestionBankRepository_Get_HappyPath(t *testing.T) {
	t.Parallel()

	created := time.Now().UTC().Add(-time.Hour)
	updated := time.Now().UTC()
	added := time.Now().UTC().Add(-time.Minute)

	tx := &covTx{
		queryRowFn: func(sql string) ([]any, bool) {
			if strings.Contains(sql, "FROM question_banks") {
				return []any{testBankID, testTenantID, testOwnerID, "Pool", "Desc", "private",
					[]string{"algebra"}, created, updated, nil}, true
			}
			return nil, false
		},
		queryFn: func(sql string) ([][]any, bool) {
			if strings.Contains(sql, "FROM question_bank_items") {
				return [][]any{{testBankID, "01970000-0000-7000-b000-000000000001", testTenantID, 1, added}}, true
			}
			return nil, false
		},
	}
	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	b, err := repo.Get(context.Background(), testTenantID, testBankID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.QuestionBankID != testBankID || b.Name != "Pool" || b.Description != "Desc" {
		t.Errorf("identity fields = %+v", b)
	}
	if len(b.Tags) != 1 || b.Tags[0] != "algebra" {
		t.Errorf("tags = %v; want [algebra]", b.Tags)
	}
	if !b.CreatedAt.Equal(created) || !b.UpdatedAt.Equal(updated) || b.DeletedAt != nil {
		t.Errorf("timestamps = %v/%v/%v", b.CreatedAt, b.UpdatedAt, b.DeletedAt)
	}
	if len(b.Items) != 1 || b.Items[0].Position != 1 || !b.Items[0].AddedAt.Equal(added) {
		t.Errorf("items = %+v; want 1 loaded item", b.Items)
	}

	// GetVisible delegates to Get (no cross-tenant PUBLIC path by design).
	v, err := repo.GetVisible(context.Background(), testTenantID, testBankID)
	if err != nil || v.QuestionBankID != testBankID {
		t.Errorf("GetVisible = %+v, %v; want same bank", v, err)
	}
}

func TestQuestionBankRepository_GetVisible_NotFound(t *testing.T) {
	t.Parallel()

	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: &covTx{}})
	_, err := repo.GetVisible(context.Background(), testTenantID, testBankID)
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want questionbank.ErrNotFound", err)
	}
}

func TestQuestionBankRepository_List_HappyPath(t *testing.T) {
	t.Parallel()

	created := time.Now().UTC()
	tx := &covTx{
		queryFn: func(sql string) ([][]any, bool) {
			switch {
			case strings.Contains(sql, "FROM question_bank_items"):
				return [][]any{}, true
			case strings.Contains(sql, "FROM question_banks"):
				return [][]any{{testBankID, testTenantID, testOwnerID, "Pool", "D", "tenant",
					[]string{}, created, created, nil}}, true
			}
			return nil, false
		},
	}
	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	out, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{
		OwnerGCID: testOwnerID,
		Q:         "pool",
		Tags:      []string{"algebra"},
		Limit:     10,
		Offset:    5,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].QuestionBankID != testBankID {
		t.Fatalf("List = %+v; want 1 bank", out)
	}
}

func TestQuestionBankRepository_ListItemsPage_HappyPath(t *testing.T) {
	t.Parallel()

	added := time.Now().UTC()
	tx := &covTx{
		queryRowFn: func(sql string) ([]any, bool) {
			if strings.Contains(sql, "COUNT(*)") {
				return []any{7}, true
			}
			return nil, false
		},
		queryFn: func(sql string) ([][]any, bool) {
			if strings.Contains(sql, "FROM question_bank_items") {
				return [][]any{
					{"01970000-0000-7000-b000-000000000001", "01970000-0000-7000-a000-000000000001", "mcq", "What is 2+2?", 3, added},
				}, true
			}
			return nil, false
		},
	}
	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	items, total, err := repo.ListItemsPage(context.Background(), testTenantID, testBankID, questionbank.ItemPageFilter{
		Query:    "2+2",
		Types:    []string{"mcq"},
		SortKey:  "added_at",
		SortDesc: true,
		Limit:    10,
		Offset:   0,
	})
	if err != nil {
		t.Fatalf("ListItemsPage: %v", err)
	}
	if total != 7 {
		t.Errorf("total = %d; want 7", total)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v; want 1", items)
	}
	it := items[0]
	if it.QuestionType != "mcq" || it.Prompt != "What is 2+2?" || it.Position != 3 || !it.AddedAt.Equal(added) {
		t.Errorf("item = %+v", it)
	}
}

func TestQuestionBankRepository_ListItemsPage_DefaultsAndEmpty(t *testing.T) {
	t.Parallel()

	// Non-positive limit / negative offset clamp to defaults; empty result
	// must come back as an empty (non-nil) slice.
	tx := &covTx{
		queryRowFn: func(sql string) ([]any, bool) {
			if strings.Contains(sql, "COUNT(*)") {
				return []any{0}, true
			}
			return nil, false
		},
	}
	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: tx})

	items, total, err := repo.ListItemsPage(context.Background(), testTenantID, testBankID, questionbank.ItemPageFilter{
		Limit:  0,
		Offset: -3,
	})
	if err != nil {
		t.Fatalf("ListItemsPage: %v", err)
	}
	if total != 0 || items == nil || len(items) != 0 {
		t.Errorf("items=%v total=%d; want empty non-nil slice, total 0", items, total)
	}
}

func TestQuestionBankRepository_ListItemsPage_TxErrorWraps(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	repo := NewQuestionBankRepositoryFromTxQuerier(&covTxQuerier{tx: &covTx{}, runErr: boom})
	if _, _, err := repo.ListItemsPage(context.Background(), testTenantID, testBankID, questionbank.ItemPageFilter{}); !errors.Is(err, boom) {
		t.Errorf("err = %v; want wrapped boom", err)
	}
}

func TestQuestionBankRepository_NilTxQuerierGuards(t *testing.T) {
	t.Parallel()

	repo := &QuestionBankRepository{}
	if err := repo.Save(context.Background(), &questionbank.QuestionBank{}); err == nil {
		t.Error("Save err = nil; want not-wired error")
	}
	if _, err := repo.Get(context.Background(), testTenantID, testBankID); err == nil {
		t.Error("Get err = nil; want not-wired error")
	}
	if _, err := repo.List(context.Background(), testTenantID, questionbank.ListFilter{}); err == nil {
		t.Error("List err = nil; want not-wired error")
	}
	if err := repo.Save(context.Background(), nil); err == nil {
		t.Error("Save(nil) err = nil; want nil-bank error")
	}
}
