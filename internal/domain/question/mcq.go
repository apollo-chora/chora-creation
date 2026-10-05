// MCQ-specific payload + validation. Lives in the Question aggregate of the
// Content Creation domain. Per CR design §2.3:
//   - At least 2 options (max 8 from contract, enforced at handler boundary)
//   - Exactly 1 IsCorrect=true (V1 ships single-correct only; multi_select
//     is reserved per migration 0005)
//   - Per-option Explainer is OPTIONAL (ADR-189 / CHO-1826) — manual authors
//     may omit it on any/all options; AI generation still auto-fills it
//   - OptionIDs unique within the payload
//   - Each option label ≤ 1024 chars
//
// MCQPayload is the AUTHOR-facing view. The LEARNER-facing projection
// (post-A16) strips IsCorrect + Explainer; that helper is `LearnerProjection`.
//
// Hexagonal: NO infrastructure imports.
package question

import (
	"fmt"
)

// MCQPayload is the discriminated payload for QuestionType=mcq. The fields
// match the OpenAPI MCQPayload schema (`chora-contracts/openapi/creation-questions.yaml`)
// minus the wire-only `scoring.mode` (we hardcode single_correct in V1 per
// the design §2.3 and migration 0005 — multi_select is reserved).
type MCQPayload struct {
	Options []MCQOption `json:"options"`
	// XPOnCorrect is the optional Familiar-economy reward per CR §2.2.
	XPOnCorrect int `json:"xp_on_correct,omitempty"`
	// TimerSeconds is an optional UI countdown hint per CR §2.2.
	TimerSeconds int `json:"timer_seconds,omitempty"`
	// ImageURL is the optional W8 AI-assist QUESTION/STEM illustration URL
	// (GCS/CDN). Pointer+omitempty so absent stays byte-stable in the
	// mcq_payload JSONB column. Persisted verbatim so it survives into the
	// published atom, the test-set snapshot, and the learner GraphQL read.
	// Wire name `image_url` — IDENTICAL across FE / BE DTO / OpenAPI.
	ImageURL *string `json:"image_url,omitempty"`
	// AnswerImageURL is the optional W8 AI-assist MODEL-ANSWER illustration
	// URL (2nd slot). Pointer+omitempty for byte-stable absence. Wire name
	// `answer_image_url` — IDENTICAL across all layers.
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
}

// MCQOption is one row in the discriminated payload.
type MCQOption struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	Explainer string `json:"explainer"`
}

// Validate enforces the §2.3 invariants. Called by NewRevision before persistence
// and by the HTTP handler at the boundary.
func (p *MCQPayload) Validate() error {
	if p == nil {
		return fmt.Errorf("mcq payload: nil")
	}
	if len(p.Options) < 2 {
		return fmt.Errorf("mcq payload: at least 2 options required, got %d", len(p.Options))
	}

	correctCount := 0
	seenIDs := make(map[string]struct{}, len(p.Options))
	for i, opt := range p.Options {
		if len(opt.Label) > 1024 {
			return fmt.Errorf("mcq payload: option[%d].label too long: %d > 1024", i, len(opt.Label))
		}
		// ADR-189 (CHO-1826): per-option explainer is OPTIONAL. Manually
		// authored options may omit the rationale on any/all options (AI
		// generation still auto-fills explainers). No empty-explainer check.
		if opt.OptionID == "" {
			return fmt.Errorf("mcq payload: option[%d].option_id is required", i)
		}
		if _, dup := seenIDs[opt.OptionID]; dup {
			return fmt.Errorf("mcq payload: duplicate option_id %q", opt.OptionID)
		}
		seenIDs[opt.OptionID] = struct{}{}
		if opt.IsCorrect {
			correctCount++
		}
	}
	// V1 ships single-correct only (multi_select is reserved per migration 0005).
	if correctCount != 1 {
		return fmt.Errorf("mcq payload: exactly one option must be is_correct=true (V1 single-correct); got %d", correctCount)
	}
	return nil
}

// CorrectOptionID returns the STORED OptionID of the option whose
// IsCorrect==true. Returns "" when no option is marked correct (defensive —
// Validate enforces exactly one correct option before persistence, but this is
// called on the authoring path where the payload has already passed validation).
// When multiple are somehow set (validation bypassed), the FIRST correct option
// wins — deterministic by slice order.
//
// CHO-2272 — this is the STORED (author/internal) id, NOT the grading key. The
// key carried on chora.creation.atom.created.v1 is ServedCorrectOptionID() — the
// DERIVED served id the learner is actually graded on. Use this only to reason
// about the stored payload (e.g. asserting minting preserved the answer key); do
// NOT ship it as the cross-domain grading key.
func (p *MCQPayload) CorrectOptionID() string {
	if p == nil {
		return ""
	}
	for _, opt := range p.Options {
		if opt.IsCorrect {
			return opt.OptionID
		}
	}
	return ""
}

