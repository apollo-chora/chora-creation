package atomic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestTopicService_CreateTopic(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		topic       *TopicNode
		setupRepo   func(*mockTopicRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name: "creates root topic successfully",
			topic: &TopicNode{
				TenantID: tenantID,
				Name:     "Mathematics",
			},
			setupRepo: func(r *mockTopicRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "creates child topic with parent",
			topic: &TopicNode{
				TenantID: tenantID,
				Name:     "Algebra",
				ParentID: &parentID,
			},
			setupRepo: func(r *mockTopicRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "propagates repository error",
			topic: &TopicNode{
				TenantID: tenantID,
				Name:     "Science",
			},
			setupRepo: func(r *mockTopicRepo) {
				r.On("Save", mock.Anything, mock.Anything).Return(errors.New("db error"))
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     errors.New("db error"),
		},
		{
			name: "rejects empty name",
			topic: &TopicNode{
				TenantID: tenantID,
				Name:     "",
			},
			setupRepo:   func(r *mockTopicRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrTopicNameRequired,
		},
		{
			name: "rejects name exceeding 255 characters",
			topic: &TopicNode{
				TenantID: tenantID,
				Name:     strings.Repeat("a", 256),
			},
			setupRepo:   func(r *mockTopicRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrTopicNameTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(topicRepo)
			tt.setupEvents(publisher)

			svc := NewTopicService(topicRepo, publisher)
			result, err := svc.CreateTopic(context.Background(), tt.topic)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, result.ID, "ID should be a non-nil UUIDv7")
			assert.False(t, result.CreatedAt.IsZero(), "created_at should be set")
			assert.False(t, result.UpdatedAt.IsZero(), "updated_at should be set")
			topicRepo.AssertExpectations(t)
		})
	}
}

func TestTopicService_CreateTopic_PublishesEvent(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())

	topicRepo := new(mockTopicRepo)
	topicRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewTopicService(topicRepo, publisher)
	_, err := svc.CreateTopic(context.Background(), &TopicNode{
		TenantID: tenantID,
		Name:     "Algebra",
		ParentID: &parentID,
	})

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	// Contract: topic must be "chora.creation.atom.events.v1" (post-M12.3.E)
	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeTopicNodeCreated, event.EventType)
	assert.Equal(t, AggregateTypeTopicNode, event.AggregateType)
	// Contract requires these payload fields per atomic-events.yaml
	assert.Contains(t, event.Payload, "topic_id", "missing topic_id in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
	assert.Contains(t, event.Payload, "name", "name should be in payload")
	assert.Contains(t, event.Payload, "parent_id", "missing parent_id in payload")
}

func TestTopicService_GetTopicTree(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		setupRepo func(*mockTopicRepo)
		wantCount int
		wantErr   error
	}{
		{
			name: "returns topic tree for tenant",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetTree", mock.Anything, tenantID).Return([]*TopicNode{
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "Math"},
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "Science"},
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "Algebra"},
				}, nil)
			},
			wantCount: 3,
		},
		{
			name: "returns empty tree",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetTree", mock.Anything, tenantID).Return([]*TopicNode{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "propagates repository error",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetTree", mock.Anything, tenantID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			tt.setupRepo(topicRepo)

			svc := NewTopicService(topicRepo, new(mockEventPublisher))
			tree, err := svc.GetTopicTree(context.Background(), tenantID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Len(t, tree, tt.wantCount)
			topicRepo.AssertExpectations(t)
		})
	}
}

func TestTopicService_MoveTopic(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	newParentID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		id          uuid.UUID
		newParentID *uuid.UUID
		setupRepo   func(*mockTopicRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:        "moves topic to new parent",
			id:          topicID,
			newParentID: &newParentID,
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
					Name:     "Algebra",
				}, nil)
				r.On("Move", mock.Anything, topicID, &newParentID).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:        "moves topic to root (nil parent)",
			id:          topicID,
			newParentID: nil,
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
					Name:     "Algebra",
				}, nil)
				r.On("Move", mock.Anything, topicID, (*uuid.UUID)(nil)).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:        "rejects self-referencing move",
			id:          topicID,
			newParentID: &topicID,
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrTopicNodeCircularRef,
		},
		{
			name:        "propagates repository error",
			id:          topicID,
			newParentID: &newParentID,
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(nil, errors.New("db error"))
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(topicRepo)
			tt.setupEvents(publisher)

			svc := NewTopicService(topicRepo, publisher)
			err := svc.MoveTopic(context.Background(), tt.id, tt.newParentID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			topicRepo.AssertExpectations(t)
		})
	}
}

