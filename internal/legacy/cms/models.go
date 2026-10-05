package cms

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// ContentType represents the type of content in a ContentItem.
type ContentType string

const (
	ContentTypeRichText    ContentType = "rich_text"
	ContentTypeMedia       ContentType = "media"
	ContentTypeCode        ContentType = "code"
	ContentTypeInteractive ContentType = "interactive"
)

// VisibilityStatus represents the visibility state of content.
type VisibilityStatus string

const (
	VisibilityStatusDraft     VisibilityStatus = "draft"
	VisibilityStatusInReview  VisibilityStatus = "in_review"
	VisibilityStatusPublished VisibilityStatus = "published"
	VisibilityStatusArchived  VisibilityStatus = "archived"
	VisibilityStatusWithdrawn VisibilityStatus = "withdrawn"
)

// WorkflowStatus represents the publishing workflow state.
type WorkflowStatus string

const (
	WorkflowStatusDraft      WorkflowStatus = "draft"
	WorkflowStatusSubmitted  WorkflowStatus = "submitted"
	WorkflowStatusAIReview   WorkflowStatus = "ai_review"
	WorkflowStatusPeerReview WorkflowStatus = "peer_review"
	WorkflowStatusApproved   WorkflowStatus = "approved"
	WorkflowStatusRejected   WorkflowStatus = "rejected"
	WorkflowStatusPublished  WorkflowStatus = "published"
)

// ProjectType represents the type of a project.
type ProjectType string

const (
	ProjectTypeIndividual ProjectType = "individual"
	ProjectTypeGroup      ProjectType = "group"
	ProjectTypeCapstone   ProjectType = "capstone"
)

// MemberRole represents the role of a project member.
type MemberRole string

const (
	MemberRoleOwner       MemberRole = "owner"
	MemberRoleContributor MemberRole = "contributor"
	MemberRoleReviewer    MemberRole = "reviewer"
)

// DeliverableStatus represents the status of a project deliverable.
type DeliverableStatus string

const (
	DeliverableStatusDraft     DeliverableStatus = "draft"
	DeliverableStatusSubmitted DeliverableStatus = "submitted"
	DeliverableStatusGraded    DeliverableStatus = "graded"
)

// StorageCategory represents the content category for cold storage lifecycle.
type StorageCategory string

const (
	StorageCategoryAssignmentSubmission StorageCategory = "assignment_submission"
	StorageCategoryExamEvidence         StorageCategory = "exam_evidence"
	StorageCategoryCredentialEvidence   StorageCategory = "credential_evidence"
	StorageCategoryAtomContent          StorageCategory = "atom_content"
	StorageCategorySocialContent        StorageCategory = "social_content"
	StorageCategoryCMSAuthored          StorageCategory = "cms_authored"
)

// ProcessingStatus represents the async processing pipeline state.
type ProcessingStatus string

const (
	ProcessingStatusPending     ProcessingStatus = "pending"
	ProcessingStatusProcessing  ProcessingStatus = "processing"
	ProcessingStatusReady       ProcessingStatus = "ready"
	ProcessingStatusQuarantined ProcessingStatus = "quarantined"
	ProcessingStatusFailed      ProcessingStatus = "failed"
)

// ScanStatus represents the security scan state.
type ScanStatus string

const (
	ScanStatusPending ScanStatus = "pending"
	ScanStatusClean   ScanStatus = "clean"
	ScanStatusThreat  ScanStatus = "threat"
	ScanStatusFlagged ScanStatus = "flagged"
)

// StorageTier represents the GCS storage class.
type StorageTier string

const (
	StorageTierStandard StorageTier = "standard"
	StorageTierNearline StorageTier = "nearline"
	StorageTierColdline StorageTier = "coldline"
	StorageTierArchive  StorageTier = "archive"
)

// VariantType represents the type of optimized image variant.
type VariantType string

const (
	VariantTypeThumb    VariantType = "thumb"
	VariantTypeMedium   VariantType = "medium"
	VariantTypeLarge    VariantType = "large"
	VariantTypeOriginal VariantType = "original"
)

// ---------------------------------------------------------------------------
// Validation Functions
// ---------------------------------------------------------------------------

// ValidContentType returns true if the given ContentType is a known value.
func ValidContentType(ct ContentType) bool {
	switch ct {
	case ContentTypeRichText, ContentTypeMedia, ContentTypeCode, ContentTypeInteractive:
		return true
	}
	return false
}

