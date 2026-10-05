package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// txCommitRecorder implements pg.TxQuerier with real commit / rollback
// semantics: statements are buffered inside the transaction function and are
// kept only when it returns nil. A rollback discards them, which is what makes
// "nothing committed" an observation rather than an assumption.
type txCommitRecorder struct {
	failOn    string
	tenants   []string
	committed []string
	beginErr  error
}

func (r *txCommitRecorder) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pg.Tx) error) error {
	if r.beginErr != nil {
		return r.beginErr
	}
	r.tenants = append(r.tenants, tenantID)
	buf := &txCommitRecorderTx{parent: r}
	if err := fn(ctx, buf); err != nil {
		return err
	}
	r.committed = append(r.committed, buf.pending...)
	return nil
}

type txCommitRecorderTx struct {
	parent  *txCommitRecorder
	pending []string
}

func (t *txCommitRecorderTx) Exec(_ context.Context, sql string, _ ...any) error {
	if t.parent.failOn != "" && strings.Contains(sql, t.parent.failOn) {
		return errors.New("connection reset by peer")
	}
	t.pending = append(t.pending, sql)
	return nil
}

func (t *txCommitRecorderTx) Query(_ context.Context, _ string, _ ...any) (pg.Rows, error) {
	return nil, errors.New("not used")
}

func (t *txCommitRecorderTx) QueryRow(_ context.Context, _ string, _ ...any) pg.Row { return nil }

// publisherFunc adapts a function to atom.EventPublisher.
type publisherFunc func(context.Context, atom.Event) error

func (f publisherFunc) Publish(ctx context.Context, e atom.Event) error { return f(ctx, e) }

func txWriterAtom(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   "01970000-0000-7000-8000-000000000001",
		Gcid:       "01970000-0000-7000-9000-000000000001",
		CourseID:   "01970000-0000-7000-7000-000000000001",
		Title:      "tx writer atom",
		Body:       "body",
		AtomType:   atom.AtomType("mcq"),
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("NewBound: %v", err)
	}
	return a
}

// TestAtomTxWriter_CommitsBothWritesInOneTransaction is the positive control:
// the harness must be able to see both writes land, inside one transaction
// scoped to the atom's tenant. Without it, the rollback assertions below could
// pass while seeing nothing at all.
func TestAtomTxWriter_CommitsBothWritesInOneTransaction(t *testing.T) {
	t.Parallel()

	rec := &txCommitRecorder{}
	a := txWriterAtom(t)
	w := pg.NewAtomTxWriter(rec, func(tx pg.Tx) atom.EventPublisher {
		return publisherFunc(func(ctx context.Context, _ atom.Event) error {
			return tx.Exec(ctx, "INSERT INTO outbox_events (id) VALUES ($1)", "row")
		})
	})

	if err := w.SaveAndPublish(context.Background(), a, atom.Event{Type: atom.EventTypeAtomCreated}); err != nil {
		t.Fatalf("SaveAndPublish: %v", err)
	}
	if len(rec.tenants) != 1 {
		t.Fatalf("transactions opened = %d; want 1 covering both writes", len(rec.tenants))
	}
	if rec.tenants[0] != a.TenantID {
		t.Errorf("transaction tenant = %q; want %q", rec.tenants[0], a.TenantID)
	}
	if len(rec.committed) != 2 {
		t.Fatalf("committed statements = %d; want 2 (atom upsert + outbox insert): %v", len(rec.committed), rec.committed)
	}
	if !strings.Contains(rec.committed[0], "learning_atoms") {
		t.Errorf("first committed statement is not the atom upsert: %s", rec.committed[0])
	}
	if !strings.Contains(rec.committed[1], "outbox_events") {
		t.Errorf("second committed statement is not the outbox insert: %s", rec.committed[1])
	}
}

// TestAtomTxWriter_EnqueueFailureRollsBackTheAtom is the defect itself: the
// outbox insert fails and the atom row must not survive it.
func TestAtomTxWriter_EnqueueFailureRollsBackTheAtom(t *testing.T) {
	t.Parallel()

	rec := &txCommitRecorder{failOn: "outbox_events"}
	a := txWriterAtom(t)
	w := pg.NewAtomTxWriter(rec, func(tx pg.Tx) atom.EventPublisher {
		return publisherFunc(func(ctx context.Context, _ atom.Event) error {
			return tx.Exec(ctx, "INSERT INTO outbox_events (id) VALUES ($1)", "row")
		})
	})

	err := w.SaveAndPublish(context.Background(), a, atom.Event{Type: atom.EventTypeAtomCreated})
	if err == nil {
		t.Fatal("SaveAndPublish returned nil; want the enqueue failure surfaced so the caller can 500")
	}
	if len(rec.tenants) != 1 {
		t.Fatalf("transactions opened = %d; want 1. This test cannot see the defect it targets", len(rec.tenants))
	}
	if len(rec.committed) != 0 {
		t.Errorf("committed statements = %d; want 0. The atom must roll back with the event that failed to queue: %v",
			len(rec.committed), rec.committed)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error = %q; want it to say the atom write rolled back", err)
	}
}

// TestAtomTxWriter_AtomWriteFailureEnqueuesNothing is the mirror case: a failed
// atom upsert must not leave an event announcing an atom that does not exist.
func TestAtomTxWriter_AtomWriteFailureEnqueuesNothing(t *testing.T) {
	t.Parallel()

	rec := &txCommitRecorder{failOn: "learning_atoms"}
	a := txWriterAtom(t)
	published := 0
	w := pg.NewAtomTxWriter(rec, func(tx pg.Tx) atom.EventPublisher {
		return publisherFunc(func(ctx context.Context, _ atom.Event) error {
			published++
			return tx.Exec(ctx, "INSERT INTO outbox_events (id) VALUES ($1)", "row")
		})
	})

	if err := w.SaveAndPublish(context.Background(), a, atom.Event{Type: atom.EventTypeAtomCreated}); err == nil {
		t.Fatal("SaveAndPublish returned nil; want the atom write failure surfaced")
	}
	if published != 0 {
		t.Errorf("publisher invoked %d times after a failed atom write; want 0", published)
	}
	if len(rec.committed) != 0 {
		t.Errorf("committed statements = %d; want 0: %v", len(rec.committed), rec.committed)
	}
}

// TestAtomTxWriter_RefusesIncompleteWiring: a writer missing either half
// cannot honour atomicity, so it fails loud instead of degrading to the
// two-commit shape it exists to remove.
func TestAtomTxWriter_RefusesIncompleteWiring(t *testing.T) {
	t.Parallel()

	a := txWriterAtom(t)
	factory := func(pg.Tx) atom.EventPublisher {
		return publisherFunc(func(context.Context, atom.Event) error { return nil })
	}

	if err := pg.NewAtomTxWriter(nil, factory).SaveAndPublish(context.Background(), a, atom.Event{}); err == nil {
		t.Error("nil tx querier: got nil error; want a loud refusal")
	}
	if err := pg.NewAtomTxWriter(&txCommitRecorder{}, nil).SaveAndPublish(context.Background(), a, atom.Event{}); err == nil {
		t.Error("nil publisher factory: got nil error; want a loud refusal")
	}
	if err := pg.NewAtomTxWriter(&txCommitRecorder{}, factory).SaveAndPublish(context.Background(), nil, atom.Event{}); err == nil {
		t.Error("nil atom: got nil error; want a loud refusal")
	}
}
