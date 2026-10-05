// MCQPayload tests — RED first. Asserts §2.3 invariants:
//   - at least 2 options
//   - exactly 1 IsCorrect (V1 single-correct only)
//   - per-option explainer required for ALL options (correct + wrong)
//   - OptionIDs unique within the payload
//   - LearnerProjection strips IsCorrect + Explainer
package question_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestMCQPayload_ShuffleOptions_ReordersButPreservesAnswerKey(t *testing.T) {
	t.Parallel()
	p := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt_1", Label: "Correct", IsCorrect: true, Explainer: "right"},
			{OptionID: "opt_2", Label: "B", IsCorrect: false, Explainer: "x"},
			{OptionID: "opt_3", Label: "C", IsCorrect: false, Explainer: "x"},
			{OptionID: "opt_4", Label: "D", IsCorrect: false, Explainer: "x"},
		},
	}
	// Deterministic reverse "shuffle" so the assertion is stable.
	reverse := func(n int, swap func(i, j int)) {
		for i := 0; i < n/2; i++ {
			swap(i, n-1-i)
		}
	}
	p.ShuffleOptions(reverse)

	if p.Options[0].OptionID != "opt_4" || p.Options[3].OptionID != "opt_1" {
		t.Fatalf("expected reversed order, got %+v", p.Options)
	}
	// The correct flag + explainer travel with their option_id.
	if !p.Options[3].IsCorrect || p.Options[3].Label != "Correct" || p.Options[3].Explainer != "right" {
		t.Fatalf("answer key not preserved on moved option: %+v", p.Options[3])
	}
	if got := p.CorrectOptionID(); got != "opt_1" {
		t.Fatalf("CorrectOptionID() = %q, want opt_1", got)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("payload invalid after shuffle: %v", err)
	}
}

func TestMCQPayload_ShuffleOptions_NoopGuards(t *testing.T) {
	t.Parallel()
	var nilP *question.MCQPayload
	nilP.ShuffleOptions(func(int, func(i, j int)) {}) // must not panic

	single := &question.MCQPayload{Options: []question.MCQOption{{OptionID: "o1"}}}
	called := false
	single.ShuffleOptions(func(int, func(i, j int)) { called = true })
	if called {
		t.Fatal("ShuffleOptions should be a no-op for <2 options")
	}

	twoNilFn := &question.MCQPayload{Options: []question.MCQOption{{OptionID: "o1"}, {OptionID: "o2"}}}
	twoNilFn.ShuffleOptions(nil) // nil shuffle → no-op, no panic
}

func TestMCQPayload_Validate_OK(t *testing.T) {
	t.Parallel()
	p := question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "why wrong"},
			{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "why right"},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestMCQPayload_Validate_RequiresMinTwoOptions(t *testing.T) {
	t.Parallel()
	p := question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: true, Explainer: "why right"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for <2 options; got nil")
	}
}

func TestMCQPayload_Validate_RequiresExactlyOneCorrect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts []question.MCQOption
	}{
		{
			name: "zero correct",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "w"},
				{OptionID: "o2", Label: "B", IsCorrect: false, Explainer: "w"},
			},
		},
		{
			name: "two correct (V1 single-correct only)",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: true, Explainer: "w"},
				{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "w"},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := question.MCQPayload{Options: tt.opts}
			if err := p.Validate(); err == nil {
				t.Errorf("expected error for %s; got nil", tt.name)
			}
		})
	}
}

func TestMCQPayload_Validate_AllowsOptionalExplainer(t *testing.T) {
	t.Parallel()
	// ADR-189 (CHO-1826): per-option explainer is OPTIONAL. A manually authored
	// question may omit explainers on any/all options (AI generation still
	// auto-fills them). Empty / blank / partial explainers must all VALIDATE so
	// long as the other invariants (≥2 options, unique ids, exactly-one-correct)
	// hold.
	tests := []struct {
		name string
		opts []question.MCQOption
	}{
		{
			name: "correct option missing explainer",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "w"},
				{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: ""},
			},
		},
		{
			name: "wrong option missing explainer",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: ""},
				{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "right"},
			},
		},
		{
			name: "all options blank explainer",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "  "},
				{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: ""},
			},
		},
		{
			name: "partial: only the correct option carries a rationale",
			opts: []question.MCQOption{
				{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: ""},
				{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "this is right"},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := question.MCQPayload{Options: tt.opts}
			if err := p.Validate(); err != nil {
				t.Errorf("explainer is optional; expected nil error for %s, got %v", tt.name, err)
			}
		})
	}
}

