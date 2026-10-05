// Package outbox_test — targeted coverage boosts on error paths + small
// helper functions that the happy-path tests don't reach. Keeps the
// outbox package at ≥85% per `feedback_strict_tdd`.
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// publisher.go uncovered paths
// -----------------------------------------------------------------------------

func TestPublisher_TopicValidation_Errors(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})

	cases := []struct {
		name  string
		topic atom.EventType
	}{
		{"empty topic", ""},
		{"only chora prefix", "chora"},
		{"wrong domain", "chora.consumption.atom.created.v1"},
		{"missing version suffix", "chora.creation.atom.created.value"},
		{"non-numeric version", "chora.creation.atom.created.vX"},
	}
	for _, tc := range cases {
		ev := atomCreatedEvent()
		ev.Type = tc.topic
		err := pub.Publish(context.Background(), ev)
		if err == nil {
			t.Errorf("topic=%q: err = nil; want error", tc.topic)
		}
	}
}

func TestPublisher_Publish_GeneratesEventIDWhenMissing(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	ev := atomCreatedEvent()
	ev.EventID = ""
	ev.IdempotencyKey = ""
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].ID == "" {
		t.Errorf("row.ID empty; want minted UUIDv7")
	}
	if rows[0].IdempotencyKey == "" {
		t.Errorf("row.IdempotencyKey empty; want defaulted to event_id")
	}
}

func TestPublisher_Publish_HandlesMissingAtomID(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	ev := atomCreatedEvent()
	ev.AtomID = ""
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].AggregateID == "" {
		t.Errorf("row.AggregateID empty; want fallback to event_id")
	}
	if rows[0].AggregateID != rows[0].ID {
		t.Errorf("AggregateID = %q; want fallback = ID %q", rows[0].AggregateID, rows[0].ID)
	}
}

func TestPublisher_Publish_BadOccurredAtFallsBackToNow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	ev := atomCreatedEvent()
	ev.OccurredAt = "not-a-date"
	ev.PublishedAt = "also-not"
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].OccurredAt.IsZero() {
		t.Errorf("OccurredAt zero; want non-zero fallback")
	}
}

func TestPublisher_Publish_SchemaVersionDefaultsTo1(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	ev := atomCreatedEvent()
	ev.SchemaVersion = 0
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].Envelope["schema_version"] != "1" {
		t.Errorf("schema_version = %q; want 1 (default)", rows[0].Envelope["schema_version"])
	}
}

// -----------------------------------------------------------------------------
// dispatcher.go uncovered paths
// -----------------------------------------------------------------------------

func TestDispatcher_DrainOnce_StoreFetchError(t *testing.T) {
	t.Parallel()
	store := &erroringFetchStore{err: errors.New("db gone")}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	_, err := d.DrainOnce(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "db gone") {
		t.Errorf("DrainOnce err = %v; want wraps 'db gone'", err)
	}
}

func TestDispatcher_DrainOnce_MarkPublishedFails_StillCountsAsPublishAttempt(t *testing.T) {
	t.Parallel()
	store := &flakyMarkPublishStore{
		InMemoryStore: outbox.NewInMemoryStore(),
		markErr:       errors.New("mark fail"),
	}
	insertRowVia(t, store.InMemoryStore, "rA", "t1")
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if bus.callCount() != 1 {
		t.Errorf("publish calls = %d; want 1", bus.callCount())
	}
}

// erroringFetchStore wraps an InMemoryStore but errors on FetchPending.
type erroringFetchStore struct {
	err error
}

func (e *erroringFetchStore) Insert(context.Context, outbox.Row) error { return nil }
func (e *erroringFetchStore) FetchPending(_ context.Context, _ int) ([]outbox.Row, error) {
	return nil, e.err
}
func (e *erroringFetchStore) MarkPublished(context.Context, string) error { return nil }
func (e *erroringFetchStore) MarkFailed(context.Context, string, string) error {
	return nil
}
func (e *erroringFetchStore) Deadletter(context.Context, string, string, int) error {
	return nil
}

// flakyMarkPublishStore wraps InMemoryStore but errors on MarkPublished.
type flakyMarkPublishStore struct {
	*outbox.InMemoryStore
	markErr error
}

func (s *flakyMarkPublishStore) MarkPublished(ctx context.Context, id string) error {
	return s.markErr
}

func insertRowVia(t *testing.T, store *outbox.InMemoryStore, id, tenant string) {
	t.Helper()
	now := time.Now().UTC()
	r := outbox.Row{
		ID:            id,
		TenantID:      tenant,
		GCID:          "00000000-0000-0000-0000-000000000001",
		AggregateType: "atom",
		AggregateID:   "atom-" + id,
		EventType:     "creation.atom.created",
		Topic:         "chora.creation.atom.created.v1",
		Payload:       []byte(`{"id":"` + id + `"}`),
		Envelope: map[string]string{
			"event_id":    id,
			"tenant_id":   tenant,
			"occurred_at": now.Format(time.RFC3339Nano),
			"traceparent": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-1111111122222222-01",
		},
		IdempotencyKey: "idem-" + id,
		OccurredAt:     now,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func TestDispatcher_Run_ProcessesMultipleBatches(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	for i := 0; i < 3; i++ {
		insertRowVia(t, store, string(rune('a'+i)), "t")
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = d.Run(ctx, 10)
	if bus.callCount() != 3 {
		t.Errorf("publish calls = %d; want 3", bus.callCount())
	}
}

func TestDispatcher_Run_HandlesFetchErrorAndContinues(t *testing.T) {
	t.Parallel()
	store := &transientErrorStore{
		InMemoryStore: outbox.NewInMemoryStore(),
		errN:          2, // fail first 2 FetchPending calls
		err:           errors.New("temp blip"),
	}
	insertRowVia(t, store.InMemoryStore, "rTr", "t")
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = d.Run(ctx, 10)
	if bus.callCount() < 1 {
		t.Errorf("publish calls = %d; want >= 1 (recovered after blips)", bus.callCount())
	}
}

type transientErrorStore struct {
	*outbox.InMemoryStore
	errN int
	err  error
	mu   int
}

func (s *transientErrorStore) FetchPending(ctx context.Context, lim int) ([]outbox.Row, error) {
	if s.mu < s.errN {
		s.mu++
		return nil, s.err
	}
	return s.InMemoryStore.FetchPending(ctx, lim)
}
