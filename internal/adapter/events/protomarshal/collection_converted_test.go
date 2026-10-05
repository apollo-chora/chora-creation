// collection_converted_test.go — ADR-233 binary-encoder slice (WS-4), RED-first.
//
// chora.creation.collection.converted_to_study_list.v1 is the ONE collection
// topic that is BINARY (Schema-Registry bound to
// chora-creation-collection-converted_to_study_list-v1). Its five siblings stay
// JSON-wire — they are schemaless because they have no consumer. This one has a
// REAL cross-domain consumer (chora-consumption builds the LearningPath from
// it), so the contract is broker-validated.
//
// Getting the encoding wrong = the publish is rejected by the schema and
// dead-letters. So this test does not merely assert bytes: it round-trips them
// back through proto.Unmarshal into the CANONICAL generated binding
// (creationv1.CollectionConvertedToStudyList). If the hand-rolled protowire
// bytes and the real schema ever disagree, this fails.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

const convertTopic = "chora.creation.collection.converted_to_study_list.v1"

func convertEnvelope() protomarshal.Envelope {
	return protomarshal.Envelope{
		EventID:        "01970000-0000-7000-8000-0000000000e1",
		IdempotencyKey: "01970000-0000-7000-8000-0000000000e1",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		GCID:           "01970000-0000-7000-9000-000000000001",
		OccurredAt:     time.Date(2026, 7, 14, 12, 0, 0, 123000000, time.UTC),
		PublishedAt:    time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "vendor=x",
		SourceProject:  "chora-local",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

func TestMarshalPayload_CollectionConvertedToStudyList_RoundTrips(t *testing.T) {
	t.Parallel()

	env := convertEnvelope()
	payload := map[string]any{
		"collection_id":       "01970000-0000-7000-7000-000000000001",
		"owner_gcid":          "01970000-0000-7000-9000-000000000001",
		"atom_ids":            []string{"atom-a", "atom-b", "atom-c"},
		"study_list_event_id": env.EventID,
	}

	bz, err := protomarshal.MarshalPayload(convertTopic, env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty wire bytes")
	}

	// THE assertion: the bytes must parse as the canonical schema message.
	var got creationv1.CollectionConvertedToStudyList
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into the canonical binding failed — Schema Registry would REJECT this publish and it would dead-letter: %v", err)
	}

	if got.GetCollectionId() != "01970000-0000-7000-7000-000000000001" {
		t.Errorf("collection_id = %q; want the curated collection", got.GetCollectionId())
	}
	if got.GetOwnerGcid() != "01970000-0000-7000-9000-000000000001" {
		t.Errorf("owner_gcid = %q", got.GetOwnerGcid())
	}
	if got.GetStudyListEventId() != env.EventID {
		t.Errorf("study_list_event_id = %q; want the event's own id %q (consumption dedups on this)",
			got.GetStudyListEventId(), env.EventID)
	}

	// Order is load-bearing — the proto pins it and the learner's sequence
	// depends on it.
	wantAtoms := []string{"atom-a", "atom-b", "atom-c"}
	if len(got.GetAtomIds()) != len(wantAtoms) {
		t.Fatalf("atom_ids = %v; want %v", got.GetAtomIds(), wantAtoms)
	}
	for i := range wantAtoms {
		if got.GetAtomIds()[i] != wantAtoms[i] {
			t.Errorf("atom_ids[%d] = %q; want %q (curated order must survive the wire)",
				i, got.GetAtomIds()[i], wantAtoms[i])
		}
	}
}

// The envelope is field 1 and carries the 11 mandatory CLAUDE.md §6 fields.
// chora-consumption reads tenant_id + gcid + traceparent off it.
func TestMarshalPayload_CollectionConvertedToStudyList_EnvelopeSurvives(t *testing.T) {
	t.Parallel()

	env := convertEnvelope()
	bz, err := protomarshal.MarshalPayload(convertTopic, env, map[string]any{
		"collection_id":       "c-1",
		"owner_gcid":          "o-1",
		"atom_ids":            []string{"a-1"},
		"study_list_event_id": env.EventID,
	})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got creationv1.CollectionConvertedToStudyList
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	e := got.GetEnvelope()
	if e == nil {
		t.Fatal("envelope missing — the broker rejects an envelope-less event")
	}
	if e.GetEventId() != env.EventID {
		t.Errorf("event_id = %q; want %q", e.GetEventId(), env.EventID)
	}
	if e.GetIdempotencyKey() != env.IdempotencyKey {
		t.Errorf("idempotency_key = %q", e.GetIdempotencyKey())
	}
	if e.GetTenantId() != env.TenantID {
		t.Errorf("tenant_id = %q", e.GetTenantId())
	}
	if e.GetGcid() != env.GCID {
		t.Errorf("gcid = %q", e.GetGcid())
	}
	if e.GetTraceparent() != env.Traceparent {
		t.Errorf("traceparent = %q; W3C trace context is mandatory in the envelope", e.GetTraceparent())
	}
	if e.GetTracestate() != env.Tracestate {
		t.Errorf("tracestate = %q", e.GetTracestate())
	}
	if e.GetSourceProject() != "chora-local" || e.GetSourceService() != "chora-creation" {
		t.Errorf("source = %s/%s", e.GetSourceProject(), e.GetSourceService())
	}
	if e.GetSchemaVersion() != 1 {
		t.Errorf("schema_version = %d; want 1", e.GetSchemaVersion())
	}
	if e.GetOccurredAt() == nil || e.GetOccurredAt().GetSeconds() != env.OccurredAt.Unix() {
		t.Errorf("occurred_at = %v; want %v", e.GetOccurredAt(), env.OccurredAt)
	}
	if e.GetOccurredAt().GetNanos() != int32(env.OccurredAt.Nanosecond()) {
		t.Errorf("occurred_at nanos = %d; want %d", e.GetOccurredAt().GetNanos(), env.OccurredAt.Nanosecond())
	}
	if e.GetPublishedAt() == nil || e.GetPublishedAt().GetSeconds() != env.PublishedAt.Unix() {
		t.Errorf("published_at = %v", e.GetPublishedAt())
	}
}

// A conversion always has >= 1 atom (zero survivors is refused upstream with
// ErrNoEntitledAtoms), but the encoder must not corrupt bytes if handed an
// empty list — proto3 elides a zero-length repeated field.
func TestMarshalPayload_CollectionConvertedToStudyList_EmptyAtomsStillParses(t *testing.T) {
	t.Parallel()

	env := convertEnvelope()
	bz, err := protomarshal.MarshalPayload(convertTopic, env, map[string]any{
		"collection_id":       "c-1",
		"owner_gcid":          "o-1",
		"study_list_event_id": env.EventID,
	})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var got creationv1.CollectionConvertedToStudyList
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if len(got.GetAtomIds()) != 0 {
		t.Errorf("atom_ids = %v; want empty", got.GetAtomIds())
	}
}

// The []any shape arrives when the payload has been JSON-decoded upstream.
func TestMarshalPayload_CollectionConvertedToStudyList_AcceptsAnySliceAtoms(t *testing.T) {
	t.Parallel()

	env := convertEnvelope()
	bz, err := protomarshal.MarshalPayload(convertTopic, env, map[string]any{
		"collection_id":       "c-1",
		"owner_gcid":          "o-1",
		"atom_ids":            []any{"a-1", "a-2"},
		"study_list_event_id": env.EventID,
	})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var got creationv1.CollectionConvertedToStudyList
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if len(got.GetAtomIds()) != 2 || got.GetAtomIds()[0] != "a-1" {
		t.Errorf("atom_ids = %v; want [a-1 a-2]", got.GetAtomIds())
	}
}

// The five sibling collection topics have NO binary encoder — they are
// deliberately schemaless (JSON-wire). MarshalPayload must say so loudly rather
// than silently emit bytes for them.
func TestMarshalPayload_SiblingCollectionTopicsRemainUnsupported(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"chora.creation.collection.created.v1",
		"chora.creation.collection.updated.v1",
		"chora.creation.collection.atom_added.v1",
		"chora.creation.collection.atom_removed.v1",
		"chora.creation.collection.deleted.v1",
	} {
		_, err := protomarshal.MarshalPayload(topic, convertEnvelope(), map[string]any{})
		if err == nil {
			t.Errorf("%s: got a binary encoding; want ErrUnsupportedTopic (this topic is JSON-wire)", topic)
			continue
		}
		if !protomarshal.IsUnsupportedTopic(err) {
			t.Errorf("%s: err = %v; want ErrUnsupportedTopic", topic, err)
		}
	}
}
