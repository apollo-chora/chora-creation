package media

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SecurityScanner scans files for malware and viruses.
type SecurityScanner interface {
	// Scan performs an antivirus scan on the given file data.
	Scan(ctx context.Context, data []byte, fileName string) (ScanResultEntry, error)
}

// ContentModerator checks images for NSFW content, violence, and policy violations.
type ContentModerator interface {
	// Moderate performs content moderation on the given image data.
	Moderate(ctx context.Context, data []byte, mimeType string) (ScanResultEntry, error)
}

// ImageOptimizer creates optimized image variants according to a specification.
type ImageOptimizer interface {
	// Optimize generates an optimized variant of the source image.
	Optimize(ctx context.Context, data []byte, mimeType string, spec VariantSpec) (*ImageVariant, error)
}

// StorageClient handles object storage operations for media files.
type StorageClient interface {
	// Download retrieves file data from storage by key.
	Download(ctx context.Context, storageKey string) ([]byte, error)

	// Upload stores file data in storage under the given key.
	Upload(ctx context.Context, storageKey string, data []byte, mimeType string) error

	// GenerateSignedUploadURL creates a time-limited signed URL for uploading.
	GenerateSignedUploadURL(ctx context.Context, storageKey string, ttlSeconds int) (string, time.Time, error)

	// GenerateSignedDownloadURL creates a time-limited signed URL for downloading.
	GenerateSignedDownloadURL(ctx context.Context, storageKey string, ttlSeconds int) (string, time.Time, error)
}

// ProcessingJobRepository persists processing job state.
type ProcessingJobRepository interface {
	// Save creates or updates a processing job.
	Save(ctx context.Context, job *ProcessingJob) error

	// FindByID retrieves a processing job by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*ProcessingJob, error)

	// FindByMediaAssetID retrieves a processing job by media asset ID. Returns nil if not found.
	FindByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) (*ProcessingJob, error)
}

// ScanResultRepository persists scan results (append-only).
type ScanResultRepository interface {
	// Save creates a scan result entry (append-only, never update or delete).
	Save(ctx context.Context, result *ScanResultEntry) error

	// ListByMediaAssetID returns all scan results for a media asset.
	ListByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) ([]ScanResultEntry, error)
}

// QualityAssessor performs image quality assessment on decoded image data.
// Implementations MUST be Go-native (no external C dependencies).
type QualityAssessor interface {
	// Assess evaluates the quality of an image and returns an assessment.
	// The assessment includes blur score, resolution adequacy, format validity,
	// OCR text detection, LLM readiness, and an overall quality grade.
	Assess(ctx context.Context, imageData []byte, mimeType string) (*QualityAssessment, error)
}

// QualityAssessmentRepository persists quality assessment results.
type QualityAssessmentRepository interface {
	// Save creates a quality assessment record (append-only).
	Save(ctx context.Context, assessment *QualityAssessment) error

	// GetByJobID retrieves a quality assessment by its processing job ID.
	// Returns nil if not found.
	GetByJobID(ctx context.Context, jobID uuid.UUID) (*QualityAssessment, error)
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error
}
