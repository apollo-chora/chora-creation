package media

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// ProcessingJobStatus represents the lifecycle state of a processing job.
type ProcessingJobStatus string

const (
	ProcessingJobStatusQueued     ProcessingJobStatus = "queued"
	ProcessingJobStatusProcessing ProcessingJobStatus = "processing"
	ProcessingJobStatusCompleted  ProcessingJobStatus = "completed"
	ProcessingJobStatusFailed     ProcessingJobStatus = "failed"
)

// ScanType represents the category of security/content scan.
type ScanType string

const (
	ScanTypeAntivirus         ScanType = "antivirus"
	ScanTypeContentModeration ScanType = "content_moderation"
	ScanTypeOCRPolicy         ScanType = "ocr_policy"
)

// ScanResult represents the outcome of a scan.
type ScanResult string

const (
	ScanResultClean           ScanResult = "clean"
	ScanResultThreat          ScanResult = "threat"
	ScanResultNSFW            ScanResult = "nsfw"
	ScanResultPolicyViolation ScanResult = "policy_violation"
)

// VariantType represents the size tier of an optimized image derivative.
type VariantType string

const (
	VariantTypeThumb    VariantType = "thumb"
	VariantTypeMedium   VariantType = "medium"
	VariantTypeLarge    VariantType = "large"
	VariantTypeOriginal VariantType = "original"
)

// ProcessingStatus represents the final status of a processed media asset.
type ProcessingStatus string

const (
	ProcessingStatusPending     ProcessingStatus = "pending"
	ProcessingStatusProcessing  ProcessingStatus = "processing"
	ProcessingStatusReady       ProcessingStatus = "ready"
	ProcessingStatusQuarantined ProcessingStatus = "quarantined"
	ProcessingStatusFailed      ProcessingStatus = "failed"
)

// ---------------------------------------------------------------------------
// Domain Models
// ---------------------------------------------------------------------------

// ProcessingJob tracks the async processing pipeline state for a media asset.
type ProcessingJob struct {
	ID           uuid.UUID           `json:"id"`
	TenantID     uuid.UUID           `json:"tenant_id"`
	MediaAssetID uuid.UUID           `json:"media_asset_id"`
	Status       ProcessingJobStatus `json:"status"`
	JobType      string              `json:"job_type"`
	StartedAt    *time.Time          `json:"started_at,omitempty"`
	CompletedAt  *time.Time          `json:"completed_at,omitempty"`
	ErrorMessage string              `json:"error_message,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// ScanResultEntry is an immutable audit trail record of a single scan performed
// on a media asset. Append-only — never update or delete.
type ScanResultEntry struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     uuid.UUID      `json:"tenant_id"`
	MediaAssetID uuid.UUID      `json:"media_asset_id"`
	ScanType     ScanType       `json:"scan_type"`
	Result       ScanResult     `json:"result"`
	Details      map[string]any `json:"details,omitempty"`
	ScannedAt    time.Time      `json:"scanned_at"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ImageVariant is an optimized derivative of a source image.
type ImageVariant struct {
	MediaAssetID uuid.UUID   `json:"media_asset_id"`
	VariantType  VariantType `json:"variant_type"`
	Data         []byte      `json:"-"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
	FileSize     int64       `json:"file_size"`
	MimeType     string      `json:"mime_type"`
}

// ProcessingRequest is the input to the processing pipeline, extracted from
// the inbound cms.media_asset.uploaded event.
type ProcessingRequest struct {
	MediaAssetID    uuid.UUID `json:"media_asset_id"`
	TenantID        uuid.UUID `json:"tenant_id"`
	StorageKey      string    `json:"storage_key"`
	MimeType        string    `json:"mime_type"`
	FileSize        int64     `json:"file_size"`
	StorageCategory string    `json:"storage_category"`
}

// ProcessingResult is the output of the full processing pipeline.
type ProcessingResult struct {
	MediaAssetID      uuid.UUID          `json:"media_asset_id"`
	TenantID          uuid.UUID          `json:"tenant_id"`
	Status            ProcessingStatus   `json:"status"`
	ScanResults       []ScanResultEntry  `json:"scan_results"`
	Variants          []ImageVariant     `json:"variants"`
	QualityAssessment *QualityAssessment `json:"quality_assessment,omitempty"`
	Width             int                `json:"width"`
	Height            int                `json:"height"`
	DurationSeconds   float64            `json:"duration_seconds"`
	ContentHash       string             `json:"content_hash"`
}

// VariantSpec defines the parameters for generating an image variant.
type VariantSpec struct {
	Type      VariantType `json:"type"`
	MaxWidth  int         `json:"max_width"`
	MaxHeight int         `json:"max_height"`
	Quality   int         `json:"quality"`
	Format    string      `json:"format"`
}

// ---------------------------------------------------------------------------
// Standard Variant Specifications
// ---------------------------------------------------------------------------

var (
	// ThumbSpec defines the thumbnail variant: 320x320, 80% quality, WebP.
	ThumbSpec = VariantSpec{
		Type:      VariantTypeThumb,
		MaxWidth:  320,
		MaxHeight: 320,
		Quality:   80,
		Format:    "webp",
	}

	// MediumSpec defines the medium variant: 768x768, 85% quality, WebP.
	MediumSpec = VariantSpec{
		Type:      VariantTypeMedium,
		MaxWidth:  768,
		MaxHeight: 768,
		Quality:   85,
		Format:    "webp",
	}

	// LargeSpec defines the large variant: 1920x1080, 90% quality, WebP.
	LargeSpec = VariantSpec{
		Type:      VariantTypeLarge,
		MaxWidth:  1920,
		MaxHeight: 1080,
		Quality:   90,
		Format:    "webp",
	}
)
