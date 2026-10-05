// atom_updated_test.go - binary wire encoding for
// chora.creation.atom.updated.v1 (ADR-244 D5).
//
// RED before the encoder lands. The topic is Schema-Registry bound to
// chora-creation-atom-updated-v2 (PROTOCOL_BUFFER), so the JSON fallback is
// not a degraded path here: it is rejected at publish with "Invalid binary
// proto message" and dead-letters. Every assertion below therefore round-trips
// the hand-rolled protowire bytes through the CANONICAL generated binding
// (creationv1.AtomUpdated) - the same decode chora-consumption performs.
//
// Schema - chora-contracts/proto/events-flat/creation/atom/updated.v2.proto:
//
//	1  Envelope  envelope
//	2  string    atom_id
//	3  AtomType  question_type            (enum varint; "atom_type" map key)
//	4  AtomStatus status                  (enum varint)
//	5  string    title
//	6  string    author_gcid
//	7  repeated string topic_node_ids     ("tags" fallback, mirror published)
//	8  int32     difficulty
//	9  string    locale
//	10 repeated string changed_fields
//	11 Timestamp updated_at
//	20 string    stem
//	21 string    subject
//	22 CognitiveLevel cognitive_level     (enum varint)
//	23 repeated ImdaDimensionTag imda_dimension_tags (packed enum)
//	24 string    author_note
//	25 repeated AtomMediaAsset media_assets
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

const atomUpdatedTopic = "chora.creation.atom.updated.v1"

// atomUpdatedPayload is the loose map the outbox projection (payloadAsMap)
// hands the encoder for a metadata PATCH on a published atom.
func atomUpdatedPayload(env protomarshal.Envelope) map[string]any {
	return map[string]any{
		"atom_id":             "01971a90-aaaa-7000-8000-000000000001",
		"atom_type":           "mcq",
		"status":              "published",
		"title":               "Comparing fractions",
		"author_gcid":         env.GCID,
		"tags":                []string{"fractions", "number-sense"},
		"difficulty":          int32(3),
		"changed_fields":      []string{"stem", "difficulty"},
		"updated_at":          env.OccurredAt,
		"stem":                "Which fraction is larger, 3/4 or 5/8?",
		"subject":             "math",
		"cognitive_level":     "analysis",
		"imda_dimension_tags": []string{"transparency", "fairness_human_oversight"},
		"author_note":         "keep the denominators small",
		"media_assets": []map[string]any{{
			"type":       "image",
			"url":        "gs://chora-atom-media-dev/fractions.png",
			"alt_text":   "two fraction bars",
			"mime":       "image/png",
			"size_bytes": int64(2048),
		}},
	}
}

// TestMarshalAtomUpdated_RoundTripsIntoCanonicalBinding - the encoder must
// produce bytes the generated binding parses, field for field.
func TestMarshalAtomUpdated_RoundTripsIntoCanonicalBinding(t *testing.T) {
	env := fixedEnvelope()

	bz, err := protomarshal.MarshalPayload(atomUpdatedTopic, env, atomUpdatedPayload(env))
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got creationv1.AtomUpdated
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into the canonical binding failed - Schema Registry would REJECT this publish and it would dead-letter: %v", err)
	}

	if got.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %q; want %q", got.GetEnvelope().GetEventId(), env.EventID)
	}
	if got.GetEnvelope().GetTenantId() != env.TenantID {
		t.Errorf("envelope.tenant_id = %q; want %q", got.GetEnvelope().GetTenantId(), env.TenantID)
	}
	if got.GetEnvelope().GetTraceparent() != env.Traceparent {
		t.Errorf("envelope.traceparent = %q; want %q", got.GetEnvelope().GetTraceparent(), env.Traceparent)
	}
	if got.GetAtomId() != "01971a90-aaaa-7000-8000-000000000001" {
		t.Errorf("atom_id = %q", got.GetAtomId())
	}
	if got.GetQuestionType() != creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
		t.Errorf("question_type = %v; want ATOM_TYPE_MULTIPLE_CHOICE", got.GetQuestionType())
	}
	if got.GetStatus() != creationv1.AtomStatus_ATOM_STATUS_PUBLISHED {
		t.Errorf("status = %v; want ATOM_STATUS_PUBLISHED", got.GetStatus())
	}
	if got.GetTitle() != "Comparing fractions" {
		t.Errorf("title = %q", got.GetTitle())
	}
	if got.GetAuthorGcid() != env.GCID {
		t.Errorf("author_gcid = %q; want %q", got.GetAuthorGcid(), env.GCID)
	}
	if ids := got.GetTopicNodeIds(); len(ids) != 2 || ids[0] != "fractions" || ids[1] != "number-sense" {
		t.Errorf("topic_node_ids = %v; want the tags fallback [fractions number-sense]", ids)
	}
	if got.GetDifficulty() != 3 {
		t.Errorf("difficulty = %d; want 3", got.GetDifficulty())
	}
	// changed_fields is the field the KG-invalidation consumer branches on -
	// it is the whole point of the event.
	if cf := got.GetChangedFields(); len(cf) != 2 || cf[0] != "stem" || cf[1] != "difficulty" {
		t.Errorf("changed_fields = %v; want [stem difficulty] in order", cf)
	}
	if ts := got.GetUpdatedAt(); ts == nil || !ts.AsTime().Equal(env.OccurredAt) {
		t.Errorf("updated_at = %v; want %v", ts, env.OccurredAt)
	}
	if got.GetStem() != "Which fraction is larger, 3/4 or 5/8?" {
		t.Errorf("stem = %q", got.GetStem())
	}
	if got.GetSubject() != "math" {
		t.Errorf("subject = %q; want math", got.GetSubject())
	}
	if got.GetCognitiveLevel() != creationv1.CognitiveLevel_COGNITIVE_LEVEL_ANALYSIS {
		t.Errorf("cognitive_level = %v; want ANALYSIS", got.GetCognitiveLevel())
	}
	wantTags := []creationv1.ImdaDimensionTag{
		creationv1.ImdaDimensionTag_IMDA_DIMENSION_TAG_TRANSPARENCY,
		creationv1.ImdaDimensionTag_IMDA_DIMENSION_TAG_FAIRNESS_HUMAN_OVERSIGHT,
	}
	if tags := got.GetImdaDimensionTags(); len(tags) != 2 || tags[0] != wantTags[0] || tags[1] != wantTags[1] {
		t.Errorf("imda_dimension_tags = %v; want %v", got.GetImdaDimensionTags(), wantTags)
	}
	if got.GetAuthorNote() != "keep the denominators small" {
		t.Errorf("author_note = %q", got.GetAuthorNote())
	}
	assets := got.GetMediaAssets()
	if len(assets) != 1 {
		t.Fatalf("media_assets = %d entries; want 1", len(assets))
	}
	if assets[0].GetType() != creationv1.AtomMediaAssetType_ATOM_MEDIA_ASSET_TYPE_IMAGE {
		t.Errorf("media_assets[0].type = %v; want IMAGE", assets[0].GetType())
	}
	if assets[0].GetUrl() != "gs://chora-atom-media-dev/fractions.png" {
		t.Errorf("media_assets[0].url = %q", assets[0].GetUrl())
	}
	if assets[0].GetAltText() != "two fraction bars" {
		t.Errorf("media_assets[0].alt_text = %q", assets[0].GetAltText())
	}
	if assets[0].GetMime() != "image/png" {
		t.Errorf("media_assets[0].mime = %q", assets[0].GetMime())
	}
	if assets[0].GetSizeBytes() != 2048 {
		t.Errorf("media_assets[0].size_bytes = %d; want 2048", assets[0].GetSizeBytes())
	}
}

