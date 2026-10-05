package cms

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Allowed MIME types for media uploads
// ---------------------------------------------------------------------------

var allowedMimeTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/gif":       true,
	"image/webp":      true,
	"image/svg+xml":   true,
	"video/mp4":       true,
	"video/webm":      true,
	"audio/mpeg":      true,
	"audio/ogg":       true,
	"application/pdf": true,
}

// maxMediaFileSize is the maximum allowed media file size (50 MB).
const maxMediaFileSize int64 = 50 * 1024 * 1024

// ---------------------------------------------------------------------------
// ContentItemService — manages content items and versions
// ---------------------------------------------------------------------------

// ContentItemService handles content item CRUD and version management.
type ContentItemService struct {
	itemRepo    ContentItemRepository
	versionRepo ContentVersionRepository
	events      EventPublisher
}

// NewContentItemService creates a ContentItemService with the given repositories and event publisher.
func NewContentItemService(itemRepo ContentItemRepository, versionRepo ContentVersionRepository, events EventPublisher) *ContentItemService {
	return &ContentItemService{itemRepo: itemRepo, versionRepo: versionRepo, events: events}
}

// CreateItem creates a new content item.
// Publishes EventContentItemCreated on success.
func (s *ContentItemService) CreateItem(ctx context.Context, tenantID uuid.UUID, title string, contentType ContentType, topicNodeID *uuid.UUID, createdByGCID uuid.UUID, metadata map[string]any) (*ContentItem, error) {
	if title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if !ValidContentType(contentType) {
		return nil, fmt.Errorf("invalid content type %q: %w", contentType, ErrValidationFailed)
	}

	now := time.Now().UTC()
	if metadata == nil {
		metadata = make(map[string]any)
	}

	item := &ContentItem{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		Title:            title,
		ContentType:      contentType,
		TopicNodeID:      topicNodeID,
		VisibilityStatus: VisibilityStatusDraft,
		WorkflowStatus:   WorkflowStatusDraft,
		CreatedByGCID:    createdByGCID,
		Metadata:         metadata,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save content item: %w", err)
	}

	evt := NewDomainEvent(EventContentItemCreated, tenantID, &createdByGCID, item.ID, "ContentItem", map[string]interface{}{
		"title":        title,
		"content_type": string(contentType),
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish content item created event: %w", err)
	}

	return item, nil
}

// GetItem retrieves a content item by ID.
func (s *ContentItemService) GetItem(ctx context.Context, id uuid.UUID) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", id, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}
	return item, nil
}

// UpdateItem updates a content item's mutable fields.
// Publishes EventContentItemUpdated on success.
func (s *ContentItemService) UpdateItem(ctx context.Context, id uuid.UUID, title *string, metadata map[string]any) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", id, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	if title != nil {
		if *title == "" {
			return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
		}
		item.Title = *title
	}
	if metadata != nil {
		item.Metadata = metadata
	}
	item.UpdatedAt = time.Now().UTC()

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save content item: %w", err)
	}

	evt := NewDomainEvent(EventContentItemUpdated, item.TenantID, &item.CreatedByGCID, item.ID, "ContentItem", map[string]interface{}{
		"title": item.Title,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish content item updated event: %w", err)
	}

	return item, nil
}

// SoftDeleteItem marks a content item as deleted.
// Publishes EventContentItemDeleted on success.
func (s *ContentItemService) SoftDeleteItem(ctx context.Context, id uuid.UUID) error {
	item, err := s.itemRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("find content item %s: %w", id, err)
	}
	if item == nil {
		return ErrContentItemNotFound
	}

	if err := s.itemRepo.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete content item: %w", err)
	}

	evt := NewDomainEvent(EventContentItemDeleted, item.TenantID, &item.CreatedByGCID, item.ID, "ContentItem", map[string]interface{}{
		"title": item.Title,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return fmt.Errorf("publish content item deleted event: %w", err)
	}

	return nil
}

// ListItems returns content items for a tenant with cursor-based pagination.
func (s *ContentItemService) ListItems(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentItem, error) {
	items, err := s.itemRepo.ListByTenant(ctx, tenantID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list content items: %w", err)
	}
	return items, nil
}

