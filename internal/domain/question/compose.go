// compose.go — ADR-195 the unified `compose` operation domain model.
//
// The five-value job_type enum (job.go) fragmented ONE capability across three
// orthogonal axes. ADR-195 collapses it into a single `compose` operation
// modelled by Intent × Input × Plan — the SAME axes the qgen composer already
// consumes (poc/.../agent/composer_question.go). The agent layer is UNCHANGED;
// this file only conforms the chora-creation job layer to it.
//
//   - Intent — the domain discriminant (different outputs/persistence).
//   - Input  — the generation seed (prompt / source files / by-hand inline).
//   - Plan   — the per-type generation plan (aiassist.TypePlan); count drives it.
//
// The aggregate is ComposeJob (defined in job.go); the legacy job_type enum +
// column were retired in WS9 (the domain branches on Intent / Input).
package question

import (
	"fmt"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
)

// -----------------------------------------------------------------------------
// Intent — the domain discriminant of a compose job (ADR-195 D1).
// -----------------------------------------------------------------------------

// Intent mirrors composer_question.go's Intent VO VERBATIM (the agent branches
// on these strings, never on the retired job_type enum). Different intents have
// genuinely different outputs/persistence, so the domain legitimately branches
// on Intent.
type Intent string

const (
	// IntentNewQuestion mints N brand-new question atoms from a seed. The ONLY
	// intent that mints atoms.
	IntentNewQuestion Intent = "new_question"
	// IntentModelAnswerFill fills the model answer of ONE existing question from
	// the author's stem (pure-LLM, single-Q). Mints nothing.
	IntentModelAnswerFill Intent = "model_answer_fill"
	// IntentImageRegen re-renders ONE image of an existing parent candidate in
	// place (carries the parent ref + placement). A post-generation,
	// user-triggered patch — never mints an atom.
	IntentImageRegen Intent = "image_regen"
)

// Valid reports whether i is one of the three compose intents.
func (i Intent) Valid() bool {
	switch i {
	case IntentNewQuestion, IntentModelAnswerFill, IntentImageRegen:
		return true
	}
	return false
}

// MintsAtoms reports whether this intent produces new question atoms. Only
// new_question does; fill/image_regen mutate an existing candidate in place.
func (i Intent) MintsAtoms() bool { return i == IntentNewQuestion }

// OperationCompose is the single `operation` value stamped on every ADR-195 .v2
// compose event (D7). There is exactly ONE operation — `intent` is the real
// discriminant — so this is a constant, never a stored column (the retired
// job_type's information is carried by Intent × Input, not a vestigial enum).
const OperationCompose = "compose"

// ComposeModelForJob resolves a job's {intent, input_kind} for the ADR-195 .v2
// compose events directly from the explicit compose VOs. Every job carries them:
// create populates them (WS3) and the repo read reconstructs them from the intent
// + input_kind columns (ReconstructInput, WS9 step 1). The legacy job_type fallback
// was retired with the enum in WS9 step 3.
func ComposeModelForJob(j *ComposeJob) (Intent, InputKind) {
	if j == nil {
		return IntentNewQuestion, InputPrompt
	}
	return j.Intent, j.Input.Kind()
}

// -----------------------------------------------------------------------------
// Input — the generation seed (ADR-195 D1).
// -----------------------------------------------------------------------------

// SourceFileRef is one role-tagged grounding file backing a source-material
// compose. Mirrors ports.QuestionBatchSourceFile / the settings source_files[]
// shape ({blob_uri, mime_type, role}).
type SourceFileRef struct {
	BlobURI  string
	MimeType string
	Role     string
}

// InputKind is the canonical classification of a compose job's seed, surfaced
// in the .v2 event `input_kind` (ADR-195 D7). Derived from Input — never stored
// as a mode.
type InputKind string

const (
	// InputByHand — the author supplies candidates inline; no LLM call.
	InputByHand InputKind = "by_hand"
	// InputSourceFiles — RAG / parse_document grounding from source files.
	InputSourceFiles InputKind = "source_files"
	// InputPrompt — pure-LLM generation from a text prompt.
	InputPrompt InputKind = "prompt"
)

// Input is the generation seed of a compose job: a prompt, role-tagged source
// files, or by-hand inline candidates. Input is an input-conditioned CAPABILITY,
// not a mode — source files → RAG, prompt-only → pure-LLM, by-hand → no LLM. The
// orchestrator threads these into the composer's Prompt / attached-file context
// unchanged.
type Input struct {
	Prompt      string
	SourceFiles []SourceFileRef
	ByHand      bool
}

// HasFiles reports whether any source file is attached.
func (in Input) HasFiles() bool { return len(in.SourceFiles) > 0 }

// HasPrompt reports whether a non-empty text prompt is present.
func (in Input) HasPrompt() bool { return in.Prompt != "" }

// RequiresLLM reports whether generation must call the model. By-hand authoring
// supplies candidates inline and never invokes the LLM.
func (in Input) RequiresLLM() bool { return !in.ByHand }