// ValidVisibilityStatus returns true if the given VisibilityStatus is a known value.
func ValidVisibilityStatus(vs VisibilityStatus) bool {
	switch vs {
	case VisibilityStatusDraft, VisibilityStatusInReview, VisibilityStatusPublished, VisibilityStatusArchived, VisibilityStatusWithdrawn:
		return true
	}
	return false
}

// ValidWorkflowStatus returns true if the given WorkflowStatus is a known value.
func ValidWorkflowStatus(ws WorkflowStatus) bool {
	switch ws {
	case WorkflowStatusDraft, WorkflowStatusSubmitted, WorkflowStatusAIReview, WorkflowStatusPeerReview, WorkflowStatusApproved, WorkflowStatusRejected, WorkflowStatusPublished:
		return true
	}
	return false
}

// ValidProjectType returns true if the given ProjectType is a known value.
func ValidProjectType(pt ProjectType) bool {
	switch pt {
	case ProjectTypeIndividual, ProjectTypeGroup, ProjectTypeCapstone:
		return true
	}
	return false
}

// ValidMemberRole returns true if the given MemberRole is a known value.
func ValidMemberRole(mr MemberRole) bool {
	switch mr {
	case MemberRoleOwner, MemberRoleContributor, MemberRoleReviewer:
		return true
	}
	return false
}

// ValidDeliverableStatus returns true if the given DeliverableStatus is a known value.
func ValidDeliverableStatus(ds DeliverableStatus) bool {
	switch ds {
	case DeliverableStatusDraft, DeliverableStatusSubmitted, DeliverableStatusGraded:
		return true
	}
	return false
}

// ValidStorageCategory returns true if the given StorageCategory is a known value.
func ValidStorageCategory(sc StorageCategory) bool {
	switch sc {
	case StorageCategoryAssignmentSubmission, StorageCategoryExamEvidence, StorageCategoryCredentialEvidence, StorageCategoryAtomContent, StorageCategorySocialContent, StorageCategoryCMSAuthored:
		return true
	}
	return false
}

// ValidProcessingStatus returns true if the given ProcessingStatus is a known value.
func ValidProcessingStatus(ps ProcessingStatus) bool {
	switch ps {
	case ProcessingStatusPending, ProcessingStatusProcessing, ProcessingStatusReady, ProcessingStatusQuarantined, ProcessingStatusFailed:
		return true
	}
	return false
}

// ValidScanStatus returns true if the given ScanStatus is a known value.
func ValidScanStatus(ss ScanStatus) bool {
	switch ss {
	case ScanStatusPending, ScanStatusClean, ScanStatusThreat, ScanStatusFlagged:
		return true
	}
	return false
}

// ValidStorageTier returns true if the given StorageTier is a known value.
func ValidStorageTier(st StorageTier) bool {
	switch st {
	case StorageTierStandard, StorageTierNearline, StorageTierColdline, StorageTierArchive:
		return true
	}
	return false
}

// ValidVariantType returns true if the given VariantType is a known value.
func ValidVariantType(vt VariantType) bool {
	switch vt {
	case VariantTypeThumb, VariantTypeMedium, VariantTypeLarge, VariantTypeOriginal:
		return true
	}
	return false
}

// ValidProcessingJobStatus returns true if the given ProcessingJobStatus is a known value.
func ValidProcessingJobStatus(pjs ProcessingJobStatus) bool {
	switch pjs {
	case ProcessingJobStatusQueued, ProcessingJobStatusProcessing, ProcessingJobStatusCompleted, ProcessingJobStatusFailed:
		return true
	}
	return false
}

// ValidScanType returns true if the given ScanType is a known value.
func ValidScanType(st ScanType) bool {
	switch st {
	case ScanTypeAntivirus, ScanTypeContentModeration, ScanTypeOCRPolicy:
		return true
	}
	return false
}

// ValidScanResult returns true if the given ScanResult is a known value.
func ValidScanResult(sr ScanResult) bool {
	switch sr {
	case ScanResultClean, ScanResultThreat, ScanResultNSFW, ScanResultPolicyViolation:
		return true
	}
	return false
}

