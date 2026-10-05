// atom_orphan_repository_test.go — ADR-229 Amendment A1 (CHO-2132) pg slice,
// RED-first. SQL-shape + arg + tenant-tx coverage for the orphan mint
// machinery via the stub TxQuerier idiom (live RLS behaviour is verified by
// the PREPARE-smoke lane per feedback_pg_prepare_smoke_over_exec_stubs).
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const (
	orphTenant   = "01970000-0000-7111-8111-000000000001"
	orphAuthor   = "01970000-0000-7000-8000-0000000000aa"
	orphOriginal = "01970000-0000-7000-8000-0000000000a1"
	orphRevision = "01970000-0000-7000-8000-0000000000f1"
)

// orphanStubTx is a per-call-scripted Tx: QueryRow returns rows from a FIFO
// queue so multi-statement flows (INSERT..RETURNING then SELECT-existing) can
// be scripted. Exec/Query record SQL + args.
type orphanStubTx struct {
	sqls []string
	args [][]any

	rowQueue []*stubRow
	rowsFn   func(sql string) (*stubRows, error)
}

func (t *orphanStubTx) Exec(_ context.Context, sql string, args ...any) error {
	t.sqls = append(t.sqls, sql)
	t.args = append(t.args, args)
	return nil
}

func (t *orphanStubTx) Query(_ context.Context, sql string, args ...any) (pg.Rows, error) {
	t.sqls = append(t.sqls, sql)
	t.args = append(t.args, args)
	if t.rowsFn != nil {
		return t.rowsFn(sql)
	}
	return &stubRows{}, nil
}

func (t *orphanStubTx) QueryRow(_ context.Context, sql string, args ...any) pg.Row {
	t.sqls = append(t.sqls, sql)
	t.args = append(t.args, args)
	if len(t.rowQueue) == 0 {
		return &stubRow{err: pg.ErrNoRows}
	}
	head := t.rowQueue[0]
	t.rowQueue = t.rowQueue[1:]
	return head
}

// orphanStubTxQuerier records the tenant each RunInTenantTx call bound.
type orphanStubTxQuerier struct {
	tx      *orphanStubTx
	tenants []string
}

func (q *orphanStubTxQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pg.Tx) error) error {
	q.tenants = append(q.tenants, tenantID)
	return fn(ctx, q.tx)
}

