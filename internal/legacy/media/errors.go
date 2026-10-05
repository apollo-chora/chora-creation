package media

import "errors"

// Sentinel errors for the media processor domain.
// Error codes use MEDIA_PROCESSOR_ prefix per error-handling conventions.
var (
	// ErrProcessingJobNotFound is returned when a processing job cannot be found.
	ErrProcessingJobNotFound = errors.New("MEDIA_PROCESSOR_PROCESSING_JOB_NOT_FOUND")

	// ErrMediaAssetNotFound is returned when a media asset cannot be found in storage.
	ErrMediaAssetNotFound = errors.New("MEDIA_PROCESSOR_MEDIA_ASSET_NOT_FOUND")

	// ErrScanFailed is returned when the antivirus scan fails unexpectedly.
	ErrScanFailed = errors.New("MEDIA_PROCESSOR_SCAN_FAILED")

	// ErrModerationFailed is returned when content moderation fails unexpectedly.
	ErrModerationFailed = errors.New("MEDIA_PROCESSOR_MODERATION_FAILED")

	// ErrOptimizationFailed is returned when image optimization fails.
	ErrOptimizationFailed = errors.New("MEDIA_PROCESSOR_OPTIMIZATION_FAILED")

	// ErrStorageUploadFailed is returned when uploading to object storage fails.
	ErrStorageUploadFailed = errors.New("MEDIA_PROCESSOR_STORAGE_UPLOAD_FAILED")

	// ErrStorageDownloadFailed is returned when downloading from object storage fails.
	ErrStorageDownloadFailed = errors.New("MEDIA_PROCESSOR_STORAGE_DOWNLOAD_FAILED")

	// ErrMediaQuarantined is returned when a media asset is quarantined due to scan results.
	ErrMediaQuarantined = errors.New("MEDIA_PROCESSOR_MEDIA_QUARANTINED")

	// ErrUnsupportedMimeType is returned when the MIME type is not supported for processing.
	ErrUnsupportedMimeType = errors.New("MEDIA_PROCESSOR_UNSUPPORTED_MIME_TYPE")

	// ErrQualityAssessmentFailed is returned when quality assessment encounters an unrecoverable error.
	ErrQualityAssessmentFailed = errors.New("MEDIA_PROCESSOR_QUALITY_ASSESSMENT_FAILED")
)
