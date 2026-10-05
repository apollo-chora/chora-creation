// tx_store.go - the outbox writer that rides the caller's transaction.
//
// The package doc has always claimed the producer writes its row "inside the
// caller's transaction". Until 2026-08-07 no implementation did: the only
// Postgres store held its own *sql.DB, so the atom row and the row announcing
// it were two commits and a failure on the second was silently discarded.
// Chaos run chaos20260807a measured 20 of 747 atoms (2.68%) stranded that way.
//
// TxStore closes that gap. It executes the same INSERT as PostgresStore, but
// through a transaction the caller already opened for its state change, so the
// two rows share one commit and one rollback.
package outbox

import "context"

// TxExecer is the minimal surface an open, caller-owned transaction must
// expose. Deliberately structural: it is satisfied by the creation pg
// adapter's Tx without either adapter package importing the other.
type TxExecer interface {
	Exec(ctx context.Context, sql string, args ...any) error
}

// TxStore writes outbox rows through a caller-owned transaction.
//
// It implements Inserter, not Store: claiming and completing rows belongs to
// the Dispatcher, which owns its own pool-scoped connection and must never
// operate inside a producer's request transaction.
type TxStore struct {
	ex TxExecer
}

// NewTxStore binds an outbox writer to an open transaction. The returned
// store is valid only for the lifetime of that transaction.
func NewTxStore(ex TxExecer) *TxStore {
	return &TxStore{ex: ex}
}

// Insert writes the pending row inside the caller's transaction. The row
// becomes visible to the Dispatcher only when the caller commits, so an event
// can never announce a state change that rolled back.
func (s *TxStore) Insert(ctx context.Context, row Row) error {
	if s == nil || s.ex == nil {
		return errNilTxExecer
	}
	args, err := insertRowArgs(row)
	if err != nil {
		return err
	}
	if err := s.ex.Exec(ctx, insertRowSQL, args...); err != nil {
		return wrapInsertErr(err, row.IdempotencyKey)
	}
	return nil
}

// errNilTxExecer fails loud rather than dropping the row: a TxStore with no
// transaction behind it is a wiring bug, not a degraded mode.
var errNilTxExecer = errTxStore("outbox: TxStore: no transaction bound")

type errTxStore string

func (e errTxStore) Error() string { return string(e) }

// Compile-time check.
var _ Inserter = (*TxStore)(nil)
