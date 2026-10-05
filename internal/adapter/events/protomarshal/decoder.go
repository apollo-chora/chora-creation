// decoder.go — binary protobuf wire-format decoders for the terminal
// AI Assist events:
//
//   - chora.creation.ai_assist.completed.v1
//   - chora.creation.ai_assist.refused.v1
//
// Mirror-image of the encoder in protomarshal.go: the encoder marshals
// chora-creation outbox payloads into canonical proto wire bytes; the
// decoder here unmarshals the matching wire bytes from a Pub/Sub Receive
// loop into typed payload structs the subscriber can act on.
//
// Why hand-rolled (same rationale as the encoder):
//
//   - chora-contracts/gen/go bindings exist but coupling the chora-
//     creation subscriber to BSR-rate-limited codegen is not worth the
//     dependency churn for two tiny terminal events.
//   - The producer (orchestrator) ALREADY sets canonical Pub/Sub message
//     attributes (idempotency_key, event_id, tenant_id, gcid). The
//     subscriber's authoritative source is the message attribute map;
//     decoding the envelope is a belt-and-braces fallback for cases the
//     producer skipped an attribute.
//
// Field numbers + wire types are pinned to:
//
//   - chora-contracts/proto/events-flat/creation/ai_assist/completed.proto
//   - chora-contracts/proto/events-flat/creation/ai_assist/refused.proto
//
// Both schemas share the same Envelope shape (15 fields starting with
// event_id at field 1) and a Timestamp submessage (int64 seconds field 1
// + int32 nanos field 2).
package protomarshal

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// -----------------------------------------------------------------------------
// AiAssistCompletedPayload — flat shape consumed by the subscriber.
// Mirrors chora.creation.v1.AiAssistCompleted with the envelope fields
// promoted up to the top level for ergonomic access (the subscriber
// only cares about envelope.idempotency_key + envelope.tenant_id +
// envelope.gcid + envelope.event_id).
// -----------------------------------------------------------------------------
type AiAssistCompletedPayload struct {
	// Envelope (promoted)
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string

	// Body
	AssistID             string
	GeneratedCount       int32
	ScreeningDecision    string
	ScreeningExplanation string
	ModelUsed            string
	InputTokenCount      int32
	OutputTokenCount     int32
	ManaCharged          int32
	CompletedAt          time.Time
	CandidatePayloadJSON string
	PipelineTraceJSON    string
	QualityWarning       bool
	AttemptCount         int32
	CriticNotes          string
	// GenerationSummary is the CHO-1819 P2 mixed-batch outcome (field 16). nil
	// when ABSENT — today's orchestrator does not emit it, so the field is
	// optional + back-compat (proto3 message default → nil, never an error).
	GenerationSummary *GenerationSummaryPayload
}

// GenerationSummaryPayload mirrors chora.creation.v1.GenerationSummary — the
// requested-vs-generated breakdown a mixed batch reports so the FE can render a
// shortfall banner. GeneratedPerType is nil when no per-type entries were sent.
type GenerationSummaryPayload struct {
	RequestedTotal   int32
	GeneratedTotal   int32
	GeneratedPerType map[string]int32
	ShortfallReason  string
}

// -----------------------------------------------------------------------------
// AiAssistRefusedPayload — flat shape consumed by the subscriber.
// Mirrors chora.creation.v1.AiAssistRefused.
// -----------------------------------------------------------------------------
type AiAssistRefusedPayload struct {
	// Envelope (promoted)
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string

	// Body
	AssistID                 string
	RefusalReason            string
	ModelArmorVerdict        string
	RefusingAgentID          string
	UserFacingMessage        string
	ManaCharged              int32
	RefusedAt                time.Time
	LastCandidatePayloadJSON string
	AttemptCount             int32
}

// ErrMalformedPayload is returned by DecodeAiAssistCompleted /
// DecodeAiAssistRefused when the wire bytes are not parseable as the
// expected message. Subscribers should NACK so the broker DLQs after
// max_delivery_attempts (the bytes are structurally broken — retry
// will not recover them, but the broker's DLQ policy is the durable
// home for poison messages).
var ErrMalformedPayload = errors.New("protomarshal: malformed wire bytes")

