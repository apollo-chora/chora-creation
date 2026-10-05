package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
)

// execRecorder is a caller-owned transaction: it records the statement it was
// asked to run and can be told to fail, standing in for a severed connection.
type execRecorder struct {
	sql  string
	args []any
	err  error
}

func (e *execRecorder) Exec(_ context.Context, sql string, args ...any) error {
	e.sql = sql
	e.args = args
	return e.err
}

func txStoreRow() outbox.Row {
	return outbox.Row{
		ID:             "019fdcd7-0000-7000-8000-000000000001",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		GCID:           "01970000-0000-7000-9000-000000000001",
		AggregateType:  "atom",
		AggregateID:    "019fdcd7-0000-7000-9000-000000000002",
		EventType:      "creation.atom.created",
		Topic:          "chora.creation.atom.created.v1",
		Payload:        []byte("payload"),
		Envelope:       map[string]string{"event_id": "019fdcd7-0000-7000-8000-000000000001"},
		IdempotencyKey: "idem-1",
		OccurredAt:     time.Now().UTC(),
	}
}

// TestTxStore_InsertWritesThroughTheCallersTransaction is the point of the
// type: the row goes out on the transaction it was handed, not on a second
// connection that cannot share the caller's commit.
func TestTxStore_InsertWritesThroughTheCallersTransaction(t *testing.T) {
	t.Parallel()

	tx := &execRecorder{}
	if err := outbox.NewTxStore(tx).Insert(context.Background(), txStoreRow()); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !strings.Contains(tx.sql, "INSERT INTO outbox_events") {
		t.Errorf("statement did not reach the transaction: %q", tx.sql)
	}
	if len(tx.args) != 11 {
		t.Fatalf("bind args = %d; want 11", len(tx.args))
	}
	if tx.args[0] != "019fdcd7-0000-7000-8000-000000000001" {
		t.Errorf("arg 1 (id) = %v; want the row id", tx.args[0])
	}
	if tx.args[9] != "idem-1" {
		t.Errorf("arg 10 (idempotency_key) = %v; want idem-1", tx.args[9])
	}
	// The envelope must reach the wire as JSON text for the ::jsonb cast.
	env, ok := tx.args[8].(string)
	if !ok || !strings.Contains(env, "event_id") {
		t.Errorf("arg 9 (envelope) = %v; want marshalled JSON carrying event_id", tx.args[8])
	}
}

// TestTxStore_InsertSurfacesFailure: a failed insert must reach the caller so
// its transaction rolls back. Swallowing here is what stranded 2.68% of atoms.
func TestTxStore_InsertSurfacesFailure(t *testing.T) {
	t.Parallel()

	tx := &execRecorder{err: errors.New("connection reset by peer")}
	err := outbox.NewTxStore(tx).Insert(context.Background(), txStoreRow())
	if err == nil {
		t.Fatal("Insert returned nil on a severed transaction; want the failure surfaced")
	}
	if !strings.Contains(err.Error(), "outbox: Insert") {
		t.Errorf("error = %v; want it identified as an outbox insert failure", err)
	}
}

// TestTxStore_InsertClassifiesDuplicateIdempotencyKey keeps the one condition
// a caller may treat as a no-op distinguishable from a real failure.
func TestTxStore_InsertClassifiesDuplicateIdempotencyKey(t *testing.T) {
	t.Parallel()

	tx := &execRecorder{err: errors.New(`ERROR: duplicate key value violates unique constraint "outbox_events_idempotency_idx" (SQLSTATE 23505)`)}
	err := outbox.NewTxStore(tx).Insert(context.Background(), txStoreRow())
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Errorf("error = %v; want ErrDuplicateIdempotencyKey", err)
	}
}

// TestTxStore_RefusesWithoutATransaction: a store with nothing behind it must
// fail loud rather than drop the row.
func TestTxStore_RefusesWithoutATransaction(t *testing.T) {
	t.Parallel()

	err := outbox.NewTxStore(nil).Insert(context.Background(), txStoreRow())
	if err == nil {
		t.Fatal("nil transaction: got nil error; want a loud refusal")
	}
	if !strings.Contains(err.Error(), "no transaction bound") {
		t.Errorf("error = %q; want it to name the missing transaction", err)
	}
}
