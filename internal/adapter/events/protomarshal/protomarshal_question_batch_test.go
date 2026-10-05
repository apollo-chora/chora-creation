// protomarshal_question_batch_test.go — RED→GREEN coverage for the Lane 1c
// (CHO-1703 / ADR-180 D10) chora.creation.question_batch.accepted.v1 BINARY
// encoder. The in_app.created lesson: a topic without a registered binary
// encoder falls through to JSON and silently dead-letters at the Schema
// Registry — so the encoder MUST be wired in MarshalPayload AND round-trip
// against the generated pb (field numbers per
// chora-contracts/proto/events-flat/creation/question_batch/accepted.proto).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

const topicQuestionBatchAccepted = "chora.creation.question_batch.accepted.v1"

func questionBatchAcceptedPayload(env protomarshal.Envelope) map[string]any {
	return map[string]any{
		"job_id":       "01970000-0000-7000-8000-00000000b001",
		"host_atom_id": "01970000-0000-7000-8000-00000000a001",
		"tenant_id":    env.TenantID,
		"author_gcid":  env.GCID,
		"test_set": map[string]any{
			"title":       "Cell Energy Quiz",
			"description": "Composed from notes.md + marks.txt",
		},
		"items": []map[string]any{
			{
				"question_atom_id": "atom-1",
				"question_id":      "q-1",
				"question_type":    "mcq",
				"points":           5,
				"display_order":    1,
			},
			{
				"question_atom_id": "atom-2",
				"question_id":      "q-2",
				"question_type":    "oe",
				"points":           10,
				"display_order":    2,
			},
		},
		"source_files": []map[string]any{
			{"blob_uri": "gs://b/t/j/source-1", "mime_type": "application/pdf", "role": "source"},
			{"blob_uri": "gs://b/t/j/rubric", "mime_type": "text/plain", "role": "rubric"},
		},
		"accepted_at": env.OccurredAt.Format(time.RFC3339Nano),
	}
}

func TestMarshalQuestionBatchAccepted_RoundTripsThroughGeneratedPB(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload(topicQuestionBatchAccepted, env, questionBatchAcceptedPayload(env))
	if err != nil {
		t.Fatalf("MarshalPayload: %v (topic MUST have a binary encoder — JSON fallback dead-letters)", err)
	}

	var m creationv1.QuestionBatchAccepted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if m.GetJobId() != "01970000-0000-7000-8000-00000000b001" {
		t.Errorf("job_id = %q", m.GetJobId())
	}
	if m.GetHostAtomId() != "01970000-0000-7000-8000-00000000a001" {
		t.Errorf("host_atom_id = %q", m.GetHostAtomId())
	}
	if m.GetTenantId() != env.TenantID || m.GetAuthorGcid() != env.GCID {
		t.Errorf("tenant/gcid = %q/%q", m.GetTenantId(), m.GetAuthorGcid())
	}
	ts := m.GetTestSet()
	if ts.GetTitle() != "Cell Energy Quiz" || ts.GetDescription() != "Composed from notes.md + marks.txt" {
		t.Errorf("test_set = %+v", ts)
	}
	items := m.GetItems()
	if len(items) != 2 {
		t.Fatalf("items len = %d; want 2", len(items))
	}
	if items[0].GetQuestionAtomId() != "atom-1" || items[0].GetQuestionId() != "q-1" ||
		items[0].GetQuestionType() != "mcq" || items[0].GetPoints() != 5 || items[0].GetDisplayOrder() != 1 {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[1].GetQuestionType() != "oe" || items[1].GetPoints() != 10 || items[1].GetDisplayOrder() != 2 {
		t.Errorf("items[1] = %+v", items[1])
	}
	files := m.GetSourceFiles()
	if len(files) != 2 {
		t.Fatalf("source_files len = %d; want 2", len(files))
	}
	if files[0].GetBlobUri() != "gs://b/t/j/source-1" || files[0].GetRole() != "source" {
		t.Errorf("source_files[0] = %+v", files[0])
	}
	if files[1].GetRole() != "rubric" || files[1].GetMimeType() != "text/plain" {
		t.Errorf("source_files[1] = %+v", files[1])
	}
	if got := m.GetAcceptedAt().AsTime().UTC(); !got.Equal(env.OccurredAt) {
		t.Errorf("accepted_at = %v; want %v", got, env.OccurredAt)
	}
	// Envelope nested round-trip.
	if m.GetEnvelope().GetTenantId() != env.TenantID || m.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope = %+v", m.GetEnvelope())
	}
}

func TestMarshalQuestionBatchAccepted_JSONDecodedShapes(t *testing.T) {
	// The bridge round-trip arrives with []any + float64 numerics.
	env := fixedEnvelope()
	payload := map[string]any{
		"job_id": "j1", "host_atom_id": "a1",
		"tenant_id": env.TenantID, "author_gcid": env.GCID,
		"test_set": map[string]any{"title": "T"},
		"items": []any{
			map[string]any{"question_atom_id": "atom-1", "question_id": "q-1", "question_type": "mcq",
				"points": float64(7), "display_order": float64(1)},
		},
		"source_files": []any{
			map[string]any{"blob_uri": "gs://b/k", "mime_type": "text/plain", "role": "source"},
		},
		"accepted_at": env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload(topicQuestionBatchAccepted, env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.QuestionBatchAccepted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if len(m.GetItems()) != 1 || m.GetItems()[0].GetPoints() != 7 {
		t.Errorf("items = %+v; want points 7 from float64 coercion", m.GetItems())
	}
	if m.GetTestSet().GetTitle() != "T" {
		t.Errorf("test_set.title = %q", m.GetTestSet().GetTitle())
	}
}

func TestMarshalQuestionBatchAccepted_RejectsBadItemShape(t *testing.T) {
	env := fixedEnvelope()
	payload := questionBatchAcceptedPayload(env)
	payload["items"] = []any{"not-a-map"}
	if _, err := protomarshal.MarshalPayload(topicQuestionBatchAccepted, env, payload); err == nil {
		t.Fatal("expected error for non-map item element")
	}
}
