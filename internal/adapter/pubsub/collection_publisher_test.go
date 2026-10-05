// collection_publisher_test.go — TDD coverage of the Collection outbox
// publisher (WS-6a Phase 4, 2026-05-26).
//
// The publisher satisfies collection.EventPublisher by writing the
// canonical envelope + payload into the shared outbox_events table. The
// background outbox.Dispatcher (existing — see
// internal/adapter/outbox/dispatcher.go) drains to Cloud Pub/Sub.
//
// Tests stub the outbox.Store + assert the canonical envelope shape:
//   - topic = chora.creation.collection.{event_type}.v1
//   - aggregate_type = "collection"
//   - envelope mandatory fields per ddd-enforcement.md / CLAUDE.md §6
package pubsub_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	creationpubsub "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

const (
	wsTenantID = "01970000-0000-7000-8000-000000000001"
	wsGcid     = "01970000-0000-7000-9000-000000000001"
	wsCollID   = "01970000-0000-7000-7000-000000000001"
	// wsActorGcid is a FORKER — deliberately NOT wsGcid (the collection's owner).
	// Convert takes the actor as an explicit input (CHO-2165), and the two must be
	// distinguishable on the wire or a fork builds the study list for the sharer.
	wsActorGcid = "01970000-0000-7000-9000-0000000000f3"
)

// captureStore records every Insert for assertion.
type captureStore struct {
	mu   sync.Mutex
	rows []creationoutbox.Row
}

func (s *captureStore) Insert(_ context.Context, r creationoutbox.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
	return nil
}

// Satisfy the rest of the Store interface — not exercised in publisher tests.
func (s *captureStore) FetchPending(_ context.Context, _ int) ([]creationoutbox.Row, error) {
	return nil, nil
}
func (s *captureStore) MarkPublished(_ context.Context, _ string) error { return nil }
func (s *captureStore) MarkFailed(_ context.Context, _, _ string) error { return nil }
func (s *captureStore) Deadletter(_ context.Context, _, _ string, _ int) error {
	return nil
}

func TestCollectionPublisher_PublishesCreatedRow(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{
		Store: store,
	})

	c, err := collection.New(collection.NewParams{
		TenantID: wsTenantID, OwnerGcid: wsGcid, Title: "Set",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev := collection.NewCollectionCreatedEvent(c, "00-tp-01", "")

	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows len = %d; want 1", len(store.rows))
	}
	row := store.rows[0]
	// Topic enforces canonical chora.creation.collection.created.v1.
	if row.Topic != "chora.creation.collection.created.v1" {
		t.Errorf("Topic = %q; want chora.creation.collection.created.v1", row.Topic)
	}
	if row.AggregateType != "collection" {
		t.Errorf("AggregateType = %q; want collection", row.AggregateType)
	}
	if row.AggregateID != c.CollectionID {
		t.Errorf("AggregateID = %q; want %q", row.AggregateID, c.CollectionID)
	}
	if row.TenantID != wsTenantID || row.GCID != wsGcid {
		t.Errorf("envelope tenant/gcid mismatch: tenant=%q gcid=%q", row.TenantID, row.GCID)
	}
	if row.IdempotencyKey == "" {
		t.Errorf("IdempotencyKey empty")
	}
	if row.OccurredAt.IsZero() {
		t.Errorf("OccurredAt zero")
	}

	// Envelope MUST include all CLAUDE.md §6 mandatory fields.
	for _, k := range []string{
		"event_id", "idempotency_key", "tenant_id", "gcid",
		"occurred_at", "published_at",
		"traceparent", "tracestate",
		"source_project", "source_service", "schema_version",
	} {
		if _, ok := row.Envelope[k]; !ok {
			t.Errorf("envelope missing mandatory key %q: %v", k, row.Envelope)
		}
	}
	if row.Envelope["traceparent"] != "00-tp-01" {
		t.Errorf("envelope traceparent = %q; want propagated", row.Envelope["traceparent"])
	}
	// schema_version is recorded as a string in the flat envelope.
	if row.Envelope["schema_version"] == "" || row.Envelope["schema_version"] == "0" {
		t.Errorf("envelope schema_version = %q; want non-zero", row.Envelope["schema_version"])
	}

	// Payload JSON is sane — at minimum collection_id + title.
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v body=%s", err, string(row.Payload))
	}
	if payload["collection_id"] != c.CollectionID {
		t.Errorf("payload.collection_id = %v; want %q", payload["collection_id"], c.CollectionID)
	}
	if payload["title"] != "Set" {
		t.Errorf("payload.title = %v; want Set", payload["title"])
	}
}

func TestCollectionPublisher_PublishesAtomAdded(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{
		Store: store,
	})
	c := &collection.Collection{
		CollectionID: wsCollID, TenantID: wsTenantID, OwnerGcid: wsGcid,
		Title: "Set", Visibility: audience.Private, UpdatedAt: time.Now().UTC(),
	}
	a := &collection.CollectionAtom{
		CollectionID: wsCollID,
		AtomID:       "01970000-0000-7000-a000-000000000001",
		Position:     0,
		AddedAt:      time.Now().UTC(),
	}
	ev := collection.NewCollectionAtomAddedEvent(c, a, "", "")
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows len = %d; want 1", len(store.rows))
	}
	row := store.rows[0]
	if !strings.HasSuffix(row.Topic, "atom_added.v1") {
		t.Errorf("Topic = %q; want suffix atom_added.v1", row.Topic)
	}
	var payload map[string]any
	_ = json.Unmarshal(row.Payload, &payload)
	if payload["atom_id"] != a.AtomID {
		t.Errorf("payload.atom_id = %v; want %q", payload["atom_id"], a.AtomID)
	}
}

func TestCollectionPublisher_RejectsNonCanonicalTopic(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{
		Store: store,
	})
	bad := collection.Event{
		Type: collection.EventType("legacy.unknown.topic"),
	}
	if err := pub.Publish(context.Background(), bad); err == nil {
		t.Errorf("expected error for non-canonical topic")
	}
	if len(store.rows) != 0 {
		t.Errorf("rows len = %d; want 0", len(store.rows))
	}
}
