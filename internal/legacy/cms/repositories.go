package cms

import (
	"context"

	"github.com/google/uuid"
)

// ContentItemRepository defines the data access interface for ContentItem entities.
type ContentItemRepository interface {
	// Save creates or updates a ContentItem.
	Save(ctx context.Context, item *ContentItem) error

	// FindByID retrieves a content item by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*ContentItem, error)

	// ListByTenant returns content items with cursor-based pagination.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentItem, error)

	// SoftDelete marks a content item as deleted.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// ContentVersionRepository defines the data access interface for ContentVersion entities.
type ContentVersionRepository interface {
	// Save creates a ContentVersion (append-only).
	Save(ctx context.Context, version *ContentVersion) error

	// FindByID retrieves a content version by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*ContentVersion, error)

	// ListByItem returns content versions for an item with cursor-based pagination.
	ListByItem(ctx context.Context, itemID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentVersion, error)

	// FindLatestPublished retrieves the latest published version for an item.
	// Returns nil if not found.
	FindLatestPublished(ctx context.Context, itemID uuid.UUID) (*ContentVersion, error)
}

// MediaAssetRepository defines the data access interface for MediaAsset entities.
type MediaAssetRepository interface {
	// Save creates or updates a MediaAsset.
	Save(ctx context.Context, asset *MediaAsset) error

	// FindByID retrieves a media asset by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*MediaAsset, error)

	// ListByTenant returns media assets with cursor-based pagination.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]MediaAsset, error)

	// SoftDelete marks a media asset as deleted.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// ProjectRepository defines the data access interface for Project entities.
type ProjectRepository interface {
	// Save creates or updates a Project.
	Save(ctx context.Context, project *Project) error

	// FindByID retrieves a project by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*Project, error)

	// ListByTenant returns projects with cursor-based pagination.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Project, error)

	// SoftDelete marks a project as deleted.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// ProjectMemberRepository defines the data access interface for ProjectMember entities.
type ProjectMemberRepository interface {
	// Save creates a ProjectMember.
	Save(ctx context.Context, member *ProjectMember) error

	// FindByProjectAndGCID retrieves a member by project ID and GCID.
	// Returns nil if not found.
	FindByProjectAndGCID(ctx context.Context, projectID, gcid uuid.UUID) (*ProjectMember, error)

	// ListByProject returns all members for a project.
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]ProjectMember, error)

	// Delete hard-deletes a project member by ID.
	Delete(ctx context.Context, id uuid.UUID) error
}

// ProjectDeliverableRepository defines the data access interface for ProjectDeliverable entities.
type ProjectDeliverableRepository interface {
	// Save creates or updates a ProjectDeliverable.
	Save(ctx context.Context, deliverable *ProjectDeliverable) error

	// FindByID retrieves a deliverable by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*ProjectDeliverable, error)

	// ListByProject returns all deliverables for a project.
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]ProjectDeliverable, error)
}

// MediaVariantRepository defines data access for MediaVariant entities.
type MediaVariantRepository interface {
	// Save creates a MediaVariant.
	Save(ctx context.Context, variant *MediaVariant) error

	// FindByMediaAssetID returns all variants for a media asset.
	FindByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) ([]MediaVariant, error)

	// DeleteByMediaAssetID hard-deletes all variants for a media asset.
	// Variants are recreated on re-processing.
	DeleteByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) error
}

// MediaProcessingJobRepository defines data access for MediaProcessingJob entities.
type MediaProcessingJobRepository interface {
	// Save creates or updates a MediaProcessingJob.
	Save(ctx context.Context, job *MediaProcessingJob) error

	// FindByID retrieves a processing job by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*MediaProcessingJob, error)

	// FindLatestByMediaAssetID retrieves the most recent job for a media asset.
	// Returns nil if not found.
	FindLatestByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) (*MediaProcessingJob, error)
}

// ContentScanResultRepository defines data access for ContentScanResult entities (append-only).
type ContentScanResultRepository interface {
	// Save creates a ContentScanResult (append-only).
	Save(ctx context.Context, result *ContentScanResult) error

	// ListByMediaAssetID returns all scan results for a media asset.
	ListByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) ([]ContentScanResult, error)
}

// ChorapediaEntryRepository defines the data access interface for ChorapediaEntry entities.
type ChorapediaEntryRepository interface {
	// Save creates or updates a ChorapediaEntry.
	Save(ctx context.Context, entry *ChorapediaEntry) error

	// FindByID retrieves a chorapedia entry by ID. Returns nil if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*ChorapediaEntry, error)

	// FindBySlug retrieves a chorapedia entry by tenant and slug. Returns nil if not found.
	FindBySlug(ctx context.Context, tenantID uuid.UUID, slug string) (*ChorapediaEntry, error)

	// ListByTenant returns chorapedia entries with optional filters and cursor-based pagination.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, category *ChorapediaCategory, isPublished *bool, cursor *uuid.UUID, limit int) ([]ChorapediaEntry, error)

	// SoftDelete marks a chorapedia entry as deleted.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// SearchPort defines the interface for full-text search on Chorapedia entries.
// Adapter will use Meilisearch in production.
type SearchPort interface {
	// Search performs a full-text search across Chorapedia entries for a tenant.
	Search(ctx context.Context, tenantID uuid.UUID, query string, limit int) ([]ChorapediaEntry, error)
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error
}