func TestMCQPayload_Validate_RejectsOversizedLabel(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("x", 1025)
	p := question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: tooLong, IsCorrect: false, Explainer: "w"},
			{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "w"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for label > 1024 chars; got nil")
	}
}

func TestMCQPayload_Validate_RejectsDuplicateOptionID(t *testing.T) {
	t.Parallel()
	p := question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "same", Label: "A", IsCorrect: false, Explainer: "w"},
			{OptionID: "same", Label: "B", IsCorrect: true, Explainer: "r"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Errorf("expected error for duplicate option_id; got nil")
	}
}

func TestMCQPayload_LearnerProjection_StripsExplainerAndIsCorrect(t *testing.T) {
	t.Parallel()
	p := question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "why wrong"},
			{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "why right"},
		},
		XPOnCorrect:  20,
		TimerSeconds: 90,
	}
	lp := p.LearnerProjection()

	if len(lp.Options) != len(p.Options) {
		t.Fatalf("LearnerProjection.Options count = %d; want %d", len(lp.Options), len(p.Options))
	}
	for i, opt := range lp.Options {
		if opt.IsCorrect {
			t.Errorf("option[%d].IsCorrect leaked in learner projection", i)
		}
		if opt.Explainer != "" {
			t.Errorf("option[%d].Explainer leaked in learner projection: %q", i, opt.Explainer)
		}
		// Label + OptionID + (text not in this skill's model — keep Label) must persist.
		if opt.Label == "" {
			t.Errorf("option[%d].Label stripped (should remain)", i)
		}
		if opt.OptionID == "" {
			t.Errorf("option[%d].OptionID stripped (should remain)", i)
		}
	}
	// Original payload untouched.
	if !p.Options[1].IsCorrect {
		t.Errorf("original payload mutated: Options[1].IsCorrect = %v; want true", p.Options[1].IsCorrect)
	}
	if p.Options[1].Explainer != "why right" {
		t.Errorf("original payload mutated: Options[1].Explainer = %q", p.Options[1].Explainer)
	}
}

// TestMCQPayload_CorrectOptionID_ReturnsIsCorrectOption asserts the CHO-1627
// grading helper returns the OptionID of the IsCorrect option (by per-option
// boolean, NOT a positional index).
func TestMCQPayload_CorrectOptionID_ReturnsIsCorrectOption(t *testing.T) {
	t.Parallel()
	p := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "why wrong"},
			{OptionID: "o2", Label: "B", IsCorrect: true, Explainer: "why right"},
			{OptionID: "o3", Label: "C", IsCorrect: false, Explainer: "why wrong"},
		},
	}
	if got := p.CorrectOptionID(); got != "o2" {
		t.Errorf("CorrectOptionID() = %q; want o2", got)
	}
	if got := p.AnswerCount(); got != 3 {
		t.Errorf("AnswerCount() = %d; want 3", got)
	}
}

// TestMCQPayload_CorrectOptionID_NilSafe asserts the helpers are nil-safe
// (returns "" / 0) — the non-MCQ / empty-payload case.
func TestMCQPayload_CorrectOptionID_NilSafe(t *testing.T) {
	t.Parallel()
	var p *question.MCQPayload
	if got := p.CorrectOptionID(); got != "" {
		t.Errorf("nil CorrectOptionID() = %q; want empty", got)
	}
	if got := p.AnswerCount(); got != 0 {
		t.Errorf("nil AnswerCount() = %d; want 0", got)
	}
}

// TestMCQPayload_CorrectOptionID_NoCorrectReturnsEmpty asserts that a payload
// with no IsCorrect option yields "" (defensive — Validate enforces exactly
// one before persistence; this guards the authoring path).
func TestMCQPayload_CorrectOptionID_NoCorrectReturnsEmpty(t *testing.T) {
	t.Parallel()
	p := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "x"},
			{OptionID: "o2", Label: "B", IsCorrect: false, Explainer: "y"},
		},
	}
	if got := p.CorrectOptionID(); got != "" {
		t.Errorf("CorrectOptionID() with no correct option = %q; want empty", got)
	}
}
