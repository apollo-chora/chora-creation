// Package aiassist — domain entity for the async AI Assist job projection.
//
// Per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md Step 4 the
// /api/atoms/ai-assist HTTP surface is async:
//
//	POST  →  202 + AiAssistJob{status: QUEUED}                   handler INSERTs
//	... orchestrator runs the qgen 2-agent crew via Pub/Sub ...
//	GET   →  200 + AiAssistJob{status: COMPLETED|REFUSED|FAILED} handler SELECTs
//
// This package is the chora-creation-owned aggregate projection of the
// orchestrator's lifecycle. The orchestrator's checkpoint state in
// chora_ai_kernel is its own concern (cross-DB queries FORBIDDEN per
// .claude/rules/ddd-enforcement.md); the row here is what the FE reads.
//
// Separate package from chora-creation `question` (the legacy 3-job-type
// AI Draft / Model Answer / Batch Source pattern with 7-state FSM); the
// qgen 2-agent crew's async job is a distinct lifecycle.
package aiassist

import (
	"fmt"
	"time"
)

// -----------------------------------------------------------------------------
// Status — 5 values per migration 0010 + OpenAPI AiAssistJob.status
// -----------------------------------------------------------------------------

// Status is the AiAssistJob FSM. Mirrors
// chora-contracts/openapi/creation-questions.yaml §AiAssistJob.status
// enum AND the migration 0010 CHECK constraint.
type Status string

const (
	StatusQueued     Status = "QUEUED"      // POST returns; orchestrator hasn't started
	StatusInProgress Status = "IN_PROGRESS" // orchestrator running the graph
	StatusCompleted  Status = "COMPLETED"   // critic accepted OR retries exhausted with quality_warning
	StatusRefused    Status = "REFUSED"     // Cloud Model Armor blocked OR validation refused
	StatusFailed     Status = "FAILED"      // orchestrator graph hit a non-recoverable error
)

// Valid returns true iff s is one of the 5 enum members.
func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusInProgress, StatusCompleted, StatusRefused, StatusFailed:
		return true
	}
	return false
}

// IsTerminal returns true iff s is a final state (no outbound transitions).
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusRefused, StatusFailed:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// QuestionType — same enum as chora-contracts QuestionType (mcq | oe)
// -----------------------------------------------------------------------------

type QuestionType string

const (
	QuestionTypeMCQ QuestionType = "mcq"
	QuestionTypeOE  QuestionType = "oe"
)