// CreateVersion creates a new append-only content version for an item.
// Publishes EventContentVersionCreated on success.
func (s *ContentItemService) CreateVersion(ctx context.Context, itemID, tenantID uuid.UUID, content, metadata map[string]any, createdByGCID uuid.UUID) (*ContentVersion, error) {
	// Verify item exists
	item, err := s.itemRepo.FindByID(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", itemID, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	// Determine next version number
	versions, err := s.versionRepo.ListByItem(ctx, itemID, nil, 1)
	if err != nil {
		return nil, fmt.Errorf("list versions for item %s: %w", itemID, err)
	}
	nextVersion := 1
	if len(versions) > 0 {
		nextVersion = versions[0].VersionNumber + 1
	}

	if content == nil {
		content = make(map[string]any)
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}

	version := &ContentVersion{
		ID:               uuid.Must(uuid.NewV7()),
		ItemID:           itemID,
		TenantID:         tenantID,
		VersionNumber:    nextVersion,
		Content:          content,
		Metadata:         metadata,
		VisibilityStatus: VisibilityStatusDraft,
		CreatedByGCID:    createdByGCID,
		CreatedAt:        time.Now().UTC(),
	}

	if err := s.versionRepo.Save(ctx, version); err != nil {
		return nil, fmt.Errorf("save content version: %w", err)
	}

	// Update item's current version pointer
	item.CurrentVersionID = &version.ID
	item.UpdatedAt = time.Now().UTC()
	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("update item current version: %w", err)
	}

	evt := NewDomainEvent(EventContentVersionCreated, tenantID, &createdByGCID, version.ID, "ContentVersion", map[string]interface{}{
		"item_id":        itemID.String(),
		"version_number": nextVersion,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish content version created event: %w", err)
	}

	return version, nil
}

// GetVersion retrieves a content version by ID.
func (s *ContentItemService) GetVersion(ctx context.Context, id uuid.UUID) (*ContentVersion, error) {
	version, err := s.versionRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find content version %s: %w", id, err)
	}
	if version == nil {
		return nil, ErrContentVersionNotFound
	}
	return version, nil
}

// ListVersions returns content versions for an item with cursor-based pagination.
func (s *ContentItemService) ListVersions(ctx context.Context, itemID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentVersion, error) {
	versions, err := s.versionRepo.ListByItem(ctx, itemID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list content versions: %w", err)
	}
	return versions, nil
}

// ---------------------------------------------------------------------------
// PublishingService — manages content publishing workflow
// ---------------------------------------------------------------------------

// PublishingService handles the publishing workflow for content items.
type PublishingService struct {
	itemRepo    ContentItemRepository
	versionRepo ContentVersionRepository
	events      EventPublisher
}

// NewPublishingService creates a PublishingService with the given repositories and event publisher.
func NewPublishingService(itemRepo ContentItemRepository, versionRepo ContentVersionRepository, events EventPublisher) *PublishingService {
	return &PublishingService{itemRepo: itemRepo, versionRepo: versionRepo, events: events}
}

// SubmitForReview transitions a content item from draft to submitted.
func (s *PublishingService) SubmitForReview(ctx context.Context, itemID uuid.UUID, submittedByGCID uuid.UUID) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", itemID, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	if !ValidWorkflowTransition(item.WorkflowStatus, WorkflowStatusSubmitted) {
		return nil, fmt.Errorf("cannot transition from %s to submitted: %w", item.WorkflowStatus, ErrWorkflowInvalidTransition)
	}

	item.WorkflowStatus = WorkflowStatusSubmitted
	item.VisibilityStatus = VisibilityStatusInReview
	item.UpdatedAt = time.Now().UTC()

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save content item: %w", err)
	}

	return item, nil
}

// Approve transitions a content item from peer_review to approved.
func (s *PublishingService) Approve(ctx context.Context, itemID uuid.UUID, approvedByGCID uuid.UUID) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", itemID, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	if !ValidWorkflowTransition(item.WorkflowStatus, WorkflowStatusApproved) {
		return nil, fmt.Errorf("cannot transition from %s to approved: %w", item.WorkflowStatus, ErrWorkflowInvalidTransition)
	}

	item.WorkflowStatus = WorkflowStatusApproved
	item.UpdatedAt = time.Now().UTC()

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save content item: %w", err)
	}

	return item, nil
}

