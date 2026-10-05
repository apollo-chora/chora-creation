// Typed domain errors for the Question aggregate. Adapters wrap these with
// HTTP status codes at the boundary; the domain layer surfaces semantics only.
package question

import "errors"

// ErrNotFound is the canonical sentinel for a missing-or-soft-deleted question.
var ErrNotFound = errors.New("question: not found")

// ErrInvalidType means the QuestionType value is not one of the 16 enum members.
var ErrInvalidType = errors.New("question: invalid type")

// ErrTypeNotEnabled means the QuestionType is one of the 14 reserved_*
// sentinels — valid wire format but not in scope for the V1 CR (MCQ + OE only).
// HTTP boundary returns 501 NOT_IMPLEMENTED.
var ErrTypeNotEnabled = errors.New("question: type not enabled (V1 ships MCQ+OE only)")

// ErrPayloadTypeMismatch means the discriminated payload (MCQ vs OE) does not
// agree with the parent question's Type. The Type column is immutable per the
// design — ApplyUpdate must reject any mismatch.
var ErrPayloadTypeMismatch = errors.New("question: payload type does not match question.type")

// ErrAtomHasQuestion is the D2 cardinality violation: 1 atom = 1 non-deleted
// question. Adapters convert to HTTP 409 Conflict.
var ErrAtomHasQuestion = errors.New("question: atom already has a non-deleted question (D2)")

// ErrInvalidStateTransition is raised by ComposeJob.Transition when
// the (from, to) pair is not in the allowed-transitions table.
var ErrInvalidStateTransition = errors.New("question.job: invalid state transition")

// ErrInsufficientMana is the canonical sentinel for the 402 path. The mana
// ledger port returns this when the deduction would underflow; handlers wrap
// it with the InsufficientManaUpsell envelope per design §3.3.
var ErrInsufficientMana = errors.New("question: insufficient mana")

// -----------------------------------------------------------------------------
// ADR-195 compose-authoring invariants (compose.go). All fail-loud — an invalid
// compose job is rejected, never coerced. Adapters map these to HTTP 400.
// -----------------------------------------------------------------------------

// ErrInvalidIntent — the Intent value is not one of new_question /
// model_answer_fill / image_regen.
var ErrInvalidIntent = errors.New("compose: invalid intent")

// ErrComposeInputEmpty — a new_question compose has no generation seed (no
// prompt, no source files, not by-hand).
var ErrComposeInputEmpty = errors.New("compose: new_question requires a prompt or source files")

// ErrComposeByHandSeeded — by-hand authoring supplies candidates inline and must
// not carry a prompt or source files.
var ErrComposeByHandSeeded = errors.New("compose: by-hand authoring must not carry a prompt or source files")

// ErrComposeByHandIntent — by-hand authoring is only valid for new_question
// (model_answer_fill / image_regen always invoke the model).
var ErrComposeByHandIntent = errors.New("compose: by-hand authoring is only valid for new_question")

// ErrComposeNoFilesForIntent — model_answer_fill / image_regen are single-target
// intents and take no source files (single-Q is pure-LLM; image_regen is a patch).
var ErrComposeNoFilesForIntent = errors.New("compose: only new_question accepts source files")

// ErrComposePlanEmpty — a new_question generation has no per-type plan; count
// must drive a non-empty plan (the count=5→1 structural fix).
var ErrComposePlanEmpty = errors.New("compose: new_question generation requires a non-empty plan (count drives the plan)")

// ErrComposePlanNotApplicable — only a new_question generation carries a plan;
// by-hand authoring and the single-target intents must not.
var ErrComposePlanNotApplicable = errors.New("compose: a generation plan is not applicable for this intent")
