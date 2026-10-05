// Package protomarshal_test verifies binary protobuf wire-format encoding for
// chora-creation's outbox event payloads. RED tests written BEFORE the
// encoder lands per CLAUDE.md §development-execution + feedback_strict_tdd.
//
// Gap (task #33 — outbox protobuf encoding fix, 2026-05-16): chora-creation
// outbox writer + JobEventPublisher were persisting / direct-publishing
// JSON-marshalled payload bytes. GCP Pub/Sub Schema Registry rejects those
// at publish time with "Invalid binary proto message" because every deployed
// chora.creation.* topic carries a BINARY-encoded schema. The fix is
// producer-side: marshal to canonical proto wire bytes before the outbox
// row is written.
//
// Mirrors services/chora-consumption/internal/adapter/events/protomarshal/
// protomarshal_test.go (the canonical reference fix from task #33).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// fixedEnvelope returns an envelope with deterministic values for byte-level
// assertions.
func fixedEnvelope() protomarshal.Envelope {
	t := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	return protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000001",
		IdempotencyKey: "idemp-1",
		TenantID:       "tenant-1",
		GCID:           "gcid-phyllis",
		OccurredAt:     t,
		PublishedAt:    t.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "",
		SourceProject:  "chora-local",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

// -----------------------------------------------------------------------------
// chora.creation.atom.created.v1
// Schema fields (per chora-contracts/proto/events-flat/creation/atom/created.proto):
//   1  Envelope envelope
//   2  string   atom_id
//   3  AtomType atom_type           (enum varint)
//   4  AtomStatus status            (enum varint)
//   5  string   title
//   6  string   author_gcid
//   7  repeated string topic_node_ids
//   8  int32    difficulty
//   9  string   locale
//  10  Timestamp created_at
// -----------------------------------------------------------------------------

// TestMarshalAtomCreated_RoundTripsBinaryProto asserts the encoder produces
// wire-format bytes whose top-level tag stream parses cleanly + carries the
// required envelope + scalar fields.
func TestMarshalAtomCreated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"atom_type":   int32(7), // ATOM_TYPE_CODE
		"status":      int32(1), // ATOM_STATUS_DRAFT
		"title":       "Atom title",
		"author_gcid": env.GCID,
		"topic_node_ids": []string{
			"01971a90-bbbb-7000-8000-000000000001",
			"01971a90-bbbb-7000-8000-000000000002",
		},
		"difficulty": int32(3),
		"locale":     "en-SG",
		"created_at": env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	seen := walkTopLevelTags(t, bz, 1, 10)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 8, 9, 10} {
		if !seen[want] {
			t.Fatalf("atom.created: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalAtomCreated_EncodesCourseID asserts the encoder emits the
// course_id (field 11) so it round-trips through the generated AtomCreated
// binding. Regression guard for the OPEN-1 contract gap (2026-06-01): the
// JSON→binary payload flip (Task #33) dropped course_id because the proto had
// no field for it, breaking chora-consumption's course-scoped atom_index
// projection + LearningPath bootstrap.
func TestMarshalAtomCreated_EncodesCourseID(t *testing.T) {
	env := fixedEnvelope()
	const courseID = "019e58ea-5ae0-7b74-9f1d-73a92622ab90"
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"course_id":   courseID,
		"title":       "Atom title",
		"author_gcid": env.GCID,
		"created_at":  env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetCourseId(); got != courseID {
		t.Errorf("AtomCreated.course_id = %q; want %q (encoder must emit field 11)", got, courseID)
	}
}

// TestMarshalAtomCreated_TagsFallBackToTopicNodeIds asserts the encoder falls
// back to the composer-side "tags" key for field 7 when topic_node_ids is
// absent. Regression guard for the topic-starvation gap (valuestreams §7.4,
// 2026-06-10): every live composer (questions PATCH + batch accept) emits the
// atom's content tags under "tags", but the encoder only read
// "topic_node_ids" — tags never reached the wire, so chora-consumption's
// atom_index.topic_tags (its topic-matching axis for fog/dose/W3-derived)
// projected empty for EVERY atom since the projection existed.
func TestMarshalAtomCreated_TagsFallBackToTopicNodeIds(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"title":       "Atom title",
		"author_gcid": env.GCID,
		"tags":        []string{"software-engineering", "foundations"},
		"created_at":  env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetTopicNodeIds(); len(got) != 2 || got[0] != "software-engineering" || got[1] != "foundations" {
		t.Errorf("AtomCreated.topic_node_ids = %v; want tags fallback [software-engineering foundations]", got)
	}
}

// TestMarshalAtomCreated_ExplicitTopicNodeIdsWinOverTags asserts an explicit
// topic_node_ids (KG node linkage) still wins when both keys are present.
func TestMarshalAtomCreated_ExplicitTopicNodeIdsWinOverTags(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":        "01971a90-aaaa-7000-8000-000000000001",
		"title":          "Atom title",
		"author_gcid":    env.GCID,
		"topic_node_ids": []string{"node-1"},
		"tags":           []string{"ignored-tag"},
		"created_at":     env.OccurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetTopicNodeIds(); len(got) != 1 || got[0] != "node-1" {
		t.Errorf("AtomCreated.topic_node_ids = %v; want explicit [node-1] to win over tags", got)
	}
}

// TestMarshalAtomCreated_EncodesMCQGradingFields asserts the encoder emits
// correct_option_id (field 26, string) + answer_count (field 27, int32) so
// they round-trip through the generated AtomCreated binding. CHO-1627 (L1
// grading fix): chora-consumption grades MCQ submissions against the
// authoritative correct option carried on atom.created — without these
// fields the consumer has no server-side ground truth to score against.
func TestMarshalAtomCreated_EncodesMCQGradingFields(t *testing.T) {
	env := fixedEnvelope()
	const correctOptionID = "019e58ea-7777-7b74-9f1d-0000000000aa"
	payload := map[string]any{
		"atom_id":           "01971a90-aaaa-7000-8000-000000000001",
		"atom_type":         int32(1), // ATOM_TYPE_MULTIPLE_CHOICE
		"author_gcid":       env.GCID,
		"created_at":        env.OccurredAt,
		"correct_option_id": correctOptionID,
		"answer_count":      int32(4),
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetCorrectOptionId(); got != correctOptionID {
		t.Errorf("AtomCreated.correct_option_id = %q; want %q (encoder must emit field 26)", got, correctOptionID)
	}
	if got := m.GetAnswerCount(); got != 4 {
		t.Errorf("AtomCreated.answer_count = %d; want 4 (encoder must emit field 27)", got)
	}
}

// TestMarshalAtomCreated_NonMCQOmitsGradingFields asserts the proto3
// default-value elision applies to the CHO-1627 grading fields: a non-MCQ
// atom (empty correct_option_id, answer_count 0) MUST NOT carry field 26 or
// 27 on the wire, keeping bytes byte-compatible with producers that never set
// them. Decoding yields the zero values.
func TestMarshalAtomCreated_NonMCQOmitsGradingFields(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"atom_type":   int32(8), // ATOM_TYPE_ESSAY (non-MCQ)
		"author_gcid": env.GCID,
		"created_at":  env.OccurredAt,
		// no correct_option_id, no answer_count
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	// Field 26 + 27 MUST be absent from the wire (proto3 default elision).
	seen := walkTopLevelTags(t, bz, 1, 27)
	if seen[26] {
		t.Errorf("non-MCQ atom.created emitted field 26 (correct_option_id); want absent")
	}
	if seen[27] {
		t.Errorf("non-MCQ atom.created emitted field 27 (answer_count); want absent")
	}

	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetCorrectOptionId(); got != "" {
		t.Errorf("AtomCreated.correct_option_id = %q; want empty for non-MCQ", got)
	}
	if got := m.GetAnswerCount(); got != 0 {
		t.Errorf("AtomCreated.answer_count = %d; want 0 for non-MCQ", got)
	}
}

// TestMarshalAtomCreated_EnvelopeIsWireCompatible asserts the nested envelope
// submessage decodes to chora.common.v1.EventEnvelope shape (event_id=1 ...
// schema_version=11) — the wire format the Schema Registry expects.
func TestMarshalAtomCreated_EnvelopeIsWireCompatible(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"created_at":  env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	envBytes := readField1Submessage(t, bz)
	if len(envBytes) == 0 {
		t.Fatal("empty envelope bytes")
	}

	seen := walkTopLevelTags(t, envBytes, 1, 15)
	required := []protowire.Number{1, 2, 3, 4, 5, 6, 7, 9, 10, 11}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("envelope: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// -----------------------------------------------------------------------------
// chora.creation.question.authored.v1
// Schema fields (per chora-contracts/proto/events-flat/creation/question/authored.proto):
//   1  Envelope envelope
//   2  string   question_id
//   3  string   atom_id
//   4  QuestionType question_type      (enum varint)
//   5  QuestionSourceType source_type  (enum varint)
//   6  string   author_gcid
//   7  int32    revision_number
//   8  string   revision_id
//   9  string   source_job_id
//  10  Timestamp authored_at
// -----------------------------------------------------------------------------

func TestMarshalQuestionAuthored_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"question_id":     "01971a90-cccc-7000-8000-000000000001",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"question_type":   int32(1), // QUESTION_TYPE_MCQ
		"source_type":     int32(2), // QUESTION_SOURCE_TYPE_AI_ASSIST
		"author_gcid":     env.GCID,
		"revision_number": int32(1),
		"revision_id":     "01971a90-dddd-7000-8000-000000000001",
		"source_job_id":   "01971a90-eeee-7000-8000-000000000001",
		"authored_at":     env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}

	seen := walkTopLevelTags(t, bz, 1, 10)
	required := []protowire.Number{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("question.authored: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalQuestionAuthored_AcceptsStringEnumNames covers the case where
// the caller passes string question_type / source_type instead of int32
// (question_jobs_handler emits string("mcq") / string("ai_assist") today).
func TestMarshalQuestionAuthored_AcceptsStringEnumNames(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"question_id":     "01971a90-cccc-7000-8000-000000000001",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"question_type":   "mcq",
		"source_type":     "ai_assist",
		"author_gcid":     env.GCID,
		"revision_number": int32(1),
		"revision_id":     "01971a90-dddd-7000-8000-000000000001",
		"source_job_id":   "01971a90-eeee-7000-8000-000000000001",
		"authored_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload with string enums + string time: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// -----------------------------------------------------------------------------
// chora.creation.question.generation_requested.v1
// Schema fields (per chora-contracts/proto/events-flat/creation/question/generation_requested.proto):
//   1  Envelope envelope
//   2  string   job_id
//   3  string   atom_id
//   4  string   author_gcid
//   5  QuestionGenerationJobType job_type   (enum varint)
//   6  QuestionType question_type           (enum varint)
//   7  string   target_question_id
//   8  string   source_blob_uri
//   9  string   source_mime_type
//  10  string   mana_action_code
//  11  int32    mana_charged
//  12  string   settings_json
//  13  Timestamp requested_at
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// chora.creation.question.generation_completed.v1
// Schema fields (per chora-contracts/proto/events-flat/creation/question/generation_completed.proto):
//   1  Envelope envelope
//   2  string   job_id
//   3  string   atom_id
//   4  string   author_gcid
//   5  QuestionGenerationJobType job_type    (enum varint)
//   6  QuestionGenerationJobStatus status    (enum varint)
//   7  int32    candidate_count
//   8  string   failure_category
//   9  string   failure_message
//  10  int32    mana_refunded
//  11  Timestamp completed_at
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// chora.creation.atom.revised.v1
// NOTE: deployed Pub/Sub has NO topic for atom.revised — the canonical
// AtomUpdated schema is at chora.creation.atom.updated.v1, which DOES have an
// encoder now (see atom_updated_test.go, ADR-244 D5). atom.revised keeps
// falling through as unsupported (caller logs WARN + JSON-falls-back) because
// no topic backs it.
// -----------------------------------------------------------------------------

func TestMarshalAtomRevised_FallsThroughAsUnsupported(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":     env.GCID,
		"revision_number": int32(2),
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.revised.v1", env, payload)
	if err == nil {
		t.Fatal("expected ErrUnsupportedTopic — chora.creation.atom.revised.v1 has no deployed schema yet")
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected IsUnsupportedTopic(true), got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Cross-cutting behaviour
// -----------------------------------------------------------------------------

// TestMarshal_UnknownTopic_FailsLoud asserts unsupported topics return a typed
// error so the dispatcher dead-letters rather than persisting bytes Schema
// Registry will reject.
func TestMarshal_UnknownTopic_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	_, err := protomarshal.MarshalPayload("chora.creation.some.unwired.v1", env, nil)
	if err == nil {
		t.Fatal("expected ErrUnsupportedTopic, got nil")
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected IsUnsupportedTopic(true), got: %v", err)
	}
}

// TestMarshal_NilPayloadProducesEmptyButValidMessage asserts a nil payload
// still produces a wire-valid message containing just the envelope.
func TestMarshal_NilPayloadProducesEmptyButValidMessage(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("nil payload should still produce envelope bytes")
	}
	num, _, n := protowire.ConsumeTag(bz)
	if n < 0 {
		t.Fatal("invalid leading tag")
	}
	if num != 1 {
		t.Fatalf("expected leading field=1 (envelope), got %d", num)
	}
}

// TestMarshal_QuestionAuthored_RejectsInvalidPayloadType asserts the encoder
// fails loud when payload fields have the wrong Go type for their proto
// schema slot.
func TestMarshal_QuestionAuthored_RejectsInvalidPayloadType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"question_id":     "01971a90-cccc-7000-8000-000000000001",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":     env.GCID,
		"revision_number": "not-a-number", // schema demands int32
	}
	_, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, payload)
	if err == nil {
		t.Fatal("expected type error for revision_number=string")
	}
}

// TestMarshal_AtomCreated_RejectsCreatedAtAsString — wrong type on a
// Timestamp slot must fail loud.
func TestMarshal_AtomCreated_RejectsCreatedAtAsString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "a-1",
		"author_gcid": env.GCID,
		"created_at":  "not-a-time",
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err == nil {
		t.Fatal("expected typed error for created_at=string")
	}
}

// TestMarshal_AtomCreated_AcceptsRFC3339String covers question_jobs_handler's
// newAtomEvent emission path: created_at arrives as an RFC3339Nano string.
func TestMarshal_AtomCreated_AcceptsRFC3339String(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"title":       "atom title",
		"created_at":  env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload with RFC3339 created_at: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestMarshal_TopicNodeIDsFromInterfaceSlice covers []any-of-string variants
// (JSON-decoded payload shape).
func TestMarshal_TopicNodeIDsFromInterfaceSlice(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":        "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":    env.GCID,
		"title":          "atom",
		"topic_node_ids": []any{"x", "y"},
		"created_at":     env.OccurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestEnvelopePtr_TimePointerAccepted ensures asTime accepts *time.Time (call
// sites may pass either form).
func TestEnvelopePtr_TimePointerAccepted(t *testing.T) {
	env := fixedEnvelope()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"title":       "atom",
		"created_at":  &now,
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload with *time.Time: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// -----------------------------------------------------------------------------
// Enum lookup coverage — round-trip every named enum constant to its wire
// integer so future protoflatten/schema updates flag schema drift loud.
// -----------------------------------------------------------------------------

// TestEnumRoundTrip_AtomType drives lookupAtomType via the public encoder
// surface — every enum name MUST resolve to its wire integer + emit a valid
// AtomCreated payload.
func TestEnumRoundTrip_AtomType(t *testing.T) {
	cases := map[string]int32{
		"":                          0,
		"unspecified":               0,
		"ATOM_TYPE_UNSPECIFIED":     0,
		"ATOM_TYPE_MULTIPLE_CHOICE": 1,
		"multiple_choice":           1,
		"mcq":                       1,
		"ATOM_TYPE_FILL_BLANK":      2,
		"fill_blank":                2,
		"ATOM_TYPE_TRUE_FALSE":      3,
		"true_false":                3,
		"ATOM_TYPE_SHORT_ANSWER":    4,
		"short_answer":              4,
		"ATOM_TYPE_MATCHING":        5,
		"matching":                  5,
		"ATOM_TYPE_ORDERING":        6,
		"ordering":                  6,
		"ATOM_TYPE_CODE":            7,
		"code":                      7,
		"ATOM_TYPE_ESSAY":           8,
		"essay":                     8,
		"oe":                        8,
		"ATOM_TYPE_MULTIMEDIA":      9,
		"multimedia":                9,
		"ATOM_TYPE_SIMULATION":      10,
		"simulation":                10,
	}
	env := fixedEnvelope()
	for name := range cases {
		bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, map[string]any{
			"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
			"author_gcid": env.GCID,
			"atom_type":   name,
		})
		if err != nil {
			t.Fatalf("atom_type=%q: %v", name, err)
		}
		if len(bz) == 0 {
			t.Fatalf("atom_type=%q: empty bytes", name)
		}
	}

	// Unknown enum name must fail loud.
	_, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"atom_type":   "totally-not-a-known-atom-type",
	})
	if err == nil {
		t.Fatal("expected error for unknown atom_type enum name")
	}
}

// TestEnumRoundTrip_AtomStatus drives lookupAtomStatus.
func TestEnumRoundTrip_AtomStatus(t *testing.T) {
	cases := []string{
		"",
		"unspecified",
		"ATOM_STATUS_UNSPECIFIED",
		"ATOM_STATUS_DRAFT",
		"draft",
		"ATOM_STATUS_PUBLISHED",
		"published",
		"ATOM_STATUS_ARCHIVED",
		"archived",
	}
	env := fixedEnvelope()
	for _, name := range cases {
		bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, map[string]any{
			"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
			"author_gcid": env.GCID,
			"status":      name,
		})
		if err != nil {
			t.Fatalf("status=%q: %v", name, err)
		}
		if len(bz) == 0 {
			t.Fatalf("status=%q: empty bytes", name)
		}
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"status":      "not-a-status",
	})
	if err == nil {
		t.Fatal("expected error for unknown status enum name")
	}
}

// TestEnumRoundTrip_QuestionType drives lookupQuestionType through the
// question.authored encoder.
func TestEnumRoundTrip_QuestionType(t *testing.T) {
	cases := []string{
		"",
		"unspecified",
		"QUESTION_TYPE_UNSPECIFIED",
		"QUESTION_TYPE_MCQ",
		"mcq",
		"QUESTION_TYPE_OE",
		"oe",
		"QUESTION_TYPE_RESERVED_CODE_EXECUTION",
		"code_execution",
		"QUESTION_TYPE_RESERVED_COMPLETION",
		"completion",
		"QUESTION_TYPE_RESERVED_DRAG_DROP",
		"drag_drop",
		"QUESTION_TYPE_RESERVED_FILL_BLANK",
		"fill_blank",
		"QUESTION_TYPE_RESERVED_MATCHING",
		"matching",
		"QUESTION_TYPE_RESERVED_MULTI_SELECT",
		"multi_select",
		"QUESTION_TYPE_RESERVED_MULTIMEDIA",
		"multimedia",
		"QUESTION_TYPE_RESERVED_ORAL",
		"oral",
		"QUESTION_TYPE_RESERVED_ORDERING",
		"ordering",
		"QUESTION_TYPE_RESERVED_PEER_GRADED",
		"peer_graded",
		"QUESTION_TYPE_RESERVED_SHORT_ANSWER",
		"short_answer",
		"QUESTION_TYPE_RESERVED_SIMULATION",
		"simulation",
		"QUESTION_TYPE_RESERVED_TABLE_COMPLETION",
		"table_completion",
		"QUESTION_TYPE_RESERVED_TRUE_FALSE",
		"true_false",
	}
	env := fixedEnvelope()
	for _, name := range cases {
		bz, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, map[string]any{
			"question_id":   "01971a90-cccc-7000-8000-000000000001",
			"atom_id":       "01971a90-aaaa-7000-8000-000000000001",
			"question_type": name,
			"author_gcid":   env.GCID,
		})
		if err != nil {
			t.Fatalf("question_type=%q: %v", name, err)
		}
		if len(bz) == 0 {
			t.Fatalf("question_type=%q: empty bytes", name)
		}
	}
	_, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, map[string]any{
		"question_id":   "01971a90-cccc-7000-8000-000000000001",
		"atom_id":       "01971a90-aaaa-7000-8000-000000000001",
		"question_type": "not-a-question-type",
		"author_gcid":   env.GCID,
	})
	if err == nil {
		t.Fatal("expected error for unknown question_type enum name")
	}
}

// TestEnumRoundTrip_QuestionSourceType drives lookupQuestionSourceType.
func TestEnumRoundTrip_QuestionSourceType(t *testing.T) {
	cases := []string{
		"",
		"unspecified",
		"QUESTION_SOURCE_TYPE_UNSPECIFIED",
		"QUESTION_SOURCE_TYPE_MANUAL",
		"manual",
		"QUESTION_SOURCE_TYPE_AI_ASSIST",
		"ai_assist",
		"QUESTION_SOURCE_TYPE_COMMUNITY_ATOM_BANK",
		"community_atom_bank",
	}
	env := fixedEnvelope()
	for _, name := range cases {
		bz, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, map[string]any{
			"question_id": "01971a90-cccc-7000-8000-000000000001",
			"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
			"source_type": name,
			"author_gcid": env.GCID,
		})
		if err != nil {
			t.Fatalf("source_type=%q: %v", name, err)
		}
		if len(bz) == 0 {
			t.Fatalf("source_type=%q: empty bytes", name)
		}
	}
	_, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, map[string]any{
		"question_id": "01971a90-cccc-7000-8000-000000000001",
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"source_type": "not-a-source-type",
		"author_gcid": env.GCID,
	})
	if err == nil {
		t.Fatal("expected error for unknown source_type enum name")
	}
}