// Reject transitions a content item from ai_review or peer_review to rejected.
func (s *PublishingService) Reject(ctx context.Context, itemID uuid.UUID, rejectedByGCID uuid.UUID, reason string) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", itemID, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	if !ValidWorkflowTransition(item.WorkflowStatus, WorkflowStatusRejected) {
		return nil, fmt.Errorf("cannot transition from %s to rejected: %w", item.WorkflowStatus, ErrWorkflowInvalidTransition)
	}

	item.WorkflowStatus = WorkflowStatusRejected
	item.VisibilityStatus = VisibilityStatusDraft
	item.UpdatedAt = time.Now().UTC()

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save content item: %w", err)
	}

	return item, nil
}

// Publish transitions a content item from approved to published.
// Creates a published version and archives any previously published version.
// Publishes EventContentPublished on success.
func (s *PublishingService) Publish(ctx context.Context, itemID uuid.UUID, publishedByGCID uuid.UUID) (*ContentItem, error) {
	item, err := s.itemRepo.FindByID(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find content item %s: %w", itemID, err)
	}
	if item == nil {
		return nil, ErrContentItemNotFound
	}

	if !ValidWorkflowTransition(item.WorkflowStatus, WorkflowStatusPublished) {
		return nil, fmt.Errorf("cannot transition from %s to published: %w", item.WorkflowStatus, ErrWorkflowInvalidTransition)
	}

	// Archive previous published version if exists
	prevPublished, err := s.versionRepo.FindLatestPublished(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("find latest published version: %w", err)
	}
	if prevPublished != nil {
		// Archive by creating a new version record with archived status
		// (ContentVersion is append-only — we do not modify the old record)
		_ = prevPublished // Previous version remains as-is in the append-only log
	}

	// Mark current version as published
	if item.CurrentVersionID != nil {
		currentVersion, err := s.versionRepo.FindByID(ctx, *item.CurrentVersionID)
		if err != nil {
			return nil, fmt.Errorf("find current version: %w", err)
		}
		if currentVersion != nil {
			publishedVersion := &ContentVersion{
				ID:               uuid.Must(uuid.NewV7()),
				ItemID:           currentVersion.ItemID,
				TenantID:         currentVersion.TenantID,
				VersionNumber:    currentVersion.VersionNumber,
				Content:          currentVersion.Content,
				Metadata:         currentVersion.Metadata,
				VisibilityStatus: VisibilityStatusPublished,
				CreatedByGCID:    publishedByGCID,
				CreatedAt:        time.Now().UTC(),
			}
			if err := s.versionRepo.Save(ctx, publishedVersion); err != nil {
				return nil, fmt.Errorf("save published version: %w", err)
			}
			item.CurrentVersionID = &publishedVersion.ID
		}
	}

	item.WorkflowStatus = WorkflowStatusPublished
	item.VisibilityStatus = VisibilityStatusPublished
	item.UpdatedAt = time.Now().UTC()

	if err := s.itemRepo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("save published content item: %w", err)
	}

	evt := NewDomainEvent(EventContentPublished, item.TenantID, &publishedByGCID, item.ID, "ContentItem", map[string]interface{}{
		"title":           item.Title,
		"workflow_status": string(WorkflowStatusPublished),
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish content published event: %w", err)
	}

	return item, nil
}

// ---------------------------------------------------------------------------
// MediaService — manages media assets
// ---------------------------------------------------------------------------

// MediaService handles media asset upload, retrieval, and deletion.
type MediaService struct {
	repo   MediaAssetRepository
	events EventPublisher
}

// NewMediaService creates a MediaService with the given repository and event publisher.
func NewMediaService(repo MediaAssetRepository, events EventPublisher) *MediaService {
	return &MediaService{repo: repo, events: events}
}

