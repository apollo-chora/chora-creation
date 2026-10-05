package protomarshal_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// TestDecodeAiAssistProgress_FullMessage pins the Go decoder's field numbers to
// chora.creation.ai_assist.progress.v1 (proto/events-flat/.../progress.proto).
// The wire bytes are hand-built to the SAME field layout the Python encoder
// (encode_ai_assist_progress) emits — a drift between the two would surface as
// a silently-wrong decode (the 4-layer binary-pubsub landmine).
func TestDecodeAiAssistProgress_FullMessage(t *testing.T) {
	progressAt := time.Date(2026, 6, 25, 14, 8, 19, 0, time.UTC)

	envBz := encodeEnvelope(
		"01971a90-e000-7000-8000-0000000000aa", // event_id
		"ai_assist.progress.3.assist-1",        // idempotency_key (per-step)
		"tenant-phyllis",
		"gcid-phyllis",
	)
	trace := `[{"name":"validate_input","status":"ACCEPTED"},{"name":"generate","status":"COMPLETED"}]`

	bz := make([]byte, 0, 256)
	bz = appendBytes(bz, 1, envBz)                       // envelope
	bz = appendString(bz, 2, "assist-1")                 // assist_id
	bz = appendString(bz, 3, "tenant-phyllis")           // tenant_id (body)
	bz = appendString(bz, 4, "gcid-phyllis")             // author_gcid
	bz = appendString(bz, 5, trace)                      // pipeline_trace_json
	bz = appendVarint(bz, 6, 2)                          // step_index
	bz = appendString(bz, 7, "generate")                 // step_name
	bz = appendString(bz, 8, "COMPLETED")                // step_status
	bz = appendBytes(bz, 9, encodeTimestamp(progressAt)) // progress_at

	got, err := protomarshal.DecodeAiAssistProgress(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistProgress: %v", err)
	}

	if got.EventID != "01971a90-e000-7000-8000-0000000000aa" {
		t.Errorf("EventID: got %q", got.EventID)
	}
	if got.IdempotencyKey != "ai_assist.progress.3.assist-1" {
		t.Errorf("IdempotencyKey: got %q", got.IdempotencyKey)
	}
	if got.TenantID != "tenant-phyllis" {
		t.Errorf("TenantID: got %q", got.TenantID)
	}
	if got.GCID != "gcid-phyllis" {
		t.Errorf("GCID (envelope): got %q", got.GCID)
	}
	if got.AssistID != "assist-1" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
	if got.AuthorGCID != "gcid-phyllis" {
		t.Errorf("AuthorGCID: got %q", got.AuthorGCID)
	}
	if got.PipelineTraceJSON != trace {
		t.Errorf("PipelineTraceJSON: got %q", got.PipelineTraceJSON)
	}
	if got.StepIndex != 2 {
		t.Errorf("StepIndex: got %d", got.StepIndex)
	}
	if got.StepName != "generate" {
		t.Errorf("StepName: got %q", got.StepName)
	}
	if got.StepStatus != "COMPLETED" {
		t.Errorf("StepStatus: got %q", got.StepStatus)
	}
	if !got.ProgressAt.Equal(progressAt) {
		t.Errorf("ProgressAt: got %v want %v", got.ProgressAt, progressAt)
	}
}

// TestDecodeAiAssistProgress_PartialOnlyRequired — proto3 default-elision lets a
// producer omit step_name/step_status/progress_at; only envelope + assist_id +
// trace + step_index are load-bearing for the consumer.
func TestDecodeAiAssistProgress_PartialOnlyRequired(t *testing.T) {
	envBz := encodeEnvelope("e-1", "k-1", "t-1", "g-1")
	bz := make([]byte, 0, 128)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-1")
	bz = appendString(bz, 5, "[]")
	bz = appendVarint(bz, 6, 1)

	got, err := protomarshal.DecodeAiAssistProgress(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistProgress: %v", err)
	}
	if got.AssistID != "assist-1" || got.TenantID != "t-1" || got.StepIndex != 1 {
		t.Errorf("partial decode mismatch: %+v", got)
	}
}

func TestDecodeAiAssistProgress_EmptyBytes_FailsLoud(t *testing.T) {
	if _, err := protomarshal.DecodeAiAssistProgress(nil); err == nil {
		t.Fatal("expected ErrMalformedPayload on empty bytes")
	}
}
