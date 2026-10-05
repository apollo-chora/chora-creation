// protomarshal_imageregen_test.go — RED→GREEN coverage for the CHO-1819 P3
// review-image-regenerate `regen` singular-message field 22 on
// chora.creation.ai_assist.started.v2.
//
// Contract: chora-contracts/proto/events-flat/creation/ai_assist/started.proto
// (AiAssistStarted.regen = 22, message ImageRegenSpec
// {1: draft_id, 2: placement, 3: prompt, 4: mode}).
//
// Per the 1a/1c/P2 lesson: encoder / decoder / Schema-Registry must move
// together field-by-field; the round-trip below decodes the hand-rolled wire
// bytes through the GENERATED binding so a field-number or wire-type drift fails
// loud here instead of dead-lettering in production.
package protomarshal_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// imageRegenStartedPayload returns an image-regen started payload
// (job_kind "image_regen") carrying the supplied regen value.
func imageRegenStartedPayload(env protomarshal.Envelope, regen any) map[string]any {
	return map[string]any{
		"assist_id":    "01971a90-dddd-7000-8000-000000000022",
		"tenant_id":    env.TenantID,
		"author_gcid":  env.GCID,
		"atom_id":      "01971a90-aaaa-7000-8000-0000000000a1",
		"content_type": "mcq",
		"prompt":       "Regenerate the stem illustration.",
		"started_at":   env.OccurredAt.Format("2006-01-02T15:04:05Z07:00"),
		"job_kind":     "image_regen",
		"regen":        regen,
	}
}

// TestMarshalAiAssistStarted_EncodesRegenField22 — the canonical image-regen
// shape emitted by the subscriber (map[string]any). Asserts field 22 appears
// once and round-trips through the generated binding with draft_id / placement /
// prompt / mode intact.
func TestMarshalAiAssistStarted_EncodesRegenField22(t *testing.T) {
	env := fixedEnvelope()
	regen := map[string]any{
		"draft_id":  "draft-7",
		"placement": "stem",
		"prompt":    "A clearer diagram of the light-dependent reactions.",
		"mode":      "replace",
	}
	payload := imageRegenStartedPayload(env, regen)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	seen := walkTopLevelTags(t, bz, 1, 22)
	if !seen[22] {
		t.Fatalf("ai_assist.started: missing regen field 22 (seen: %v)", seen)
	}

	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetRegen()
	if got == nil {
		t.Fatal("AiAssistStarted.regen is nil; want populated ImageRegenSpec")
	}
	if got.GetDraftId() != "draft-7" || got.GetPlacement() != "stem" {
		t.Errorf("regen = {draft_id:%q, placement:%q}; want {draft-7, stem}",
			got.GetDraftId(), got.GetPlacement())
	}
	if got.GetPrompt() != "A clearer diagram of the light-dependent reactions." {
		t.Errorf("regen.prompt = %q", got.GetPrompt())
	}
	if got.GetMode() != "replace" {
		t.Errorf("regen.mode = %q; want replace", got.GetMode())
	}
	// ADR-195 WS9: the v2 message drops job_kind — image_regen is now carried as
	// the compose intent (operation/intent/input_kind), not a job_kind discriminant.
}

// TestMarshalAiAssistStarted_Regen_EncodesEditedContext — the I2 contract add:
// the regen spec carries the author's CURRENT edited question context so the
// image regenerate prompt reflects unsaved edits. Asserts current_stem (5),
// current_model_answer (6), original_source (7) round-trip through the generated
// binding alongside the existing draft_id/placement/prompt/mode.
func TestMarshalAiAssistStarted_Regen_EncodesEditedContext(t *testing.T) {
	env := fixedEnvelope()
	regen := map[string]any{
		"draft_id":             "draft-9",
		"placement":            "stem",
		"prompt":               "Match the edited stem.",
		"mode":                 "replace",
		"current_stem":         "What gas do plants release during photosynthesis?",
		"current_model_answer": "Oxygen (O2).",
		"original_source":      "Biology textbook, ch.4 — light-dependent reactions.",
	}
	payload := imageRegenStartedPayload(env, regen)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetRegen()
	if got == nil {
		t.Fatal("AiAssistStarted.regen is nil; want populated ImageRegenSpec")
	}
	if got.GetCurrentStem() != "What gas do plants release during photosynthesis?" {
		t.Errorf("regen.current_stem = %q", got.GetCurrentStem())
	}
	if got.GetCurrentModelAnswer() != "Oxygen (O2)." {
		t.Errorf("regen.current_model_answer = %q", got.GetCurrentModelAnswer())
	}
	if got.GetOriginalSource() != "Biology textbook, ch.4 — light-dependent reactions." {
		t.Errorf("regen.original_source = %q", got.GetOriginalSource())
	}
	// The pre-existing fields must still round-trip unchanged.
	if got.GetDraftId() != "draft-9" || got.GetPlacement() != "stem" {
		t.Errorf("regen base fields drifted: {draft_id:%q, placement:%q}", got.GetDraftId(), got.GetPlacement())
	}
}

