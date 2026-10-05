package outbox

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// payloadAsMap feeds the protobuf encoder, which reads payload["tags"] into
// wire field 7 (topic_node_ids) — the field chora-consumption projects into
// atom_index.topic_tags. Before CHO-2142 payloadAsMap never emitted a "tags"
// key, so every typed-event publish (atom.created.v1 / atom.published.v1)
// reached the wire tagless. These tests pin the key onto the map.
func TestPayloadAsMap_EmitsTagsForPublished(t *testing.T) {
	body := payload{
		AtomID: "019f278f-5acb-7415-a526-eb852b6409c7",
		Title:  "Comparing Fractions",
		Tags:   []string{"fractions", "number-sense"},
	}
	m := payloadAsMap(string(atom.EventTypeAtomPublished), body, time.Now().UTC())

	got, ok := m["tags"].([]string)
	if !ok {
		t.Fatalf("payloadAsMap must emit a []string \"tags\" key, got %#v", m["tags"])
	}
	if len(got) != 2 || got[0] != "fractions" || got[1] != "number-sense" {
		t.Fatalf("tags not carried: %v", got)
	}
}

func TestPayloadAsMap_EmitsTagsForCreated(t *testing.T) {
	body := payload{AtomID: "a1", Tags: []string{"fractions"}}
	m := payloadAsMap(string(atom.EventTypeAtomCreated), body, time.Now().UTC())

	got, ok := m["tags"].([]string)
	if !ok || len(got) != 1 || got[0] != "fractions" {
		t.Fatalf("created must carry tags, got %#v", m["tags"])
	}
}

func TestPayloadAsMap_OmitsEmptyTags(t *testing.T) {
	// An untagged atom must NOT emit a tags key. The consumption upsert is
	// non-blanking (empty EXCLUDED preserves existing topic_tags), but emitting
	// an empty slice would still be a lie on the wire.
	body := payload{AtomID: "a1"}
	m := payloadAsMap(string(atom.EventTypeAtomPublished), body, time.Now().UTC())

	if _, present := m["tags"]; present {
		t.Fatalf("empty tags must be omitted, got %#v", m["tags"])
	}
}

// The orphan_created topic uses a purpose-built key set — tags are not part of
// its schema and must not leak in.
func TestPayloadAsMap_OrphanCreatedCarriesNoTags(t *testing.T) {
	body := payload{
		AtomID:             "orphan-1",
		OrphanedFromAtomID: "orig-1",
		Tags:               []string{"fractions"},
	}
	m := payloadAsMap(string(atom.EventTypeAtomOrphanCreated), body, time.Now().UTC())

	if _, present := m["tags"]; present {
		t.Fatal("orphan_created must not carry tags")
	}
}

// Publish must thread atom.Event.Tags through to the encoded outbox row.
func TestPublish_ThreadsTagsIntoOutboxRow(t *testing.T) {
	store := NewInMemoryStore()
	p := NewPublisher(PublisherConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
		Now:           func() time.Time { return time.Now().UTC() },
	})

	ev := atom.Event{
		EventID:        "019f278f-0000-7000-8000-000000000001",
		IdempotencyKey: "atom-1:published:rev-1",
		Type:           atom.EventTypeAtomPublished,
		TenantID:       "11111111-1111-7111-8111-111111111111",
		Gcid:           "00000000-0000-7000-8000-000000001999",
		AtomID:         "atom-1",
		Title:          "Comparing Fractions",
		Tags:           []string{"fractions"},
		TraceParent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	if err := p.Publish(t.Context(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rows, err := store.FetchPending(t.Context(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 outbox row, got %d", len(rows))
	}
	// traceparent is a MANDATORY envelope field — a missing one silently fails
	// envelope validation downstream.
	if rows[0].Envelope["traceparent"] == "" {
		t.Fatal("outbox envelope must carry traceparent")
	}
	if len(rows[0].Payload) == 0 {
		t.Fatal("outbox row must carry an encoded payload")
	}
}