// MintOptionIDs replaces EVERY option_id with a freshly minted opaque id, in
// place. `newID` supplies the ids (uuid); a nil newID or a nil/empty payload is
// a no-op. Call it at PERSIST time, before the payload is stored.
//
// CHO-2255 (SECURITY) — the caller-supplied option_id is answer-bearing data,
// not an identifier. The qgen crew mints ids that NAME the key: `opt_correct` /
// `opt_distractor_1..3` are live in this database (7 of 187 MCQs), and the
// learner-safe projection ships them verbatim — so the allow-list on FIELDS
// (learnerSafeMCQOptions returns only option_id + label) does exactly its job
// and the answer walks out inside the id anyway. Naming only the distractors is
// equally fatal: it identifies the answer by elimination.
//
// Minting is UNCONDITIONAL. Never inspect the incoming id to decide — the naming
// is precisely what cannot be trusted, and "neutral-looking" ids are not neutral
// (CHO-2244 found `opt_1a`/`opt_1` encode POSITION, which correlates with the
// answer). A caller has no legitimate need to choose the id.
//
// CHO-2272 UPDATE — minting is now the AT-REST defense-in-depth layer, no longer
// the only line. The learner-facing id + order are DERIVED from display content
// at serve (ServedLearnerOptions / servedOptionID in served_id.go), so a stored
// id never reaches a learner even for the legacy rows minting cannot reach. This
// SUPERSEDES the earlier reasoning here ("mint at persist, never derive at serve,
// because the key crosses a domain boundary and atom_index holds no labels"):
// creation — which DOES hold the labels — computes ServedCorrectOptionID() and
// ships THAT on chora.creation.atom.created.v1, so served == emitted == graded
// with no change to the label-free consumer (ADR-241). Minting still runs so the
// stored ids and the author view carry no answer-naming signal either.
func (p *MCQPayload) MintOptionIDs(newID func() string) {
	if p == nil || newID == nil {
		return
	}
	for i := range p.Options {
		p.Options[i].OptionID = newID()
	}
}

// ShuffleOptions reorders the options in place. `shuffle` must match the
// semantics of math/rand/v2.Shuffle (or math/rand.Shuffle): it invokes
// swap(i, j) to permute n elements. Each option's fields
// (OptionID / Label / IsCorrect / Explainer) move together, so the answer key
// is preserved — only positions change. No-op for a nil payload, <2 options,
// or a nil shuffle func.
//
// Removes the generation bias where the qgen composer always emits the correct
// option first (and the FE therefore always renders it as "A"). Grading is
// option_id-based, so reordering is safe.
func (p *MCQPayload) ShuffleOptions(shuffle func(n int, swap func(i, j int))) {
	if p == nil || shuffle == nil || len(p.Options) < 2 {
		return
	}
	shuffle(len(p.Options), func(i, j int) {
		p.Options[i], p.Options[j] = p.Options[j], p.Options[i]
	})
}

// AnswerCount returns the number of MCQ options. 0 for a nil payload. Carried
// on chora.creation.atom.created.v1 (CHO-1627) so consumers know the option
// cardinality without loading the question.
func (p *MCQPayload) AnswerCount() int {
	if p == nil {
		return 0
	}
	return len(p.Options)
}

// LearnerProjection returns a learner-safe copy of the payload with IsCorrect
// + Explainer stripped from every option. The original payload is NOT mutated.
//
// TODO(post-grade enrichment): the chora-consumption SubmitAnswer handler will
// surface IsCorrect + Explainer for the option(s) the learner picked, plus the
// authoritative correct option, AFTER grading. See design §5.
func (p MCQPayload) LearnerProjection() MCQPayload {
	out := MCQPayload{
		XPOnCorrect:  p.XPOnCorrect,
		TimerSeconds: p.TimerSeconds,
		Options:      make([]MCQOption, len(p.Options)),
	}
	for i, opt := range p.Options {
		out.Options[i] = MCQOption{
			OptionID: opt.OptionID,
			Label:    opt.Label,
			// IsCorrect intentionally zero (false)
			// Explainer intentionally empty
		}
	}
	return out
}
