// decoder_generation_summary_test.go — RED→GREEN coverage for the CHO-1819 P2
// GenerationSummary nested message (field 16) on
// chora.creation.ai_assist.completed.v1.
//
// Contract: chora-contracts/proto/events-flat/creation/ai_assist/completed.proto
// (AiAssistCompleted.generation_summary = 16, message GenerationSummary
// {1: requested_total, 2: generated_total, 3: map<string,int32> generated_per_type,
// 4: shortfall_reason}).
//
// Back-compat: today's orchestrator does NOT emit field 16. An ABSENT summary
// must decode to a nil *GenerationSummaryPayload (proto3 default) — NEVER a
// decode error — so the live completion path keeps working unchanged.
package protomarshal_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// encodeMapEntryStringInt32 builds one proto map<string,int32> entry submessage
// {1: key (string), 2: value (int32 varint)}.
func encodeMapEntryStringInt32(key string, val int32) []byte {
	out := make([]byte, 0, 16+len(key))
	if key != "" {
		out = appendString(out, 1, key)
	}
	if val != 0 {
		out = appendVarint(out, 2, uint64(uint32(val)))
	}
	return out
}

// encodeGenerationSummary builds the nested GenerationSummary submessage bytes.
func encodeGenerationSummary(requestedTotal, generatedTotal int32, perType map[string]int32, shortfall string) []byte {
	out := make([]byte, 0, 64)
	if requestedTotal != 0 {
		out = appendVarint(out, 1, uint64(uint32(requestedTotal)))
	}
	if generatedTotal != 0 {
		out = appendVarint(out, 2, uint64(uint32(generatedTotal)))
	}
	// field 3: repeated MapEntry — emit deterministically (mcq before oe) so
	// the fixture bytes are stable.
	for _, k := range []string{"mcq", "oe"} {
		if v, ok := perType[k]; ok {
			out = appendBytes(out, 3, encodeMapEntryStringInt32(k, v))
		}
	}
	if shortfall != "" {
		out = appendString(out, 4, shortfall)
	}
	return out
}

// completedWithSummary builds a minimal completed.v1 message carrying the
// generated_count scalar (field 3) + the generation_summary message (field 16).
func completedWithSummary(summary []byte) []byte {
	envBz := encodeEnvelope(
		"01971a90-e000-7000-8000-000000000016",
		"chora.creation.ai_assist.completed.v1|assist-mixed",
		"tenant-mixed",
		"gcid-mixed",
	)
	bz := make([]byte, 0, 256)
	bz = appendBytes(bz, 1, envBz)           // envelope
	bz = appendString(bz, 2, "assist-mixed") // assist_id
	bz = appendVarint(bz, 3, 9)              // generated_count (field 3)
	bz = appendString(bz, 11, `[{"stem":"q"}]`)
	if summary != nil {
		bz = appendBytes(bz, 16, summary) // generation_summary (field 16)
	}
	return bz
}

func TestDecodeAiAssistCompleted_GenerationSummaryPresent(t *testing.T) {
	summary := encodeGenerationSummary(10, 9, map[string]int32{"mcq": 7, "oe": 2}, "model_refused_one_oe")
	bz := completedWithSummary(summary)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.GeneratedCount != 9 {
		t.Errorf("generated_count = %d; want 9", got.GeneratedCount)
	}
	if got.GenerationSummary == nil {
		t.Fatal("generation_summary should be non-nil when field 16 present")
	}
	gs := got.GenerationSummary
	if gs.RequestedTotal != 10 {
		t.Errorf("requested_total = %d; want 10", gs.RequestedTotal)
	}
	if gs.GeneratedTotal != 9 {
		t.Errorf("generated_total = %d; want 9", gs.GeneratedTotal)
	}
	if gs.ShortfallReason != "model_refused_one_oe" {
		t.Errorf("shortfall_reason = %q; want model_refused_one_oe", gs.ShortfallReason)
	}
	if gs.GeneratedPerType["mcq"] != 7 || gs.GeneratedPerType["oe"] != 2 {
		t.Errorf("generated_per_type = %v; want mcq=7 oe=2", gs.GeneratedPerType)
	}
}

func TestDecodeAiAssistCompleted_GenerationSummaryAbsent_IsNil(t *testing.T) {
	// Today's orchestrator emits no field 16 — must decode to nil, not error.
	bz := completedWithSummary(nil)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted (no summary): %v", err)
	}
	if got.GenerationSummary != nil {
		t.Errorf("generation_summary = %v; want nil when field 16 absent", got.GenerationSummary)
	}
	if got.GeneratedCount != 9 {
		t.Errorf("generated_count = %d; want 9", got.GeneratedCount)
	}
}

func TestDecodeAiAssistCompleted_GenerationSummaryEmptyMessage(t *testing.T) {
	// A present-but-empty summary message (all proto3 defaults) decodes to a
	// non-nil struct with zero fields + a nil per-type map.
	bz := completedWithSummary([]byte{})

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted (empty summary): %v", err)
	}
	if got.GenerationSummary == nil {
		t.Fatal("present-but-empty summary should decode to a non-nil struct")
	}
	if got.GenerationSummary.RequestedTotal != 0 || got.GenerationSummary.GeneratedTotal != 0 {
		t.Errorf("empty summary should have zero totals, got %+v", got.GenerationSummary)
	}
}