// ValidWorkflowTransition returns true if the transition from → to is allowed.
// Allowed transitions:
//   - draft → submitted
//   - submitted → ai_review
//   - ai_review → peer_review, rejected
//   - peer_review → approved, rejected
//   - approved → published
//   - rejected → draft
func ValidWorkflowTransition(from, to WorkflowStatus) bool {
	switch from {
	case WorkflowStatusDraft:
		return to == WorkflowStatusSubmitted
	case WorkflowStatusSubmitted:
		return to == WorkflowStatusAIReview
	case WorkflowStatusAIReview:
		return to == WorkflowStatusPeerReview || to == WorkflowStatusRejected
	case WorkflowStatusPeerReview:
		return to == WorkflowStatusApproved || to == WorkflowStatusRejected
	case WorkflowStatusApproved:
		return to == WorkflowStatusPublished
	case WorkflowStatusRejected:
		return to == WorkflowStatusDraft
	}
	return false
}

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// ContentItem is the aggregate root for authored content.
type ContentItem struct {
	ID               uuid.UUID        `json:"id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	Title            string           `json:"title"`
	ContentType      ContentType      `json:"content_type"`
	TopicNodeID      *uuid.UUID       `json:"topic_node_id,omitempty"`
	CurrentVersionID *uuid.UUID       `json:"current_version_id,omitempty"`
	VisibilityStatus VisibilityStatus `json:"visibility_status"`
	WorkflowStatus   WorkflowStatus   `json:"workflow_status"`
	CreatedByGCID    uuid.UUID        `json:"created_by_gcid"`
	Metadata         map[string]any   `json:"metadata"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	DeletedAt        *time.Time       `json:"deleted_at,omitempty"`
}

// ContentVersion is a child of ContentItem. Append-only — no UpdatedAt, no DeletedAt.
type ContentVersion struct {
	ID               uuid.UUID        `json:"id"`
	ItemID           uuid.UUID        `json:"item_id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	VersionNumber    int              `json:"version_number"`
	Content          map[string]any   `json:"content"`
	Metadata         map[string]any   `json:"metadata"`
	VisibilityStatus VisibilityStatus `json:"visibility_status"`
	CreatedByGCID    uuid.UUID        `json:"created_by_gcid"`
	CreatedAt        time.Time        `json:"created_at"`
}

// MediaAsset is the aggregate root for uploaded media files.
type MediaAsset struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	StorageKey     string    `json:"storage_key"`
	FileName       string    `json:"file_name"`
	MimeType       string    `json:"mime_type"`
	FileSize       int64     `json:"file_size"`
	AltText        string    `json:"alt_text"`
	Description    string    `json:"description"`
	UploadedByGCID uuid.UUID `json:"uploaded_by_gcid"`
	// ADR-134: Content ingestion pipeline fields
	StorageCategory  StorageCategory  `json:"storage_category"`
	ProcessingStatus ProcessingStatus `json:"processing_status"`
	ScanStatus       ScanStatus       `json:"scan_status"`
	StorageTier      StorageTier      `json:"storage_tier"`
	Width            *int             `json:"width,omitempty"`
	Height           *int             `json:"height,omitempty"`
	DurationSeconds  *float64         `json:"duration_seconds,omitempty"`
	ContentHash      string           `json:"content_hash,omitempty"`
	OriginalFileSize *int64           `json:"original_file_size,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	DeletedAt        *time.Time       `json:"deleted_at,omitempty"`
}