// DecodeAiAssistCompleted parses canonical binary proto bytes for
// chora.creation.ai_assist.completed.v1 into AiAssistCompletedPayload.
//
// Returns ErrMalformedPayload (wrapped) on any tag/varint/bytes parse
// failure. Fields 1..16 are decoded; any higher unknown field is skipped per
// proto3 forward-compat (the wire type is still validated so a corrupted
// stream is rejected loud).
func DecodeAiAssistCompleted(data []byte) (AiAssistCompletedPayload, error) {
	var out AiAssistCompletedPayload
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
		case 3: // int32 generated_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.GeneratedCount = v
		case 4: // string screening_decision
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ScreeningDecision = s
		case 5: // string screening_explanation
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ScreeningExplanation = s
		case 6: // string model_used
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ModelUsed = s
		case 7: // int32 input_token_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.InputTokenCount = v
		case 8: // int32 output_token_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.OutputTokenCount = v
		case 9: // int32 mana_charged
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ManaCharged = v
		case 10: // Timestamp completed_at
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return out, fmt.Errorf("%w: completed_at bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			t, err := consumeTimestamp(b)
			if err != nil {
				return out, err
			}
			out.CompletedAt = t
		case 11: // string candidate_payload_json
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.CandidatePayloadJSON = s
		case 12: // string pipeline_trace_json
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.PipelineTraceJSON = s
		case 13: // bool quality_warning
			v, m, err := consumeBoolField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.QualityWarning = v
		case 14: // int32 attempt_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.AttemptCount = v
		case 15: // string critic_notes
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.CriticNotes = s
		case 16: // GenerationSummary generation_summary (CHO-1819 P2)
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return out, fmt.Errorf("%w: generation_summary bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			gs, err := consumeGenerationSummary(b)
			if err != nil {
				return out, err
			}
			out.GenerationSummary = gs
		default:
			// Unknown field — skip per proto3 forward-compat rule. We
			// still validate the wire type so a malformed varint /
			// length-delimited stream is rejected loud.
			m, err := skipField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
		}
	}
	return out, nil
}

// consumeGenerationSummary parses the nested GenerationSummary submessage
// (field 16 of AiAssistCompleted):
//
//	1 int32              requested_total
//	2 int32              generated_total
//	3 map<string,int32>  generated_per_type (repeated MapEntry submessages)
//	4 string             shortfall_reason
//
// Returns a non-nil struct for a present (even empty) message; GeneratedPerType
// stays nil until the first map entry is seen.
func consumeGenerationSummary(data []byte) (*GenerationSummaryPayload, error) {
	out := &GenerationSummaryPayload{}
	rem := data
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return nil, fmt.Errorf("%w: generation_summary tag", ErrMalformedPayload)
		}
		rem = rem[n:]
		switch num {
		case 1: // int32 requested_total
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return nil, err
			}
			rem = rem[m:]
			out.RequestedTotal = v
		case 2: // int32 generated_total
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return nil, err
			}
			rem = rem[m:]
			out.GeneratedTotal = v
		case 3: // map<string,int32> generated_per_type entry
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return nil, fmt.Errorf("%w: generated_per_type entry bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			k, val, err := consumeMapEntryStringInt32(b)
			if err != nil {
				return nil, err
			}
			if out.GeneratedPerType == nil {
				out.GeneratedPerType = make(map[string]int32)
			}
			out.GeneratedPerType[k] = val
		case 4: // string shortfall_reason
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return nil, err
			}
			rem = rem[m:]
			out.ShortfallReason = s
		default:
			m, err := skipField(rem, typ)
			if err != nil {
				return nil, err
			}
			rem = rem[m:]
		}
	}
	return out, nil
}

// consumeMapEntryStringInt32 parses one proto map<string,int32> entry submessage
// {1: key (string), 2: value (int32 varint)}.
func consumeMapEntryStringInt32(data []byte) (string, int32, error) {
	var (
		key string
		val int32
	)
	rem := data
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return "", 0, fmt.Errorf("%w: map entry tag", ErrMalformedPayload)
		}
		rem = rem[n:]
		switch num {
		case 1:
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return "", 0, err
			}
			rem = rem[m:]
			key = s
		case 2:
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return "", 0, err
			}
			rem = rem[m:]
			val = v
		default:
			m, err := skipField(rem, typ)
			if err != nil {
				return "", 0, err
			}
			rem = rem[m:]
		}
	}
	return key, val, nil
}

// DecodeAiAssistRefused parses canonical binary proto bytes for
// chora.creation.ai_assist.refused.v1 into AiAssistRefusedPayload.
//
// Returns ErrMalformedPayload (wrapped) on parse failure. Same DLQ
// semantics as DecodeAiAssistCompleted.
func DecodeAiAssistRefused(data []byte) (AiAssistRefusedPayload, error) {
	var out AiAssistRefusedPayload
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
		case 3: // string refusal_reason
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.RefusalReason = s
		case 4: // string model_armor_verdict
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ModelArmorVerdict = s
		case 5: // string refusing_agent_id
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.RefusingAgentID = s
		case 6: // string user_facing_message
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.UserFacingMessage = s
		case 7: // int32 mana_charged
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ManaCharged = v
		case 8: // Timestamp refused_at
			b, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return out, fmt.Errorf("%w: refused_at bytes", ErrMalformedPayload)
			}
			rem = rem[m:]
			t, err := consumeTimestamp(b)
			if err != nil {
				return out, err
			}
			out.RefusedAt = t
		case 9: // string last_candidate_payload_json
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.LastCandidatePayloadJSON = s
		case 10: // int32 attempt_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.AttemptCount = v
		default:
			m, err := skipField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Envelope decoder (shared by both messages).
// -----------------------------------------------------------------------------

// decodedEnvelope is the subset of envelope fields the subscriber needs
// at decode time. Promoted into the typed payload structs so callers
// don't have to import this type.
type decodedEnvelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string
}

