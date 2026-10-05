// Append-only revision history of a Question. Per ddd-enforcement.md
// invariant #4, QuestionRevision is APPEND-ONLY: never UPDATEd or DELETEd
// in place (the migration 0007 trigger enforces this at the DB layer using
// the shared enforce_atom_revisions_append_only() function).
//
// Exactly ONE of MCQPayload / OEPayload is non-nil per revision row — the
// migration adds a CHECK constraint mirroring this invariant.
package question

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// QuestionRevision is one immutable snapshot of a Question's payload at a
// specific RevisionNumber. The corresponding `questions.latest_revision_id`
// points at the most-recent QuestionRevision row.
//
// Source provenance (manual / ai_assist / ...) matches atom.SourceType to
// preserve the parallel revision history pattern from `atom/phyllis.go`.
type QuestionRevision struct {
	RevisionID     string            `json:"revision_id"`
	QuestionID     string            `json:"question_id"`
	AtomID         string            `json:"atom_id"`
	TenantID       string            `json:"tenant_id"`
	RevisionNumber int               `json:"revision_number"`
	Prompt         string            `json:"prompt"`
	MCQPayload     *MCQPayload       `json:"mcq_payload,omitempty"`
	OEPayload      *OEPayload        `json:"oe_payload,omitempty"`
	SourceType     atom.SourceType   `json:"source_type"`
	SourceMetadata map[string]string `json:"source_metadata,omitempty"`
	AuthoredByGCID string            `json:"authored_by_gcid"`
	AuthoredAt     time.Time         `json:"authored_at"`
}

// NewRevision constructs a QuestionRevision tied to a parent Question.
// Enforces:
//   - exactly one of MCQPayload / OEPayload is non-nil (mirror the migration CHECK)
//   - payload type matches parent Question.Type
//   - revision_number is monotonically increasing (q.Revision + 1)
//   - the supplied payload passes .Validate()
//
// Used by both initial creation (first revision) and ApplyUpdate (PATCH).
func NewRevision(
	q *Question,
	prompt string,
	mcq *MCQPayload,
	oe *OEPayload,
	authoredBy string,
	source atom.SourceType,
) (*QuestionRevision, error) {
	if q == nil {
		return nil, errors.New("question revision: parent question is nil")
	}
	now := time.Now().UTC()
	return newRevisionInternal(q, prompt, mcq, oe, authoredBy, source, now)
}

// newRevisionInternal is the shared implementation between NewRevision and
// ApplyUpdate so the "now" timestamp can be threaded explicitly when one
// caller has already computed it (keeps the parent Question.UpdatedAt and
// the revision's AuthoredAt aligned to the same instant).
func newRevisionInternal(
	q *Question,
	prompt string,
	mcq *MCQPayload,
	oe *OEPayload,
	authoredBy string,
	source atom.SourceType,
	now time.Time,
) (*QuestionRevision, error) {
	if strings.TrimSpace(authoredBy) == "" {
		return nil, errors.New("question revision: authored_by_gcid is required")
	}
	if !source.Valid() {
		return nil, fmt.Errorf("question revision: invalid source_type: %q", string(source))
	}

	// Exactly-one-payload enforcement (mirrors migration CHECK constraint).
	switch {
	case mcq == nil && oe == nil:
		return nil, errors.New("question revision: exactly one of mcq/oe payload must be set, got neither")
	case mcq != nil && oe != nil:
		return nil, errors.New("question revision: exactly one of mcq/oe payload must be set, got both")
	}

	// Payload type matches parent question Type.
	switch q.Type {
	case TypeMCQ:
		if mcq == nil {
			return nil, fmt.Errorf("%w: mcq question requires mcq payload", ErrPayloadTypeMismatch)
		}
		if err := mcq.Validate(); err != nil {
			return nil, err
		}
	case TypeOpenEnded:
		if oe == nil {
			return nil, fmt.Errorf("%w: oe question requires oe payload", ErrPayloadTypeMismatch)
		}
		if err := oe.Validate(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidType, string(q.Type))
	}

	revID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("question revision: uuidv7: %w", err)
	}

	// Compute monotonic revision number. The parent Question.Revision counter is
	// incremented by ApplyUpdate _before_ calling newRevisionInternal (so it's
	// already at the target value). For the seed-revision case (called directly
	// via NewRevision on a fresh Question whose Revision==0), the parent counter
	// is bumped here to 1.
	nextRevNum := q.Revision
	if nextRevNum == 0 {
		nextRevNum = 1
		q.Revision = 1
	}

	return &QuestionRevision{
		RevisionID:     revID.String(),
		QuestionID:     q.QuestionID,
		AtomID:         q.AtomID,
		TenantID:       q.TenantID,
		RevisionNumber: nextRevNum,
		Prompt:         prompt,
		MCQPayload:     mcq,
		OEPayload:      oe,
		SourceType:     source,
		AuthoredByGCID: authoredBy,
		AuthoredAt:     now,
	}, nil
}
