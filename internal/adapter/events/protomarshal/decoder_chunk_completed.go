package protomarshal

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// -----------------------------------------------------------------------------
// AiAssistChunkCompletedPayload (ADR-251 D5, CHO-2398) - flat shape consumed
// by chora-creation's chunk subscriber. Mirrors
// chora.creation.v1.AiAssistChunkCompleted (one per finished chunk of a
// multi-chunk set job), with the envelope fields promoted to the top level.
//
// The consumer treats ChunkIndex as the slot key: persistence is idempotent
// and out-of-order safe (SQL slot guard). The terminal completed.v1 stays
// authoritative for the full set.
// -----------------------------------------------------------------------------
type AiAssistChunkCompletedPayload struct {
	// Envelope (promoted)
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string

	// Body
	AssistID              string
	AuthorGCID            string
	ChunkIndex            int32
	ChunkCount            int32
	CandidatesPayloadJSON string
	CandidateCount        int32
	WarnedCount           int32
	ImagesRendered        int32
	ImagesDropped         int32
	ImagesFailed          int32
	ImagesSkipped         int32
}

// DecodeAiAssistChunkCompleted parses canonical binary proto bytes for
// chora.creation.ai_assist.chunk_completed.v1 into
// AiAssistChunkCompletedPayload.
//
// Field layout (proto/events-flat/creation/ai_assist/chunk_completed.proto):
//
//	1  Envelope  envelope
//	2  string    assist_id
//	3  string    tenant_id               (body mirror; == envelope.tenant_id)
//	4  string    author_gcid
//	5  int32     chunk_index
//	6  int32     chunk_count
//	7  string    candidates_payload_json (published shape; specs stripped)
//	8  int32     candidate_count
//	9  Timestamp chunk_completed_at      (skipped; envelope carries the times)
//	10 int32     warned_count
//	11 int32     images_rendered
//	12 int32     images_dropped
//	13 int32     images_failed
//	14 int32     images_skipped
//
// Returns ErrMalformedPayload (wrapped) on any tag/varint/bytes parse failure.
// Unknown fields are skipped per proto3 forward-compat (wire type still
// validated, so a corrupted stream is rejected loud).
func DecodeAiAssistChunkCompleted(data []byte) (AiAssistChunkCompletedPayload, error) {
	var out AiAssistChunkCompletedPayload
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
		case 5: // int32 chunk_index
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ChunkIndex = v
		case 6: // int32 chunk_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ChunkCount = v
		case 7: // string candidates_payload_json
			s, m, err := consumeStringField(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.CandidatesPayloadJSON = s
		case 8: // int32 candidate_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.CandidateCount = v
		case 10: // int32 warned_count
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.WarnedCount = v
		case 11: // int32 images_rendered
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ImagesRendered = v
		case 12: // int32 images_dropped
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ImagesDropped = v
		case 13: // int32 images_failed
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ImagesFailed = v
		case 14: // int32 images_skipped
			v, m, err := consumeInt32Field(rem, typ)
			if err != nil {
				return out, err
			}
			rem = rem[m:]
			out.ImagesSkipped = v
		default: // forward-compat: skip unknown fields, loud on corruption
			m := protowire.ConsumeFieldValue(num, typ, rem)
			if m < 0 {
				return out, fmt.Errorf("%w: field %d", ErrMalformedPayload, num)
			}
			rem = rem[m:]
		}
	}
	if out.AssistID == "" {
		return out, fmt.Errorf("%w: missing assist_id", ErrMalformedPayload)
	}
	return out, nil
}
