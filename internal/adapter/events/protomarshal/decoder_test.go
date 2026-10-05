// Package protomarshal_test verifies the wire-format decoders for the
// chora.creation.ai_assist.{completed,refused}.v1 topics.
//
// Strategy: hand-construct the canonical wire bytes using google.golang.org/
// protobuf/encoding/protowire helpers (the SAME library the encoder side
// uses), then assert DecodeAiAssistCompleted / DecodeAiAssistRefused
// produce the expected struct. This catches encoder ↔ decoder drift
// because both sides must agree on the same field numbers + wire types.
package protomarshal_test

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// -----------------------------------------------------------------------------
// Wire-builder helpers — minimal proto3 encoders used to construct test
// fixtures. Mirror image of the consumer side so the test's encode +
// the decoder's parse must agree.
// -----------------------------------------------------------------------------

func appendString(b []byte, field protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendString(b, v)
	return b
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	b = protowire.AppendVarint(b, v)
	return b
}

func appendBytes(b []byte, field protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

// encodeTimestamp builds the nested Timestamp submessage wire bytes:
//
//	1 varint int64 seconds
//	2 varint int32 nanos
func encodeTimestamp(t time.Time) []byte {
	out := make([]byte, 0, 16)
	t = t.UTC()
	if secs := t.Unix(); secs != 0 {
		out = appendVarint(out, 1, uint64(secs))
	}
	if nanos := int32(t.Nanosecond()); nanos != 0 {
		out = appendVarint(out, 2, uint64(uint32(nanos)))
	}
	return out
}

// encodeEnvelope builds the nested Envelope submessage wire bytes with
// the 4 fields the decoder cares about (event_id=1, idempotency_key=2,
// tenant_id=3, gcid=4) — matching chora.common.v1.EventEnvelope.
func encodeEnvelope(eventID, idempotencyKey, tenantID, gcid string) []byte {
	out := make([]byte, 0, 128)
	if eventID != "" {
		out = appendString(out, 1, eventID)
	}
	if idempotencyKey != "" {
		out = appendString(out, 2, idempotencyKey)
	}
	if tenantID != "" {
		out = appendString(out, 3, tenantID)
	}
	if gcid != "" {
		out = appendString(out, 4, gcid)
	}
	return out
}

// -----------------------------------------------------------------------------
// DecodeAiAssistCompleted
// -----------------------------------------------------------------------------

// TestDecodeAiAssistCompleted_FullMessage covers every schema field
// (envelope + 14 scalars + 1 timestamp) and asserts the decoder produces
// matching struct values.
func TestDecodeAiAssistCompleted_FullMessage(t *testing.T) {
	completedAt := time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC)

	envBz := encodeEnvelope(
		"01971a90-e000-7000-8000-000000000001", // event_id
		"chora.creation.ai_assist.completed.v1|assist-1",
		"tenant-phyllis",
		"gcid-phyllis",
	)

	bz := make([]byte, 0, 256)
	bz = appendBytes(bz, 1, envBz)                                       // envelope
	bz = appendString(bz, 2, "assist-1")                                 // assist_id
	bz = appendVarint(bz, 3, 3)                                          // generated_count
	bz = appendString(bz, 4, "approved")                                 // screening_decision
	bz = appendString(bz, 5, "no policy violations detected")            // screening_explanation
	bz = appendString(bz, 6, "gemini-2.5-pro")                           // model_used
	bz = appendVarint(bz, 7, 1234)                                       // input_token_count
	bz = appendVarint(bz, 8, 567)                                        // output_token_count
	bz = appendVarint(bz, 9, 50)                                         // mana_charged
	bz = appendBytes(bz, 10, encodeTimestamp(completedAt))               // completed_at
	bz = appendString(bz, 11, `{"candidates":[{"stem":"What gas"}]}`)    // candidate_payload_json
	bz = appendString(bz, 12, `[{"step":"validator","duration_ms":40}]`) // pipeline_trace_json
	bz = appendVarint(bz, 13, 1)                                         // quality_warning = true
	bz = appendVarint(bz, 14, 2)                                         // attempt_count
	bz = appendString(bz, 15, "critic flagged minor stem ambiguity")     // critic_notes

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}

	if got.EventID != "01971a90-e000-7000-8000-000000000001" {
		t.Errorf("EventID: got %q", got.EventID)
	}
	if got.IdempotencyKey != "chora.creation.ai_assist.completed.v1|assist-1" {
		t.Errorf("IdempotencyKey: got %q", got.IdempotencyKey)
	}
	if got.TenantID != "tenant-phyllis" {
		t.Errorf("TenantID: got %q", got.TenantID)
	}
	if got.GCID != "gcid-phyllis" {
		t.Errorf("GCID: got %q", got.GCID)
	}
	if got.AssistID != "assist-1" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
	if got.GeneratedCount != 3 {
		t.Errorf("GeneratedCount: got %d", got.GeneratedCount)
	}
	if got.ScreeningDecision != "approved" {
		t.Errorf("ScreeningDecision: got %q", got.ScreeningDecision)
	}
	if got.ScreeningExplanation != "no policy violations detected" {
		t.Errorf("ScreeningExplanation: got %q", got.ScreeningExplanation)
	}
	if got.ModelUsed != "gemini-2.5-pro" {
		t.Errorf("ModelUsed: got %q", got.ModelUsed)
	}
	if got.InputTokenCount != 1234 {
		t.Errorf("InputTokenCount: got %d", got.InputTokenCount)
	}
	if got.OutputTokenCount != 567 {
		t.Errorf("OutputTokenCount: got %d", got.OutputTokenCount)
	}
	if got.ManaCharged != 50 {
		t.Errorf("ManaCharged: got %d", got.ManaCharged)
	}
	if !got.CompletedAt.Equal(completedAt) {
		t.Errorf("CompletedAt: got %v want %v", got.CompletedAt, completedAt)
	}
	if got.CandidatePayloadJSON != `{"candidates":[{"stem":"What gas"}]}` {
		t.Errorf("CandidatePayloadJSON: got %q", got.CandidatePayloadJSON)
	}
	if got.PipelineTraceJSON != `[{"step":"validator","duration_ms":40}]` {
		t.Errorf("PipelineTraceJSON: got %q", got.PipelineTraceJSON)
	}
	if !got.QualityWarning {
		t.Errorf("QualityWarning: got false, want true")
	}
	if got.AttemptCount != 2 {
		t.Errorf("AttemptCount: got %d", got.AttemptCount)
	}
	if got.CriticNotes != "critic flagged minor stem ambiguity" {
		t.Errorf("CriticNotes: got %q", got.CriticNotes)
	}
}

