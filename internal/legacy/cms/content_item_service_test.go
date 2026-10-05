package cms

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestContentItemService_CreateItem(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("creates item with defaults and publishes event", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		versionRepo := new(mockContentVersionRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewContentItemService(itemRepo, versionRepo, publisher)
		item, err := svc.CreateItem(context.Background(), tenantID, "Test Item", ContentTypeRichText, nil, gcid, nil)

		require.NoError(t, err)
		assert.Equal(t, "Test Item", item.Title)
		assert.Equal(t, ContentTypeRichText, item.ContentType)
		assert.Equal(t, VisibilityStatusDraft, item.VisibilityStatus)
		assert.Equal(t, WorkflowStatusDraft, item.WorkflowStatus)
		assert.Equal(t, tenantID, item.TenantID)
		assert.Equal(t, gcid, item.CreatedByGCID)
		assert.NotNil(t, item.Metadata)

		// Verify event
		assert.Equal(t, EventContentItemCreated, capturedEvent.EventType)
		assert.Equal(t, "ContentItem", capturedEvent.AggregateType)
		assert.Equal(t, tenantID, capturedEvent.TenantID)
		assert.Equal(t, "Test Item", capturedEvent.Payload["title"])
		assert.Equal(t, "rich_text", capturedEvent.Payload["content_type"])

		itemRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("rejects empty title", func(t *testing.T) {
		t.Parallel()

		svc := NewContentItemService(new(mockContentItemRepo), new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.CreateItem(context.Background(), tenantID, "", ContentTypeRichText, nil, gcid, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects invalid content type", func(t *testing.T) {
		t.Parallel()

		svc := NewContentItemService(new(mockContentItemRepo), new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.CreateItem(context.Background(), tenantID, "Title", ContentType("invalid"), nil, gcid, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}

func TestContentItemService_GetItem(t *testing.T) {
	t.Parallel()

	t.Run("returns item when found", func(t *testing.T) {
		t.Parallel()

		itemID := uuid.Must(uuid.NewV7())
		item := &ContentItem{ID: itemID, Title: "Found"}

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.GetItem(context.Background(), itemID)

		require.NoError(t, err)
		assert.Equal(t, "Found", got.Title)
		itemRepo.AssertExpectations(t)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemID := uuid.Must(uuid.NewV7())
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.GetItem(context.Background(), itemID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}

func TestContentItemService_UpdateItem(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("updates title and publishes event", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, TenantID: tenantID, Title: "Old", CreatedByGCID: gcid}
		itemRepo := new(mockContentItemRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), publisher)
		newTitle := "New Title"
		got, err := svc.UpdateItem(context.Background(), itemID, &newTitle, nil)

		require.NoError(t, err)
		assert.Equal(t, "New Title", got.Title)
		assert.Equal(t, EventContentItemUpdated, capturedEvent.EventType)
		assert.Equal(t, "ContentItem", capturedEvent.AggregateType)
		assert.Equal(t, "New Title", capturedEvent.Payload["title"])

		itemRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.UpdateItem(context.Background(), itemID, nil, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})

	t.Run("rejects empty title", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, Title: "Existing"}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		emptyTitle := ""
		_, err := svc.UpdateItem(context.Background(), itemID, &emptyTitle, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}

func TestContentItemService_SoftDeleteItem(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("soft deletes and publishes event", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, TenantID: tenantID, Title: "ToDelete", CreatedByGCID: gcid}
		itemRepo := new(mockContentItemRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("SoftDelete", mock.Anything, itemID).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), publisher)
		err := svc.SoftDeleteItem(context.Background(), itemID)

		require.NoError(t, err)
		assert.Equal(t, EventContentItemDeleted, capturedEvent.EventType)
		assert.Equal(t, "ContentItem", capturedEvent.AggregateType)
		assert.Equal(t, "ToDelete", capturedEvent.Payload["title"])

		itemRepo.AssertCalled(t, "SoftDelete", mock.Anything, itemID)
		publisher.AssertExpectations(t)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		err := svc.SoftDeleteItem(context.Background(), itemID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})

	t.Run("returns error on find failure", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, assert.AnError)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		err := svc.SoftDeleteItem(context.Background(), itemID)

		require.Error(t, err)
	})

	t.Run("returns error on soft delete failure", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, TenantID: tenantID, Title: "ToDelete", CreatedByGCID: gcid}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("SoftDelete", mock.Anything, itemID).Return(assert.AnError)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		err := svc.SoftDeleteItem(context.Background(), itemID)

		require.Error(t, err)
	})
}

func TestContentItemService_ListItems(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns items for tenant", func(t *testing.T) {
		t.Parallel()

		items := []ContentItem{{Title: "A"}, {Title: "B"}}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(items, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.ListItems(context.Background(), tenantID, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 2)
		itemRepo.AssertExpectations(t)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(nil, assert.AnError)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.ListItems(context.Background(), tenantID, nil, 10)

		require.Error(t, err)
	})
}

func TestContentItemService_CreateVersion(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("creates first version and publishes event", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, TenantID: tenantID}
		itemRepo := new(mockContentItemRepo)
		versionRepo := new(mockContentVersionRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		versionRepo.On("ListByItem", mock.Anything, itemID, (*uuid.UUID)(nil), 1).Return([]ContentVersion{}, nil)
		versionRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentVersion")).Return(nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewContentItemService(itemRepo, versionRepo, publisher)
		content := map[string]any{"body": "hello"}
		version, err := svc.CreateVersion(context.Background(), itemID, tenantID, content, nil, gcid)

		require.NoError(t, err)
		assert.Equal(t, 1, version.VersionNumber)
		assert.Equal(t, itemID, version.ItemID)
		assert.Equal(t, VisibilityStatusDraft, version.VisibilityStatus)
		assert.NotNil(t, item.CurrentVersionID)

		// Verify event
		assert.Equal(t, EventContentVersionCreated, capturedEvent.EventType)
		assert.Equal(t, "ContentVersion", capturedEvent.AggregateType)
		assert.Equal(t, itemID.String(), capturedEvent.Payload["item_id"])
		assert.Equal(t, 1, capturedEvent.Payload["version_number"])

		versionRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("increments version number", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, TenantID: tenantID}
		itemRepo := new(mockContentItemRepo)
		versionRepo := new(mockContentVersionRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		versionRepo.On("ListByItem", mock.Anything, itemID, (*uuid.UUID)(nil), 1).Return([]ContentVersion{{VersionNumber: 3}}, nil)
		versionRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentVersion")).Return(nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).Return(nil)

		svc := NewContentItemService(itemRepo, versionRepo, publisher)
		version, err := svc.CreateVersion(context.Background(), itemID, tenantID, nil, nil, gcid)

		require.NoError(t, err)
		assert.Equal(t, 4, version.VersionNumber)
	})

	t.Run("returns not found when item missing", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewContentItemService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.CreateVersion(context.Background(), itemID, tenantID, nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}

func TestContentItemService_GetVersion(t *testing.T) {
	t.Parallel()

	versionID := uuid.Must(uuid.NewV7())

	t.Run("returns version when found", func(t *testing.T) {
		t.Parallel()

		version := &ContentVersion{ID: versionID, VersionNumber: 1}
		versionRepo := new(mockContentVersionRepo)
		versionRepo.On("FindByID", mock.Anything, versionID).Return(version, nil)

		svc := NewContentItemService(new(mockContentItemRepo), versionRepo, new(mockEventPublisher))
		got, err := svc.GetVersion(context.Background(), versionID)

		require.NoError(t, err)
		assert.Equal(t, 1, got.VersionNumber)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		versionRepo := new(mockContentVersionRepo)
		versionRepo.On("FindByID", mock.Anything, versionID).Return(nil, nil)

		svc := NewContentItemService(new(mockContentItemRepo), versionRepo, new(mockEventPublisher))
		_, err := svc.GetVersion(context.Background(), versionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentVersionNotFound)
	})
}

func TestContentItemService_ListVersions(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())

	t.Run("returns versions for item", func(t *testing.T) {
		t.Parallel()

		versions := []ContentVersion{{VersionNumber: 1}, {VersionNumber: 2}}
		versionRepo := new(mockContentVersionRepo)
		versionRepo.On("ListByItem", mock.Anything, itemID, (*uuid.UUID)(nil), 10).Return(versions, nil)

		svc := NewContentItemService(new(mockContentItemRepo), versionRepo, new(mockEventPublisher))
		got, err := svc.ListVersions(context.Background(), itemID, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		versionRepo := new(mockContentVersionRepo)
		versionRepo.On("ListByItem", mock.Anything, itemID, (*uuid.UUID)(nil), 10).Return(nil, assert.AnError)

		svc := NewContentItemService(new(mockContentItemRepo), versionRepo, new(mockEventPublisher))
		_, err := svc.ListVersions(context.Background(), itemID, nil, 10)

		require.Error(t, err)
	})
}