// TestMarshalAtomUpdated_MinimalPayloadElidesDefaults - an atom carrying only
// the load-bearing fields still encodes cleanly, with proto3 defaults elided.
func TestMarshalAtomUpdated_MinimalPayloadElidesDefaults(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload(atomUpdatedTopic, env, map[string]any{
		"atom_id":        "01971a90-aaaa-7000-8000-000000000002",
		"author_gcid":    env.GCID,
		"status":         "published",
		"changed_fields": []string{"title"},
		"updated_at":     env.OccurredAt,
		"title":          "Renamed",
	})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var got creationv1.AtomUpdated
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got.GetStem() != "" || got.GetSubject() != "" || got.GetAuthorNote() != "" {
		t.Errorf("absent metadata must stay at proto3 defaults, got stem=%q subject=%q note=%q",
			got.GetStem(), got.GetSubject(), got.GetAuthorNote())
	}
	if len(got.GetMediaAssets()) != 0 || len(got.GetImdaDimensionTags()) != 0 {
		t.Errorf("absent repeated fields must stay empty, got media=%v imda=%v",
			got.GetMediaAssets(), got.GetImdaDimensionTags())
	}
	if cf := got.GetChangedFields(); len(cf) != 1 || cf[0] != "title" {
		t.Errorf("changed_fields = %v; want [title]", cf)
	}
}

// TestMarshalAtomUpdated_UnknownImdaLabel_FailsLoud - an unknown label must
// error rather than encode as UNSPECIFIED: a silently-blanked governance tag
// is exactly the kind of evidence gap the IMDA dimensions exist to prevent.
func TestMarshalAtomUpdated_UnknownImdaLabel_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := atomUpdatedPayload(env)
	payload["imda_dimension_tags"] = []string{"risk_levels"} // pre-ADR-141 label

	_, err := protomarshal.MarshalPayload(atomUpdatedTopic, env, payload)
	if err == nil {
		t.Fatal("expected a loud error for an unknown IMDA label, got nil")
	}
	if protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("topic has no encoder at all, so this proves nothing about the label: %v", err)
	}
}

// TestMarshalAtomUpdated_UnknownMediaType_FailsLoud - same contract for the
// media asset kind; encoding an unknown kind as UNSPECIFIED would strand the
// asset with no way for a consumer to tell what it is.
func TestMarshalAtomUpdated_UnknownMediaType_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := atomUpdatedPayload(env)
	payload["media_assets"] = []map[string]any{{
		"type": "hologram", "url": "gs://x/y", "mime": "image/png", "size_bytes": int64(1),
	}}

	_, err := protomarshal.MarshalPayload(atomUpdatedTopic, env, payload)
	if err == nil {
		t.Fatal("expected a loud error for an unknown media asset type, got nil")
	}
	if protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("topic has no encoder at all, so this proves nothing about the type: %v", err)
	}
}