// TestDecodeAiAssistCompleted_PartialOnlyRequired covers the success path
// when only the load-bearing fields (envelope + assist_id + candidate)
// are present — proto3 default-elision permits this.
func TestDecodeAiAssistCompleted_PartialOnlyRequired(t *testing.T) {
	envBz := encodeEnvelope("evt-1", "idemp-1", "tenant-A", "gcid-A")
	bz := make([]byte, 0, 128)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-partial")
	bz = appendString(bz, 11, `{"candidates":[]}`)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.TenantID != "tenant-A" {
		t.Errorf("TenantID: got %q", got.TenantID)
	}
	if got.AssistID != "assist-partial" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
	if got.CandidatePayloadJSON != `{"candidates":[]}` {
		t.Errorf("CandidatePayloadJSON: got %q", got.CandidatePayloadJSON)
	}
	// Default-elision: all other scalars zero / empty.
	if got.GeneratedCount != 0 || got.QualityWarning || got.AttemptCount != 0 {
		t.Errorf("expected proto3 default-elision for unset fields, got %+v", got)
	}
}

// TestDecodeAiAssistCompleted_EmptyEnvelope — envelope present but
// zero-length submessage. Decoder should produce a zero-valued struct
// without erroring.
func TestDecodeAiAssistCompleted_EmptyEnvelope(t *testing.T) {
	bz := make([]byte, 0, 32)
	bz = appendBytes(bz, 1, nil) // empty envelope submessage
	bz = appendString(bz, 2, "assist-x")

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.EventID != "" || got.TenantID != "" || got.GCID != "" {
		t.Errorf("expected empty envelope fields, got %+v", got)
	}
	if got.AssistID != "assist-x" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
}