// Valid returns true iff qt is one of the Phyllis-scope enum members.
func (qt QuestionType) Valid() bool {
	switch qt {
	case QuestionTypeMCQ, QuestionTypeOE:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// RefusalReason — 3 values per OpenAPI AiAssistRefusal.reason
// -----------------------------------------------------------------------------

type RefusalReason string

const (
	RefusalReasonGuardrailPre  RefusalReason = "GUARDRAIL_PRE"
	RefusalReasonGuardrailPost RefusalReason = "GUARDRAIL_POST"
	RefusalReasonValidation    RefusalReason = "VALIDATION"
)

// Valid returns true iff rr is one of the 3 enum members OR empty
// (empty == not-refused).
func (rr RefusalReason) Valid() bool {
	switch rr {
	case "", RefusalReasonGuardrailPre, RefusalReasonGuardrailPost, RefusalReasonValidation:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Job aggregate
// -----------------------------------------------------------------------------

// Job is the AiAssistJob projection row. Mirrors migration 0010 column-
// for-column plus the OpenAPI AiAssistJob field shape.
//
// The aggregate is intentionally thin — most state transitions happen
// in the orchestrator (LangGraph state machine) and arrive here via the
// Pub/Sub subscriber. The handler just INSERTs the QUEUED row at POST
// time; the subscriber UPDATEs on completed.v1 / refused.v1; FE GETs.
type Job struct {
	// Identity
	ID         string // UUIDv7 minted at POST
	TenantID   string
	AuthorGCID string

	// FSM
	Status Status

	// Discriminator
	QuestionType QuestionType

	// Original AiAssistGenerateRequest body — verbatim JSON for audit
	// + for the orchestrator to read out of the started.v1 event.
	RequestPayload []byte // JSON

	// Populated when Status reaches COMPLETED (or REFUSED with last_
	// candidate). Mirrors OpenAPI AiAssistCandidate.
	ResultPayload []byte // JSON or nil

	// Per-step IMDA D2 transparency trace. Mirrors OpenAPI
	// PipelineTraceStep[].
	PipelineTrace []byte // JSON or nil

	// True when critic rejected on all max_retries attempts but
	// orchestrator still emitted completed.v1 (NOT refused.v1).
	QualityWarning bool

	// Populated when Status = REFUSED.
	RefusalReason        RefusalReason
	RefusalArmorVerdict  string
	RefusalUserFacingMsg string

	// Loop accounting.
	AttemptCount int

	// Cost finalised at terminal time.
	ManaCharged int

	// Timestamps
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// NewQueuedJob constructs a freshly-minted Job in QUEUED state, ready to
// hand to the repository's Create.
func NewQueuedJob(
	id, tenantID, authorGCID string,
	qt QuestionType,
	requestPayload []byte,
) (*Job, error) {
	if id == "" {
		return nil, fmt.Errorf("aiassist: id required")
	}
	if tenantID == "" {
		return nil, fmt.Errorf("aiassist: tenant_id required")
	}
	if authorGCID == "" {
		return nil, fmt.Errorf("aiassist: author_gcid required")
	}
	if !qt.Valid() {
		return nil, fmt.Errorf("aiassist: invalid question_type %q (mcq | oe only)", qt)
	}
	if len(requestPayload) == 0 {
		return nil, fmt.Errorf("aiassist: request_payload required")
	}
	now := time.Now().UTC()
	return &Job{
		ID:             id,
		TenantID:       tenantID,
		AuthorGCID:     authorGCID,
		Status:         StatusQueued,
		QuestionType:   qt,
		RequestPayload: requestPayload,
		AttemptCount:   0,
		ManaCharged:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// MarkInProgress is the QUEUED → IN_PROGRESS transition called by the
// subscriber on first orchestrator-side observation (e.g., when an
// attempt.v1 event arrives — M14.2 streaming scope). The handler-time
// repo never invokes this; today MarkCompleted/MarkRefused/MarkFailed
// are sufficient for the terminal-only event coverage.
func (j *Job) MarkInProgress() error {
	if j.Status != StatusQueued {
		return fmt.Errorf("aiassist: cannot transition %s → IN_PROGRESS", j.Status)
	}
	j.Status = StatusInProgress
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkCompleted is the subscriber-called terminal transition on
// chora.creation.ai_assist.completed.v1.
//
// quality_warning is true when the critic rejected on all attempts;
// the candidate is still surfaced + carries critic_notes per the user-
// locked semantics 2026-05-17.
func (j *Job) MarkCompleted(
	resultPayload, pipelineTrace []byte,
	qualityWarning bool,
	attemptCount int,
	manaCharged int,
) error {
	if j.Status.IsTerminal() {
		return fmt.Errorf("aiassist: cannot transition terminal %s → COMPLETED", j.Status)
	}
	now := time.Now().UTC()
	j.Status = StatusCompleted
	j.ResultPayload = resultPayload
	j.PipelineTrace = pipelineTrace
	j.QualityWarning = qualityWarning
	j.AttemptCount = attemptCount
	j.ManaCharged = manaCharged
	j.UpdatedAt = now
	j.CompletedAt = &now
	return nil
}

// MarkRefused is the subscriber-called terminal transition on
// chora.creation.ai_assist.refused.v1.
//
// reason MUST be one of the 3 RefusalReason enum members. Per
// [[feedback-no-stubs-real-wiring]] empty reason on a refused row is
// rejected.
func (j *Job) MarkRefused(
	reason RefusalReason,
	armorVerdict, userFacingMsg string,
	lastCandidatePayload, pipelineTrace []byte,
	attemptCount int,
	manaCharged int,
) error {
	if j.Status.IsTerminal() {
		return fmt.Errorf("aiassist: cannot transition terminal %s → REFUSED", j.Status)
	}
	if reason == "" || !reason.Valid() {
		return fmt.Errorf("aiassist: refusal_reason %q invalid (must be GUARDRAIL_PRE | GUARDRAIL_POST | VALIDATION)", reason)
	}
	now := time.Now().UTC()
	j.Status = StatusRefused
	j.RefusalReason = reason
	j.RefusalArmorVerdict = armorVerdict
	j.RefusalUserFacingMsg = userFacingMsg
	j.ResultPayload = lastCandidatePayload // best-effort last attempt
	j.PipelineTrace = pipelineTrace
	j.AttemptCount = attemptCount
	j.ManaCharged = manaCharged
	j.UpdatedAt = now
	j.CompletedAt = &now
	return nil
}

// MarkFailed is the subscriber-called terminal transition for non-
// recoverable orchestrator errors (graph crash beyond checkpointer
// recovery; dispatch transport failure with retries exhausted).
//
// Failed jobs MAY carry partial pipeline_trace + last attempt for
// debugging but no candidate is surfaced to the FE (status=FAILED is
// rendered as a generic error).
func (j *Job) MarkFailed(
	pipelineTrace []byte,
	errMsg string,
	attemptCount int,
	manaCharged int,
) error {
	if j.Status.IsTerminal() {
		return fmt.Errorf("aiassist: cannot transition terminal %s → FAILED", j.Status)
	}
	now := time.Now().UTC()
	j.Status = StatusFailed
	j.PipelineTrace = pipelineTrace
	j.RefusalUserFacingMsg = errMsg // surfaced to FE under a generic 'failed' chip
	j.AttemptCount = attemptCount
	j.ManaCharged = manaCharged
	j.UpdatedAt = now
	j.CompletedAt = &now
	return nil
}
