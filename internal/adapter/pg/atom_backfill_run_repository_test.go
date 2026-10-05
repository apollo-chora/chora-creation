// atom_backfill_run_repository_test.go — CHO-2159.
//
// ⚠ THE CHO-2170 LESSON, WHICH THIS FILE EXISTS TO NOT REPEAT.
//
// atom_backfill_runs carries FORCE ROW LEVEL SECURITY with a tenant_isolation
// policy keyed on current_setting('chora.tenant_id'). A statement that reaches
// that table WITHOUT the GUC set in the same transaction does not error in the
// obvious way — it silently matches NOTHING (a fresh connection) or throws
// 22P02 (a pooled one, where the custom GUC reverts to the EMPTY STRING rather
// than unset when its SET LOCAL transaction ends). An explicit `tenant_id = $1`
// predicate does NOT substitute for the GUC.
//
// In chora-creation the GUC is applied by the SEAM, not by the closure:
// PgxPoolQuerier.RunInTenantTx does `SET LOCAL chora.tenant_id` and only THEN
// invokes fn. So the invariant to hold — and the thing these tests assert — is
// that EVERY statement of this repo (reads AND writes) runs inside
// RunInTenantTx, with the right tenant. The repository holds a TxQuerier and
// nothing else, so it is structurally incapable of executing outside that seam.
//
// ⚠ AND: CHO-2170 shipped broken *because its test asserted an exec-call COUNT*
// (len(execCalls) != 1), which could only pass if the RLS statement were
// missing — a green test that enforced the defect. Nothing here counts exec
// calls. These tests assert the ORDER and CONTENT of the emitted SQL and the
// tenant the seam was entered with.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// doubles (uniquely named — the package already carries stubTxQuerier/stubTx)
// -----------------------------------------------------------------------------

// bkTx records every statement in ORDER (never a count) and serves canned rows.
type bkTx struct {
	stmts []string // every SQL statement, in the order the repo emitted it
	args  [][]any

	row     *bkRow
	rows    *bkRows
	execErr error
}

func (t *bkTx) record(sql string, args []any) {
	t.stmts = append(t.stmts, sql)
	t.args = append(t.args, args)
}

func (t *bkTx) Exec(_ context.Context, sql string, args ...any) error {
	t.record(sql, args)
	return t.execErr
}

func (t *bkTx) Query(_ context.Context, sql string, args ...any) (pg.Rows, error) {
	t.record(sql, args)
	if t.rows == nil {
		return &bkRows{}, nil
	}
	return t.rows, nil
}

func (t *bkTx) QueryRow(_ context.Context, sql string, args ...any) pg.Row {
	t.record(sql, args)
	if t.row == nil {
		return &bkRow{err: pg.ErrNoRows}
	}
	return t.row
}

// bkRow scans a canned run row.
type bkRow struct {
	err  error
	vals []any
}

func (r *bkRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return bkAssign(dest, r.vals)
}

// bkRows serves n canned rows (used by the RETURNING-based sweep count).
type bkRows struct {
	vals [][]any
	i    int
	err  error
}

func (r *bkRows) Next() bool {
	if r.i >= len(r.vals) {
		return false
	}
	r.i++
	return true
}
func (r *bkRows) Scan(dest ...any) error { return bkAssign(dest, r.vals[r.i-1]) }
func (r *bkRows) Close() error           { return nil }
func (r *bkRows) Err() error             { return r.err }

func bkAssign(dest []any, vals []any) error {
	if len(vals) < len(dest) {
		return errors.New("bkAssign: not enough canned values")
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			if v, ok := vals[i].(string); ok {
				*p = v
			}
		case *bool:
			if v, ok := vals[i].(bool); ok {
				*p = v
			}
		case *int:
			if v, ok := vals[i].(int); ok {
				*p = v
			}
		case *[]byte:
			if v, ok := vals[i].([]byte); ok {
				*p = v
			}
		case *time.Time:
			if v, ok := vals[i].(time.Time); ok {
				*p = v
			}
		case **time.Time:
			if v, ok := vals[i].(*time.Time); ok {
				*p = v
			}
		}
	}
	return nil
}

// bkTxQuerier is the tenant-tx seam. It records the tenant every statement was
// scoped to — in the live adapter that tenant becomes SET LOCAL chora.tenant_id
// BEFORE fn runs, which is the entire RLS guarantee.
type bkTxQuerier struct {
	tx      *bkTx
	tenants []string // the tenant each RunInTenantTx was entered with, in order
	runErr  error
}

func newBkTxQuerier(tx *bkTx) *bkTxQuerier {
	if tx == nil {
		tx = &bkTx{}
	}
	return &bkTxQuerier{tx: tx}
}

func (q *bkTxQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pg.Tx) error) error {
	q.tenants = append(q.tenants, tenantID)
	if q.runErr != nil {
		return q.runErr
	}
	return fn(ctx, q.tx)
}