// TestMarshalAiAssistStarted_Regen_EncodesOriginalImageGcsUri — ADR-210 B3: the
// regen spec carries the persisted durable gs:// OBJECT path (field 8) so the
// orchestrator fetches the original bytes for true image-to-image editing.
func TestMarshalAiAssistStarted_Regen_EncodesOriginalImageGcsUri(t *testing.T) {
	env := fixedEnvelope()
	regen := map[string]any{
		"draft_id":               "draft-i2i",
		"placement":              "stem",
		"prompt":                 "Restyle the diagram in crayon.",
		"original_image_gcs_uri": "gs://chora-atom-media-dev/tenants/t/atoms/a/stem.png",
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, imageRegenStartedPayload(env, regen))
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetRegen()
	if got == nil {
		t.Fatal("AiAssistStarted.regen is nil; want populated ImageRegenSpec")
	}
	if got.GetOriginalImageGcsUri() != "gs://chora-atom-media-dev/tenants/t/atoms/a/stem.png" {
		t.Errorf("regen.original_image_gcs_uri = %q", got.GetOriginalImageGcsUri())
	}
	if got.GetDraftId() != "draft-i2i" || got.GetPlacement() != "stem" {
		t.Errorf("regen base fields drifted: {draft_id:%q, placement:%q}", got.GetDraftId(), got.GetPlacement())
	}
}

// TestMarshalAiAssistStarted_Regen_EditedContextElidesWhenEmpty — the new
// context fields follow proto3 scalar default-elision: a regen spec that sets
// ONLY the base fields (no edited context) is byte-identical to a pre-I2
// producer that never carried them.
func TestMarshalAiAssistStarted_Regen_EditedContextElidesWhenEmpty(t *testing.T) {
	env := fixedEnvelope()

	base := map[string]any{
		"draft_id":  "draft-base",
		"placement": "stem",
	}
	bzBase, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, imageRegenStartedPayload(env, base))
	if err != nil {
		t.Fatalf("MarshalPayload (base): %v", err)
	}

	withEmptyContext := map[string]any{
		"draft_id":               "draft-base",
		"placement":              "stem",
		"current_stem":           "",
		"current_model_answer":   "",
		"original_source":        "",
		"original_image_gcs_uri": "",
	}
	bzEmpty, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, imageRegenStartedPayload(env, withEmptyContext))
	if err != nil {
		t.Fatalf("MarshalPayload (empty context): %v", err)
	}

	if !bytes.Equal(bzBase, bzEmpty) {
		t.Error("empty edited-context fields must be byte-identical to omitting them (proto3 scalar default-elision)")
	}
}

// TestMarshalAiAssistStarted_Regen_JSONDecodedShape — the bridge's JSON-decoded
// shape arrives as map[string]any with string values; mode omitted defaults to
// "" on the wire (proto3 scalar default-elision).
func TestMarshalAiAssistStarted_Regen_JSONDecodedShape(t *testing.T) {
	env := fixedEnvelope()
	regen := map[string]any{
		"draft_id":  "draft-x",
		"placement": "answer",
		"prompt":    "Regenerate the model-answer illustration.",
	}
	payload := imageRegenStartedPayload(env, regen)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetRegen()
	if got == nil || got.GetPlacement() != "answer" || got.GetDraftId() != "draft-x" {
		t.Fatalf("regen round-trip failed: %+v", got)
	}
	if got.GetMode() != "" {
		t.Errorf("regen.mode = %q; want empty (omitted)", got.GetMode())
	}
}

// TestMarshalAiAssistStarted_RegenAbsent_ByteIdentical — proto3 message
// default-elision: an absent / empty regen emits nothing so a non-regen
// (legacy) producer's bytes stay BYTE-IDENTICAL.
func TestMarshalAiAssistStarted_RegenAbsent_ByteIdentical(t *testing.T) {
	env := fixedEnvelope()

	without := imageRegenStartedPayload(env, nil)
	delete(without, "regen")
	bzWithout, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, without)
	if err != nil {
		t.Fatalf("MarshalPayload (absent): %v", err)
	}

	empty := imageRegenStartedPayload(env, map[string]any{})
	bzEmpty, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, empty)
	if err != nil {
		t.Fatalf("MarshalPayload (empty): %v", err)
	}
	if !bytes.Equal(bzWithout, bzEmpty) {
		t.Error("empty regen must be byte-identical to omitting the key (proto3 message default-elision)")
	}

	seen := walkTopLevelTags(t, bzWithout, 1, 22)
	if seen[22] {
		t.Error("absent regen must not emit field 22")
	}
}

// TestMarshalAiAssistStarted_Regen_RejectsBadShape — fail-loud when regen is not
// a map or a sub-field carries the wrong scalar type (a corrupted slot would
// silently mis-encode the wire).
func TestMarshalAiAssistStarted_Regen_RejectsBadShape(t *testing.T) {
	env := fixedEnvelope()

	bad := imageRegenStartedPayload(env, "not-a-map")
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, bad); err == nil {
		t.Fatal("expected error for non-map regen value; got nil")
	}

	badField := imageRegenStartedPayload(env, map[string]any{"draft_id": 123})
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, badField); err == nil {
		t.Fatal("expected error for non-string regen.draft_id; got nil")
	}
}
