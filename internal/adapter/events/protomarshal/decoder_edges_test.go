// decoder_edges_test.go — branch coverage for the shared wire-consume
// helpers behind DecodeAiAssistCompleted / DecodeAiAssistRefused /
// DecodeAiAssistProgress: skipField (varint + unsupported wire types),
// consumeInt32Field / consumeBoolField wire-type validation, consumeTimestamp
// (partial + unknown fields), consumeEnvelope forward-compat skips, and
// consumeGenerationSummary / consumeMapEntryStringInt32 malformed paths.
package protomarshal_test

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// -----------------------------------------------------------------------------
// skipField — unknown fields of every wire type.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistCompleted_UnknownVarintFieldSkipped(t *testing.T) {
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-v")
	bz = appendVarint(bz, 99, 12345) // unknown varint field

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.AssistID != "assist-v" {
		t.Errorf("AssistID: got %q", got.AssistID)
	}
}

func TestDecodeAiAssistCompleted_UnknownFixed32FieldFailsLoud(t *testing.T) {
	// No chora.creation.* schema uses fixed32 — skipField must reject it
	// rather than silently desyncing the stream.
	bz := make([]byte, 0, 16)
	bz = appendString(bz, 2, "assist-f32")
	bz = protowire.AppendTag(bz, 99, protowire.Fixed32Type)
	bz = append(bz, 0x01, 0x02, 0x03, 0x04)

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistCompleted_UnknownFieldTruncatedVarintFailsLoud(t *testing.T) {
	// Unknown varint field whose varint runs past the buffer end.
	bz := make([]byte, 0, 8)
	bz = appendString(bz, 2, "assist-tv")
	bz = protowire.AppendTag(bz, 99, protowire.VarintType)
	bz = append(bz, 0x80) // continuation bit set, no following byte

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistCompleted_UnknownFieldTruncatedBytesFailsLoud(t *testing.T) {
	bz := make([]byte, 0, 8)
	bz = appendString(bz, 2, "assist-tb")
	bz = protowire.AppendTag(bz, 99, protowire.BytesType)
	bz = append(bz, 10, 0x61) // claims length 10, only 1 byte present

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// consumeInt32Field / consumeBoolField — wire-type validation.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistCompleted_Int32FieldWrongWireTypeFailsLoud(t *testing.T) {
	// Field 3 (generated_count) is int32/varint; send it as a string.
	bz := make([]byte, 0, 16)
	bz = appendString(bz, 2, "assist-wt")
	bz = appendString(bz, 3, "three")

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistCompleted_BoolFieldWrongWireTypeFailsLoud(t *testing.T) {
	// Field 13 (quality_warning) is bool/varint; send it as a string.
	bz := make([]byte, 0, 16)
	bz = appendString(bz, 2, "assist-bwt")
	bz = appendString(bz, 13, "true")

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// consumeTimestamp — partial messages + forward-compat skip + validation.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistCompleted_TimestampNanosOnly(t *testing.T) {
	// A Timestamp carrying only nanos decodes to the zero time (the
	// secs==0 && nanos==0 guard reads the varint but produces no wall time).
	ts := appendVarint(nil, 2, 500)
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-ns")
	bz = appendBytes(bz, 10, ts)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.CompletedAt.Nanosecond() != 500 {
		t.Errorf("CompletedAt nanos: got %d want 500", got.CompletedAt.Nanosecond())
	}
}

func TestDecodeAiAssistCompleted_TimestampUnknownFieldSkipped(t *testing.T) {
	// Future Timestamp schema bump (field 9) must be skipped, not rejected.
	ts := appendVarint(nil, 1, 1715000000)
	ts = appendVarint(ts, 9, 42)
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-tu")
	bz = appendBytes(bz, 10, ts)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.CompletedAt.Unix() != 1715000000 {
		t.Errorf("CompletedAt: got %v", got.CompletedAt)
	}
}

func TestDecodeAiAssistCompleted_TimestampSecondsWrongWireTypeFailsLoud(t *testing.T) {
	// seconds declared as length-delimited instead of varint.
	ts := appendString(nil, 1, "not-a-varint")
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-twt")
	bz = appendBytes(bz, 10, ts)

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistCompleted_TimestampTruncatedFailsLoud(t *testing.T) {
	// Timestamp bytes cut off mid-tag.
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-tt")
	bz = appendBytes(bz, 10, []byte{0x80})

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// consumeEnvelope — forward-compat skips over the fields the terminal path
// does not consume (traceparent / schema_version / ...), + malformed bytes.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistCompleted_EnvelopeExtraFieldsSkipped(t *testing.T) {
	envBz := encodeEnvelope("evt-extra", "idemp-extra", "tenant-extra", "gcid-extra")
	envBz = appendString(envBz, 7, "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01") // traceparent
	envBz = appendVarint(envBz, 11, 1)                                                        // schema_version

	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-env")

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.TenantID != "tenant-extra" || got.GCID != "gcid-extra" {
		t.Errorf("envelope fields lost: %+v", got)
	}
}

func TestDecodeAiAssistCompleted_EnvelopeMalformedTagFailsLoud(t *testing.T) {
	bz := make([]byte, 0, 16)
	bz = appendBytes(bz, 1, []byte{0x80}) // truncated tag inside envelope
	bz = appendString(bz, 2, "assist-em")

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// consumeGenerationSummary / consumeMapEntryStringInt32 — full parse,
// forward-compat skips, and malformed-entry rejection.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistCompleted_GenerationSummaryUnknownFieldsSkipped(t *testing.T) {
	entry := appendString(nil, 1, "mcq")
	entry = appendVarint(entry, 2, 6)
	entry = appendVarint(entry, 9, 1) // unknown map-entry field — skipped

	gs := appendVarint(nil, 1, 8) // requested_total
	gs = appendVarint(gs, 2, 6)   // generated_total
	gs = appendBytes(gs, 3, entry)
	gs = appendString(gs, 4, "critic capped the batch")
	gs = appendVarint(gs, 9, 7) // unknown summary field — skipped

	bz := make([]byte, 0, 128)
	bz = appendString(bz, 2, "assist-gs")
	bz = appendBytes(bz, 16, gs)

	got, err := protomarshal.DecodeAiAssistCompleted(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistCompleted: %v", err)
	}
	if got.GenerationSummary == nil {
		t.Fatal("GenerationSummary: got nil, want populated")
	}
	if got.GenerationSummary.RequestedTotal != 8 || got.GenerationSummary.GeneratedTotal != 6 {
		t.Errorf("totals: got %+v", got.GenerationSummary)
	}
	if got.GenerationSummary.GeneratedPerType["mcq"] != 6 {
		t.Errorf("per-type: got %v", got.GenerationSummary.GeneratedPerType)
	}
	if got.GenerationSummary.ShortfallReason != "critic capped the batch" {
		t.Errorf("shortfall: got %q", got.GenerationSummary.ShortfallReason)
	}
}

func TestDecodeAiAssistCompleted_GenerationSummaryMalformedEntryFailsLoud(t *testing.T) {
	gs := appendBytes(nil, 3, []byte{0x80}) // truncated map entry
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-gm")
	bz = appendBytes(bz, 16, gs)

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistCompleted_GenerationSummaryTruncatedBytesFailsLoud(t *testing.T) {
	// Field 16 claims a length the buffer does not carry.
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-gt")
	bz = protowire.AppendTag(bz, 16, protowire.BytesType)
	bz = append(bz, 20, 0x08)

	_, err := protomarshal.DecodeAiAssistCompleted(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// DecodeAiAssistProgress — edge branches not covered by the full-message
// test: wrong wire types, unknown-field skip, malformed timestamp.
// -----------------------------------------------------------------------------

func TestDecodeAiAssistProgress_UnknownVarintFieldSkipped(t *testing.T) {
	bz := make([]byte, 0, 32)
	bz = appendString(bz, 2, "assist-p")
	bz = appendVarint(bz, 6, 3)   // step_index
	bz = appendVarint(bz, 42, 99) // unknown varint field

	got, err := protomarshal.DecodeAiAssistProgress(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistProgress: %v", err)
	}
	if got.AssistID != "assist-p" || got.StepIndex != 3 {
		t.Errorf("got %+v", got)
	}
}

func TestDecodeAiAssistProgress_StepIndexWrongWireTypeFailsLoud(t *testing.T) {
	bz := make([]byte, 0, 16)
	bz = appendString(bz, 2, "assist-pw")
	bz = appendString(bz, 6, "three") // step_index must be varint

	_, err := protomarshal.DecodeAiAssistProgress(bz)
	if !errors.Is(err, protomarshal.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

func TestDecodeAiAssistProgress_TenantBodyOverridesEnvelope(t *testing.T) {
	envBz := encodeEnvelope("evt-p", "", "tenant-env", "gcid-p")
	bz := make([]byte, 0, 64)
	bz = appendBytes(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-pt")
	bz = appendString(bz, 3, "tenant-body")

	got, err := protomarshal.DecodeAiAssistProgress(bz)
	if err != nil {
		t.Fatalf("DecodeAiAssistProgress: %v", err)
	}
	if got.TenantID != "tenant-body" {
		t.Errorf("TenantID: got %q want body mirror", got.TenantID)
	}
}
