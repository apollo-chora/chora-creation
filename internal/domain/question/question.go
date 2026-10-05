// Package question is the Question sub-entity of the LearningAtom aggregate
// in the Content Creation domain.
//
// Per ddd-enforcement.md invariant #4, QuestionRevision is APPEND-ONLY —
// every authoring mutation creates a new revision row + bumps the parent
// atom revision; nothing is ever UPDATEd in place. The Question struct
// itself is treated as an immutable value relative to its revision history:
// ApplyUpdate returns a NEW *Question rather than mutating the receiver.
//
// Per design §2.1 (`docs/m14/cr-question-authoring-design-2026-05-15.md`)
// the QuestionType enum has 16 values mirroring migration 0005:
// `mcq` + `oe` in scope; 14 `reserved_*` sentinels for future onboarding.
//
// Hexagonal architecture: this package has NO infrastructure imports
// (no pgx, no http, no grpc, no protobuf). Adapters depend on this package;
// never the reverse.
package question

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// QuestionType — 16-value enum (in alphabetical order across reserved_*)
//
// Wire-format strings match the chora_creation.question_type Postgres ENUM
// declared in migration 0005_question_type_enum.up.sql AND the OpenAPI
// enum in chora-contracts/openapi/creation-questions.yaml. The ordinal
// stability of reserved_* is part of the contract — DO NOT reorder.
// -----------------------------------------------------------------------------

type QuestionType string

const (
	// In scope for this CR
	TypeMCQ       QuestionType = "mcq" // Multiple-Choice (deterministic grade)
	TypeOpenEnded QuestionType = "oe"  // Open-Ended (LLM-graded downstream)

	// Reserved — out of scope for this CR, enum-stable for future onboarding.
	// Alphabetical order across the reserved_* slots is part of the wire contract.
	TypeReservedCodeExecution   QuestionType = "reserved_code_execution"
	TypeReservedCompletion      QuestionType = "reserved_completion"
	TypeReservedDragDrop        QuestionType = "reserved_drag_drop"
	TypeReservedFillBlank       QuestionType = "reserved_fill_blank"
	TypeReservedMatching        QuestionType = "reserved_matching"
	TypeReservedMultiSelect     QuestionType = "reserved_multi_select"
	TypeReservedMultimedia      QuestionType = "reserved_multimedia"
	TypeReservedOral            QuestionType = "reserved_oral"
	TypeReservedOrdering        QuestionType = "reserved_ordering"
	TypeReservedPeerGraded      QuestionType = "reserved_peer_graded"
	TypeReservedShortAnswer     QuestionType = "reserved_short_answer"
	TypeReservedSimulation      QuestionType = "reserved_simulation"
	TypeReservedTableCompletion QuestionType = "reserved_table_completion"
	TypeReservedTrueFalse       QuestionType = "reserved_true_false"
)

// Enabled returns true iff the type is in scope for the V1 CR (MCQ + OE).
// Reserved_* values are valid wire format but the handler returns 501.
func (t QuestionType) Enabled() bool {
	switch t {
	case TypeMCQ, TypeOpenEnded:
		return true
	}
	return false
}

