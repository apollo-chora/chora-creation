// protomarshal_atom_published_test.go — binary-encoding round-trip for
// chora.creation.atom.published.v1. RED before encodeAtomPublished lands.
//
// Without a registered encoder, MarshalPayload returns ErrUnsupportedTopic and
// the outbox falls back to JSON, which the BINARY-encoded Schema Registry
// schema REJECTS at publish → the event dead-letters. This test pins the
// answerability contract (status / current revision pointer / MCQ answer key /
// has_open_ended_question) through the generated AtomPublished binding.
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// TestMarshalPayload_AtomPublished_BinaryEncodes — an MCQ publish event
// round-trips: status=PUBLISHED (4), question_type (3), current_revision_id
// (10), current_revision_number (11), published_at (12), correct_option_id
// (26), answer_count (27), has_open_ended_question=false (28).
func TestMarshalPayload_AtomPublished_BinaryEncodes(t *testing.T) {
	env := fixedEnvelope()
	const (
		atomID          = "01971a90-aaaa-7000-8000-000000000001"
		correctOptionID = "019e58ea-7777-7b74-9f1d-0000000000aa"
		revisionID      = "019e58ea-9999-7b74-9f1d-0000000000bb"
	)
	payload := map[string]any{
		"atom_id":                 atomID,
		"atom_type":               int32(1), // ATOM_TYPE_MULTIPLE_CHOICE
		"status":                  "published",
		"title":                   "Photosynthesis basics",
		"author_gcid":             env.GCID,
		"difficulty":              int32(3),
		"current_revision_id":     revisionID,
		"current_revision_number": int32(2),
		"published_at":            env.OccurredAt,
		"stem":                    "What gas do plants release?",
		"subject":                 "biology",
		"author_note":             "demo",
		"correct_option_id":       correctOptionID,
		"answer_count":            int32(4),
		"has_open_ended_question": false,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v (want a registered binary encoder, NOT ErrUnsupportedTopic)", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	var m creationv1.AtomPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomPublished: %v", err)
	}
	if m.GetStatus() != creationv1.AtomStatus_ATOM_STATUS_PUBLISHED {
		t.Errorf("AtomPublished.status = %v; want ATOM_STATUS_PUBLISHED (field 4)", m.GetStatus())
	}
	if got := m.GetAtomId(); got != atomID {
		t.Errorf("AtomPublished.atom_id = %q; want %q", got, atomID)
	}
	if got := m.GetQuestionType(); got != creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
		t.Errorf("AtomPublished.question_type = %v; want ATOM_TYPE_MULTIPLE_CHOICE (field 3)", got)
	}
	if got := m.GetCurrentRevisionId(); got != revisionID {
		t.Errorf("AtomPublished.current_revision_id = %q; want %q (field 10)", got, revisionID)
	}
	if got := m.GetCurrentRevisionNumber(); got != 2 {
		t.Errorf("AtomPublished.current_revision_number = %d; want 2 (field 11)", got)
	}
	if m.GetPublishedAt() == nil {
		t.Errorf("AtomPublished.published_at nil; want set (field 12)")
	}
	if got := m.GetCorrectOptionId(); got != correctOptionID {
		t.Errorf("AtomPublished.correct_option_id = %q; want %q (field 26)", got, correctOptionID)
	}
	if got := m.GetAnswerCount(); got != 4 {
		t.Errorf("AtomPublished.answer_count = %d; want 4 (field 27)", got)
	}
	if m.GetHasOpenEndedQuestion() {
		t.Errorf("AtomPublished.has_open_ended_question = true; want false (field 28)")
	}
}

// TestMarshalPayload_AtomPublished_OEAnswerable — an OE atom carries
// has_open_ended_question=true (28) with no MCQ answer key (26/27 elided).
func TestMarshalPayload_AtomPublished_OEAnswerable(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":                 "01971a90-aaaa-7000-8000-000000000002",
		"atom_type":               int32(8), // ATOM_TYPE_ESSAY
		"status":                  "published",
		"author_gcid":             env.GCID,
		"current_revision_id":     "rev-oe-1",
		"current_revision_number": int32(1),
		"published_at":            env.OccurredAt,
		"has_open_ended_question": true,
		// no correct_option_id, no answer_count
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m creationv1.AtomPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if !m.GetHasOpenEndedQuestion() {
		t.Errorf("AtomPublished.has_open_ended_question = false; want true (field 28)")
	}
	if got := m.GetStatus(); got != creationv1.AtomStatus_ATOM_STATUS_PUBLISHED {
		t.Errorf("status = %v; want PUBLISHED", got)
	}
	if got := m.GetCorrectOptionId(); got != "" {
		t.Errorf("correct_option_id = %q; want empty for OE", got)
	}
	if got := m.GetAnswerCount(); got != 0 {
		t.Errorf("answer_count = %d; want 0 for OE", got)
	}
}

// TestMarshalPayload_AtomPublished_EncodesCognitiveLevel — WS-C3 (CHO-2082):
// the publish snapshot carries the atom's ORIGINAL-Bloom level (field 22,
// enum) so chora-consumption's atom_index projection can level-filter the
// campaign question lane. Absent/blank level elides the field (proto3).
func TestMarshalPayload_AtomPublished_EncodesCognitiveLevel(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":         "01971a90-aaaa-7000-8000-000000000002",
		"status":          "published",
		"cognitive_level": "application",
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m creationv1.AtomPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m.GetCognitiveLevel(); got != creationv1.CognitiveLevel_COGNITIVE_LEVEL_APPLICATION {
		t.Fatalf("cognitive_level = %v, want APPLICATION", got)
	}

	// Blank level elides the field entirely.
	delete(payload, "cognitive_level")
	bz2, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("marshal (no level): %v", err)
	}
	var m2 creationv1.AtomPublished
	if err := proto.Unmarshal(bz2, &m2); err != nil {
		t.Fatalf("unmarshal (no level): %v", err)
	}
	if m2.GetCognitiveLevel() != creationv1.CognitiveLevel_COGNITIVE_LEVEL_UNSPECIFIED {
		t.Fatalf("absent level must stay UNSPECIFIED, got %v", m2.GetCognitiveLevel())
	}
}
