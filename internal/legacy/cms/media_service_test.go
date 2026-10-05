package cms

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestMediaService_UploadAsset(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("creates asset and publishes event", func(t *testing.T) {
		t.Parallel()

		repo := new(mockMediaAssetRepo)
		publisher := new(mockEventPublisher)

		repo.On("Save", mock.Anything, mock.AnythingOfType("*cms.MediaAsset")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewMediaService(repo, publisher)
		asset, err := svc.UploadAsset(context.Background(), tenantID, "uploads/abc.png", "test.png", "image/png", 1024, "Alt", "Desc", gcid, StorageCategoryCMSAuthored)

		require.NoError(t, err)
		assert.Equal(t, "test.png", asset.FileName)
		assert.Equal(t, "image/png", asset.MimeType)
		assert.Equal(t, int64(1024), asset.FileSize)
		assert.Equal(t, tenantID, asset.TenantID)

		// Verify event payload matches AsyncAPI contract
		assert.Equal(t, EventMediaAssetUploaded, capturedEvent.EventType)
		assert.Equal(t, "MediaAsset", capturedEvent.AggregateType)
		assert.Equal(t, "test.png", capturedEvent.Payload["file_name"])
		assert.Equal(t, "image/png", capturedEvent.Payload["mime_type"])
		assert.Equal(t, int64(1024), capturedEvent.Payload["file_size"])

		repo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("rejects empty storage key", func(t *testing.T) {
		t.Parallel()

		svc := NewMediaService(new(mockMediaAssetRepo), new(mockEventPublisher))
		_, err := svc.UploadAsset(context.Background(), tenantID, "", "test.png", "image/png", 1024, "", "", gcid, StorageCategoryCMSAuthored)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects empty file name", func(t *testing.T) {
		t.Parallel()

		svc := NewMediaService(new(mockMediaAssetRepo), new(mockEventPublisher))
		_, err := svc.UploadAsset(context.Background(), tenantID, "key", "", "image/png", 1024, "", "", gcid, StorageCategoryCMSAuthored)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects file exceeding 50MB", func(t *testing.T) {
		t.Parallel()

		svc := NewMediaService(new(mockMediaAssetRepo), new(mockEventPublisher))
		_, err := svc.UploadAsset(context.Background(), tenantID, "key", "big.mp4", "video/mp4", 51*1024*1024, "", "", gcid, StorageCategoryCMSAuthored)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMediaSizeExceeded)
	})

	t.Run("rejects negative file size", func(t *testing.T) {
		t.Parallel()

		svc := NewMediaService(new(mockMediaAssetRepo), new(mockEventPublisher))
		_, err := svc.UploadAsset(context.Background(), tenantID, "key", "file.png", "image/png", -1, "", "", gcid, StorageCategoryCMSAuthored)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects disallowed MIME type", func(t *testing.T) {
		t.Parallel()

		svc := NewMediaService(new(mockMediaAssetRepo), new(mockEventPublisher))
		_, err := svc.UploadAsset(context.Background(), tenantID, "key", "file.exe", "application/x-executable", 1024, "", "", gcid, StorageCategoryCMSAuthored)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMediaTypeNotAllowed)
	})
}

func TestMediaService_GetAsset(t *testing.T) {
	t.Parallel()

	assetID := uuid.Must(uuid.NewV7())

	t.Run("returns asset when found", func(t *testing.T) {
		t.Parallel()

		asset := &MediaAsset{ID: assetID, FileName: "test.png"}
		repo := new(mockMediaAssetRepo)
		repo.On("FindByID", mock.Anything, assetID).Return(asset, nil)

		svc := NewMediaService(repo, new(mockEventPublisher))
		got, err := svc.GetAsset(context.Background(), assetID)

		require.NoError(t, err)
		assert.Equal(t, "test.png", got.FileName)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		repo := new(mockMediaAssetRepo)
		repo.On("FindByID", mock.Anything, assetID).Return(nil, nil)

		svc := NewMediaService(repo, new(mockEventPublisher))
		_, err := svc.GetAsset(context.Background(), assetID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMediaAssetNotFound)
	})
}

func TestMediaService_ListAssets(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns assets for tenant", func(t *testing.T) {
		t.Parallel()

		assets := []MediaAsset{{FileName: "a.png"}, {FileName: "b.png"}}
		repo := new(mockMediaAssetRepo)
		repo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(assets, nil)

		svc := NewMediaService(repo, new(mockEventPublisher))
		got, err := svc.ListAssets(context.Background(), tenantID, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		repo := new(mockMediaAssetRepo)
		repo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(nil, assert.AnError)

		svc := NewMediaService(repo, new(mockEventPublisher))
		_, err := svc.ListAssets(context.Background(), tenantID, nil, 10)

		require.Error(t, err)
	})
}

func TestMediaService_SoftDeleteAsset(t *testing.T) {
	t.Parallel()

	assetID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("soft deletes and publishes event", func(t *testing.T) {
		t.Parallel()

		asset := &MediaAsset{ID: assetID, TenantID: tenantID, FileName: "deleted.png", UploadedByGCID: gcid}
		repo := new(mockMediaAssetRepo)
		publisher := new(mockEventPublisher)

		repo.On("FindByID", mock.Anything, assetID).Return(asset, nil)
		repo.On("SoftDelete", mock.Anything, assetID).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewMediaService(repo, publisher)
		err := svc.SoftDeleteAsset(context.Background(), assetID)

		require.NoError(t, err)
		assert.Equal(t, EventMediaAssetDeleted, capturedEvent.EventType)
		assert.Equal(t, "MediaAsset", capturedEvent.AggregateType)
		assert.Equal(t, "deleted.png", capturedEvent.Payload["file_name"])

		repo.AssertCalled(t, "SoftDelete", mock.Anything, assetID)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		repo := new(mockMediaAssetRepo)
		repo.On("FindByID", mock.Anything, assetID).Return(nil, nil)

		svc := NewMediaService(repo, new(mockEventPublisher))
		err := svc.SoftDeleteAsset(context.Background(), assetID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMediaAssetNotFound)
	})

	t.Run("returns error on find failure", func(t *testing.T) {
		t.Parallel()

		repo := new(mockMediaAssetRepo)
		repo.On("FindByID", mock.Anything, assetID).Return(nil, assert.AnError)

		svc := NewMediaService(repo, new(mockEventPublisher))
		err := svc.SoftDeleteAsset(context.Background(), assetID)

		require.Error(t, err)
	})

	t.Run("returns error on soft delete failure", func(t *testing.T) {
		t.Parallel()

		asset := &MediaAsset{ID: assetID, TenantID: tenantID, FileName: "deleted.png", UploadedByGCID: gcid}
		repo := new(mockMediaAssetRepo)
		repo.On("FindByID", mock.Anything, assetID).Return(asset, nil)
		repo.On("SoftDelete", mock.Anything, assetID).Return(assert.AnError)

		svc := NewMediaService(repo, new(mockEventPublisher))
		err := svc.SoftDeleteAsset(context.Background(), assetID)

		require.Error(t, err)
	})
}
