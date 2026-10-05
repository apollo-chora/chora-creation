// OEPayload tests — RED first. Asserts §2.4 invariants:
//   - ModelAnswer non-empty + length ≤ 16384
//   - If Rubric set: Criteria non-empty; sum WeightPercent == 100; Descriptions non-empty
//   - LearnerProjection strips ModelAnswer
package question_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestOEPayload_Validate_OK(t *testing.T) {
	t.Parallel()
	p := question.OEPayload{
		ModelAnswer: "A model answer.",
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestOEPayload_Validate_RequiresModelAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ma   string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := question.OEPayload{ModelAnswer: tt.ma}
			if err := p.Validate(); err == nil {
				t.Errorf("expected error for %s; got nil", tt.name)
			}
		})
	}
}

func TestOEPayload_Validate_RejectsOversizedModelAnswer(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("a", 16385)
	p := question.OEPayload{ModelAnswer: tooLong}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for model_answer > 16384 chars; got nil")
	}
}

func TestOEPayload_Validate_RubricWeightsSumTo100(t *testing.T) {
	t.Parallel()
	good := question.OEPayload{
		ModelAnswer: "x",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "A", WeightPercent: 60},
				{CriterionID: "c2", Description: "B", WeightPercent: 40},
			},
		},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate(60+40=100) unexpected error: %v", err)
	}

	bad := question.OEPayload{
		ModelAnswer: "x",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "A", WeightPercent: 60},
				{CriterionID: "c2", Description: "B", WeightPercent: 30}, // sum = 90
			},
		},
	}
	if err := bad.Validate(); err == nil {
		t.Errorf("expected error for rubric sum=90 != 100; got nil")
	}

	over := question.OEPayload{
		ModelAnswer: "x",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "A", WeightPercent: 60},
				{CriterionID: "c2", Description: "B", WeightPercent: 50}, // sum = 110
			},
		},
	}
	if err := over.Validate(); err == nil {
		t.Errorf("expected error for rubric sum=110 != 100; got nil")
	}
}

func TestOEPayload_Validate_RubricRequiresCriteria(t *testing.T) {
	t.Parallel()
	p := question.OEPayload{
		ModelAnswer:    "x",
		WeightedRubric: &question.Rubric{Criteria: []question.RubricCriterion{}},
	}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for empty Rubric.Criteria; got nil")
	}
}

func TestOEPayload_Validate_RubricCriterionDescriptionRequired(t *testing.T) {
	t.Parallel()
	p := question.OEPayload{
		ModelAnswer: "x",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "", WeightPercent: 100},
			},
		},
	}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for criterion missing description; got nil")
	}
}

func TestOEPayload_LearnerProjection_StripsModelAnswer(t *testing.T) {
	t.Parallel()
	p := question.OEPayload{
		ModelAnswer: "A learner must NEVER see this.",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "A", WeightPercent: 100},
			},
		},
	}
	lp := p.LearnerProjection()
	if lp.ModelAnswer != "" {
		t.Errorf("ModelAnswer leaked in learner projection: %q", lp.ModelAnswer)
	}
	// Rubric also must NOT leak (per A16 §5 reconciliation).
	if lp.WeightedRubric != nil {
		t.Errorf("WeightedRubric leaked in learner projection (must be nil for learner)")
	}
	// Original payload untouched.
	if p.ModelAnswer == "" {
		t.Errorf("original payload mutated: ModelAnswer cleared")
	}
}

// bug #2-B (2026-06-03): min/max_response_chars + grader_tier are accepted +
// validated. Absent (nil) is always valid; bad values fail loud.
func TestOEPayload_Validate_ResponseCharsAndGraderTier(t *testing.T) {
	t.Parallel()
	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	tests := []struct {
		name    string
		p       question.OEPayload
		wantErr bool
	}{
		{"all absent", question.OEPayload{ModelAnswer: "ma"}, false},
		{"min only", question.OEPayload{ModelAnswer: "ma", MinResponseChars: intp(50)}, false},
		{"max only", question.OEPayload{ModelAnswer: "ma", MaxResponseChars: intp(600)}, false},
		{"min<=max", question.OEPayload{ModelAnswer: "ma", MinResponseChars: intp(50), MaxResponseChars: intp(600)}, false},
		{"min>max", question.OEPayload{ModelAnswer: "ma", MinResponseChars: intp(700), MaxResponseChars: intp(600)}, true},
		{"min<0", question.OEPayload{ModelAnswer: "ma", MinResponseChars: intp(-1)}, true},
		{"max<0", question.OEPayload{ModelAnswer: "ma", MaxResponseChars: intp(-5)}, true},
		{"grader T1", question.OEPayload{ModelAnswer: "ma", GraderTier: strp("T1")}, false},
		{"grader T2", question.OEPayload{ModelAnswer: "ma", GraderTier: strp("T2")}, false},
		{"grader bad", question.OEPayload{ModelAnswer: "ma", GraderTier: strp("T3")}, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.p.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("%s: expected error; got nil", tt.name)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("%s: unexpected error: %v", tt.name, err)
			}
		})
	}
}
