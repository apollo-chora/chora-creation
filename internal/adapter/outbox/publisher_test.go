// Package outbox_test — OutboxPublisher adapter tests.
//
// OutboxPublisher satisfies atom.EventPublisher by writing the atom event
// to the outbox_events table (via the Store port) instead of publishing
// directly to Pub/Sub. A separate Dispatcher drains the outbox to Cloud
// Pub/Sub. This decouples atom event emission from Pub/Sub availability:
// a crash between domain state-write and Pub/Sub publish no longer loses
// events because the row is durably committed to chora_creation before
// the HTTP request returns.
//
// Per `feedback_d6_resilience_first_class` B.6.2.a — producer-side durable
// emission for chora-creation's `chora.creation.atom.*.v1` streams.
// Composes with the LangGraph PostgresSaver pattern used by the closure
// saga + AI Kernel orchestrator.
package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func atomCreatedEvent() atom.Event {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	return atom.Event{
		EventID:        "01970000-0000-7000-8000-000000000001",
		IdempotencyKey: "01970000-0000-7000-8000-000000000001",
		Type:           atom.EventTypeAtomCreated,
		TenantID:       "22222222-2222-7222-8222-222222222222",
		Gcid:           "00000000-0000-7000-8000-000000001002",
		OccurredAt:     now.Format(time.RFC3339Nano),
		PublishedAt:    now.Format(time.RFC3339Nano),
		TraceParent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-local",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
		AtomID:         "00000000-0000-7000-8000-000000005e10",
		CourseID:       "33333333-3333-7333-8333-333333333333",
		Title:          "What is a Product Owner?",
	}
}

// TestOutboxPublisher_Publish_EncodesMCQGradingFields asserts the typed
// atom.Event → outbox row path threads the CHO-1627 MCQ grading fields
// (CorrectOptionID → field 26, AnswerCount → field 27) through the
// payload struct + payloadAsMap into the binary-protobuf row payload, so
// they round-trip via the generated AtomCreated binding.
func TestOutboxPublisher_Publish_EncodesMCQGradingFields(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, SourceProject: "chora-local"})

	const correctOptionID = "44444444-4444-7444-8444-444444444444"
	ev := atomCreatedEvent()
	ev.AtomType = atom.TypeMCQ
	ev.CorrectOptionID = correctOptionID
	ev.AnswerCount = 4

	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}

	var m creationv1.AtomCreated
	if err := proto.Unmarshal(rows[0].Payload, &m); err != nil {
		t.Fatalf("proto.Unmarshal row payload: %v", err)
	}
	if got := m.GetCorrectOptionId(); got != correctOptionID {
		t.Errorf("AtomCreated.correct_option_id = %q; want %q", got, correctOptionID)
	}
	if got := m.GetAnswerCount(); got != 4 {
		t.Errorf("AtomCreated.answer_count = %d; want 4", got)
	}
}

// TestOutboxPublisher_Publish_NonMCQOmitsGradingFields asserts that a
// non-MCQ atom.Event (no CorrectOptionID / AnswerCount) leaves the grading
// fields at their proto3 defaults on the wire — the typed path must not
// fabricate a correct option for non-MCQ atoms.
func TestOutboxPublisher_Publish_NonMCQOmitsGradingFields(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, SourceProject: "chora-local"})

	ev := atomCreatedEvent() // no CorrectOptionID, AnswerCount == 0
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}

	var m creationv1.AtomCreated
	if err := proto.Unmarshal(rows[0].Payload, &m); err != nil {
		t.Fatalf("proto.Unmarshal row payload: %v", err)
	}
	if got := m.GetCorrectOptionId(); got != "" {
		t.Errorf("AtomCreated.correct_option_id = %q; want empty for non-MCQ", got)
	}
	if got := m.GetAnswerCount(); got != 0 {
		t.Errorf("AtomCreated.answer_count = %d; want 0 for non-MCQ", got)
	}
}

func TestOutboxPublisher_Publish_WritesRowToStore(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
	})

	ev := atomCreatedEvent()
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != "chora.creation.atom.created.v1" {
		t.Errorf("row.Topic = %q; want chora.creation.atom.created.v1", row.Topic)
	}
	if row.TenantID != ev.TenantID {
		t.Errorf("row.TenantID = %q; want %q", row.TenantID, ev.TenantID)
	}
	if row.GCID != ev.Gcid {
		t.Errorf("row.GCID = %q; want %q", row.GCID, ev.Gcid)
	}
	if row.AggregateType != "atom" {
		t.Errorf("row.AggregateType = %q; want atom", row.AggregateType)
	}
	if row.AggregateID != ev.AtomID {
		t.Errorf("row.AggregateID = %q; want %q", row.AggregateID, ev.AtomID)
	}
	if row.IdempotencyKey != ev.IdempotencyKey {
		t.Errorf("row.IdempotencyKey = %q; want %q", row.IdempotencyKey, ev.IdempotencyKey)
	}
	if row.EventType != "creation.atom.created" {
		t.Errorf("row.EventType = %q; want creation.atom.created", row.EventType)
	}
}

