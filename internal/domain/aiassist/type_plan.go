// type_plan.go — mixed-type batch generation value object (CHO-1819 P2).
//
// A TypePlan is the per-question-type quota breakdown of a mixed AI batch:
// "8 MCQ + 2 OE" rather than a single content_type + count. It mirrors the
// chora-contracts AiAssistStarted.type_plan field (repeated
// GenerationTypeQuota{question_type, count, max_images}).
//
// Semantics (locked with the contract):
//   - An EMPTY quota slice is the legacy single-type path — NewTypePlan returns
//     a nil plan + nil error and the caller keeps its existing content_type +
//     requested_count plumbing byte-for-byte unchanged.
//   - A NON-EMPTY plan is validated FAIL-LOUD (no coercion): every quota names a
//     valid question_type (mcq | oe), counts are >= 1, max_images is in
//     [0, count], the total never exceeds the batch ceiling, and the total
//     equals the request's declared requested_count.
//
// Invariants live HERE (the domain), not in the HTTP/Pub-Sub adapters — those
// map the wire shape onto TypeQuota and re-validate through NewTypePlan.
package aiassist

import (
	"errors"
	"fmt"
)

// MaxBatchCount is the canonical ceiling on questions generated per batch job
// (single-type or mixed). The pubsub batch dispatcher's clampBatchCount + the
// HTTP batch handler reference this constant so the ceiling has ONE home.
// ADR-251 D1 (owner-ruled 2026-08-16): 50 → 200. The orchestrator's chunk
// loop bounds every LLM call regardless of set size, so this ceiling is a
// product/cost brake (the mana gate is the spend brake), not a token-limit
// guard. Kept in lockstep with the orchestrator's validate_type_plan default
// and QGenBatchRunner._MAX_BATCH, and with creation-questions.yaml's
// count/requested_count maxima.
const MaxBatchCount = 200

// Typed sentinel errors — adapters map these to HTTP 400 with a specific code.
// All are fail-loud: an invalid plan is rejected, never silently coerced.
var (
	// ErrTypePlanInvalidType — a quota names a question_type outside the
	// Phyllis enum (mcq | oe), or an empty type.
	ErrTypePlanInvalidType = errors.New("aiassist: type_plan quota has an invalid question_type")
	// ErrTypePlanCountTooLow — a quota count is < 1.
	ErrTypePlanCountTooLow = errors.New("aiassist: type_plan quota count must be >= 1")
	// ErrTypePlanMaxImagesRange — a quota max_images is outside [0, count].
	ErrTypePlanMaxImagesRange = errors.New("aiassist: type_plan quota max_images must be in [0, count]")
	// ErrTypePlanCeilingExceeded — the summed count exceeds MaxBatchCount.
	ErrTypePlanCeilingExceeded = errors.New("aiassist: type_plan total exceeds the batch ceiling")
	// ErrTypePlanCountMismatch — the summed count does not equal the request's
	// declared requested_count (the back-compat/mana total).
	ErrTypePlanCountMismatch = errors.New("aiassist: type_plan total does not equal requested_count")
)

// TypeQuota is one per-question-type quota line of a mixed batch. Mirrors the
// proto GenerationTypeQuota{question_type=1, count=2, max_images=3,
// image_for_stem=4, image_for_answer=5}.
type TypeQuota struct {
	QuestionType string
	Count        int
	MaxImages    int

	// CHO-1825 2b: deterministic per-type AUTHOR image opt-in. When true, EVERY
	// question of this type MUST carry an image on that placement (mirrors the
	// single-candidate AiAssistStarted.image_for_stem/_answer). Independent of
	// MaxImages (the model-decided "AI decides" budget) — no range validation
	// applies to a bool. Both default false ⇒ legacy behaviour unchanged.
	ImageForStem   bool
	ImageForAnswer bool
}

// TypePlan is a validated, non-empty mixed-batch breakdown (or nil for the
// legacy single-type path). Construct ONLY via NewTypePlan.
type TypePlan []TypeQuota

// NewTypePlan validates the quotas against the request's declared
// requested_count and returns the constructed plan.
//
//   - An EMPTY quotas slice yields (nil, nil) — the legacy single-type path;
//     requestedCount is intentionally NOT checked (the legacy count lives
//     outside the type_plan).
//   - A NON-EMPTY slice is validated quota-by-quota then in aggregate; the
//     FIRST violated invariant returns its typed sentinel error wrapped with
//     positional context. The returned plan is a defensive copy (the caller
//     cannot mutate internal state post-construction).
func NewTypePlan(quotas []TypeQuota, requestedCount int) (TypePlan, error) {
	if len(quotas) == 0 {
		return nil, nil // legacy single-type path
	}

	total := 0
	for i, q := range quotas {
		if !QuestionType(q.QuestionType).Valid() {
			return nil, fmt.Errorf("%w: quota[%d] question_type %q (mcq | oe only)",
				ErrTypePlanInvalidType, i, q.QuestionType)
		}
		if q.Count < 1 {
			return nil, fmt.Errorf("%w: quota[%d] (%s) count=%d",
				ErrTypePlanCountTooLow, i, q.QuestionType, q.Count)
		}
		if q.MaxImages < 0 || q.MaxImages > q.Count {
			return nil, fmt.Errorf("%w: quota[%d] (%s) max_images=%d count=%d",
				ErrTypePlanMaxImagesRange, i, q.QuestionType, q.MaxImages, q.Count)
		}
		total += q.Count
	}

	if total > MaxBatchCount {
		return nil, fmt.Errorf("%w: total=%d ceiling=%d",
			ErrTypePlanCeilingExceeded, total, MaxBatchCount)
	}
	if total != requestedCount {
		return nil, fmt.Errorf("%w: total=%d requested_count=%d",
			ErrTypePlanCountMismatch, total, requestedCount)
	}

	out := make(TypePlan, len(quotas))
	copy(out, quotas)
	return out, nil
}

// IsEmpty reports the legacy single-type path (no per-type breakdown).
func (p TypePlan) IsEmpty() bool { return len(p) == 0 }

// TotalCount is the sum of every quota's Count (0 for an empty plan).
func (p TypePlan) TotalCount() int {
	total := 0
	for _, q := range p {
		total += q.Count
	}
	return total
}

// PerTypeCounts folds the quotas into a count keyed by question_type, summing
// any duplicate-type quotas. Returns nil for an empty plan.
func (p TypePlan) PerTypeCounts() map[string]int {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]int, len(p))
	for _, q := range p {
		out[q.QuestionType] += q.Count
	}
	return out
}
