package cms

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ ContentItemRepository        = (*mockContentItemRepo)(nil)
	_ ContentVersionRepository     = (*mockContentVersionRepo)(nil)
	_ MediaAssetRepository         = (*mockMediaAssetRepo)(nil)
	_ ProjectRepository            = (*mockProjectRepo)(nil)
	_ ProjectMemberRepository      = (*mockProjectMemberRepo)(nil)
	_ ProjectDeliverableRepository = (*mockProjectDeliverableRepo)(nil)
	_ EventPublisher               = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockContentItemRepo
// ---------------------------------------------------------------------------

type mockContentItemRepo struct{ mock.Mock }

func (m *mockContentItemRepo) Save(ctx context.Context, item *ContentItem) error {
	args := m.Called(ctx, item)
	return args.Error(0)
}

func (m *mockContentItemRepo) FindByID(ctx context.Context, id uuid.UUID) (*ContentItem, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ContentItem), args.Error(1)
}

func (m *mockContentItemRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentItem, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ContentItem), args.Error(1)
}

func (m *mockContentItemRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// mockContentVersionRepo
// ---------------------------------------------------------------------------

type mockContentVersionRepo struct{ mock.Mock }

func (m *mockContentVersionRepo) Save(ctx context.Context, version *ContentVersion) error {
	args := m.Called(ctx, version)
	return args.Error(0)
}

func (m *mockContentVersionRepo) FindByID(ctx context.Context, id uuid.UUID) (*ContentVersion, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ContentVersion), args.Error(1)
}

func (m *mockContentVersionRepo) ListByItem(ctx context.Context, itemID uuid.UUID, cursor *uuid.UUID, limit int) ([]ContentVersion, error) {
	args := m.Called(ctx, itemID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ContentVersion), args.Error(1)
}

func (m *mockContentVersionRepo) FindLatestPublished(ctx context.Context, itemID uuid.UUID) (*ContentVersion, error) {
	args := m.Called(ctx, itemID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ContentVersion), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockMediaAssetRepo
// ---------------------------------------------------------------------------

type mockMediaAssetRepo struct{ mock.Mock }

func (m *mockMediaAssetRepo) Save(ctx context.Context, asset *MediaAsset) error {
	args := m.Called(ctx, asset)
	return args.Error(0)
}

func (m *mockMediaAssetRepo) FindByID(ctx context.Context, id uuid.UUID) (*MediaAsset, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MediaAsset), args.Error(1)
}

func (m *mockMediaAssetRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]MediaAsset, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]MediaAsset), args.Error(1)
}

func (m *mockMediaAssetRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// mockProjectRepo
// ---------------------------------------------------------------------------

type mockProjectRepo struct{ mock.Mock }

func (m *mockProjectRepo) Save(ctx context.Context, project *Project) error {
	args := m.Called(ctx, project)
	return args.Error(0)
}

func (m *mockProjectRepo) FindByID(ctx context.Context, id uuid.UUID) (*Project, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Project), args.Error(1)
}

func (m *mockProjectRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Project, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Project), args.Error(1)
}

func (m *mockProjectRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// mockProjectMemberRepo
// ---------------------------------------------------------------------------

type mockProjectMemberRepo struct{ mock.Mock }

func (m *mockProjectMemberRepo) Save(ctx context.Context, member *ProjectMember) error {
	args := m.Called(ctx, member)
	return args.Error(0)
}

func (m *mockProjectMemberRepo) FindByProjectAndGCID(ctx context.Context, projectID, gcid uuid.UUID) (*ProjectMember, error) {
	args := m.Called(ctx, projectID, gcid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ProjectMember), args.Error(1)
}

func (m *mockProjectMemberRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]ProjectMember, error) {
	args := m.Called(ctx, projectID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ProjectMember), args.Error(1)
}

func (m *mockProjectMemberRepo) Delete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// mockProjectDeliverableRepo
// ---------------------------------------------------------------------------

type mockProjectDeliverableRepo struct{ mock.Mock }

func (m *mockProjectDeliverableRepo) Save(ctx context.Context, deliverable *ProjectDeliverable) error {
	args := m.Called(ctx, deliverable)
	return args.Error(0)
}

func (m *mockProjectDeliverableRepo) FindByID(ctx context.Context, id uuid.UUID) (*ProjectDeliverable, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ProjectDeliverable), args.Error(1)
}

func (m *mockProjectDeliverableRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]ProjectDeliverable, error) {
	args := m.Called(ctx, projectID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ProjectDeliverable), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockChorapediaEntryRepo
// ---------------------------------------------------------------------------

type mockChorapediaEntryRepo struct{ mock.Mock }

var _ ChorapediaEntryRepository = (*mockChorapediaEntryRepo)(nil)

func (m *mockChorapediaEntryRepo) Save(ctx context.Context, entry *ChorapediaEntry) error {
	args := m.Called(ctx, entry)
	return args.Error(0)
}

func (m *mockChorapediaEntryRepo) FindByID(ctx context.Context, id uuid.UUID) (*ChorapediaEntry, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ChorapediaEntry), args.Error(1)
}

func (m *mockChorapediaEntryRepo) FindBySlug(ctx context.Context, tenantID uuid.UUID, slug string) (*ChorapediaEntry, error) {
	args := m.Called(ctx, tenantID, slug)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ChorapediaEntry), args.Error(1)
}

func (m *mockChorapediaEntryRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID, category *ChorapediaCategory, isPublished *bool, cursor *uuid.UUID, limit int) ([]ChorapediaEntry, error) {
	args := m.Called(ctx, tenantID, category, isPublished, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ChorapediaEntry), args.Error(1)
}

func (m *mockChorapediaEntryRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// mockSearchPort
// ---------------------------------------------------------------------------

type mockSearchPort struct{ mock.Mock }

var _ SearchPort = (*mockSearchPort)(nil)

func (m *mockSearchPort) Search(ctx context.Context, tenantID uuid.UUID, query string, limit int) ([]ChorapediaEntry, error) {
	args := m.Called(ctx, tenantID, query, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ChorapediaEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	args := m.Called(ctx, topic, event)
	return args.Error(0)
}
