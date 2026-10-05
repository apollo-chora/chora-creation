package protomarshal

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// -----------------------------------------------------------------------------
// AiAssistProgressPayload — flat shape consumed by chora-creation's progress
// subscriber. Mirrors chora.creation.v1.AiAssistProgress (the mid-run live-
// trace event emitted once per qgen graph node), with the envelope fields
// promoted to the top level for ergonomic access.
//
// EPHEMERAL: a lost/duplicated/out-of-order progress event is harmless — the
// subscriber applies it ONLY when StepIndex advances and the job is RUNNING;
// the terminal completed.v1 (field 12) is always authoritative.
// -----------------------------------------------------------------------------
type AiAssistProgressPayload struct {
	// Envelope (promoted)
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string

	// Body
	AssistID          string
	AuthorGCID        string
	PipelineTraceJSON string
	StepIndex         int32
	StepName          string
	StepStatus        string
	ProgressAt        time.Time
}

// DecodeAiAssistProgress parses canonical binary proto bytes for
// chora.creation.ai_assist.progress.v1 into AiAssistProgressPayload.
//
// Field layout (proto/events-flat/creation/ai_assist/progress.proto):
//
//	1 Envelope  envelope
//	2 string    assist_id
//	3 string    tenant_id          (body mirror; == envelope.tenant_id)
//	4 string    author_gcid
//	5 string    pipeline_trace_json (cumulative-so-far)
//	6 int32     step_index          (monotonic == len(trace))
//	7 string    step_name
//	8 string    step_status
//	9 Timestamp progress_at
//
// Returns ErrMalformedPayload (wrapped) on any tag/varint/bytes parse failure.
// Higher unknown fields are skipped per proto3 forward-compat (wire type still
// validated, so a corrupted stream is rejected loud).
func DecodeAiAssistProgress(data []byte) (AiAssistProgressPayload, error) {
	var out AiAssistProgressPayload
	if len(data) == 0 {
		return out, fmt.Errorf("%w: empty bytes", ErrMalformedPayload)
	}
	rem := data
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return out, fmt.Errorf("%w: invalid tag", ErrMalformedPayload)
		}
		rem = rem[n:]
		switch num {
		case 1: // Envelope
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return out, fmt.Errorf("%w: envelope bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			ev, err := consumeEnvelope(b)
			if err != nil {
				return out, err
			}
			out.EventID = ev.EventID
			out.IdempotencyKey = ev.IdempotencyKey
			out.TenantID = ev.TenantID
			out.GCID = ev.GCID
		case 2: // string assist_id
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.AssistID = s
		case 3: // string tenant_id (body mirror of envelope.tenant_id)
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			if s != "" {
				out.TenantID = s
			}
		case 4: // string author_gcid
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.AuthorGCID = s
		case 5: // string pipeline_trace_json
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.PipelineTraceJSON = s
		case 6: // int32 step_index
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.StepIndex = v
		case 7: // string step_name
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.StepName = s
		case 8: // string step_status
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.StepStatus = s
		case 9: // Timestamp progress_at
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return out, fmt.Errorf("%w: progress_at bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			t, err := consumeTimestamp(b)
			if err != nil {
				return out, err
			}
			out.ProgressAt = t
		default:
			// Unknown field — skip per proto3 forward-compat; still validate
			// the wire type so a malformed stream is rejected loud.
			m, err := skipField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
		}
	}
	return out, nil
}