// consumeEnvelope parses the nested Envelope submessage (proto schema:
// 15 fields per chora.common.v1.EventEnvelope). We extract only the
// 4 fields the subscriber's terminal-update path needs; remaining fields
// are skipped per proto3 forward-compat.
func consumeEnvelope(data []byte) (decodedEnvelope, error) {
	var out decodedEnvelope
	rem := data
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return out, fmt.Errorf("%w: envelope tag", ErrMalformedPayload)
		}
		rem = rem[n:]
		switch num {
		case 1: // string event_id
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.EventID = s
		case 2: // string idempotency_key
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.IdempotencyKey = s
		case 3: // string tenant_id
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.TenantID = s
		case 4: // string gcid
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.GCID = s
		default:
			// Skip remaining envelope fields (occurred_at, published_at,
			// traceparent, tracestate, source_project, source_service,
			// schema_version, correlation_id, causation_id,
			// chora_imda_dimension, imda_lifecycle_stage). The subscriber
			// does not consume them on the terminal-update path.
			m, err := skipField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
		}
	}
	return out, nil
}

// consumeTimestamp parses the nested Timestamp submessage:
//
//	1 varint int64  seconds
//	2 varint int32  nanos
func consumeTimestamp(data []byte) (time.Time, error) {
	var secs int64
	var nanos int32
	rem := data
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return time.Time{}, fmt.Errorf("%w: timestamp tag", ErrMalformedPayload)
		}
		rem = rem[n:]
		switch num {
		case 1:
			if typ != protowire.VarintType {
				return time.Time{}, fmt.Errorf("%w: timestamp.seconds wire type", ErrMalformedPayload)
			}
			v, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				return time.Time{}, fmt.Errorf("%w: timestamp.seconds varint", ErrMalformedPayload)
			}
			rem = rem[m:]
			secs = int64(v)
		case 2:
			if typ != protowire.VarintType {
				return time.Time{}, fmt.Errorf("%w: timestamp.nanos wire type", ErrMalformedPayload)
			}
			v, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				return time.Time{}, fmt.Errorf("%w: timestamp.nanos varint", ErrMalformedPayload)
			}
			rem = rem[m:]
			nanos = int32(uint32(v))
		default:
			m, err := skipField(rem, typ)
			if err != nil {
				return time.Time{}, err
			}
			rem = rem[m:]
		}
	}
	if secs == 0 && nanos == 0 {
		return time.Time{}, nil
	}
	return time.Unix(secs, int64(nanos)).UTC(), nil
}

// -----------------------------------------------------------------------------
// Wire-format consume helpers — thin protowire wrappers that ALSO validate
// the wire type matches the schema slot. All return (value, bytesConsumed,
// err); callers advance `rem = rem[bytesConsumed:]`.
// -----------------------------------------------------------------------------

func consumeStringField(rem []byte, typ protowire.Type) (string, int, error) {
	if typ != protowire.BytesType {
		return "", 0, fmt.Errorf("%w: expected bytes (string), got wire type %d", ErrMalformedPayload, typ)
	}
	b, n := protowire.ConsumeBytes(rem)
	if n < 0 {
		return "", 0, fmt.Errorf("%w: string bytes", ErrMalformedPayload)
	}
	return string(b), n, nil
}

func consumeInt32Field(rem []byte, typ protowire.Type) (int32, int, error) {
	if typ != protowire.VarintType {
		return 0, 0, fmt.Errorf("%w: expected varint (int32), got wire type %d", ErrMalformedPayload, typ)
	}
	v, n := protowire.ConsumeVarint(rem)
	if n < 0 {
		return 0, 0, fmt.Errorf("%w: int32 varint", ErrMalformedPayload)
	}
	return int32(uint32(v)), n, nil
}

func consumeBoolField(rem []byte, typ protowire.Type) (bool, int, error) {
	if typ != protowire.VarintType {
		return false, 0, fmt.Errorf("%w: expected varint (bool), got wire type %d", ErrMalformedPayload, typ)
	}
	v, n := protowire.ConsumeVarint(rem)
	if n < 0 {
		return false, 0, fmt.Errorf("%w: bool varint", ErrMalformedPayload)
	}
	return v != 0, n, nil
}

// skipField advances past an unknown field per proto3 forward-compat. We
// support the two wire types our schemas use (varint + length-delimited);
// fixed32 / fixed64 / start-group / end-group raise ErrMalformedPayload
// because no chora.creation.* schema uses them.
func skipField(rem []byte, typ protowire.Type) (int, error) {
	switch typ {
	case protowire.VarintType:
		_, n := protowire.ConsumeVarint(rem)
		if n < 0 {
			return 0, fmt.Errorf("%w: skip varint", ErrMalformedPayload)
		}
		return n, nil
	case protowire.BytesType:
		_, n := protowire.ConsumeBytes(rem)
		if n < 0 {
			return 0, fmt.Errorf("%w: skip bytes", ErrMalformedPayload)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%w: unsupported wire type %d", ErrMalformedPayload, typ)
	}
}