func bkRun() *atom.BackfillRun {
	started := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	return &atom.BackfillRun{
		RunID:      "01919d3f-0000-7000-8000-000000000001",
		TenantID:   "01935b5a-9bcf-7000-7000-000000000001",
		DryRun:     true,
		Status:     atom.BackfillRunning,
		LimitN:     3,
		ReemitSalt: "s1",
		StartedAt:  started,
	}
}

// assertTenantScoped is the CHO-2170 guard: the statement must have run inside
// the tenant-tx seam (which emits SET LOCAL chora.tenant_id first), scoped to
// the expected tenant. NOT an exec-call count.
func assertTenantScoped(t *testing.T, q *bkTxQuerier, wantTenant string) {
	t.Helper()
	if len(q.tenants) == 0 {
		t.Fatal("statement ran OUTSIDE RunInTenantTx — under FORCE RLS with no chora.tenant_id GUC it would silently match nothing (fresh conn) or throw 22P02 (pooled conn). This is the CHO-2170 defect.")
	}
	for i, got := range q.tenants {
		if got != wantTenant {
			t.Fatalf("RunInTenantTx[%d] entered with tenant %q, want %q — the RLS GUC would scope the statement to the wrong tenant", i, got, wantTenant)
		}
	}
}

// -----------------------------------------------------------------------------
// Create — the `running` row persisted before the 202
// -----------------------------------------------------------------------------

func TestAtomBackfillRunRepo_Create_IsTenantScopedAndInserts(t *testing.T) {
	tx := &bkTx{}
	q := newBkTxQuerier(tx)
	r := pg.NewAtomBackfillRunRepository(q)
	run := bkRun()

	if err := r.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertTenantScoped(t, q, run.TenantID)

	if len(tx.stmts) == 0 {
		t.Fatal("Create emitted no SQL")
	}
	if !strings.Contains(tx.stmts[0], "INSERT INTO atom_backfill_runs") {
		t.Fatalf("first statement = %q, want the INSERT", tx.stmts[0])
	}
}

func TestAtomBackfillRunRepo_Create_NilRunFailsLoud(t *testing.T) {
	r := pg.NewAtomBackfillRunRepository(newBkTxQuerier(nil))
	if err := r.Create(context.Background(), nil); err == nil {
		t.Fatal("want a loud error on a nil run")
	}
}

func TestAtomBackfillRunRepo_Create_UnwiredTxFailsLoud(t *testing.T) {
	// A repo with no tx seam must NEVER pretend to persist — a 202 would be
	// handed out for a run row that does not exist.
	r := pg.NewAtomBackfillRunRepository(nil)
	if err := r.Create(context.Background(), bkRun()); err == nil {
		t.Fatal("want a loud error when the tenant-tx seam is unwired")
	}
}

// -----------------------------------------------------------------------------
// Finish — the terminal row
// -----------------------------------------------------------------------------

func TestAtomBackfillRunRepo_Finish_IsTenantScopedAndUpdates(t *testing.T) {
	tx := &bkTx{}
	q := newBkTxQuerier(tx)
	r := pg.NewAtomBackfillRunRepository(q)

	run := bkRun()
	fin := run.StartedAt.Add(280 * time.Second)
	run.Status = atom.BackfillCompleted
	run.Scanned, run.Candidates, run.Tagged, run.Emitted = 117, 74, 72, 72
	run.Failed = []atom.TopicTagFailure{{AtomID: "a2", Title: "T2", Reason: "classify: boom"}}
	run.FinishedAt = &fin

	if err := r.Finish(context.Background(), run); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	assertTenantScoped(t, q, run.TenantID)

	if len(tx.stmts) == 0 {
		t.Fatal("Finish emitted no SQL")
	}
	if !strings.Contains(tx.stmts[0], "UPDATE atom_backfill_runs") {
		t.Fatalf("first statement = %q, want the terminal UPDATE", tx.stmts[0])
	}
	// The report body is what the operator polls for — it must be bound, not
	// dropped. failed[] carries the reasons that were undiagnosable before.
	joined := strings.Join(tx.stmts, " ")
	if !strings.Contains(joined, "report") {
		t.Errorf("the terminal UPDATE must write the report JSONB; got %q", tx.stmts[0])
	}
	if !strings.Contains(joined, "finished_at") {
		t.Errorf("the terminal UPDATE must stamp finished_at; got %q", tx.stmts[0])
	}
}

// -----------------------------------------------------------------------------
// Get — the poll
// -----------------------------------------------------------------------------