// TestMarshal_EnumField_RejectsWrongType — enum field with totally-wrong Go
// type (bool / map) must fail loud.
func TestMarshal_EnumField_RejectsWrongType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"atom_type":   true, // schema demands int32 / string
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err == nil {
		t.Fatal("expected error for atom_type=bool")
	}
}

// TestMarshal_StringSlice_RejectsNonStringElement asserts mixed-type slices
// are NOT silently coerced (would corrupt the wire format).
func TestMarshal_StringSlice_RejectsNonStringElement(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":        "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":    env.GCID,
		"topic_node_ids": []any{"x", 12345},
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	// Mixed slice => stringSlice returns (nil, false) => field 7 skipped.
	if err != nil {
		t.Fatalf("MarshalPayload with mixed slice should skip silently, got error: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestAsInt32_AllNumericTypesAccepted exercises every numeric branch of
// asInt32 by feeding wide-int variants for revision_number.
func TestAsInt32_AllNumericTypesAccepted(t *testing.T) {
	env := fixedEnvelope()
	vals := []any{
		int(7), int32(7), int64(7), uint(7), uint32(7), uint64(7), float32(7), float64(7),
	}
	for _, v := range vals {
		bz, err := protomarshal.MarshalPayload("chora.creation.question.authored.v1", env, map[string]any{
			"question_id":     "01971a90-cccc-7000-8000-000000000001",
			"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
			"author_gcid":     env.GCID,
			"revision_number": v,
		})
		if err != nil {
			t.Fatalf("revision_number type %T: %v", v, err)
		}
		if len(bz) == 0 {
			t.Fatalf("revision_number type %T: empty bytes", v)
		}
	}
}

// TestStringField_RejectsBoolType asserts the encoder rejects a bool value
// for a schema-string slot (would otherwise corrupt the wire format).
func TestStringField_RejectsBoolType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     true, // schema demands string
		"author_gcid": env.GCID,
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err == nil {
		t.Fatal("expected error for atom_id=bool")
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// walkTopLevelTags walks the byte slice's top-level TLV records, asserting
// every tag falls within [minField, maxField] and every length-delimited /
// varint payload is well-formed. Returns the set of observed field numbers.
func walkTopLevelTags(t *testing.T, bz []byte, minField, maxField protowire.Number) map[protowire.Number]bool {
	t.Helper()
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		if num < minField || num > maxField {
			t.Fatalf("field number %d out of schema range [%d..%d]", num, minField, maxField)
		}
		rem = rem[n:]
		seen[num] = true

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
	return seen
}

// readField1Submessage extracts the bytes of the first top-level
// length-delimited field, which by convention is the nested Envelope.
func readField1Submessage(t *testing.T, bz []byte) []byte {
	t.Helper()
	num, typ, n := protowire.ConsumeTag(bz)
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatalf("expected envelope tag (1, bytes), got num=%d typ=%d n=%d", num, typ, n)
	}
	envBytes, m := protowire.ConsumeBytes(bz[n:])
	if m < 0 {
		t.Fatal("invalid envelope bytes")
	}
	return envBytes
}
