// protomarshal_typeplan_test.go — RED→GREEN coverage for the CHO-1819 P2
// mixed-type `type_plan` repeated-message field 21 on
// chora.creation.ai_assist.started.v2.
//
// Contract: chora-contracts/proto/events/creation/ai_assist.proto
// (AiAssistStarted.type_plan = 21, message GenerationTypeQuota
// {1: question_type, 2: count, 3: max_images}); flat Schema Registry copy at
// chora-contracts/proto/events-flat/creation/ai_assist/started.proto.
//
// Per the 1a/1c lesson: encoder / decoder / Schema-Registry must move together
// field-by-field; the round-trip below decodes the hand-rolled wire bytes
// through the GENERATED binding so a field-number or wire-type drift fails loud
// here instead of dead-lettering in production.
package protomarshal_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// typePlanStartedPayload returns a mixed-batch started payload (content_type
// "mixed") carrying the supplied type_plan value.
func typePlanStartedPayload(env protomarshal.Envelope, typePlan any) map[string]any {
	return map[string]any{
		"assist_id":       "01971a90-cccc-7000-8000-000000000021",
		"tenant_id":       env.TenantID,
		"author_gcid":     env.GCID,
		"content_type":    "mixed",
		"prompt":          "Generate a mixed set from the uploaded source.",
		"started_at":      env.OccurredAt.Format("2006-01-02T15:04:05Z07:00"),
		"job_kind":        "batch",
		"requested_count": 10,
		"grounding_mode":  "strict",
		"type_plan":       typePlan,
	}
}

// TestMarshalAiAssistStarted_EncodesTypePlanField21 — the canonical mixed shape
// emitted by the subscriber ([]map[string]any). Asserts field 21 appears once
// per quota and round-trips through the generated binding with question_type /
// count / max_images intact + ordered.
func TestMarshalAiAssistStarted_EncodesTypePlanField21(t *testing.T) {
	env := fixedEnvelope()
	quotas := []map[string]any{
		{"question_type": "mcq", "count": 8, "max_images": 2},
		{"question_type": "oe", "count": 2, "max_images": 0},
	}
	payload := typePlanStartedPayload(env, quotas)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	seen := walkTopLevelTags(t, bz, 1, 21)
	if !seen[21] {
		t.Fatalf("ai_assist.started: missing type_plan field 21 (seen: %v)", seen)
	}

	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetTypePlan()
	if len(got) != 2 {
		t.Fatalf("AiAssistStarted.type_plan len = %d; want 2", len(got))
	}
	if got[0].GetQuestionType() != "mcq" || got[0].GetCount() != 8 || got[0].GetMaxImages() != 2 {
		t.Errorf("type_plan[0] = {%q,%d,%d}; want {mcq,8,2}",
			got[0].GetQuestionType(), got[0].GetCount(), got[0].GetMaxImages())
	}
	if got[1].GetQuestionType() != "oe" || got[1].GetCount() != 2 || got[1].GetMaxImages() != 0 {
		t.Errorf("type_plan[1] = {%q,%d,%d}; want {oe,2,0}",
			got[1].GetQuestionType(), got[1].GetCount(), got[1].GetMaxImages())
	}
	// content_type carries the "mixed" discriminator (field 6).
	if m.GetContentType() != "mixed" {
		t.Errorf("content_type = %q; want mixed", m.GetContentType())
	}
}

// TestMarshalAiAssistStarted_TypePlan_JSONDecodedShape — the bridge's
// typed-struct round-trip arrives as []any of map[string]any with numbers as
// float64. Must encode identically.
func TestMarshalAiAssistStarted_TypePlan_JSONDecodedShape(t *testing.T) {
	env := fixedEnvelope()
	quotas := []any{
		map[string]any{"question_type": "mcq", "count": float64(3), "max_images": float64(1)},
		map[string]any{"question_type": "oe", "count": float64(7), "max_images": float64(0)},
	}
	payload := typePlanStartedPayload(env, quotas)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetTypePlan()
	if len(got) != 2 {
		t.Fatalf("type_plan len = %d; want 2", len(got))
	}
	if got[0].GetQuestionType() != "mcq" || got[0].GetCount() != 3 || got[0].GetMaxImages() != 1 {
		t.Errorf("type_plan[0] = {%q,%d,%d}; want {mcq,3,1}",
			got[0].GetQuestionType(), got[0].GetCount(), got[0].GetMaxImages())
	}
	if got[1].GetQuestionType() != "oe" || got[1].GetCount() != 7 {
		t.Errorf("type_plan[1] = {%q,%d}; want {oe,7}", got[1].GetQuestionType(), got[1].GetCount())
	}
}

// TestMarshalAiAssistStarted_TypePlanAbsent_ByteIdentical — proto3 repeated
// default-elision: absent / empty type_plan emits nothing so a legacy
// single-type producer's bytes stay BYTE-IDENTICAL.
func TestMarshalAiAssistStarted_TypePlanAbsent_ByteIdentical(t *testing.T) {
	env := fixedEnvelope()

	without := typePlanStartedPayload(env, nil)
	delete(without, "type_plan")
	bzWithout, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, without)
	if err != nil {
		t.Fatalf("MarshalPayload (absent): %v", err)
	}

	empty := typePlanStartedPayload(env, []map[string]any{})
	bzEmpty, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, empty)
	if err != nil {
		t.Fatalf("MarshalPayload (empty): %v", err)
	}
	if !bytes.Equal(bzWithout, bzEmpty) {
		t.Error("empty type_plan must be byte-identical to omitting the key (proto3 repeated default-elision)")
	}

	seen := walkTopLevelTags(t, bzWithout, 1, 21)
	if seen[21] {
		t.Error("absent type_plan must not emit field 21")
	}
}

// TestMarshalAiAssistStarted_TypePlan_RejectsBadShape — fail-loud when an
// element is not a map or a field carries the wrong scalar type (a corrupted
// slot would silently mis-encode the wire).
func TestMarshalAiAssistStarted_TypePlan_RejectsBadShape(t *testing.T) {
	env := fixedEnvelope()

	bad := typePlanStartedPayload(env, []any{"not-a-map"})
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, bad); err == nil {
		t.Fatal("expected error for non-map type_plan element; got nil")
	}

	notSlice := typePlanStartedPayload(env, "not-a-slice")
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, notSlice); err == nil {
		t.Fatal("expected error for non-slice type_plan value; got nil")
	}

	badCount := typePlanStartedPayload(env, []map[string]any{
		{"question_type": "mcq", "count": "eight", "max_images": 0},
	})
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, badCount); err == nil {
		t.Fatal("expected error for non-int type_plan count; got nil")
	}
}
