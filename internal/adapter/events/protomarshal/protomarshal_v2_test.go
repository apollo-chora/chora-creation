// protomarshal_v2_test.go — ADR-195 WS7 (D7) round-trip tests for the .v2
// compose events. Each v2 payload carries the unified compose model
// {operation:"compose", intent, input_kind} and DROPS the legacy job_type /
// job_kind discriminant. The v1 messages were deleted in the WS9 v1-retirement
// pass; the encoder structurally cannot write the retired tags (the .v2 proto
// `reserved 5` / `reserved 15` discipline is the enduring guard), so these
// tests now assert only the live v2 encoder output.
//
// RED before the encoder cases land (strict TDD).
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

func TestMarshalPayload_QuestionGenerationRequestedV2(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"job_id":           "01971a90-aaaa-7000-8000-000000000010",
		"atom_id":          "01971a90-aaaa-7000-8000-000000000011",
		"author_gcid":      env.GCID,
		"question_type":    "mcq",
		"mana_action_code": "question_authoring_ai_draft",
		"mana_charged":     int32(10),
		"settings_json":    `{"count":5}`,
		"requested_at":     env.OccurredAt,
		"operation":        "compose",
		"intent":           "new_question",
		"input_kind":       "prompt",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.question.generation_requested.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var v2 creationv1.QuestionGenerationRequestedV2
	if err := proto.Unmarshal(bz, &v2); err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if v2.GetOperation() != "compose" || v2.GetIntent() != "new_question" || v2.GetInputKind() != "prompt" {
		t.Errorf("compose trio = (%q,%q,%q); want (compose,new_question,prompt)",
			v2.GetOperation(), v2.GetIntent(), v2.GetInputKind())
	}
	if v2.GetJobId() != "01971a90-aaaa-7000-8000-000000000010" ||
		v2.GetQuestionType() != creationv1.QuestionType_QUESTION_TYPE_MCQ ||
		v2.GetSettingsJson() != `{"count":5}` || v2.GetManaCharged() != 10 {
		t.Errorf("carried fields wrong: job_id=%q qtype=%v settings=%q mana=%d",
			v2.GetJobId(), v2.GetQuestionType(), v2.GetSettingsJson(), v2.GetManaCharged())
	}
}

func TestMarshalPayload_QuestionGenerationCompletedV2(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"job_id":          "01971a90-aaaa-7000-8000-000000000020",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000021",
		"author_gcid":     env.GCID,
		"status":          "succeeded",
		"candidate_count": int32(5),
		"completed_at":    env.OccurredAt,
		"operation":       "compose",
		"intent":          "new_question",
		"input_kind":      "prompt",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var v2 creationv1.QuestionGenerationCompletedV2
	if err := proto.Unmarshal(bz, &v2); err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if v2.GetOperation() != "compose" || v2.GetIntent() != "new_question" || v2.GetInputKind() != "prompt" {
		t.Errorf("compose trio = (%q,%q,%q); want (compose,new_question,prompt)",
			v2.GetOperation(), v2.GetIntent(), v2.GetInputKind())
	}
	if v2.GetStatus() != creationv1.QuestionGenerationJobStatus_QUESTION_GENERATION_JOB_STATUS_SUCCEEDED ||
		v2.GetCandidateCount() != 5 {
		t.Errorf("carried fields wrong: status=%v candidate_count=%d", v2.GetStatus(), v2.GetCandidateCount())
	}
}

func TestMarshalPayload_AiAssistStartedV2(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"assist_id":       "01971a90-aaaa-7000-8000-000000000030",
		"tenant_id":       env.TenantID,
		"author_gcid":     env.GCID,
		"content_type":    "mcq",
		"prompt":          "the water cycle",
		"requested_count": int32(5),
		"started_at":      env.OccurredAt,
		"operation":       "compose",
		"intent":          "new_question",
		"input_kind":      "prompt",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var v2 creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &v2); err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if v2.GetOperation() != "compose" || v2.GetIntent() != "new_question" || v2.GetInputKind() != "prompt" {
		t.Errorf("compose trio = (%q,%q,%q); want (compose,new_question,prompt)",
			v2.GetOperation(), v2.GetIntent(), v2.GetInputKind())
	}
	if v2.GetAssistId() != "01971a90-aaaa-7000-8000-000000000030" ||
		v2.GetContentType() != "mcq" || v2.GetRequestedCount() != 5 {
		t.Errorf("carried fields wrong: assist_id=%q content_type=%q requested_count=%d",
			v2.GetAssistId(), v2.GetContentType(), v2.GetRequestedCount())
	}
}

// TestMarshalPayload_AiAssistStartedV2_ExistingQuestionJson — CHO-1658: the
// model_answer_fill producer threads the author's existing question as the
// existing_question_json string (proto field 26). The hand-rolled v2 encoder
// MUST emit it so the orchestrator's proto_wire decode surfaces it; an empty
// value is proto3-elided (byte-stable for every non-fill event).
func TestMarshalPayload_AiAssistStartedV2_ExistingQuestionJson(t *testing.T) {
	env := fixedEnvelope()
	const eqJSON = `{"stem":"Explain photosynthesis.","oe_rubric":[{"criterion":"accuracy","weight":1}],"model_answer":"…"}`
	payload := map[string]any{
		"assist_id":              "01971a90-aaaa-7000-8000-000000000031",
		"tenant_id":              env.TenantID,
		"author_gcid":            env.GCID,
		"content_type":           "oe",
		"prompt":                 "Explain photosynthesis.",
		"started_at":             env.OccurredAt,
		"operation":              "compose",
		"intent":                 "model_answer_fill",
		"input_kind":             "prompt",
		"existing_question_json": eqJSON,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var v2 creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &v2); err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if v2.GetIntent() != "model_answer_fill" {
		t.Errorf("intent = %q; want model_answer_fill", v2.GetIntent())
	}
	if v2.GetExistingQuestionJson() != eqJSON {
		t.Errorf("existing_question_json = %q; want %q (field 26 must encode)", v2.GetExistingQuestionJson(), eqJSON)
	}

	// Byte-stable elision: a payload WITHOUT the key emits no field-26 bytes.
	bare := map[string]any{
		"assist_id": "x", "tenant_id": env.TenantID, "author_gcid": env.GCID,
		"content_type": "mcq", "operation": "compose", "intent": "new_question", "input_kind": "prompt",
	}
	bz2, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, bare)
	if err != nil {
		t.Fatalf("MarshalPayload(bare): %v", err)
	}
	var v2bare creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz2, &v2bare); err != nil {
		t.Fatalf("unmarshal bare: %v", err)
	}
	if v2bare.GetExistingQuestionJson() != "" {
		t.Errorf("existing_question_json on a non-fill event = %q; want empty (proto3 elision)", v2bare.GetExistingQuestionJson())
	}
}
