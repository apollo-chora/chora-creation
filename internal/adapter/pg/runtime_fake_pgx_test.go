// runtime_fake_pgx_test.go — DB-free coverage of the thin pgx delegation
// wrappers in runtime.go (pgxPoolRow, pgxPoolRows, PgxTxQuerier, pgxTx).
//
// pgx.Row / pgx.Rows / pgx.Tx are interfaces, so the tests embed them and
// override only the methods the wrappers call — the remaining surface panics
// if accidentally reached, which is exactly what we want.
package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// -----------------------------------------------------------------------------
// Test doubles — embedded pgx interfaces with the used methods overridden
// -----------------------------------------------------------------------------

type fakePgxRow struct {
	pgx.Row
	err error
}

func (r *fakePgxRow) Scan(dest ...any) error { return r.err }

type fakePgxRows struct {
	pgx.Rows
	next   bool
	closed bool
	scanEr error
	err    error
}

func (r *fakePgxRows) Next() bool {
	n := r.next
	r.next = false
	return n
}
func (r *fakePgxRows) Scan(dest ...any) error { return r.scanEr }
func (r *fakePgxRows) Close()                 { r.closed = true }
func (r *fakePgxRows) Err() error             { return r.err }

type fakePgxTx struct {
	pgx.Tx
	execErr   error
	queryErr  error
	rowErr    error
	execSQL   string
	rolled    bool
	committed bool
}

func (t *fakePgxTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.execSQL = sql
	return pgconn.CommandTag{}, t.execErr
}
func (t *fakePgxTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if t.queryErr != nil {
		return nil, t.queryErr
	}
	return &fakePgxRows{}, nil
}
func (t *fakePgxTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return &fakePgxRow{err: t.rowErr}
}
func (t *fakePgxTx) Rollback(context.Context) error { t.rolled = true; return nil }
func (t *fakePgxTx) Commit(context.Context) error   { t.committed = true; return nil }

// -----------------------------------------------------------------------------
// pgxPoolRow — ErrNoRows translation + passthrough
// -----------------------------------------------------------------------------

func TestPgxPoolRow_Scan(t *testing.T) {
	t.Parallel()

	var s string
	if err := (&pgxPoolRow{r: &fakePgxRow{err: pgx.ErrNoRows}}).Scan(&s); !errors.Is(err, ErrNoRows) {
		t.Errorf("err = %v; want ErrNoRows translation", err)
	}
	boom := errors.New("boom")
	if err := (&pgxPoolRow{r: &fakePgxRow{err: boom}}).Scan(&s); !errors.Is(err, boom) {
		t.Errorf("err = %v; want passthrough boom", err)
	}
	if err := (&pgxPoolRow{r: &fakePgxRow{}}).Scan(&s); err != nil {
		t.Errorf("err = %v; want nil", err)
	}
}

// -----------------------------------------------------------------------------
// pgxPoolRows — delegation
// -----------------------------------------------------------------------------

func TestPgxPoolRows_Delegation(t *testing.T) {
	t.Parallel()

	inner := &fakePgxRows{next: true, err: errors.New("iter")}
	r := &pgxPoolRows{r: inner}
	if !r.Next() {
		t.Error("Next() = false; want true on first call")
	}
	if r.Next() {
		t.Error("Next() = true; want false on second call")
	}
	if err := r.Scan(new(string)); err != nil {
		t.Errorf("Scan err = %v; want nil", err)
	}
	if err := r.Close(); err != nil || !inner.closed {
		t.Errorf("Close err = %v closed = %v; want nil, true", err, inner.closed)
	}
	if err := r.Err(); err == nil {
		t.Error("Err() = nil; want inner error")
	}
}

// -----------------------------------------------------------------------------
// PgxTxQuerier — wrap pgx.Tx as Querier
// -----------------------------------------------------------------------------

func TestPgxTxQuerier_Surface(t *testing.T) {
	t.Parallel()

	tx := &fakePgxTx{}
	q := NewPgxTxQuerier(tx)
	if q == nil {
		t.Fatal("NewPgxTxQuerier = nil")
	}
	if err := q.Exec(context.Background(), "UPDATE x"); err != nil {
		t.Errorf("Exec err = %v; want nil", err)
	}
	if tx.execSQL != "UPDATE x" {
		t.Errorf("execSQL = %q; want UPDATE x", tx.execSQL)
	}
	boom := errors.New("boom")
	tx.execErr = boom
	if err := q.Exec(context.Background(), "UPDATE x"); !errors.Is(err, boom) {
		t.Errorf("Exec err = %v; want wrapped boom", err)
	}

	// QueryRow wraps into pgxPoolRow (ErrNoRows translation applies there).
	tx.rowErr = pgx.ErrNoRows
	var s string
	if err := q.QueryRow(context.Background(), "SELECT 1").Scan(&s); !errors.Is(err, ErrNoRows) {
		t.Errorf("QueryRow err = %v; want ErrNoRows", err)
	}

	// Query success + error branches.
	tx.queryErr = nil
	rows, err := q.Query(context.Background(), "SELECT 1")
	if err != nil || rows == nil {
		t.Errorf("Query = %v, %v; want rows, nil", rows, err)
	}
	tx.queryErr = boom
	if _, err := q.Query(context.Background(), "SELECT 1"); !errors.Is(err, boom) {
		t.Errorf("Query err = %v; want wrapped boom", err)
	}
}

// -----------------------------------------------------------------------------
// pgxTx — Tx surface used inside RunInTenantTx
// -----------------------------------------------------------------------------

func TestPgxTx_Surface(t *testing.T) {
	t.Parallel()

	inner := &fakePgxTx{}
	tx := &pgxTx{tx: inner}

	if err := tx.Exec(context.Background(), "UPDATE x"); err != nil {
		t.Errorf("Exec err = %v; want nil", err)
	}
	boom := errors.New("boom")
	inner.execErr = boom
	if err := tx.Exec(context.Background(), "UPDATE x"); !errors.Is(err, boom) {
		t.Errorf("Exec err = %v; want wrapped boom", err)
	}

	rows, err := tx.Query(context.Background(), "SELECT 1")
	if err != nil || rows == nil {
		t.Errorf("Query = %v, %v; want rows, nil", rows, err)
	}
	inner.queryErr = boom
	if _, err := tx.Query(context.Background(), "SELECT 1"); !errors.Is(err, boom) {
		t.Errorf("Query err = %v; want wrapped boom", err)
	}

	inner.rowErr = pgx.ErrNoRows
	var s string
	if err := tx.QueryRow(context.Background(), "SELECT 1").Scan(&s); !errors.Is(err, ErrNoRows) {
		t.Errorf("QueryRow err = %v; want ErrNoRows", err)
	}
}
