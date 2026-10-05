package outbox

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// payloadAsMap is what stands between the typed atom.Event and the binary
// encoder. chora.creation.atom.updated.v1 (ADR-244 D5) needs its own
// projection: changed_fields is the field the KG-invalidation consumer
// branches on, and updated_at is the mutation instant, not created_at.
func TestPayloadAsMap_AtomUpdated_CarriesChangedFieldsAndSnapshot(t *testing.T) {
	updatedAt := time.Date(2026, 8, 16, 9, 30, 0, 123456789, time.UTC)
	body := payload{
		AtomID:            "019f278f-5acb-7415-a526-eb852b6409c7",
		GCID:              "00000000-0000-7000-8000-000000001999",
		Title:             "Comparing Fractions",
		AtomType:          "mcq",
		Status:            "published",
		Difficulty:        3,
		Tags:              []string{"fractions", "number-sense"},
		Stem:              "Which fraction is larger?",
		Subject:           "math",
		AuthorNote:        "keep the denominators small",
		CognitiveLevel:    "analysis",
		ImdaDimensionTags: []string{"transparency"},
		MediaAssets: []atom.MediaAsset{{
			Type: "image", URL: "gs://chora-atom-media-dev/f.png",
			AltText: "two fraction bars", MIME: "image/png", SizeBytes: 2048,
		}},
		ChangedFields: []string{"stem", "difficulty"},
	}

	m := payloadAsMap(string(atom.EventTypeAtomUpdated), body, updatedAt)

	cf, ok := m["changed_fields"].([]string)
	if !ok || len(cf) != 2 || cf[0] != "stem" || cf[1] != "difficulty" {
		t.Fatalf("changed_fields = %#v; want []string{stem, difficulty}", m["changed_fields"])
	}
	ts, ok := m["updated_at"].(time.Time)
	if !ok || !ts.Equal(updatedAt) {
		t.Fatalf("updated_at = %#v; want the mutation instant %v", m["updated_at"], updatedAt)
	}
	if _, present := m["created_at"]; present {
		t.Errorf("atom.updated must not carry created_at: %#v", m["created_at"])
	}
	tags, ok := m["tags"].([]string)
	if !ok || len(tags) != 2 {
		t.Errorf("tags = %#v; want the topic axis carried through", m["tags"])
	}
	if m["status"] != "published" {
		t.Errorf("status = %#v; want published", m["status"])
	}
	if m["stem"] != "Which fraction is larger?" {
		t.Errorf("stem = %#v", m["stem"])
	}
	if m["subject"] != "math" {
		t.Errorf("subject = %#v", m["subject"])
	}
	if m["author_note"] != "keep the denominators small" {
		t.Errorf("author_note = %#v", m["author_note"])
	}
	if m["cognitive_level"] != "analysis" {
		t.Errorf("cognitive_level = %#v", m["cognitive_level"])
	}
	imda, ok := m["imda_dimension_tags"].([]string)
	if !ok || len(imda) != 1 || imda[0] != "transparency" {
		t.Errorf("imda_dimension_tags = %#v; want [transparency]", m["imda_dimension_tags"])
	}
	assets, ok := m["media_assets"].([]map[string]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("media_assets = %#v; want one coercible entry", m["media_assets"])
	}
	if assets[0]["url"] != "gs://chora-atom-media-dev/f.png" || assets[0]["size_bytes"] != int64(2048) {
		t.Errorf("media_assets[0] = %#v", assets[0])
	}
}

// An atom with nothing but the mandatory fields must not invent keys.
func TestPayloadAsMap_AtomUpdated_OmitsAbsentMetadata(t *testing.T) {
	m := payloadAsMap(string(atom.EventTypeAtomUpdated), payload{
		AtomID:        "a1",
		Status:        "published",
		ChangedFields: []string{"title"},
	}, time.Now().UTC())

	for _, key := range []string{"tags", "subject", "author_note", "cognitive_level", "imda_dimension_tags", "media_assets"} {
		if _, present := m[key]; present {
			t.Errorf("absent %s must be omitted, got %#v", key, m[key])
		}
	}
}

// End to end through the publisher: the row that lands in outbox_events must
// carry BINARY protobuf. The topic is schema-bound, so a JSON fallback here is
// not a soft failure - the broker rejects it and the event dead-letters.
func TestPublish_AtomUpdated_WritesBinaryProtoRow(t *testing.T) {
	store := NewInMemoryStore()
	p := NewPublisher(PublisherConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
		Now:           func() time.Time { return time.Now().UTC() },
	})

	ev := atom.Event{
		EventID:        "019f278f-0000-7000-8000-000000000002",
		IdempotencyKey: "atom-1:updated:2026-08-16T09:30:00.123456789Z",
		Type:           atom.EventTypeAtomUpdated,
		TenantID:       "11111111-1111-7111-8111-111111111111",
		Gcid:           "00000000-0000-7000-8000-000000001999",
		OccurredAt:     "2026-08-16T09:30:00.123456789Z",
		AtomID:         "atom-1",
		Title:          "Comparing Fractions",
		Status:         string(atom.StatusPublished),
		Stem:           "Which fraction is larger?",
		ChangedFields:  []string{"stem"},
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
	if rows[0].Topic != "chora.creation.atom.updated.v1" {
		t.Errorf("topic = %q", rows[0].Topic)
	}
	if rows[0].Envelope["traceparent"] == "" {
		t.Error("outbox envelope must carry traceparent")
	}

	var got creationv1.AtomUpdated
	if err := proto.Unmarshal(rows[0].Payload, &got); err != nil {
		t.Fatalf("outbox payload is not binary AtomUpdated (the broker would reject it): %v", err)
	}
	if got.GetAtomId() != "atom-1" {
		t.Errorf("decoded atom_id = %q; want atom-1", got.GetAtomId())
	}
	if cf := got.GetChangedFields(); len(cf) != 1 || cf[0] != "stem" {
		t.Errorf("decoded changed_fields = %v; want [stem]", cf)
	}
	if ts := got.GetUpdatedAt(); ts == nil {
		t.Error("decoded updated_at is nil; the mutation instant must ride the wire")
	}
}