func TestTopicService_MoveTopic_PublishesEvent(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	oldParentID := uuid.Must(uuid.NewV7())
	newParentID := uuid.Must(uuid.NewV7())

	topicRepo := new(mockTopicRepo)
	topicRepo.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
		ID:       topicID,
		TenantID: tenantID,
		Name:     "Algebra",
		ParentID: &oldParentID,
	}, nil)
	topicRepo.On("Move", mock.Anything, topicID, &newParentID).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewTopicService(topicRepo, publisher)
	err := svc.MoveTopic(context.Background(), topicID, &newParentID)

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topicStr := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	// Contract: topic must be "chora.creation.atom.events.v1" (post-M12.3.E)
	assert.Equal(t, TopicAtomicEvents, topicStr, "wrong topic name")
	assert.Equal(t, EventTypeTopicNodeMoved, event.EventType)
	// Contract requires these payload fields per atomic-events.yaml
	assert.Contains(t, event.Payload, "topic_id", "missing topic_id in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
	assert.Contains(t, event.Payload, "old_parent_id", "missing old_parent_id in payload")
	assert.Contains(t, event.Payload, "new_parent_id", "new_parent_id should be in payload")
}

func TestTopicService_GetTopic(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		setupRepo func(*mockTopicRepo)
		wantErr   error
	}{
		{
			name: "returns topic when found",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
					Name:     "Mathematics",
				}, nil)
			},
		},
		{
			name: "returns error when not found",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(nil, ErrTopicNodeNotFound)
			},
			wantErr: ErrTopicNodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			tt.setupRepo(topicRepo)

			svc := NewTopicService(topicRepo, new(mockEventPublisher))
			topic, err := svc.GetTopic(context.Background(), topicID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, topicID, topic.ID)
			topicRepo.AssertExpectations(t)
		})
	}
}

func TestTopicService_UpdateTopic(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		topic     *TopicNode
		setupRepo func(*mockTopicRepo)
		wantErr   error
	}{
		{
			name: "updates topic name",
			topic: &TopicNode{
				ID:       topicID,
				TenantID: tenantID,
				Name:     "Advanced Mathematics",
			},
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
					Name:     "Mathematics",
				}, nil)
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(nil)
			},
		},
		{
			name: "rejects empty name",
			topic: &TopicNode{
				ID:       topicID,
				TenantID: tenantID,
				Name:     "",
			},
			setupRepo: func(r *mockTopicRepo) {},
			wantErr:   ErrTopicNameRequired,
		},
		{
			name: "rejects name exceeding 255 characters",
			topic: &TopicNode{
				ID:       topicID,
				TenantID: tenantID,
				Name:     strings.Repeat("b", 256),
			},
			setupRepo: func(r *mockTopicRepo) {},
			wantErr:   ErrTopicNameTooLong,
		},
		{
			name: "returns error when topic not found",
			topic: &TopicNode{
				ID:       topicID,
				TenantID: tenantID,
				Name:     "Test",
			},
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(nil, ErrTopicNodeNotFound)
			},
			wantErr: ErrTopicNodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			tt.setupRepo(topicRepo)

			svc := NewTopicService(topicRepo, new(mockEventPublisher))
			result, err := svc.UpdateTopic(context.Background(), tt.topic)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.False(t, result.UpdatedAt.IsZero(), "updated_at should be refreshed")
			topicRepo.AssertExpectations(t)
		})
	}
}

func TestTopicService_DeleteTopic(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		setupRepo func(*mockTopicRepo)
		wantErr   error
	}{
		{
			name: "soft-deletes topic",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(&TopicNode{
					ID:       topicID,
					TenantID: tenantID,
					Name:     "Mathematics",
				}, nil)
				r.On("SoftDelete", mock.Anything, topicID).Return(nil)
			},
		},
		{
			name: "returns error when topic not found",
			setupRepo: func(r *mockTopicRepo) {
				r.On("GetByID", mock.Anything, topicID).Return(nil, ErrTopicNodeNotFound)
			},
			wantErr: ErrTopicNodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topicRepo := new(mockTopicRepo)
			tt.setupRepo(topicRepo)

			svc := NewTopicService(topicRepo, new(mockEventPublisher))
			err := svc.DeleteTopic(context.Background(), topicID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			topicRepo.AssertExpectations(t)
		})
	}
}
