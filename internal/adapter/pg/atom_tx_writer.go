// atom_tx_writer.go - the atomic half of the D6.2 producer-side outbox.
//
// AtomTxWriter satisfies atom.TransactionalPublisher: it writes the
// learning_atoms row and the outbox_events row that announces it inside ONE
// tenant-scoped transaction. Either both commit or neither does.
//
// What it replaces
// ----------------
// The handlers used to call repo.Save (commit 1) and then publisher.Publish
// (commit 2, on a SEPARATE *sql.DB opened from CHORA_OUTBOX_DSN), logging and
// discarding a failure on the second. Chaos run chaos20260807a killed the sole
// pod under 64 concurrent writers and measured the result: 20 of 747 atoms
// (2.68%) committed with no event, ten of them after answering HTTP 201. The
// two pools made atomicity structurally impossible, and both DSNs resolved to
// the same secret, so the split bought nothing at all.
//
// The publisher is injected as a factory over the open transaction rather than
// imported, so this adapter stays independent of the outbox adapter and the
// composition stays in main().
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// TxPublisherFactory builds an event publisher bound to an open transaction.
// main() supplies one that writes to outbox_events through that same tx.
type TxPublisherFactory func(tx Tx) atom.EventPublisher

// AtomTxWriter writes an atom and its event in one transaction.
type AtomTxWriter struct {
	tx           TxQuerier
	newPublisher TxPublisherFactory
}

// NewAtomTxWriter wires the writer. Both arguments are required: a writer
// missing either one cannot honour atomicity and refuses at first use rather
// than degrading to the two-commit shape it exists to remove.
func NewAtomTxWriter(tq TxQuerier, newPublisher TxPublisherFactory) *AtomTxWriter {
	return &AtomTxWriter{tx: tq, newPublisher: newPublisher}
}

// SaveAndPublish satisfies atom.TransactionalPublisher.
//
// Sequence inside one tx: SET LOCAL chora.tenant_id, UPSERT learning_atoms,
// INSERT outbox_events, COMMIT. A failure at any step rolls the whole thing
// back, so the caller can answer 500 truthfully: nothing was committed, and
// the author retries rather than being handed a receipt for an atom the
// platform will never hear about.
func (w *AtomTxWriter) SaveAndPublish(ctx context.Context, a *atom.LearningAtom, ev atom.Event) error {
	if w == nil || w.tx == nil {
		return errors.New("pg.AtomTxWriter: tx querier not wired")
	}
	if w.newPublisher == nil {
		return errors.New("pg.AtomTxWriter: publisher factory not wired")
	}
	stmt, args, err := atomUpsert(a)
	if err != nil {
		return err
	}
	return w.tx.RunInTenantTx(ctx, a.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, stmt, args...); err != nil {
			return fmt.Errorf("pg.AtomTxWriter: save atom %s: %w", a.AtomID, err)
		}
		if err := w.newPublisher(tx).Publish(ctx, ev); err != nil {
			return fmt.Errorf("pg.AtomTxWriter: enqueue %s for atom %s (atom write rolled back): %w",
				ev.Type, a.AtomID, err)
		}
		return nil
	})
}

// Compile-time port assertion.
var _ atom.TransactionalPublisher = (*AtomTxWriter)(nil)