// Valid returns true iff the type is one of the 16 enum members.
func (t QuestionType) Valid() bool {
	switch t {
	case TypeMCQ, TypeOpenEnded,
		TypeReservedCodeExecution, TypeReservedCompletion, TypeReservedDragDrop,
		TypeReservedFillBlank, TypeReservedMatching, TypeReservedMultiSelect,
		TypeReservedMultimedia, TypeReservedOral, TypeReservedOrdering,
		TypeReservedPeerGraded, TypeReservedShortAnswer, TypeReservedSimulation,
		TypeReservedTableCompletion, TypeReservedTrueFalse:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Question — sub-entity aggregate
// -----------------------------------------------------------------------------

// Question is the LearningAtom sub-entity tracked per migration 0006_questions.
// One Question per atom is enforced at the DB layer (D2 — partial UNIQUE on
// (atom_id) WHERE deleted_at IS NULL). Soft delete only.
type Question struct {
	QuestionID       string          `json:"question_id"`
	AtomID           string          `json:"atom_id"`
	TenantID         string          `json:"tenant_id"`
	AuthorGcid       string          `json:"author_gcid"`
	Type             QuestionType    `json:"type"`
	Prompt           string          `json:"prompt"`
	SourceType       atom.SourceType `json:"source_type"`
	LatestRevisionID string          `json:"latest_revision_id,omitempty"`
	Revision         int             `json:"revision"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`

	// Discriminated payload — exactly one of these is non-nil based on Type.
	// (Wire shape matches OpenAPI Question schema.)
	MCQ *MCQPayload `json:"mcq,omitempty"`
	OE  *OEPayload  `json:"oe,omitempty"`
}

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID   string
	AtomID     string
	AuthorGcid string
	Type       QuestionType
	Prompt     string
	SourceType atom.SourceType
	MCQ        *MCQPayload // required iff Type == TypeMCQ
	OE         *OEPayload  // required iff Type == TypeOpenEnded
}

// New constructs a Question aggregate enforcing the §2 invariants. Returns
// a typed error when an invariant is violated. Does NOT touch any
// infrastructure (no DB, no clock injection — uses time.Now().UTC()).
func New(p NewParams) (*Question, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("question: tenant_id is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("question: atom_id is required")
	}
	if strings.TrimSpace(p.AuthorGcid) == "" {
		return nil, errors.New("question: author_gcid is required")
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return nil, errors.New("question: prompt is required")
	}
	if len(prompt) > 4096 {
		return nil, fmt.Errorf("question: prompt too long: %d > 4096", len(prompt))
	}

	if !p.Type.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidType, string(p.Type))
	}
	if !p.Type.Enabled() {
		return nil, fmt.Errorf("%w: %q", ErrTypeNotEnabled, string(p.Type))
	}

	if !p.SourceType.Valid() {
		return nil, fmt.Errorf("question: invalid source_type: %q", string(p.SourceType))
	}

	// Discriminated payload enforcement — exactly one matching the Type.
	switch p.Type {
	case TypeMCQ:
		if p.MCQ == nil {
			return nil, errors.New("question: MCQ payload required for type=mcq")
		}
		if p.OE != nil {
			return nil, fmt.Errorf("%w: mcq question with oe payload", ErrPayloadTypeMismatch)
		}
		if err := p.MCQ.Validate(); err != nil {
			return nil, err
		}
	case TypeOpenEnded:
		if p.OE == nil {
			return nil, errors.New("question: OE payload required for type=oe")
		}
		if p.MCQ != nil {
			return nil, fmt.Errorf("%w: oe question with mcq payload", ErrPayloadTypeMismatch)
		}
		if err := p.OE.Validate(); err != nil {
			return nil, err
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("question: uuidv7: %w", err)
	}

	now := time.Now().UTC()
	return &Question{
		QuestionID: id.String(),
		AtomID:     p.AtomID,
		TenantID:   p.TenantID,
		AuthorGcid: p.AuthorGcid,
		Type:       p.Type,
		Prompt:     prompt,
		SourceType: p.SourceType,
		Revision:   0, // bumped to 1 by NewRevision when first revision is created
		CreatedAt:  now,
		UpdatedAt:  now,
		MCQ:        p.MCQ,
		OE:         p.OE,
	}, nil
}

// SoftDelete sets DeletedAt to now. Idempotent: re-calling on an already
// soft-deleted question is a no-op (DeletedAt preserved).
//
// Per ddd-enforcement invariant #5, this is the only deletion semantic for
// the aggregate; hard delete is reserved for crypto-shred at closure time.
func (q *Question) SoftDelete() {
	if q.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	q.DeletedAt = &now
	q.UpdatedAt = now
}

// IsActive returns true iff the question is not soft-deleted.
func (q *Question) IsActive() bool { return q.DeletedAt == nil }