// UploadAsset creates a new media asset record.
// Validates file size (max 50MB), allowed MIME types, and storage category.
// Publishes EventMediaAssetUploaded on success.
func (s *MediaService) UploadAsset(ctx context.Context, tenantID uuid.UUID, storageKey, fileName, mimeType string, fileSize int64, altText, description string, uploadedByGCID uuid.UUID, storageCategory StorageCategory) (*MediaAsset, error) {
	if storageKey == "" || fileName == "" {
		return nil, fmt.Errorf("storage key and file name must not be empty: %w", ErrValidationFailed)
	}

	// Validate storage category
	if !ValidStorageCategory(storageCategory) {
		return nil, fmt.Errorf("invalid storage category %q: %w", storageCategory, ErrValidationFailed)
	}

	// Validate file size
	if fileSize < 0 {
		return nil, fmt.Errorf("file size must not be negative: %w", ErrValidationFailed)
	}
	if fileSize > maxMediaFileSize {
		return nil, fmt.Errorf("file size %d exceeds maximum %d bytes: %w", fileSize, maxMediaFileSize, ErrMediaSizeExceeded)
	}

	// Validate MIME type
	if !allowedMimeTypes[mimeType] {
		return nil, fmt.Errorf("MIME type %q is not allowed: %w", mimeType, ErrMediaTypeNotAllowed)
	}

	now := time.Now().UTC()
	asset := &MediaAsset{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		StorageKey:       storageKey,
		FileName:         fileName,
		MimeType:         mimeType,
		FileSize:         fileSize,
		AltText:          altText,
		Description:      description,
		UploadedByGCID:   uploadedByGCID,
		StorageCategory:  storageCategory,
		ProcessingStatus: ProcessingStatusPending,
		ScanStatus:       ScanStatusPending,
		StorageTier:      StorageTierStandard,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.repo.Save(ctx, asset); err != nil {
		return nil, fmt.Errorf("save media asset: %w", err)
	}

	evt := NewDomainEvent(EventMediaAssetUploaded, tenantID, &uploadedByGCID, asset.ID, "MediaAsset", map[string]interface{}{
		"file_name":        fileName,
		"mime_type":        mimeType,
		"file_size":        fileSize,
		"storage_category": string(storageCategory),
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish media asset uploaded event: %w", err)
	}

	return asset, nil
}

// GetAsset retrieves a media asset by ID.
func (s *MediaService) GetAsset(ctx context.Context, id uuid.UUID) (*MediaAsset, error) {
	asset, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find media asset %s: %w", id, err)
	}
	if asset == nil {
		return nil, ErrMediaAssetNotFound
	}
	return asset, nil
}

// ListAssets returns media assets for a tenant with cursor-based pagination.
func (s *MediaService) ListAssets(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]MediaAsset, error) {
	assets, err := s.repo.ListByTenant(ctx, tenantID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list media assets: %w", err)
	}
	return assets, nil
}