func mintableOrphan(t *testing.T) *atom.LearningAtom {
	t.Helper()
	src := &atom.LearningAtom{
		AtomID:          orphOriginal,
		TenantID:        orphTenant,
		Gcid:            orphAuthor,
		Title:           "Fractions",
		Body:            "b",
		Mode:            atom.ModeStraightUp,
		Status:          atom.StatusPublished,
		Stem:            "1/2 + 1/4?",
		Revision:        3,
		ReuseVisibility: atom.ReuseTenant,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	o, err := atom.CloneOrphan(atom.OrphanCloneParams{Source: src, SourceRevisionID: orphRevision})
	if err != nil {
		t.Fatalf("CloneOrphan: %v", err)
	}
	return o
}

// -----------------------------------------------------------------------------
// Save / Get project the new columns
// -----------------------------------------------------------------------------

func TestAtomRepository_Save_PersistsOrphanColumns(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	if err := r.Save(context.Background(), mintableOrphan(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, col := range []string{"orphaned_from_atom_id", "orphaned_source_revision_id", "orphaned_at"} {
		if !strings.Contains(tq.execSQL, col) {
			t.Errorf("Save SQL missing %s; got %q", col, tq.execSQL)
		}
	}
}

func TestAtomRepository_Get_ProjectsOrphanColumns(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, _ = r.Get(context.Background(), orphTenant, orphOriginal)
	for _, col := range []string{"orphaned_from_atom_id", "orphaned_source_revision_id", "orphaned_at"} {
		if !strings.Contains(tq.querySQL, col) {
			t.Errorf("Get SELECT missing %s; got %q", col, tq.querySQL)
		}
	}
}

// -----------------------------------------------------------------------------
// GetAnyState — the archive-trigger loader (no deleted_at filter)
// -----------------------------------------------------------------------------

func TestAtomRepository_GetAnyState_IncludesSoftDeleted(t *testing.T) {
	tx := &orphanStubTx{rowQueue: []*stubRow{{err: pg.ErrNoRows}}}
	tq := &orphanStubTxQuerier{tx: tx}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, err := r.GetAnyState(context.Background(), orphTenant, orphOriginal)
	if err != atom.ErrNotFound {
		t.Fatalf("GetAnyState missing row: err = %v, want atom.ErrNotFound", err)
	}
	if len(tx.sqls) == 0 {
		t.Fatalf("no SQL issued")
	}
	sql := tx.sqls[len(tx.sqls)-1]
	if strings.Contains(sql, "deleted_at IS NULL") {
		t.Errorf("GetAnyState must NOT filter soft-deleted (archive-trigger mint loads archived originals); got %q", sql)
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != orphTenant {
		t.Errorf("GetAnyState must run in the tenant tx; tenants = %v", tq.tenants)
	}
}

// -----------------------------------------------------------------------------
// MintOrphan — idempotent singleton insert
// -----------------------------------------------------------------------------

func TestAtomRepository_MintOrphan_InsertsWithSingletonConflictClause(t *testing.T) {
	o := mintableOrphan(t)
	// Inserted path: RETURNING scan succeeds.
	tx := &orphanStubTx{rowQueue: []*stubRow{{cols: []any{o.AtomID}}}}
	tq := &orphanStubTxQuerier{tx: tx}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	got, inserted, err := r.MintOrphan(context.Background(), o)
	if err != nil {
		t.Fatalf("MintOrphan: %v", err)
	}
	if !inserted {
		t.Fatalf("inserted = false, want true on first mint")
	}
	if got == nil || got.AtomID != o.AtomID {
		t.Fatalf("MintOrphan returned %+v", got)
	}
	if len(tx.sqls) == 0 {
		t.Fatalf("no SQL issued")
	}
	insertSQL := tx.sqls[0]
	for _, want := range []string{
		"INSERT INTO learning_atoms",
		"ON CONFLICT (orphaned_from_atom_id, orphaned_source_revision_id)",
		"WHERE orphaned_from_atom_id IS NOT NULL",
		"DO NOTHING",
		"RETURNING atom_id",
	} {
		if !strings.Contains(insertSQL, want) {
			t.Errorf("MintOrphan INSERT missing %q; got %q", want, insertSQL)
		}
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != orphTenant {
		t.Errorf("MintOrphan must run in the orphan's tenant tx; tenants = %v", tq.tenants)
	}
}

func TestAtomRepository_MintOrphan_ConflictReturnsExistingWithoutNewInsert(t *testing.T) {
	o := mintableOrphan(t)
	existingID := "01970000-0000-7000-8000-00000000dead"
	// Conflict path: RETURNING scans no row; the fall-through SELECT loads
	// the existing orphan (scan by full atom projection → first col atom_id).
	existingCols := make([]any, 0, 32)
	existingCols = append(existingCols, existingID, orphTenant, orphAuthor)
	tx := &orphanStubTx{rowQueue: []*stubRow{
		{err: pg.ErrNoRows},  // INSERT .. RETURNING → conflict
		{cols: existingCols}, // SELECT existing by singleton key
	}}
	tq := &orphanStubTxQuerier{tx: tx}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	got, inserted, err := r.MintOrphan(context.Background(), o)
	if err != nil {
		t.Fatalf("MintOrphan conflict path: %v", err)
	}
	if inserted {
		t.Fatalf("inserted = true on conflict, want false")
	}
	if got == nil || got.AtomID != existingID {
		t.Fatalf("conflict path must return the EXISTING orphan; got %+v", got)
	}
	if len(tx.sqls) < 2 {
		t.Fatalf("expected INSERT then SELECT, got %v", tx.sqls)
	}
	selectSQL := tx.sqls[1]
	if !strings.Contains(selectSQL, "orphaned_from_atom_id = $1") || !strings.Contains(selectSQL, "orphaned_source_revision_id = $2") {
		t.Errorf("existing-orphan SELECT must key on the singleton pair; got %q", selectSQL)
	}
	if len(tx.args[1]) < 2 || tx.args[1][0] != orphOriginal || tx.args[1][1] != orphRevision {
		t.Errorf("existing-orphan SELECT args = %v", tx.args[1])
	}
}

func TestAtomRepository_MintOrphan_RefusesNonOrphanRow(t *testing.T) {
	tq := &orphanStubTxQuerier{tx: &orphanStubTx{}}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)
	notOrphan := &atom.LearningAtom{AtomID: orphOriginal, TenantID: orphTenant, Gcid: orphAuthor}
	if _, _, err := r.MintOrphan(context.Background(), notOrphan); err == nil {
		t.Fatalf("MintOrphan must refuse a row without orphan markers")
	}
}

// -----------------------------------------------------------------------------
// RepointCollections — creation's own collection entries, non-author owners
// -----------------------------------------------------------------------------

func TestAtomRepository_RepointCollections_RepointsAndMerges(t *testing.T) {
	tx := &orphanStubTx{}
	tq := &orphanStubTxQuerier{tx: tx}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	orphanID := "01970000-0000-7000-8000-00000000000e"
	_, err := r.RepointCollections(context.Background(), orphTenant, orphOriginal, orphanID, orphAuthor)
	if err != nil {
		t.Fatalf("RepointCollections: %v", err)
	}
	if len(tx.sqls) < 2 {
		t.Fatalf("expected repoint UPDATE + merge UPDATE, got %d statements: %v", len(tx.sqls), tx.sqls)
	}
	repoint, merge := tx.sqls[0], tx.sqls[1]

	for _, want := range []string{
		"UPDATE collection_atoms",
		"SET atom_id = $2",
		"deleted_at IS NULL",
		"owner_gcid <> $3", // the author's own entries keep the live atom
		"NOT EXISTS",       // PK-safe: skip collections already holding the orphan
	} {
		if !strings.Contains(repoint, want) {
			t.Errorf("repoint UPDATE missing %q; got %q", want, repoint)
		}
	}
	// The leftover rows (collection already holds the orphan) are merged via
	// soft-delete — a dedupe, NEVER a revoke/hard-delete.
	if !strings.Contains(merge, "SET deleted_at = now()") {
		t.Errorf("merge UPDATE must soft-delete duplicates; got %q", merge)
	}
	if strings.Contains(strings.ToUpper(merge), "DELETE FROM") {
		t.Errorf("merge must never hard-delete; got %q", merge)
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != orphTenant {
		t.Errorf("RepointCollections must run in tenant tx; tenants = %v", tq.tenants)
	}
}

// -----------------------------------------------------------------------------
// QuestionRepository.GetByAtomIDAnyState — archive-trigger question loader
// -----------------------------------------------------------------------------

func TestQuestionRepository_GetByAtomIDAnyState_IncludesSoftDeleted(t *testing.T) {
	tx := &orphanStubTx{rowQueue: []*stubRow{{err: pg.ErrNoRows}}}
	tq := &orphanStubTxQuerier{tx: tx}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	_, _, err := r.GetByAtomIDAnyState(context.Background(), orphTenant, orphOriginal)
	if err == nil {
		t.Fatalf("expected not-found error from empty stub")
	}
	if len(tx.sqls) == 0 {
		t.Fatalf("no SQL issued")
	}
	sql := tx.sqls[len(tx.sqls)-1]
	if strings.Contains(sql, "q.deleted_at IS NULL") {
		t.Errorf("GetByAtomIDAnyState must include the soft-deleted question (archive cascade already ran); got %q", sql)
	}
	if !strings.Contains(sql, "q.atom_id = $2") {
		t.Errorf("GetByAtomIDAnyState must key on atom_id; got %q", sql)
	}
}
