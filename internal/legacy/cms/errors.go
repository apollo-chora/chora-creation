package cms

import "errors"

// Sentinel errors for the CMS domain.
// Error codes use CMS_ prefix per error-handling conventions.
var (
	// ErrContentItemNotFound is returned when a ContentItem cannot be found.
	ErrContentItemNotFound = errors.New("CMS_CONTENT_ITEM_NOT_FOUND")

	// ErrContentVersionNotFound is returned when a ContentVersion cannot be found.
	ErrContentVersionNotFound = errors.New("CMS_CONTENT_VERSION_NOT_FOUND")

	// ErrMediaAssetNotFound is returned when a MediaAsset cannot be found.
	ErrMediaAssetNotFound = errors.New("CMS_MEDIA_ASSET_NOT_FOUND")

	// ErrProjectNotFound is returned when a Project cannot be found.
	ErrProjectNotFound = errors.New("CMS_PROJECT_NOT_FOUND")

	// ErrProjectMemberNotFound is returned when a ProjectMember cannot be found.
	ErrProjectMemberNotFound = errors.New("CMS_PROJECT_MEMBER_NOT_FOUND")

	// ErrDeliverableNotFound is returned when a ProjectDeliverable cannot be found.
	ErrDeliverableNotFound = errors.New("CMS_DELIVERABLE_NOT_FOUND")

	// ErrWorkflowInvalidTransition is returned when a workflow state transition is not allowed.
	ErrWorkflowInvalidTransition = errors.New("CMS_WORKFLOW_INVALID_TRANSITION")

	// ErrVersionImmutable is returned when attempting to modify an append-only ContentVersion.
	ErrVersionImmutable = errors.New("CMS_VERSION_IMMUTABLE")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("CMS_VALIDATION_FAILED")

	// ErrMediaSizeExceeded is returned when a media file exceeds the maximum allowed size.
	ErrMediaSizeExceeded = errors.New("CMS_MEDIA_SIZE_EXCEEDED")

	// ErrMediaTypeNotAllowed is returned when a media file's MIME type is not in the allow list.
	ErrMediaTypeNotAllowed = errors.New("CMS_MEDIA_TYPE_NOT_ALLOWED")

	// ErrProjectMemberDuplicate is returned when a member already exists in the project.
	ErrProjectMemberDuplicate = errors.New("CMS_PROJECT_MEMBER_DUPLICATE")

	// ErrDeliverableAlreadyGraded is returned when attempting to grade an already-graded deliverable.
	ErrDeliverableAlreadyGraded = errors.New("CMS_DELIVERABLE_ALREADY_GRADED")

	// ErrInsufficientScope is returned when the caller lacks required permissions.
	ErrInsufficientScope = errors.New("CMS_INSUFFICIENT_SCOPE")

	// ErrUnauthorized is returned when authentication is missing or invalid.
	ErrUnauthorized = errors.New("CMS_UNAUTHORIZED")

	// ErrStorageQuotaExceeded is returned when tenant storage quota is exceeded.
	ErrStorageQuotaExceeded = errors.New("CMS_STORAGE_QUOTA_EXCEEDED")

	// ErrMediaQuarantined is returned when a media asset is quarantined.
	ErrMediaQuarantined = errors.New("CMS_MEDIA_QUARANTINED")

	// ErrProcessingJobNotFound is returned when a MediaProcessingJob cannot be found.
	ErrProcessingJobNotFound = errors.New("CMS_PROCESSING_JOB_NOT_FOUND")

	// ErrMediaVariantNotFound is returned when a MediaVariant cannot be found.
	ErrMediaVariantNotFound = errors.New("CMS_MEDIA_VARIANT_NOT_FOUND")

	// ErrContentScanResultNotFound is returned when a ContentScanResult cannot be found.
	ErrContentScanResultNotFound = errors.New("CMS_CONTENT_SCAN_RESULT_NOT_FOUND")

	// ErrChorapediaEntryNotFound is returned when a ChorapediaEntry cannot be found.
	ErrChorapediaEntryNotFound = errors.New("CMS_CHORAPEDIA_ENTRY_NOT_FOUND")

	// ErrSlugAlreadyExists is returned when a slug is already taken within a tenant.
	ErrSlugAlreadyExists = errors.New("CMS_SLUG_ALREADY_EXISTS")

	// ErrEntryNotPublished is returned when an action requires the entry to be published.
	ErrEntryNotPublished = errors.New("CMS_ENTRY_NOT_PUBLISHED")
)