// MediaVariant is an optimized derivative of a MediaAsset (e.g., thumb, medium, large).
type MediaVariant struct {
	ID           uuid.UUID   `json:"id"`
	TenantID     uuid.UUID   `json:"tenant_id"`
	MediaAssetID uuid.UUID   `json:"media_asset_id"`
	VariantType  VariantType `json:"variant_type"`
	StorageKey   string      `json:"storage_key"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
	FileSize     int64       `json:"file_size"`
	MimeType     string      `json:"mime_type"`
	CreatedAt    time.Time   `json:"created_at"`
}

// MediaProcessingJob tracks async processing pipeline state.
type MediaProcessingJob struct {
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

// ProcessingJobStatus represents the processing job state.
type ProcessingJobStatus string

const (
	ProcessingJobStatusQueued     ProcessingJobStatus = "queued"
	ProcessingJobStatusProcessing ProcessingJobStatus = "processing"
	ProcessingJobStatusCompleted  ProcessingJobStatus = "completed"
	ProcessingJobStatusFailed     ProcessingJobStatus = "failed"
)

// ContentScanResult is an append-only audit record of a security scan.
// No UpdatedAt, no DeletedAt — immutable per ADR-134.
type ContentScanResult struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     uuid.UUID      `json:"tenant_id"`
	MediaAssetID uuid.UUID      `json:"media_asset_id"`
	ScanType     ScanType       `json:"scan_type"`
	Result       ScanResult     `json:"result"`
	Details      map[string]any `json:"details,omitempty"`
	ScannedAt    time.Time      `json:"scanned_at"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ScanType represents the type of security scan.
type ScanType string

const (
	ScanTypeAntivirus         ScanType = "antivirus"
	ScanTypeContentModeration ScanType = "content_moderation"
	ScanTypeOCRPolicy         ScanType = "ocr_policy"
)

// ScanResult represents the outcome of a security scan.
type ScanResult string

const (
	ScanResultClean           ScanResult = "clean"
	ScanResultThreat          ScanResult = "threat"
	ScanResultNSFW            ScanResult = "nsfw"
	ScanResultPolicyViolation ScanResult = "policy_violation"
)

// Project is the aggregate root for project-based learning.
type Project struct {
	ID            uuid.UUID   `json:"id"`
	TenantID      uuid.UUID   `json:"tenant_id"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	ProjectType   ProjectType `json:"project_type"`
	DueDate       *time.Time  `json:"due_date,omitempty"`
	CreatedByGCID uuid.UUID   `json:"created_by_gcid"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	DeletedAt     *time.Time  `json:"deleted_at,omitempty"`
}

// ProjectMember is a child of Project. No UpdatedAt, no DeletedAt.
type ProjectMember struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	GCID       uuid.UUID  `json:"gcid"`
	MemberRole MemberRole `json:"member_role"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ChorapediaCategory represents the category of a Chorapedia encyclopedia entry.
type ChorapediaCategory string

const (
	ChorapediaCategoryLore      ChorapediaCategory = "lore"
	ChorapediaCategoryMechanics ChorapediaCategory = "mechanics"
	ChorapediaCategoryEntity    ChorapediaCategory = "entity"
	ChorapediaCategoryGuide     ChorapediaCategory = "guide"
	ChorapediaCategoryFAQ       ChorapediaCategory = "faq"
)

// IsValid returns true if the ChorapediaCategory is a known value.
func (c ChorapediaCategory) IsValid() bool {
	switch c {
	case ChorapediaCategoryLore, ChorapediaCategoryMechanics, ChorapediaCategoryEntity, ChorapediaCategoryGuide, ChorapediaCategoryFAQ:
		return true
	}
	return false
}

// ChorapediaEntry is the aggregate root for Chorapedia encyclopedia entries.
type ChorapediaEntry struct {
	ID             uuid.UUID          `json:"id"`
	TenantID       uuid.UUID          `json:"tenant_id"`
	Title          string             `json:"title"`
	Slug           string             `json:"slug"`
	ContentBody    string             `json:"content_body"`
	Summary        string             `json:"summary"`
	Category       ChorapediaCategory `json:"category"`
	LinkedAtomIDs  []uuid.UUID        `json:"linked_atom_ids"`
	LinkedTopicIDs []uuid.UUID        `json:"linked_topic_ids"`
	AuthorGCID     uuid.UUID          `json:"author_gcid"`
	IsPublished    bool               `json:"is_published"`
	PublishedAt    *time.Time         `json:"published_at,omitempty"`
	ViewCount      int                `json:"view_count"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	DeletedAt      *time.Time         `json:"deleted_at,omitempty"`
}

// ProjectDeliverable is a child of Project.
type ProjectDeliverable struct {
	ID              uuid.UUID         `json:"id"`
	ProjectID       uuid.UUID         `json:"project_id"`
	TenantID        uuid.UUID         `json:"tenant_id"`
	Title           string            `json:"title"`
	Content         map[string]any    `json:"content"`
	MediaAssetIDs   []uuid.UUID       `json:"media_asset_ids"`
	SubmittedByGCID uuid.UUID         `json:"submitted_by_gcid"`
	Status          DeliverableStatus `json:"status"`
	Score           *float64          `json:"score,omitempty"`
	Feedback        *string           `json:"feedback,omitempty"`
	RubricResults   map[string]any    `json:"rubric_results"`
	SubmittedAt     *time.Time        `json:"submitted_at,omitempty"`
	GradedAt        *time.Time        `json:"graded_at,omitempty"`
	GradedByGCID    *uuid.UUID        `json:"graded_by_gcid,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