// Kind folds the seed into its canonical classification (precedence:
// by-hand > source-files > prompt) for the .v2 event input_kind.
func (in Input) Kind() InputKind {
	switch {
	case in.ByHand:
		return InputByHand
	case in.HasFiles():
		return InputSourceFiles
	default:
		return InputPrompt
	}
}

// ReconstructInput rebuilds the Input VO from its persisted classification — the
// input_kind column that migration 0022 stamped (= Input.Kind()) on every row —
// plus the durable seed recovered from the job row. It is the INVERSE of Kind():
// for the three real kinds, ReconstructInput(k, ...).Kind() == k.
//
// ADR-195 WS9 step 1: the repo read uses it so a loaded ComposeJob carries its
// Input VO, letting the domain branch on the explicit compose model — retiring the
// job_type fallback in ComposeModelForJob (WS9 step 2/3). by_hand / source_files
// MUST set their discriminating field (the job_type tier that previously classified
// them is dropped); prompt is the catch-all (an empty seed still classifies as
// prompt). The caller passes ≥1 file for source_files (the read guarantees this via
// the canonical source_blob_uri), so the source_files round-trip holds.
func ReconstructInput(kind InputKind, prompt string, files []SourceFileRef) Input {
	switch kind {
	case InputByHand:
		return Input{ByHand: true}
	case InputSourceFiles:
		return Input{SourceFiles: files}
	default: // InputPrompt + any empty/unknown kind (a NULL pre-0022 transitional row)
		return Input{Prompt: prompt}
	}
}

// Validate enforces the intent-conditioned seed invariants (fail-loud — an
// invalid seed is rejected, never coerced):
//
//   - by-hand is valid ONLY for new_question, and must NOT carry a prompt or
//     files (candidates are supplied inline);
//   - new_question (non-by-hand) requires a prompt or source files;
//   - model_answer_fill / image_regen are single-target intents and take NO
//     source files (single-Q is intentionally pure-LLM; image_regen is a patch).
func (in Input) Validate(intent Intent) error {
	if !intent.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidIntent, string(intent))
	}
	if in.ByHand {
		if intent != IntentNewQuestion {
			return fmt.Errorf("%w: intent=%s", ErrComposeByHandIntent, intent)
		}
		if in.HasPrompt() || in.HasFiles() {
			return ErrComposeByHandSeeded
		}
		return nil
	}
	switch intent {
	case IntentNewQuestion:
		if !in.HasPrompt() && !in.HasFiles() {
			return ErrComposeInputEmpty
		}
	case IntentModelAnswerFill, IntentImageRegen:
		if in.HasFiles() {
			return fmt.Errorf("%w: intent=%s", ErrComposeNoFilesForIntent, intent)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Plan — NewComposePlan: count ALWAYS drives a non-empty per-type plan.
// -----------------------------------------------------------------------------

// NewComposePlan builds the per-type generation plan for a new_question compose
// job. count ALWAYS drives the plan — count=1 yields a 1-entry plan — retiring
// the SetMode boolean and the "count=5 → 1 question" defect (ADR-195 D1/D8).
//
//   - quotas non-empty (mixed-type): validated via aiassist.NewTypePlan against
//     count (their sum must equal count).
//   - quotas empty (single-type): a 1-entry plan {fallbackType, count} is built
//     and validated, so a single-type request is a REAL plan — not the legacy
//     "count lives outside the plan" path.
func NewComposePlan(quotas []aiassist.TypeQuota, fallbackType string, count int) (aiassist.TypePlan, error) {
	if len(quotas) == 0 {
		if count < 1 {
			return nil, fmt.Errorf("%w: single-type count=%d", aiassist.ErrTypePlanCountTooLow, count)
		}
		quotas = []aiassist.TypeQuota{{QuestionType: fallbackType, Count: count}}
	}
	return aiassist.NewTypePlan(quotas, count)
}

// -----------------------------------------------------------------------------
// ComposeJob invariants (the aggregate is defined in job.go)
// -----------------------------------------------------------------------------

// Validate enforces the ADR-195 intent-branched compose invariants over the
// aggregate. The domain legitimately branches on Intent (D1): only a
// new_question generation (RequiresLLM) carries a per-type Plan; by-hand
// authoring and the single-target intents (model_answer_fill / image_regen)
// carry none.
func (j *ComposeJob) Validate() error {
	if !j.Intent.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidIntent, string(j.Intent))
	}
	if err := j.Input.Validate(j.Intent); err != nil {
		return err
	}
	planPresent := !j.Plan.IsEmpty()
	if j.Intent == IntentNewQuestion && j.Input.RequiresLLM() {
		// count must drive a non-empty plan (the count=5→1 structural fix).
		if !planPresent {
			return fmt.Errorf("%w: intent=new_question input=%s", ErrComposePlanEmpty, j.Input.Kind())
		}
		return nil
	}
	// by-hand new_question, model_answer_fill, image_regen: inline / single-target
	// — a generation plan is not applicable.
	if planPresent {
		return fmt.Errorf("%w: intent=%s input=%s", ErrComposePlanNotApplicable, j.Intent, j.Input.Kind())
	}
	return nil
}
