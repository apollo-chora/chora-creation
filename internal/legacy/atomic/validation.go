package atomic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ValidateAnswer evaluates a learner's answer against a validation rule.
// Returns true if the answer is correct per the rule's criteria.
// Manual and LLM-graded rules always return false (require external grading).
func ValidateAnswer(rule AnswerValidationRule, answer map[string]any) bool {
	switch rule.RuleType {
	case ValidationRuleExactMatch:
		return validateExactMatch(rule, answer)
	case ValidationRuleRegex:
		return validateRegex(rule, answer)
	case ValidationRuleRange:
		return validateRange(rule, answer)
	case ValidationRuleKeyword:
		return validateKeyword(rule, answer)
	case ValidationRuleManual, ValidationRuleLLMGraded:
		return false
	default:
		return false
	}
}

// validateExactMatch checks if the answer value matches the expected value exactly.
// Supports expected as either {"value": X} (structured) or X (raw value from seed data).
// Extracts answer from answer["value"], answer["selected_option"], answer["text"],
// or answer["answers"][0] for cross-renderer compatibility.
func validateExactMatch(rule AnswerValidationRule, answer map[string]any) bool {
	expectedVal := extractExpected(rule.Expected)
	answerVal := extractAnswer(answer)
	if expectedVal == nil || answerVal == nil {
		return false
	}

	expectedStr := fmt.Sprintf("%v", expectedVal)
	answerStr := fmt.Sprintf("%v", answerVal)

	if !rule.CaseSensitive {
		return strings.EqualFold(expectedStr, answerStr)
	}
	return expectedStr == answerStr
}

// extractExpected gets the expected value from either {"value": X} or raw X.
func extractExpected(expected any) any {
	if m, ok := expected.(map[string]any); ok {
		if v, has := m["value"]; has {
			return v
		}
		return expected
	}
	// Raw value (number, string, bool)
	return expected
}

// extractAnswer gets the answer value from the learner's answer map,
// checking common keys used by different renderer types.
func extractAnswer(answer map[string]any) any {
	// Direct value key (true/false, standardized format)
	if v, ok := answer["value"]; ok {
		return v
	}
	// MCQ renderer: selected_option index
	if v, ok := answer["selected_option"]; ok {
		return v
	}
	// Short answer / essay renderer: text
	if v, ok := answer["text"]; ok {
		return v
	}
	// Code renderer
	if v, ok := answer["code"]; ok {
		return v
	}
	// Fill-blank renderer: answers array — use first element
	if arr, ok := answer["answers"]; ok {
		if slice, ok2 := arr.([]any); ok2 && len(slice) > 0 {
			return slice[0]
		}
	}
	return nil
}

// validateRegex checks if the answer value matches a regular expression pattern.
func validateRegex(rule AnswerValidationRule, answer map[string]any) bool {
	expected, ok := rule.Expected.(map[string]any)
	if !ok {
		return false
	}
	pattern, ok := expected["pattern"].(string)
	if !ok {
		return false
	}
	answerVal := extractAnswer(answer)
	if answerVal == nil {
		return false
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(fmt.Sprintf("%v", answerVal))
}

// validateRange checks if a numeric answer falls within [min, max], with optional tolerance.
func validateRange(rule AnswerValidationRule, answer map[string]any) bool {
	expected, ok := rule.Expected.(map[string]any)
	if !ok {
		return false
	}

	raw := extractAnswer(answer)
	if raw == nil {
		return false
	}
	answerNum, ok := toFloat64(raw)
	if !ok {
		// Try parsing string as number (e.g., "78.54" from text input)
		if s, ok2 := raw.(string); ok2 {
			var f float64
			if _, err := fmt.Sscanf(s, "%f", &f); err == nil {
				answerNum = f
			} else {
				return false
			}
		} else {
			return false
		}
	}

	minVal, hasMin := toFloat64(expected["min"])
	maxVal, hasMax := toFloat64(expected["max"])

	tolerance := 0.0
	if rule.Tolerance != nil {
		tolerance = *rule.Tolerance
	}

	if hasMin && answerNum < minVal-tolerance {
		return false
	}
	if hasMax && answerNum > maxVal+tolerance {
		return false
	}
	return hasMin || hasMax
}

// validateKeyword checks if the answer contains all required keywords.
func validateKeyword(rule AnswerValidationRule, answer map[string]any) bool {
	expected, ok := rule.Expected.(map[string]any)
	if !ok {
		return false
	}

	keywordsRaw, ok := expected["keywords"]
	if !ok {
		return false
	}

	keywords, ok := toStringSlice(keywordsRaw)
	if !ok || len(keywords) == 0 {
		return false
	}

	answerVal := extractAnswer(answer)
	if answerVal == nil {
		return false
	}
	answerStr := fmt.Sprintf("%v", answerVal)
	if !rule.CaseSensitive {
		answerStr = strings.ToLower(answerStr)
	}

	for _, kw := range keywords {
		target := kw
		if !rule.CaseSensitive {
			target = strings.ToLower(kw)
		}
		if !strings.Contains(answerStr, target) {
			return false
		}
	}
	return true
}

// toFloat64 converts a numeric interface value to float64.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// toStringSlice converts an interface slice to []string.
func toStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		result := make([]string, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[i] = str
		}
		return result, true
	default:
		return nil, false
	}
}
