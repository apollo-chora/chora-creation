package atomic

import (
	"testing"
)

func TestValidateAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rule   AnswerValidationRule
		answer map[string]any
		want   bool
	}{
		// Exact match tests.
		{
			name: "exact_match_correct_case_sensitive",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleExactMatch,
				Expected:      map[string]any{"value": "Paris"},
				CaseSensitive: true,
			},
			answer: map[string]any{"value": "Paris"},
			want:   true,
		},
		{
			name: "exact_match_wrong_case_sensitive",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleExactMatch,
				Expected:      map[string]any{"value": "Paris"},
				CaseSensitive: true,
			},
			answer: map[string]any{"value": "paris"},
			want:   false,
		},
		{
			name: "exact_match_case_insensitive",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleExactMatch,
				Expected:      map[string]any{"value": "Paris"},
				CaseSensitive: false,
			},
			answer: map[string]any{"value": "paris"},
			want:   true,
		},
		{
			name: "exact_match_wrong_answer",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleExactMatch,
				Expected:      map[string]any{"value": "Paris"},
				CaseSensitive: false,
			},
			answer: map[string]any{"value": "London"},
			want:   false,
		},
		{
			name: "exact_match_missing_answer_value",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleExactMatch,
				Expected: map[string]any{"value": "Paris"},
			},
			answer: map[string]any{},
			want:   false,
		},
		{
			name: "exact_match_missing_expected_value",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleExactMatch,
				Expected: map[string]any{},
			},
			answer: map[string]any{"value": "Paris"},
			want:   false,
		},
		{
			name: "exact_match_nil_expected",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleExactMatch,
				Expected: nil,
			},
			answer: map[string]any{"value": "Paris"},
			want:   false,
		},

		// Regex tests.
		{
			name: "regex_match",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRegex,
				Expected: map[string]any{"pattern": `^\d{3}-\d{4}$`},
			},
			answer: map[string]any{"value": "123-4567"},
			want:   true,
		},
		{
			name: "regex_no_match",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRegex,
				Expected: map[string]any{"pattern": `^\d{3}-\d{4}$`},
			},
			answer: map[string]any{"value": "abc-defg"},
			want:   false,
		},
		{
			name: "regex_invalid_pattern",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRegex,
				Expected: map[string]any{"pattern": `[invalid`},
			},
			answer: map[string]any{"value": "anything"},
			want:   false,
		},
		{
			name: "regex_missing_pattern",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRegex,
				Expected: map[string]any{},
			},
			answer: map[string]any{"value": "anything"},
			want:   false,
		},

		// Range tests.
		{
			name: "range_within_bounds",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 5.0},
			want:   true,
		},
		{
			name: "range_at_min",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 1.0},
			want:   true,
		},
		{
			name: "range_at_max",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 10.0},
			want:   true,
		},
		{
			name: "range_below_min",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 0.5},
			want:   false,
		},
		{
			name: "range_above_max",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 11.0},
			want:   false,
		},
		{
			name: "range_with_tolerance",
			rule: AnswerValidationRule{
				RuleType:  ValidationRuleRange,
				Expected:  map[string]any{"min": 1.0, "max": 10.0},
				Tolerance: floatPtr(0.5),
			},
			answer: map[string]any{"value": 0.6},
			want:   true,
		},
		{
			name: "range_outside_tolerance",
			rule: AnswerValidationRule{
				RuleType:  ValidationRuleRange,
				Expected:  map[string]any{"min": 1.0, "max": 10.0},
				Tolerance: floatPtr(0.5),
			},
			answer: map[string]any{"value": 0.4},
			want:   false,
		},
		{
			name: "range_non_numeric_answer",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": "not a number"},
			want:   false,
		},
		{
			name: "range_integer_answer",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleRange,
				Expected: map[string]any{"min": 1.0, "max": 10.0},
			},
			answer: map[string]any{"value": 5},
			want:   true,
		},

		// Keyword tests.
		{
			name: "keyword_all_present",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleKeyword,
				Expected:      map[string]any{"keywords": []any{"photosynthesis", "chloroplast"}},
				CaseSensitive: false,
			},
			answer: map[string]any{"value": "Photosynthesis occurs in the chloroplast of plant cells."},
			want:   true,
		},
		{
			name: "keyword_missing_one",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleKeyword,
				Expected:      map[string]any{"keywords": []any{"photosynthesis", "mitochondria"}},
				CaseSensitive: false,
			},
			answer: map[string]any{"value": "Photosynthesis occurs in plant cells."},
			want:   false,
		},
		{
			name: "keyword_case_sensitive",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleKeyword,
				Expected:      map[string]any{"keywords": []any{"DNA"}},
				CaseSensitive: true,
			},
			answer: map[string]any{"value": "dna is the blueprint of life"},
			want:   false,
		},
		{
			name: "keyword_case_sensitive_match",
			rule: AnswerValidationRule{
				RuleType:      ValidationRuleKeyword,
				Expected:      map[string]any{"keywords": []any{"DNA"}},
				CaseSensitive: true,
			},
			answer: map[string]any{"value": "DNA is the blueprint of life"},
			want:   true,
		},
		{
			name: "keyword_empty_keywords",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleKeyword,
				Expected: map[string]any{"keywords": []any{}},
			},
			answer: map[string]any{"value": "anything"},
			want:   false,
		},

		// Manual and LLM graded.
		{
			name: "manual_always_false",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleManual,
			},
			answer: map[string]any{"value": "any answer"},
			want:   false,
		},
		{
			name: "llm_graded_always_false",
			rule: AnswerValidationRule{
				RuleType: ValidationRuleLLMGraded,
			},
			answer: map[string]any{"value": "any answer"},
			want:   false,
		},

		// Unknown rule type.
		{
			name: "unknown_rule_type_returns_false",
			rule: AnswerValidationRule{
				RuleType: "unknown",
			},
			answer: map[string]any{"value": "any answer"},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ValidateAnswer(tt.rule, tt.answer)
			if got != tt.want {
				t.Errorf("ValidateAnswer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func floatPtr(f float64) *float64 {
	return &f
}