// TestDecodeAiAssistCompleted_EmptyBytes_FailsLoud — zero-length payload
// is malformed (a valid frame always has at least the envelope tag).
func TestDecodeAiAssistCompleted_EmptyBytes_FailsLoud(t *testing.T) {
	_, err := protomarshal.DecodeAiAssistCompleted(nil)
	if err == nil {
		t.Fatal("expected ErrMalformedPayload on nil input")
	}
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestDecodeAiAssistCompleted_TruncatedBytes_FailsLoud — a valid tag
// followed by truncated bytes must return ErrMalformedPayload (no panic).
func TestDecodeAiAssistCompleted_TruncatedBytes_FailsLoud(t *testing.T) {
	// Tag for field 2 (assist_id, string) with claimed length 100 but
	// only 4 actual bytes follow.
	bz := []byte{0x12, 100, 0x61, 0x61, 0x61, 0x61}
	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err == nil {
		t.Fatal("expected ErrMalformedPayload on truncated bytes")
	}
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestDecodeAiAssistCompleted_WrongWireType_FailsLoud — schema says
// assist_id is a string (length-delimited), but the bytes claim it's a
// varint. The decoder must reject.
func TestDecodeAiAssistCompleted_WrongWireType_FailsLoud(t *testing.T) {
	bz := make([]byte, 0, 16)
	// field 2 with VarintType instead of BytesType
	bz = appendVarint(bz, 2, 42)
	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err == nil {
		t.Fatal("expected ErrMalformedPayload on wire-type mismatch")
	}
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestDecodeAiAssistCompleted_UnknownFieldSkipped — proto3 forward-compat
// rule: unknown fields are skipped, not rejected. Use a field number
// outside the [1..15] schema range to simulate a future schema bump.
func TestDecodeAiAssistCompleted_UnknownFieldSkipped(t *testing.T) {
	envBz := encodeEnvelope("evt-skip", "", "tenant-skip", "gcid-skip")
	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-skip")
	bz = appendString(bz, 99, "future-field-value")

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: unexpected error on unknown field: %v", err)
	}
	if got.AssistID != "assist-skip" || got.TenantID != "tenant-skip" {
		t.Errorf("expected known fields preserved, got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// DecodeAiAssistRefused
// -----------------------------------------------------------------------------

// TestDecodeAiAssistRefused_FullMessage covers every schema field (envelope
// + 8 scalars + 1 timestamp).
func TestDecodeAiAssistRefused_FullMessage(t *testing.T) {
	refusedAt := time.Date(2026, 5, 17, 14, 31, 0, 0, time.UTC)

	envBz := encodeEnvelope(
		"01971a90-e000-7000-8000-000000000002",
		"chora.creation.ai_assist.refused.v1|assist-r1",
		"tenant-phyllis",
		"gcid-phyllis",
	)

	bz := make([]byte, 0, 256)
	bz = appendBytes(bz, 1, envBz)                                               // envelope
	bz = appendString(bz, 2, "assist-r1")                                        // assist_id
	bz = appendString(bz, 3, "GUARDRAIL_PRE")                                    // refusal_reason
	bz = appendString(bz, 4, "armor:pii_high_risk_block")                        // model_armor_verdict
	bz = appendString(bz, 5, "qgen-validator")                                   // refusing_agent_id
	bz = appendString(bz, 6, "Cannot generate that question — please rephrase.") // user_facing_message
	bz = appendVarint(bz, 7, 25)                                                 // mana_charged
	bz = appendBytes(bz, 8, encodeTimestamp(refusedAt))                          // refused_at
	bz = appendString(bz, 9, `{"last":"refused"}`)                               // last_candidate_payload_json
	bz = appendVarint(bz, 10, 1)                                                 // attempt_count

	got, err := protomarshal.DecodeAiAssistRefused(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistRefused: %v", err)
	}
	if got.EventID != "01971a90-e000-7000-8000-000000000002" {
		t.Errorf("EventID: got %q", got.EventID)
	}
	if got.IdempotencyKey != "chora.creation.ai_assist.refused.v1|assist-r1" {
		t.Errorf("IdempotencyKey: got %q", got.IdempotencyKey)
	}
	if got.TenantID != "tenant-phyllis" {
		t.Errorf("TenantID: got %q", got.TenantID)
	}
	if got.GCID != "gcid-phyllis" {
		t.Errorf("GCID: got %q", got.GCID)
	}
	if got.AssistID != "assist-r1" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
	if got.RefusalReason != "GUARDRAIL_PRE" {
		t.Errorf("RefusalReason: got %q", got.RefusalReason)
	}
	if got.ModelArmorVerdict != "armor:pii_high_risk_block" {
		t.Errorf("ModelArmorVerdict: got %q", got.ModelArmorVerdict)
	}
	if got.RefusingAgentID != "qgen-validator" {
		t.Errorf("RefusingAgentID: got %q", got.RefusingAgentID)
	}
	if got.UserFacingMessage != "Cannot generate that question — please rephrase." {
		t.Errorf("UserFacingMessage: got %q", got.UserFacingMessage)
	}
	if got.ManaCharged != 25 {
		t.Errorf("ManaCharged: got %d", got.ManaCharged)
	}
	if !got.RefusedAt.Equal(refusedAt) {
		t.Errorf("RefusedAt: got %v want %v", got.RefusedAt, refusedAt)
	}
	if got.LastCandidatePayloadJSON != `{"last":"refused"}` {
		t.Errorf("LastCandidatePayloadJSON: got %q", got.LastCandidatePayloadJSON)
	}
	if got.AttemptCount != 1 {
		t.Errorf("AttemptCount: got %d", got.AttemptCount)
	}
}

// TestDecodeAiAssistRefused_PartialOnlyRequired — minimal frame the
// subscriber's HandleRefused contract requires (envelope + assist_id +
// refusal_reason).
func TestDecodeAiAssistRefused_PartialOnlyRequired(t *testing.T) {
	envBz := encodeEnvelope("evt-r-2", "idemp-r-2", "tenant-B", "gcid-B")
	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-r2")
	bz = appendString(bz, 3, "VALIDATION")

	got, err := protomarshal.DecodeAiAssistRefused(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistRefused: %v", err)
	}
	if got.TenantID != "tenant-B" || got.AssistID != "assist-r2" {
		t.Errorf("expected envelope+assist_id preserved, got %+v", got)
	}
	if got.RefusalReason != "VALIDATION" {
		t.Errorf("RefusalReason: got %q", got.RefusalReason)
	}
	// Default-elision for everything else.
	if got.ManaCharged != 0 || got.AttemptCount != 0 {
		t.Errorf("expected default-elision, got %+v", got)
	}
}

// TestDecodeAiAssistRefused_EmptyBytes_FailsLoud.
func TestDecodeAiAssistRefused_EmptyBytes_FailsLoud(t *testing.T) {
	_, err := protomarshal.DecodeAiAssistRefused(nil)
	if err == nil {
		t.Fatal("expected ErrMalformedPayload on nil input")
	}
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestDecodeAiAssistRefused_MalformedTimestamp_FailsLoud — corrupt
// nested Timestamp submessage bytes; the decoder must fail loud not panic.
func TestDecodeAiAssistRefused_MalformedTimestamp_FailsLoud(t *testing.T) {
	envBz := encodeEnvelope("evt-r-3", "", "tenant-C", "gcid-C")
	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-r3")
	bz = appendString(bz, 3, "VALIDATION")
	// Field 8 = refused_at (Timestamp). Claim length 5 but only 1 byte
	// of inner data + that byte declares an invalid wire type for
	// seconds.
	bz = append(bz, byte(0x42), byte(5), byte(0xff), byte(0xff), byte(0xff), byte(0xff), byte(0xff))
	_, err := protomarshal.DecodeAiAssistRefused(bz)
	if err == nil {
		t.Fatal("expected ErrMalformedPayload on malformed Timestamp")
	}
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestDecodeAiAssistRefused_UnknownFieldSkipped — forward-compat rule.
func TestDecodeAiAssistRefused_UnknownFieldSkipped(t *testing.T) {
	envBz := encodeEnvelope("evt-r-4", "", "tenant-D", "gcid-D")
	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-r4")
	bz = appendString(bz, 3, "GUARDRAIL_POST")
	bz = appendString(bz, 88, "future-field")

	got, err := protomarshal.DecodeAiAssistRefused(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistRefused: unexpected error on unknown field: %v", err)
	}
	if got.RefusalReason != "GUARDRAIL_POST" {
		t.Errorf("RefusalReason: got %q", got.RefusalReason)
	}
}