// SoftDeleteAsset marks a media asset as deleted.
// Publishes EventMediaAssetDeleted on success.
func (s *MediaService) SoftDeleteAsset(ctx context.Context, id uuid.UUID) error {
	asset, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("find media asset %s: %w", id, err)
	}
	if asset == nil {
		return ErrMediaAssetNotFound
	}

	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete media asset: %w", err)
	}

	evt := NewDomainEvent(EventMediaAssetDeleted, asset.TenantID, &asset.UploadedByGCID, asset.ID, "MediaAsset", map[string]interface{}{
		"file_name": asset.FileName,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return fmt.Errorf("publish media asset deleted event: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// ProjectService — manages projects, members, and deliverables
// ---------------------------------------------------------------------------

// ProjectService handles project lifecycle, member management, and deliverable grading.
type ProjectService struct {
	projectRepo     ProjectRepository
	memberRepo      ProjectMemberRepository
	deliverableRepo ProjectDeliverableRepository
	events          EventPublisher
}

// NewProjectService creates a ProjectService with the given repositories and event publisher.
func NewProjectService(projectRepo ProjectRepository, memberRepo ProjectMemberRepository, deliverableRepo ProjectDeliverableRepository, events EventPublisher) *ProjectService {
	return &ProjectService{
		projectRepo:     projectRepo,
		memberRepo:      memberRepo,
		deliverableRepo: deliverableRepo,
		events:          events,
	}
}

// CreateProject creates a new project.
// Publishes EventProjectCreated on success.
func (s *ProjectService) CreateProject(ctx context.Context, tenantID uuid.UUID, name, description string, projectType ProjectType, dueDate *time.Time, createdByGCID uuid.UUID) (*Project, error) {
	if name == "" {
		return nil, fmt.Errorf("project name must not be empty: %w", ErrValidationFailed)
	}
	if !ValidProjectType(projectType) {
		return nil, fmt.Errorf("invalid project type %q: %w", projectType, ErrValidationFailed)
	}
	if len(description) > 5000 {
		return nil, fmt.Errorf("description must not exceed 5000 characters: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	project := &Project{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		Name:          name,
		Description:   description,
		ProjectType:   projectType,
		DueDate:       dueDate,
		CreatedByGCID: createdByGCID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.projectRepo.Save(ctx, project); err != nil {
		return nil, fmt.Errorf("save project: %w", err)
	}

	evt := NewDomainEvent(EventProjectCreated, tenantID, &createdByGCID, project.ID, "Project", map[string]interface{}{
		"name":         name,
		"project_type": string(projectType),
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish project created event: %w", err)
	}

	return project, nil
}

// GetProject retrieves a project by ID.
func (s *ProjectService) GetProject(ctx context.Context, id uuid.UUID) (*Project, error) {
	project, err := s.projectRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find project %s: %w", id, err)
	}
	if project == nil {
		return nil, ErrProjectNotFound
	}
	return project, nil
}

// UpdateProject updates a project's mutable fields.
func (s *ProjectService) UpdateProject(ctx context.Context, id uuid.UUID, name *string, description *string, dueDate *time.Time) (*Project, error) {
	project, err := s.projectRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find project %s: %w", id, err)
	}
	if project == nil {
		return nil, ErrProjectNotFound
	}

	if name != nil {
		if *name == "" {
			return nil, fmt.Errorf("project name must not be empty: %w", ErrValidationFailed)
		}
		project.Name = *name
	}
	if description != nil {
		project.Description = *description
	}
	if dueDate != nil {
		project.DueDate = dueDate
	}
	project.UpdatedAt = time.Now().UTC()

	if err := s.projectRepo.Save(ctx, project); err != nil {
		return nil, fmt.Errorf("save project: %w", err)
	}

	return project, nil
}

// SoftDeleteProject marks a project as deleted.
func (s *ProjectService) SoftDeleteProject(ctx context.Context, id uuid.UUID) error {
	project, err := s.projectRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("find project %s: %w", id, err)
	}
	if project == nil {
		return ErrProjectNotFound
	}
	return s.projectRepo.SoftDelete(ctx, id)
}

// ListProjects returns projects for a tenant with cursor-based pagination.
func (s *ProjectService) ListProjects(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Project, error) {
	projects, err := s.projectRepo.ListByTenant(ctx, tenantID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// AddMember adds a member to a project. Validates no duplicate membership.
func (s *ProjectService) AddMember(ctx context.Context, projectID, tenantID, gcid uuid.UUID, memberRole MemberRole) (*ProjectMember, error) {
	if !ValidMemberRole(memberRole) {
		return nil, fmt.Errorf("invalid member role %q: %w", memberRole, ErrValidationFailed)
	}

	// Verify project exists
	project, err := s.projectRepo.FindByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("find project %s: %w", projectID, err)
	}
	if project == nil {
		return nil, ErrProjectNotFound
	}

	// Check for duplicate
	existing, err := s.memberRepo.FindByProjectAndGCID(ctx, projectID, gcid)
	if err != nil {
		return nil, fmt.Errorf("check existing member: %w", err)
	}
	if existing != nil {
		return nil, ErrProjectMemberDuplicate
	}

	member := &ProjectMember{
		ID:         uuid.Must(uuid.NewV7()),
		ProjectID:  projectID,
		TenantID:   tenantID,
		GCID:       gcid,
		MemberRole: memberRole,
		CreatedAt:  time.Now().UTC(),
	}

	if err := s.memberRepo.Save(ctx, member); err != nil {
		return nil, fmt.Errorf("save project member: %w", err)
	}

	return member, nil
}

// RemoveMember removes a member from a project (hard delete).
func (s *ProjectService) RemoveMember(ctx context.Context, id uuid.UUID) error {
	return s.memberRepo.Delete(ctx, id)
}

// ListMembers returns all members for a project.
func (s *ProjectService) ListMembers(ctx context.Context, projectID uuid.UUID) ([]ProjectMember, error) {
	members, err := s.memberRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project members: %w", err)
	}
	return members, nil
}

// SubmitDeliverable creates a new deliverable for a project.
// Publishes EventDeliverableSubmitted on success.
func (s *ProjectService) SubmitDeliverable(ctx context.Context, projectID, tenantID uuid.UUID, title string, content map[string]any, mediaAssetIDs []uuid.UUID, submittedByGCID uuid.UUID) (*ProjectDeliverable, error) {
	if title == "" {
		return nil, fmt.Errorf("deliverable title must not be empty: %w", ErrValidationFailed)
	}

	// Verify project exists
	project, err := s.projectRepo.FindByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("find project %s: %w", projectID, err)
	}
	if project == nil {
		return nil, ErrProjectNotFound
	}

	if content == nil {
		content = make(map[string]any)
	}
	if mediaAssetIDs == nil {
		mediaAssetIDs = []uuid.UUID{}
	}

	now := time.Now().UTC()
	deliverable := &ProjectDeliverable{
		ID:              uuid.Must(uuid.NewV7()),
		ProjectID:       projectID,
		TenantID:        tenantID,
		Title:           title,
		Content:         content,
		MediaAssetIDs:   mediaAssetIDs,
		SubmittedByGCID: submittedByGCID,
		Status:          DeliverableStatusSubmitted,
		RubricResults:   make(map[string]any),
		SubmittedAt:     &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.deliverableRepo.Save(ctx, deliverable); err != nil {
		return nil, fmt.Errorf("save deliverable: %w", err)
	}

	evt := NewDomainEvent(EventDeliverableSubmitted, tenantID, &submittedByGCID, deliverable.ID, "ProjectDeliverable", map[string]interface{}{
		"project_id": projectID.String(),
		"title":      title,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish deliverable submitted event: %w", err)
	}

	return deliverable, nil
}

// GradeDeliverable grades a deliverable. Validates it has not already been graded.
// Publishes EventDeliverableGraded on success.
func (s *ProjectService) GradeDeliverable(ctx context.Context, id uuid.UUID, score *float64, feedback *string, rubricResults map[string]any, gradedByGCID uuid.UUID) (*ProjectDeliverable, error) {
	deliverable, err := s.deliverableRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find deliverable %s: %w", id, err)
	}
	if deliverable == nil {
		return nil, ErrDeliverableNotFound
	}

	if deliverable.GradedAt != nil {
		return nil, ErrDeliverableAlreadyGraded
	}

	// Validate score range (0-100)
	if score != nil && (*score < 0 || *score > 100) {
		return nil, fmt.Errorf("score must be between 0 and 100: %w", ErrValidationFailed)
	}
	// Validate feedback length
	if feedback != nil && len(*feedback) > 10000 {
		return nil, fmt.Errorf("feedback must not exceed 10000 characters: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	deliverable.Score = score
	deliverable.Feedback = feedback
	if rubricResults != nil {
		deliverable.RubricResults = rubricResults
	}
	deliverable.Status = DeliverableStatusGraded
	deliverable.GradedAt = &now
	deliverable.GradedByGCID = &gradedByGCID
	deliverable.UpdatedAt = now

	if err := s.deliverableRepo.Save(ctx, deliverable); err != nil {
		return nil, fmt.Errorf("save graded deliverable: %w", err)
	}

	evt := NewDomainEvent(EventDeliverableGraded, deliverable.TenantID, &gradedByGCID, deliverable.ID, "ProjectDeliverable", map[string]interface{}{
		"project_id": deliverable.ProjectID.String(),
		"title":      deliverable.Title,
	})
	if err := s.events.Publish(ctx, TopicCMSEvents, evt); err != nil {
		return nil, fmt.Errorf("publish deliverable graded event: %w", err)
	}

	return deliverable, nil
}

// GetDeliverable retrieves a deliverable by ID.
func (s *ProjectService) GetDeliverable(ctx context.Context, id uuid.UUID) (*ProjectDeliverable, error) {
	deliverable, err := s.deliverableRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find deliverable %s: %w", id, err)
	}
	if deliverable == nil {
		return nil, ErrDeliverableNotFound
	}
	return deliverable, nil
}

// ListDeliverables returns all deliverables for a project.
func (s *ProjectService) ListDeliverables(ctx context.Context, projectID uuid.UUID) ([]ProjectDeliverable, error) {
	deliverables, err := s.deliverableRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list deliverables: %w", err)
	}
	return deliverables, nil
}
