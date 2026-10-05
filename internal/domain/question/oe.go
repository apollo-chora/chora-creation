// OE-specific (Open-Ended) payload + validation. Per CR design §2.4:
//   - ModelAnswer non-empty after trim; length ≤ 16384
//   - If WeightedRubric set: Criteria non-empty;
//     sum of WeightPercent == 100; each Description non-empty
//
// OEPayload is the AUTHOR-facing view. The LEARNER-facing projection
// (post-A16) strips ModelAnswer + WeightedRubric; both are surfaced only at
// post-grade time via the chora-consumption SubmitAnswer response.
//
// Hexagonal: NO infrastructure imports.
package question

import (
	"fmt"
	"strings"
)

// OEPayload is the discriminated payload for QuestionType=oe.
type OEPayload struct {
	ModelAnswer    string  `json:"model_answer"`
	WeightedRubric *Rubric `json:"rubric,omitempty"`
	// ImageURL is the optional W8 AI-assist QUESTION/STEM illustration URL
	// (GCS/CDN). Pointer+omitempty so absent stays byte-stable in the
	// oe_payload JSONB column. Persisted verbatim so it survives into the
	// published atom, the test-set snapshot, and the learner GraphQL read.
	// Wire name `image_url` — IDENTICAL across FE / BE DTO / OpenAPI.
	ImageURL *string `json:"image_url,omitempty"`
	// AnswerImageURL is the optional W8 AI-assist MODEL-ANSWER illustration
	// URL (2nd slot). Pointer+omitempty for byte-stable absence. Wire name
	// `answer_image_url` — IDENTICAL across all layers.
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
	// MinResponseChars / MaxResponseChars are optional learner answer-length
	// guidance (characters). Pointer+omitempty for byte-stable absence in the
	// oe_payload JSONB. Wire names IDENTICAL across FE / BE DTO / OpenAPI.
	MinResponseChars *int `json:"min_response_chars,omitempty"`
	MaxResponseChars *int `json:"max_response_chars,omitempty"`
	// GraderTier optionally pins the OE AI-grading model tier (T1 fast/cheap,
	// T2 deeper/costlier). Absent → orchestrator default. Wire name `grader_tier`.
	GraderTier *string `json:"grader_tier,omitempty"`
}

// Rubric groups a list of weighted grading criteria. Weights are integer
// percents (0..100) for deterministic equality checks; the OpenAPI surface
// expresses these as floats summing to 1.0 but the domain canonicalises on
// integer percents to dodge float-equality bugs at the boundary.
type Rubric struct {
	Criteria []RubricCriterion `json:"criteria"`
}

// RubricCriterion is one row of the weighted rubric.
type RubricCriterion struct {
	CriterionID   string `json:"criterion_id"`
	Description   string `json:"description"`
	WeightPercent int    `json:"weight_percent"`
}

// Validate enforces the §2.4 invariants. Called by NewRevision before
// persistence and by the HTTP handler at the boundary.
func (p *OEPayload) Validate() error {
	if p == nil {
		return fmt.Errorf("oe payload: nil")
	}
	if strings.TrimSpace(p.ModelAnswer) == "" {
		return fmt.Errorf("oe payload: model_answer is required (non-empty)")
	}
	if len(p.ModelAnswer) > 16384 {
		return fmt.Errorf("oe payload: model_answer too long: %d > 16384", len(p.ModelAnswer))
	}
	if p.WeightedRubric != nil {
		if len(p.WeightedRubric.Criteria) == 0 {
			return fmt.Errorf("oe payload: weighted_rubric set but criteria is empty")
		}
		sum := 0
		for i, c := range p.WeightedRubric.Criteria {
			if strings.TrimSpace(c.Description) == "" {
				return fmt.Errorf("oe payload: rubric.criteria[%d].description is required", i)
			}
			sum += c.WeightPercent
		}
		if sum != 100 {
			return fmt.Errorf("oe payload: rubric weight_percent sum = %d; want 100", sum)
		}
	}
	if p.MinResponseChars != nil && *p.MinResponseChars < 0 {
		return fmt.Errorf("oe payload: min_response_chars must be >= 0")
	}
	if p.MaxResponseChars != nil && *p.MaxResponseChars < 0 {
		return fmt.Errorf("oe payload: max_response_chars must be >= 0")
	}
	if p.MinResponseChars != nil && p.MaxResponseChars != nil && *p.MinResponseChars > *p.MaxResponseChars {
		return fmt.Errorf("oe payload: min_response_chars (%d) > max_response_chars (%d)", *p.MinResponseChars, *p.MaxResponseChars)
	}
	if p.GraderTier != nil && *p.GraderTier != "T1" && *p.GraderTier != "T2" {
		return fmt.Errorf("oe payload: grader_tier must be T1 or T2, got %q", *p.GraderTier)
	}
	return nil
}

// LearnerProjection returns a learner-safe copy of the OE payload with the
// ModelAnswer + WeightedRubric stripped. The original payload is NOT mutated.
//
// The model answer + rubric become visible to the learner only via the
// chora-consumption SubmitAnswer response (post-grade) per design §5.
func (p OEPayload) LearnerProjection() OEPayload {
	return OEPayload{
		// ModelAnswer intentionally empty
		// WeightedRubric intentionally nil
	}
}