// -----------------------------------------------------------------------------
// ApplyUpdate — append-only PATCH semantics
//
// Per ddd-enforcement #4, ApplyUpdate does NOT mutate the receiver. It returns
// a NEW *Question (so the original aggregate snapshot remains visible to the
// caller for diff/audit) plus the NEW *QuestionRevision the caller must persist.
//
// The Type field is immutable — passing a payload that does not match the
// existing Type returns ErrPayloadTypeMismatch.
// -----------------------------------------------------------------------------

// UpdateParams is the partial-update payload (PATCH semantics). Only fields
// set to non-nil are touched on the returned *Question. The new revision
// snapshot uses the present payload; if the caller leaves a payload nil, the
// previous revision's payload is reused.
type UpdateParams struct {
	Prompt     *string         // optional new prompt
	MCQ        *MCQPayload     // optional new MCQ payload (must match q.Type=mcq)
	OE         *OEPayload      // optional new OE payload (must match q.Type=oe)
	AuthorGcid string          // author of the edit (required)
	SourceType atom.SourceType // provenance of this edit (required)
}

// ApplyUpdate produces an updated *Question + a new *QuestionRevision without
// mutating the receiver. Caller is responsible for persisting both atomically
// via QuestionRepository.AppendRevision + Save.
func (q *Question) ApplyUpdate(p UpdateParams) (*Question, *QuestionRevision, error) {
	if q.DeletedAt != nil {
		return nil, nil, errors.New("question: cannot update soft-deleted question")
	}

	// Type immutability — payload must match q.Type.
	switch q.Type {
	case TypeMCQ:
		if p.OE != nil {
			return nil, nil, fmt.Errorf("%w: mcq question with oe payload", ErrPayloadTypeMismatch)
		}
	case TypeOpenEnded:
		if p.MCQ != nil {
			return nil, nil, fmt.Errorf("%w: oe question with mcq payload", ErrPayloadTypeMismatch)
		}
	}

	if strings.TrimSpace(p.AuthorGcid) == "" {
		return nil, nil, errors.New("question: author_gcid is required for ApplyUpdate")
	}
	if !p.SourceType.Valid() {
		return nil, nil, fmt.Errorf("question: invalid source_type: %q", string(p.SourceType))
	}

	// Compute the next prompt (use previous when omitted).
	newPrompt := q.Prompt
	if p.Prompt != nil {
		nm := strings.TrimSpace(*p.Prompt)
		if nm == "" {
			return nil, nil, errors.New("question: prompt cannot be cleared (empty after trim)")
		}
		if len(nm) > 4096 {
			return nil, nil, fmt.Errorf("question: prompt too long: %d > 4096", len(nm))
		}
		newPrompt = nm
	}

	// Compute next payload (use previous when omitted).
	nextMCQ := q.MCQ
	nextOE := q.OE
	if p.MCQ != nil {
		nextMCQ = p.MCQ
	}
	if p.OE != nil {
		nextOE = p.OE
	}

	// Build a NEW question snapshot — receiver is NOT mutated (append-only).
	now := time.Now().UTC()
	updated := &Question{
		QuestionID:       q.QuestionID,
		AtomID:           q.AtomID,
		TenantID:         q.TenantID,
		AuthorGcid:       q.AuthorGcid,
		Type:             q.Type,
		Prompt:           newPrompt,
		SourceType:       p.SourceType,
		LatestRevisionID: "", // filled below
		Revision:         q.Revision + 1,
		DeletedAt:        nil,
		CreatedAt:        q.CreatedAt,
		UpdatedAt:        now,
		MCQ:              nextMCQ,
		OE:               nextOE,
	}

	rev, err := newRevisionInternal(updated, newPrompt, nextMCQ, nextOE, p.AuthorGcid, p.SourceType, now)
	if err != nil {
		return nil, nil, err
	}
	updated.LatestRevisionID = rev.RevisionID
	return updated, rev, nil
}
