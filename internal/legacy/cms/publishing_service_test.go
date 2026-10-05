package cms

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPublishingService_SubmitForReview(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("transitions draft to submitted", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusDraft, VisibilityStatus: VisibilityStatusDraft}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.SubmitForReview(context.Background(), itemID, gcid)

		require.NoError(t, err)
		assert.Equal(t, WorkflowStatusSubmitted, got.WorkflowStatus)
		assert.Equal(t, VisibilityStatusInReview, got.VisibilityStatus)
		itemRepo.AssertExpectations(t)
	})

	t.Run("rejects invalid transition from published", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusPublished}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.SubmitForReview(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkflowInvalidTransition)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.SubmitForReview(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}

func TestPublishingService_Approve(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("transitions peer_review to approved", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusPeerReview}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.Approve(context.Background(), itemID, gcid)

		require.NoError(t, err)
		assert.Equal(t, WorkflowStatusApproved, got.WorkflowStatus)
	})

	t.Run("rejects invalid transition from draft", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusDraft}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Approve(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkflowInvalidTransition)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Approve(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}

func TestPublishingService_Reject(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("transitions ai_review to rejected", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusAIReview, VisibilityStatus: VisibilityStatusInReview}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.Reject(context.Background(), itemID, gcid, "Quality issues")

		require.NoError(t, err)
		assert.Equal(t, WorkflowStatusRejected, got.WorkflowStatus)
		assert.Equal(t, VisibilityStatusDraft, got.VisibilityStatus)
	})

	t.Run("transitions peer_review to rejected", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusPeerReview}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		got, err := svc.Reject(context.Background(), itemID, gcid, "Needs revision")

		require.NoError(t, err)
		assert.Equal(t, WorkflowStatusRejected, got.WorkflowStatus)
	})

	t.Run("rejects invalid transition from draft", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusDraft}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Reject(context.Background(), itemID, gcid, "reason")

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkflowInvalidTransition)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Reject(context.Background(), itemID, gcid, "reason")

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}

func TestPublishingService_Publish(t *testing.T) {
	t.Parallel()

	itemID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	versionID := uuid.Must(uuid.NewV7())

	t.Run("transitions approved to published and publishes event", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{
			ID:               itemID,
			TenantID:         tenantID,
			Title:            "My Article",
			WorkflowStatus:   WorkflowStatusApproved,
			CurrentVersionID: &versionID,
		}
		currentVersion := &ContentVersion{
			ID:            versionID,
			ItemID:        itemID,
			TenantID:      tenantID,
			VersionNumber: 2,
			Content:       map[string]any{"body": "content"},
		}

		itemRepo := new(mockContentItemRepo)
		versionRepo := new(mockContentVersionRepo)
		publisher := new(mockEventPublisher)

		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)
		versionRepo.On("FindLatestPublished", mock.Anything, itemID).Return(nil, nil)
		versionRepo.On("FindByID", mock.Anything, versionID).Return(currentVersion, nil)
		versionRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentVersion")).Return(nil)
		itemRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ContentItem")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewPublishingService(itemRepo, versionRepo, publisher)
		got, err := svc.Publish(context.Background(), itemID, gcid)

		require.NoError(t, err)
		assert.Equal(t, WorkflowStatusPublished, got.WorkflowStatus)
		assert.Equal(t, VisibilityStatusPublished, got.VisibilityStatus)

		// Verify event matches AsyncAPI contract
		assert.Equal(t, EventContentPublished, capturedEvent.EventType)
		assert.Equal(t, "ContentItem", capturedEvent.AggregateType)
		assert.Equal(t, "My Article", capturedEvent.Payload["title"])
		assert.Equal(t, "published", capturedEvent.Payload["workflow_status"])

		publisher.AssertExpectations(t)
	})

	t.Run("rejects invalid transition from draft", func(t *testing.T) {
		t.Parallel()

		item := &ContentItem{ID: itemID, WorkflowStatus: WorkflowStatusDraft}
		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(item, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Publish(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkflowInvalidTransition)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		itemRepo := new(mockContentItemRepo)
		itemRepo.On("FindByID", mock.Anything, itemID).Return(nil, nil)

		svc := NewPublishingService(itemRepo, new(mockContentVersionRepo), new(mockEventPublisher))
		_, err := svc.Publish(context.Background(), itemID, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContentItemNotFound)
	})
}