func TestAtomBackfillRunRepo_Get_IsTenantScopedAndFiltersSoftDeleted(t *testing.T) {
	fin := time.Date(2026, 7, 14, 12, 4, 40, 0, time.UTC)
	tx := &bkTx{row: &bkRow{vals: []any{
		"01919d3f-0000-7000-8000-000000000001", // run_id
		"01935b5a-9bcf-7000-7000-000000000001", // tenant_id
		true,                                   // dry_run
		"completed",                            // status
		3,                                      // limit_n
		"s1",                                   // reemit_salt
		117, 74, 72, 72,                        // scanned candidates tagged emitted
		[]byte(`{"proposed":[{"atom_id":"a1","title":"T1","tags":["fractions"]}],"failed":[{"atom_id":"a2","title":"T2","reason":"classify: boom"}]}`), // report
		"", // error
		time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), // started_at
		&fin, // finished_at
	}}}
	q := newBkTxQuerier(tx)
	r := pg.NewAtomBackfillRunRepository(q)

	got, err := r.Get(context.Background(), "01935b5a-9bcf-7000-7000-000000000001", "01919d3f-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertTenantScoped(t, q, "01935b5a-9bcf-7000-7000-000000000001")

	if !strings.Contains(tx.stmts[0], "FROM atom_backfill_runs") {
		t.Fatalf("first statement = %q, want the SELECT", tx.stmts[0])
	}
	if !strings.Contains(tx.stmts[0], "deleted_at IS NULL") {
		t.Errorf("the read must filter soft-deleted rows; got %q", tx.stmts[0])
	}
	if got.Status != atom.BackfillCompleted || got.Scanned != 117 || got.Candidates != 74 {
		t.Errorf("run not hydrated: %+v", got)
	}
	// The report JSONB must round-trip — this IS the deliverable of the story.
	if len(got.Failed) != 1 || got.Failed[0].Reason != "classify: boom" {
		t.Errorf("failed[] must round-trip out of the report JSONB, got %+v", got.Failed)
	}
	if len(got.Proposed) != 1 || got.Proposed[0].AtomID != "a1" {
		t.Errorf("proposed[] must round-trip out of the report JSONB, got %+v", got.Proposed)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(fin) {
		t.Errorf("finished_at = %v, want %v", got.FinishedAt, fin)
	}
}

func TestAtomBackfillRunRepo_Get_UnknownRunIsNotFound(t *testing.T) {
	tx := &bkTx{row: &bkRow{err: pg.ErrNoRows}}
	r := pg.NewAtomBackfillRunRepository(newBkTxQuerier(tx))

	_, err := r.Get(context.Background(), "01935b5a-9bcf-7000-7000-000000000001", "01919d3f-0000-7000-8000-00000000dead")
	if !errors.Is(err, atom.ErrBackfillRunNotFound) {
		t.Fatalf("want atom.ErrBackfillRunNotFound (the handler maps it to 404), got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SweepStranded — a pod death mid-run must not strand the row at `running`
// -----------------------------------------------------------------------------

func TestAtomBackfillRunRepo_SweepStranded_IsTenantScopedAndReclaims(t *testing.T) {
	tx := &bkTx{rows: &bkRows{vals: [][]any{
		{"01919d3f-0000-7000-8000-00000000000a"},
		{"01919d3f-0000-7000-8000-00000000000b"},
	}}}
	q := newBkTxQuerier(tx)
	r := pg.NewAtomBackfillRunRepository(q)

	cutoff := time.Date(2026, 7, 14, 11, 15, 0, 0, time.UTC)
	n, err := r.SweepStranded(context.Background(), "01935b5a-9bcf-7000-7000-000000000001", cutoff)
	if err != nil {
		t.Fatalf("SweepStranded: %v", err)
	}
	assertTenantScoped(t, q, "01935b5a-9bcf-7000-7000-000000000001")

	if n != 2 {
		t.Errorf("reclaimed = %d, want 2", n)
	}
	// Normalise whitespace so the assertions pin the SQL's MEANING, not its
	// column alignment.
	sql := strings.Join(strings.Fields(tx.stmts[0]), " ")
	if !strings.Contains(sql, "UPDATE atom_backfill_runs") {
		t.Fatalf("first statement = %q, want the reclaim UPDATE", sql)
	}
	// Only genuinely stranded rows: still `running`, started before the cutoff,
	// never finished. A live run must not be reclaimed out from under itself.
	if !strings.Contains(sql, "status = 'running'") {
		t.Errorf("the sweep must only touch `running` rows; got %q", sql)
	}
	if !strings.Contains(sql, "started_at < $2") {
		t.Errorf("the sweep must be bounded by the cutoff; got %q", sql)
	}
	if !strings.Contains(sql, "finished_at IS NULL") {
		t.Errorf("the sweep must only touch rows that never finished; got %q", sql)
	}
}

func TestAtomBackfillRunRepo_SweepStranded_RequiresTenant(t *testing.T) {
	r := pg.NewAtomBackfillRunRepository(newBkTxQuerier(nil))
	if _, err := r.SweepStranded(context.Background(), "", time.Now()); err == nil {
		t.Fatal("want an error without a tenant — an unscoped sweep would cross tenants")
	}
}

// -----------------------------------------------------------------------------
// The port is satisfied (a drifting signature must break the build, not prod)
// -----------------------------------------------------------------------------

func TestAtomBackfillRunRepo_SatisfiesDomainPort(t *testing.T) {
	var _ atom.BackfillRunStore = pg.NewAtomBackfillRunRepository(newBkTxQuerier(nil))
}
