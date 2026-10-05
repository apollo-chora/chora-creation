// type_plan_test.go — RED→GREEN coverage for the mixed-type batch TypePlan
// value object (CHO-1819 P2).
//
// Contract: chora-contracts AiAssistStarted.type_plan (repeated
// GenerationTypeQuota{question_type, count, max_images}). EMPTY quotas ⇒ the
// legacy single-type path (nil plan, no error). NON-EMPTY ⇒ a validated mixed
// batch whose per-type counts sum to requested_count and never exceed the
// batch ceiling.
package aiassist

import (
	"errors"
	"testing"
)

func TestNewTypePlan_EmptyQuotasIsLegacyPath(t *testing.T) {
	// An empty quota slice is the legacy single-type path: nil plan, no error.
	for _, in := range [][]TypeQuota{nil, {}} {
		plan, err := NewTypePlan(in, 0)
		if err != nil {
			t.Fatalf("NewTypePlan(empty): unexpected error %v", err)
		}
		if !plan.IsEmpty() {
			t.Fatalf("NewTypePlan(empty): plan should be empty, got %v", plan)
		}
		if plan.TotalCount() != 0 {
			t.Fatalf("NewTypePlan(empty): TotalCount=%d, want 0", plan.TotalCount())
		}
	}
}

func TestNewTypePlan_EmptyIgnoresRequestedCount(t *testing.T) {
	// The sum==requested_count invariant only applies to NON-empty plans —
	// the legacy path carries its own count outside the type_plan.
	plan, err := NewTypePlan(nil, 10)
	if err != nil {
		t.Fatalf("NewTypePlan(nil, 10): unexpected error %v", err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("expected empty plan, got %v", plan)
	}
}

func TestNewTypePlan_HappyMixed8MCQ2OE(t *testing.T) {
	quotas := []TypeQuota{
		{QuestionType: "mcq", Count: 8, MaxImages: 0},
		{QuestionType: "oe", Count: 2, MaxImages: 1},
	}
	plan, err := NewTypePlan(quotas, 10)
	if err != nil {
		t.Fatalf("NewTypePlan(8mcq+2oe): unexpected error %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("plan should be non-empty")
	}
	if len(plan) != 2 {
		t.Fatalf("len(plan)=%d, want 2", len(plan))
	}
	if plan.TotalCount() != 10 {
		t.Fatalf("TotalCount=%d, want 10", plan.TotalCount())
	}
	per := plan.PerTypeCounts()
	if per["mcq"] != 8 || per["oe"] != 2 {
		t.Fatalf("PerTypeCounts=%v, want mcq=8 oe=2", per)
	}
}

func TestNewTypePlan_DefensiveCopy(t *testing.T) {
	// NewTypePlan must not alias the caller's slice — a post-construction
	// mutation of the input must not leak into the returned plan.
	quotas := []TypeQuota{{QuestionType: "mcq", Count: 5, MaxImages: 0}}
	plan, err := NewTypePlan(quotas, 5)
	if err != nil {
		t.Fatalf("NewTypePlan: %v", err)
	}
	quotas[0].Count = 999
	if plan.TotalCount() != 5 {
		t.Fatalf("plan aliased caller slice: TotalCount=%d, want 5", plan.TotalCount())
	}
}

func TestNewTypePlan_RejectsInvalidQuestionType(t *testing.T) {
	quotas := []TypeQuota{{QuestionType: "essay", Count: 5, MaxImages: 0}}
	_, err := NewTypePlan(quotas, 5)
	if !errors.Is(err, ErrTypePlanInvalidType) {
		t.Fatalf("err=%v, want ErrTypePlanInvalidType", err)
	}
}

func TestNewTypePlan_RejectsEmptyQuestionType(t *testing.T) {
	quotas := []TypeQuota{{QuestionType: "", Count: 5, MaxImages: 0}}
	_, err := NewTypePlan(quotas, 5)
	if !errors.Is(err, ErrTypePlanInvalidType) {
		t.Fatalf("err=%v, want ErrTypePlanInvalidType", err)
	}
}

func TestNewTypePlan_RejectsCountBelowOne(t *testing.T) {
	for _, c := range []int{0, -3} {
		quotas := []TypeQuota{{QuestionType: "mcq", Count: c, MaxImages: 0}}
		_, err := NewTypePlan(quotas, c)
		if !errors.Is(err, ErrTypePlanCountTooLow) {
			t.Fatalf("count=%d: err=%v, want ErrTypePlanCountTooLow", c, err)
		}
	}
}

func TestNewTypePlan_RejectsNegativeMaxImages(t *testing.T) {
	quotas := []TypeQuota{{QuestionType: "mcq", Count: 5, MaxImages: -1}}
	_, err := NewTypePlan(quotas, 5)
	if !errors.Is(err, ErrTypePlanMaxImagesRange) {
		t.Fatalf("err=%v, want ErrTypePlanMaxImagesRange", err)
	}
}

func TestNewTypePlan_RejectsMaxImagesExceedingCount(t *testing.T) {
	quotas := []TypeQuota{{QuestionType: "mcq", Count: 3, MaxImages: 4}}
	_, err := NewTypePlan(quotas, 3)
	if !errors.Is(err, ErrTypePlanMaxImagesRange) {
		t.Fatalf("err=%v, want ErrTypePlanMaxImagesRange", err)
	}
}

func TestNewTypePlan_MaxImagesEqualToCountIsValid(t *testing.T) {
	quotas := []TypeQuota{{QuestionType: "mcq", Count: 3, MaxImages: 3}}
	plan, err := NewTypePlan(quotas, 3)
	if err != nil {
		t.Fatalf("max_images==count should be valid, got %v", err)
	}
	if plan.TotalCount() != 3 {
		t.Fatalf("TotalCount=%d, want 3", plan.TotalCount())
	}
}

func TestNewTypePlan_RejectsBatchCeilingExceeded(t *testing.T) {
	// sum = MaxBatchCount + 1 — trips the ceiling guard before the mismatch
	// guard (requested_count matches the sum here so only the ceiling fires).
	quotas := []TypeQuota{
		{QuestionType: "mcq", Count: MaxBatchCount, MaxImages: 0},
		{QuestionType: "oe", Count: 1, MaxImages: 0},
	}
	_, err := NewTypePlan(quotas, MaxBatchCount+1)
	if !errors.Is(err, ErrTypePlanCeilingExceeded) {
		t.Fatalf("err=%v, want ErrTypePlanCeilingExceeded", err)
	}
}

func TestNewTypePlan_AtCeilingIsValid(t *testing.T) {
	quotas := []TypeQuota{
		{QuestionType: "mcq", Count: MaxBatchCount - 5, MaxImages: 0},
		{QuestionType: "oe", Count: 5, MaxImages: 0},
	}
	plan, err := NewTypePlan(quotas, MaxBatchCount)
	if err != nil {
		t.Fatalf("sum==ceiling should be valid, got %v", err)
	}
	if plan.TotalCount() != MaxBatchCount {
		t.Fatalf("TotalCount=%d, want %d", plan.TotalCount(), MaxBatchCount)
	}
}

func TestNewTypePlan_CeilingIsTwoHundred_ADR251(t *testing.T) {
	// ADR-251 D1 (owner-ruled 2026-08-16): the batch ceiling is 200, kept in
	// lockstep with the orchestrator's validate_type_plan default and the
	// QGenBatchRunner clamp. The chunk loop bounds every LLM call regardless,
	// so this ceiling is a product/cost brake, not a token-limit guard. The
	// pin is ABSOLUTE on purpose: the relative tests above stay green through
	// any drift, and drift here is a cross-service contract break.
	quotas := []TypeQuota{{QuestionType: "mcq", Count: 200, MaxImages: 0}}
	plan, err := NewTypePlan(quotas, 200)
	if err != nil {
		t.Fatalf("a 200-question plan must be within the ceiling, got %v", err)
	}
	if plan.TotalCount() != 200 {
		t.Fatalf("TotalCount=%d, want 200", plan.TotalCount())
	}

	over := []TypeQuota{{QuestionType: "mcq", Count: 201, MaxImages: 0}}
	if _, err := NewTypePlan(over, 201); !errors.Is(err, ErrTypePlanCeilingExceeded) {
		t.Fatalf("201 must exceed the ceiling, got %v", err)
	}
}

func TestNewTypePlan_RejectsCountMismatch(t *testing.T) {
	quotas := []TypeQuota{
		{QuestionType: "mcq", Count: 8, MaxImages: 0},
		{QuestionType: "oe", Count: 2, MaxImages: 0},
	}
	_, err := NewTypePlan(quotas, 9) // sum is 10, declared 9
	if !errors.Is(err, ErrTypePlanCountMismatch) {
		t.Fatalf("err=%v, want ErrTypePlanCountMismatch", err)
	}
}

func TestTypePlan_PerTypeCountsSumsDuplicateTypes(t *testing.T) {
	// Duplicate question_types are summed (not rejected) — the per-type map
	// folds them so a downstream consumer reads one count per type.
	quotas := []TypeQuota{
		{QuestionType: "mcq", Count: 4, MaxImages: 0},
		{QuestionType: "mcq", Count: 3, MaxImages: 0},
		{QuestionType: "oe", Count: 3, MaxImages: 0},
	}
	plan, err := NewTypePlan(quotas, 10)
	if err != nil {
		t.Fatalf("NewTypePlan(dup types): %v", err)
	}
	per := plan.PerTypeCounts()
	if per["mcq"] != 7 || per["oe"] != 3 {
		t.Fatalf("PerTypeCounts=%v, want mcq=7 oe=3", per)
	}
}

func TestTypePlan_PerTypeCountsEmptyIsNil(t *testing.T) {
	var plan TypePlan
	if plan.PerTypeCounts() != nil {
		t.Fatal("PerTypeCounts on empty plan should be nil")
	}
}