func TestOutboxPublisher_Publish_StampsEnvelopeFields(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	pub := outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
		Now:           func() time.Time { return now },
	})
	ev := atomCreatedEvent()
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	env := rows[0].Envelope

	for _, key := range []string{
		"event_id", "idempotency_key", "tenant_id", "occurred_at", "published_at",
		"traceparent", "source_project", "source_service", "schema_version",
	} {
		if env[key] == "" {
			t.Errorf("envelope.%s empty; want non-empty (mandatory per CLAUDE.md §6)", key)
		}
	}
	if env["source_project"] != "chora-local" {
		t.Errorf("envelope.source_project = %q; want chora-local", env["source_project"])
	}
	if env["source_service"] != "chora-creation" {
		t.Errorf("envelope.source_service = %q; want chora-creation", env["source_service"])
	}
	if env["schema_version"] != "1" {
		t.Errorf("envelope.schema_version = %q; want 1", env["schema_version"])
	}
	if env["tenant_id"] != ev.TenantID {
		t.Errorf("envelope.tenant_id = %q; want %q", env["tenant_id"], ev.TenantID)
	}
	if env["idempotency_key"] != ev.IdempotencyKey {
		t.Errorf("envelope.idempotency_key = %q; want %q", env["idempotency_key"], ev.IdempotencyKey)
	}
}

func TestOutboxPublisher_Publish_RejectsNilStore(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{})
	err := pub.Publish(context.Background(), atomCreatedEvent())
	if err == nil {
		t.Errorf("Publish without store = nil err; want error")
	}
}

func TestOutboxPublisher_Publish_RejectsInvalidTopic(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	ev := atomCreatedEvent()
	ev.Type = "chora.atomic.events" // legacy non-canonical
	err := pub.Publish(context.Background(), ev)
	if err == nil {
		t.Errorf("Publish with legacy topic err = nil; want error")
	}
}

func TestOutboxPublisher_Publish_PayloadIsBinaryProtobuf(t *testing.T) {
	// Task #33 (2026-05-16) — outbox payload encoding migrated from JSON
	// to canonical binary protobuf so the broker's binary schema (BINARY
	// encoding) accepts the row at publish. The dispatcher passes bytes
	// through unchanged, so the wire format of the payload column drives
	// schema validation. Asserts the payload is NOT JSON anymore + the
	// first top-level field is the nested envelope (field 1) per the
	// chora.common.v1.EventEnvelope-derived layout.
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, SourceProject: "chora-local"})
	ev := atomCreatedEvent()
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}

	// Should NOT be JSON anymore — fail loud if a regression re-introduces it.
	var jsonProbe map[string]any
	if err := json.Unmarshal(rows[0].Payload, &jsonProbe); err == nil {
		t.Fatalf("payload decoded as JSON — outbox payload regressed to JSON encoding (post task #33 must be binary protobuf): %s", string(rows[0].Payload))
	}

	// First top-level tag must be envelope (field 1, bytes type).
	num, typ, n := protowire.ConsumeTag(rows[0].Payload)
	if n < 0 {
		t.Fatalf("invalid leading tag in payload")
	}
	if num != 1 {
		t.Fatalf("first field = %d; want 1 (envelope)", num)
	}
	if typ != protowire.BytesType {
		t.Fatalf("envelope wire type = %d; want %d (length-delimited)", typ, protowire.BytesType)
	}

	// Walk every top-level tag; assert no field number exceeds the
	// chora.creation.atom.created.v1 encoder range [1..11] (field 11 =
	// course_id, added 2026-06-01 to restore the course association the
	// Task #33 JSON→binary flip dropped).
	rem := rows[0].Payload
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(rows[0].Payload)-len(rem))
		}
		if num < 1 || num > 11 {
			t.Fatalf("field number %d out of schema range [1..11]", num)
		}
		rem = rem[n:]
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid length-delimited value for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint value for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
}

func TestOutboxPublisher_Publish_DuplicateIdempotencyKeyError(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, SourceProject: "chora-local"})
	ev := atomCreatedEvent()
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish 1: %v", err)
	}
	// Re-publish same event → store rejects duplicate.
	err := pub.Publish(context.Background(), ev)
	if err == nil {
		t.Errorf("expected duplicate idempotency_key rejection on second Publish")
	}
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Errorf("err = %v; want ErrDuplicateIdempotencyKey", err)
	}
}

func TestOutboxPublisher_Publish_DefaultsSourceProjectAndService(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store}) // no project/service
	ev := atomCreatedEvent()
	ev.SourceProject = ""
	ev.SourceService = ""
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].Envelope["source_project"] != "chora-local" {
		t.Errorf("default source_project = %q; want chora-local", rows[0].Envelope["source_project"])
	}
	if rows[0].Envelope["source_service"] != "chora-creation" {
		t.Errorf("default source_service = %q; want chora-creation", rows[0].Envelope["source_service"])
	}
}

// Compile-time check that OutboxPublisher satisfies atom.EventPublisher.
var _ atom.EventPublisher = (*outbox.Publisher)(nil)

// guard against accidental import-rename surprises.
func TestPublisher_TopicValidator_AcceptsCanonical(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	for _, topic := range []atom.EventType{
		atom.EventTypeAtomCreated,
		atom.EventTypeAtomRevised,
	} {
		ev := atomCreatedEvent()
		ev.EventID = string(topic) + "-id" // unique idempotency_key per iter
		ev.IdempotencyKey = string(topic) + "-idem"
		ev.AtomID = string(topic) + "-atom"
		ev.Type = topic
		if err := pub.Publish(context.Background(), ev); err != nil {
			t.Errorf("Publish %s: %v", topic, err)
		}
	}
	if !strings.HasPrefix(string(atom.EventTypeAtomCreated), "chora.creation.") {
		t.Errorf("EventTypeAtomCreated should be in chora.creation.* namespace")
	}
}
