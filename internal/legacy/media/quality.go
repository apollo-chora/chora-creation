package media

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Quality Assessment Domain Model
// ---------------------------------------------------------------------------

// QualityGrade represents the overall quality classification of a media asset.
type QualityGrade string

const (
	// QualityGradeA — Excellent: sharp, high-res, valid format.
	QualityGradeA QualityGrade = "A"
	// QualityGradeB — Good: minor issues but fully usable.
	QualityGradeB QualityGrade = "B"
	// QualityGradeC — Acceptable: usable but not optimal for learning content.
	QualityGradeC QualityGrade = "C"
	// QualityGradeD — Poor: significant quality issues, may hinder comprehension.
	QualityGradeD QualityGrade = "D"
	// QualityGradeF — Unusable: corrupt, unreadable, or too low quality.
	QualityGradeF QualityGrade = "F"
)

// QualityAssessment captures the result of an image quality evaluation.
// Recorded after optimization but before marking a media asset as ready.
// This does NOT block the pipeline — content authors decide on poor quality.
type QualityAssessment struct {
	// ID is the unique UUIDv7 identifier for this assessment.
	ID uuid.UUID `json:"id"`

	// ProcessingJobID links this assessment to its parent processing job.
	ProcessingJobID uuid.UUID `json:"processing_job_id"`

	// BlurScore ranges from 0.0 (very blurry) to 1.0 (sharp).
	// Calculated via Laplacian variance, normalized.
	BlurScore float64 `json:"blur_score"`

	// ResolutionAdequate is true if the shortest side >= 768px (tablet-first mandate).
	ResolutionAdequate bool `json:"resolution_adequate"`

	// FormatValid is true if the image is decodable and not corrupted.
	FormatValid bool `json:"format_valid"`

	// OCRTextDetected is true if the image contains significant text regions
	// (high contrast edge density heuristic).
	OCRTextDetected bool `json:"ocr_text_detected"`

	// LLMReady is the composite readiness flag: !blurry && adequate resolution && valid format.
	LLMReady bool `json:"llm_ready"`

	// QualityGrade is the overall letter grade (A/B/C/D/F).
	QualityGrade QualityGrade `json:"quality_grade"`

	// AssessedAt is the timestamp when the assessment was performed.
	AssessedAt time.Time `json:"assessed_at"`
}

// ---------------------------------------------------------------------------
// Quality Thresholds
// ---------------------------------------------------------------------------

const (
	// BlurThresholdSharp is the minimum blur score considered "sharp".
	BlurThresholdSharp = 0.7

	// BlurThresholdAcceptable is the minimum blur score considered "acceptable".
	BlurThresholdAcceptable = 0.5

	// BlurThresholdBlurry is the threshold below which an image is considered blurry.
	BlurThresholdBlurry = 0.3

	// MinResolutionShortSide is the minimum pixel count on the shortest side
	// for tablet-first content (ADR-087 / UI mandate: >= 768px).
	MinResolutionShortSide = 768
)

// ComputeQualityGrade determines the quality grade from assessment metrics.
func ComputeQualityGrade(blurScore float64, resolutionAdequate, formatValid bool) QualityGrade {
	if !formatValid {
		return QualityGradeF
	}

	if blurScore >= BlurThresholdSharp && resolutionAdequate {
		return QualityGradeA
	}
	if blurScore >= BlurThresholdAcceptable && resolutionAdequate {
		return QualityGradeB
	}
	if blurScore >= BlurThresholdBlurry && resolutionAdequate {
		return QualityGradeC
	}
	if blurScore >= BlurThresholdBlurry {
		// Acceptable blur but low resolution.
		return QualityGradeD
	}
	// Blurry image.
	return QualityGradeD
}

// ComputeLLMReady returns true if the image meets minimum quality for
// AI/LLM consumption: not blurry, adequate resolution, and valid format.
func ComputeLLMReady(blurScore float64, resolutionAdequate, formatValid bool) bool {
	return formatValid && resolutionAdequate && blurScore >= BlurThresholdBlurry
}
