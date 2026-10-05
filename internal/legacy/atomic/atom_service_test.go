package atomic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAtomService_CreateAtom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		atom         *LearningAtom
		setupRepo    func(*mockAtomRepo)
		setupRevRepo func(*mockRevisionRepo)
		setupEvents  func(*mockEventPublisher)
		wantErr      error
		wantStatus   AtomStatus
		wantUUIDv7   bool
	}{
		{
			name: "creates valid atom with UUIDv7 and draft status",
			atom: &LearningAtom{
				TenantID:     uuid.Must(uuid.NewV7()),
				AtomType:     AtomTypeMultipleChoice,
				Difficulty:   3,
				LanguageCode: "en",
				CreatedBy:    uuid.Must(uuid.NewV7()),
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
			},
			setupRevRepo: func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: AtomStatusDraft,
			wantUUIDv7: true,
		},
		{
			name: "rejects invalid atom type",
			atom: &LearningAtom{
				TenantID:   uuid.Must(uuid.NewV7()),
				AtomType:   AtomType("invalid_type"),
				Difficulty: 3,
				CreatedBy:  uuid.Must(uuid.NewV7()),
			},
			setupRepo:    func(r *mockAtomRepo) {},
			setupRevRepo: func(r *mockRevisionRepo) {},
			setupEvents:  func(p *mockEventPublisher) {},
			wantErr:      ErrInvalidAtomType,
		},
		{
			name: "rejects difficulty below 1",
			atom: &LearningAtom{
				TenantID:   uuid.Must(uuid.NewV7()),
				AtomType:   AtomTypeMultipleChoice,
				Difficulty: 0,
				CreatedBy:  uuid.Must(uuid.NewV7()),
			},
			setupRepo:    func(r *mockAtomRepo) {},
			setupRevRepo: func(r *mockRevisionRepo) {},
			setupEvents:  func(p *mockEventPublisher) {},
			wantErr:      ErrInvalidDifficulty,
		},
		{
			name: "rejects difficulty above 5",
			atom: &LearningAtom{
				TenantID:   uuid.Must(uuid.NewV7()),
				AtomType:   AtomTypeTrueFalse,
				Difficulty: 6,
				CreatedBy:  uuid.Must(uuid.NewV7()),
			},
			setupRepo:    func(r *mockAtomRepo) {},
			setupRevRepo: func(r *mockRevisionRepo) {},
			setupEvents:  func(p *mockEventPublisher) {},
			wantErr:      ErrInvalidDifficulty,
		},
		{
			name: "propagates repository error",
			atom: &LearningAtom{
				TenantID:   uuid.Must(uuid.NewV7()),
				AtomType:   AtomTypeShortAnswer,
				Difficulty: 2,
				CreatedBy:  uuid.Must(uuid.NewV7()),
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("Save", mock.Anything, mock.Anything).Return(errors.New("db error"))
			},
			setupRevRepo: func(r *mockRevisionRepo) {},
			setupEvents:  func(p *mockEventPublisher) {},
			wantErr:      errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			revRepo := new(mockRevisionRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(atomRepo)
			tt.setupRevRepo(revRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, revRepo, publisher)
			result, err := svc.CreateAtom(context.Background(), tt.atom)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, result.ID, "ID should be a non-nil UUIDv7")
			assert.Equal(t, tt.wantStatus, result.Status, "new atom should start as draft")
			assert.False(t, result.CreatedAt.IsZero(), "created_at should be set")
			assert.False(t, result.UpdatedAt.IsZero(), "updated_at should be set")
			atomRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_CreateAtom_PublishesEvent(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)

	revRepo := new(mockRevisionRepo)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, revRepo, publisher)
	_, err := svc.CreateAtom(context.Background(), &LearningAtom{
		TenantID:     tenantID,
		AtomType:     AtomTypeMultipleChoice,
		Difficulty:   3,
		LanguageCode: "en",
		CreatedBy:    createdBy,
	})

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	// Contract: topic must be "chora.creation.atom.events.v1" (post-M12.3.E)
	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeAtomCreated, event.EventType)
	assert.Equal(t, AggregateTypeLearningAtom, event.AggregateType)
	// Contract requires these payload fields per atomic-events.yaml
	assert.Contains(t, event.Payload, "atom_id", "missing atom_id in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
	assert.Contains(t, event.Payload, "atom_type", "atom_type should be in payload")
	assert.Contains(t, event.Payload, "created_by", "missing created_by in payload")
}

func TestAtomService_GetAtom(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		id        uuid.UUID
		setupRepo func(*mockAtomRepo)
		wantErr   error
	}{
		{
			name: "returns atom when found",
			id:   existingID,
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&LearningAtom{
					ID:       existingID,
					AtomType: AtomTypeMultipleChoice,
					Status:   AtomStatusDraft,
				}, nil)
			},
		},
		{
			name: "propagates repository error",
			id:   uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrAtomNotFound)
			},
			wantErr: ErrAtomNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			tt.setupRepo(atomRepo)

			svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
			atom, err := svc.GetAtom(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.id, atom.ID)
			atomRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_UpdateAtomMetadata(t *testing.T) {
	t.Parallel()

	activeID := uuid.Must(uuid.NewV7())
	archivedID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		atom        *LearningAtom
		setupRepo   func(*mockAtomRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name: "updates valid atom metadata",
			atom: &LearningAtom{
				ID:         activeID,
				TenantID:   tenantID,
				AtomType:   AtomTypeMultipleChoice,
				Difficulty: 4,
				Tags:       []string{"math", "algebra"},
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, activeID).Return(&LearningAtom{
					ID:       activeID,
					TenantID: tenantID,
					Status:   AtomStatusDraft,
				}, nil)
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "rejects update of archived atom",
			atom: &LearningAtom{
				ID:         archivedID,
				TenantID:   tenantID,
				AtomType:   AtomTypeMultipleChoice,
				Difficulty: 3,
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, archivedID).Return(&LearningAtom{
					ID:       archivedID,
					TenantID: tenantID,
					Status:   AtomStatusArchived,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrAtomAlreadyArchived,
		},
		{
			name: "rejects invalid difficulty",
			atom: &LearningAtom{
				ID:         activeID,
				TenantID:   tenantID,
				AtomType:   AtomTypeMultipleChoice,
				Difficulty: 0,
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, activeID).Return(&LearningAtom{
					ID:       activeID,
					TenantID: tenantID,
					Status:   AtomStatusDraft,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrInvalidDifficulty,
		},
		{
			name: "propagates repository error",
			atom: &LearningAtom{
				ID:         activeID,
				TenantID:   tenantID,
				AtomType:   AtomTypeMultipleChoice,
				Difficulty: 3,
			},
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, activeID).Return(nil, errors.New("db error"))
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(atomRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
			result, err := svc.UpdateAtomMetadata(context.Background(), tt.atom)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.False(t, result.UpdatedAt.IsZero(), "updated_at should be refreshed")
			atomRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_UpdateAtomMetadata_PublishesEvent(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
		Status:   AtomStatusDraft,
	}, nil)
	atomRepo.On("Save", mock.Anything, mock.Anything).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
	_, err := svc.UpdateAtomMetadata(context.Background(), &LearningAtom{
		ID:         atomID,
		TenantID:   tenantID,
		AtomType:   AtomTypeMultipleChoice,
		Difficulty: 4,
		Tags:       []string{"updated"},
	})

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	// Contract: topic must be "chora.creation.atom.events.v1" (post-M12.3.E)
	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeAtomUpdated, event.EventType)
	// Contract requires these payload fields per atomic-events.yaml
	assert.Contains(t, event.Payload, "atom_id", "missing atom_id in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
	assert.Contains(t, event.Payload, "changed_fields", "missing changed_fields in payload")
}

func TestAtomService_ArchiveAtom(t *testing.T) {
	t.Parallel()

	activeID := uuid.Must(uuid.NewV7())
	archivedID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		id          uuid.UUID
		setupRepo   func(*mockAtomRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name: "archives active atom",
			id:   activeID,
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, activeID).Return(&LearningAtom{
					ID:       activeID,
					TenantID: tenantID,
					Status:   AtomStatusDraft,
				}, nil)
				r.On("Save", mock.Anything, mock.MatchedBy(func(a *LearningAtom) bool {
					return a.Status == AtomStatusArchived
				})).Return(nil)
				r.On("SoftDelete", mock.Anything, activeID).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "rejects archiving already-archived atom",
			id:   archivedID,
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, archivedID).Return(&LearningAtom{
					ID:       archivedID,
					TenantID: tenantID,
					Status:   AtomStatusArchived,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrAtomAlreadyArchived,
		},
		{
			name: "calls SoftDelete on atom repository",
			id:   activeID,
			setupRepo: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, activeID).Return(&LearningAtom{
					ID:       activeID,
					TenantID: tenantID,
					Status:   AtomStatusDraft,
				}, nil)
				r.On("Save", mock.Anything, mock.Anything).Return(nil)
				r.On("SoftDelete", mock.Anything, activeID).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(atomRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
			err := svc.ArchiveAtom(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			atomRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_ArchiveAtom_PublishesEvent(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
		Status:   AtomStatusDraft,
	}, nil)
	atomRepo.On("Save", mock.Anything, mock.Anything).Return(nil)
	atomRepo.On("SoftDelete", mock.Anything, atomID).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
	err := svc.ArchiveAtom(context.Background(), atomID)

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	// Contract: topic must be "chora.creation.atom.events.v1" (post-M12.3.E)
	assert.Equal(t, TopicAtomicEvents, topic)
	assert.Equal(t, EventTypeAtomArchived, event.EventType)
	assert.NotNil(t, event.Payload, "payload should not be nil")
	assert.Contains(t, event.Payload, "atom_id")
	assert.Contains(t, event.Payload, "tenant_id")
}

func TestAtomService_PublishRevision(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name            string
		atomID          uuid.UUID
		content         map[string]any
		rules           []AnswerValidationRule
		setupAtom       func(*mockAtomRepo)
		setupRev        func(*mockRevisionRepo)
		setupEvents     func(*mockEventPublisher)
		wantErr         error
		wantRevNum      int
		wantPublishedAt bool
	}{
		{
			name:    "first revision gets number 1",
			atomID:  atomID,
			content: map[string]any{"question": "What is 2+2?"},
			rules:   []AnswerValidationRule{{RuleType: ValidationRuleExactMatch, Expected: "4"}},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
				r.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
				r.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantRevNum:      1,
			wantPublishedAt: true,
		},
		{
			name:    "increments revision number from latest",
			atomID:  atomID,
			content: map[string]any{"question": "Updated question"},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
				r.On("GetLatest", mock.Anything, atomID).Return(&AtomRevision{
					RevisionNumber: 3,
				}, nil)
				r.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantRevNum:      4,
			wantPublishedAt: true,
		},
		{
			name:        "rejects nil content",
			atomID:      atomID,
			content:     nil,
			setupAtom:   func(r *mockAtomRepo) {},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionContentRequired,
		},
		{
			name:        "rejects empty content map",
			atomID:      atomID,
			content:     map[string]any{},
			setupAtom:   func(r *mockAtomRepo) {},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionContentRequired,
		},
		{
			name:    "returns error when atom not found",
			atomID:  uuid.Must(uuid.NewV7()),
			content: map[string]any{"question": "test"},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrAtomNotFound)
			},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrAtomNotFound,
		},
		{
			name:    "sets publishedAt timestamp and visibility",
			atomID:  atomID,
			content: map[string]any{"question": "Timestamp test"},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
				r.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
				r.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantRevNum:      1,
			wantPublishedAt: true,
		},
	}

	createdBy := uuid.Must(uuid.NewV7())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			revRepo := new(mockRevisionRepo)
			publisher := new(mockEventPublisher)
			tt.setupAtom(atomRepo)
			tt.setupRev(revRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, revRepo, publisher)
			revision, err := svc.PublishRevision(context.Background(), tt.atomID, tt.content, tt.rules, nil, createdBy)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantRevNum, revision.RevisionNumber)
			assert.NotEqual(t, uuid.Nil, revision.ID)
			assert.Equal(t, RevisionVisibilityPublished, revision.VisibilityStatus)
			if tt.wantPublishedAt {
				assert.NotNil(t, revision.PublishedAt, "publishedAt should be set")
			}
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_PublishRevision_PublishesEvent(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
	}, nil)

	revRepo := new(mockRevisionRepo)
	revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
	revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
	revRepo.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, revRepo, publisher)
	_, err := svc.PublishRevision(context.Background(), atomID,
		map[string]any{"question": "Test?"}, nil, nil, createdBy)

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeAtomRevisionPublished, event.EventType)
	assert.Contains(t, event.Payload, "atom_id", "missing atom_id in payload")
	assert.Contains(t, event.Payload, "revision_id", "missing revision_id in payload")
	assert.Contains(t, event.Payload, "revision_number", "revision_number should be in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
}

func TestAtomService_GetLatestRevision(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name     string
		setupRev func(*mockRevisionRepo)
		wantNil  bool
		wantErr  error
	}{
		{
			name: "returns latest revision",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatest", mock.Anything, atomID).Return(&AtomRevision{
					ID:             uuid.Must(uuid.NewV7()),
					AtomID:         atomID,
					RevisionNumber: 2,
				}, nil)
			},
		},
		{
			name: "returns nil when no revisions exist",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
			},
			wantNil: true,
		},
		{
			name: "propagates repository error",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatest", mock.Anything, atomID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			revRepo := new(mockRevisionRepo)
			tt.setupRev(revRepo)

			svc := NewAtomService(new(mockAtomRepo), revRepo, new(mockEventPublisher))
			revision, err := svc.GetLatestRevision(context.Background(), atomID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, revision)
			} else {
				assert.NotNil(t, revision)
			}
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_ListAtomsByTenant(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		setupRepo func(*mockAtomRepo)
		wantCount int
		wantErr   error
	}{
		{
			name: "returns atoms for tenant",
			setupRepo: func(r *mockAtomRepo) {
				r.On("ListByTenant", mock.Anything, tenantID).Return([]*LearningAtom{
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, AtomType: AtomTypeMultipleChoice},
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, AtomType: AtomTypeTrueFalse},
				}, nil)
			},
			wantCount: 2,
		},
		{
			name: "propagates repository error",
			setupRepo: func(r *mockAtomRepo) {
				r.On("ListByTenant", mock.Anything, tenantID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			tt.setupRepo(atomRepo)

			svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
			atoms, err := svc.ListAtomsByTenant(context.Background(), tenantID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Len(t, atoms, tt.wantCount)
			atomRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_GetRevisionsByAtomID(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name     string
		setupRev func(*mockRevisionRepo)
		wantLen  int
		wantErr  error
	}{
		{
			name: "returns all revisions for atom",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByAtomID", mock.Anything, atomID).Return([]*AtomRevision{
					{ID: uuid.Must(uuid.NewV7()), AtomID: atomID, RevisionNumber: 1},
					{ID: uuid.Must(uuid.NewV7()), AtomID: atomID, RevisionNumber: 2},
				}, nil)
			},
			wantLen: 2,
		},
		{
			name: "returns empty slice when no revisions",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByAtomID", mock.Anything, atomID).Return([]*AtomRevision{}, nil)
			},
			wantLen: 0,
		},
		{
			name: "propagates repository error",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByAtomID", mock.Anything, atomID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			revRepo := new(mockRevisionRepo)
			tt.setupRev(revRepo)

			svc := NewAtomService(new(mockAtomRepo), revRepo, new(mockEventPublisher))
			revisions, err := svc.GetRevisionsByAtomID(context.Background(), atomID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Len(t, revisions, tt.wantLen)
			revRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// Draft Workflow Tests (7.6.9)
// ---------------------------------------------------------------------------

func TestAtomService_SaveDraft(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		atomID      uuid.UUID
		content     map[string]any
		rules       []AnswerValidationRule
		metadata    map[string]any
		setupAtom   func(*mockAtomRepo)
		setupRev    func(*mockRevisionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:    "creates draft revision with draft visibility and nil publishedAt",
			atomID:  atomID,
			content: map[string]any{"question": "What is Go?"},
			rules:   []AnswerValidationRule{{RuleType: ValidationRuleExactMatch, Expected: "a programming language"}},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetDraft", mock.Anything, atomID).Return(nil, nil)
				r.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
				r.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:        "rejects empty content",
			atomID:      atomID,
			content:     map[string]any{},
			setupAtom:   func(r *mockAtomRepo) {},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionContentRequired,
		},
		{
			name:        "rejects nil content",
			atomID:      atomID,
			content:     nil,
			setupAtom:   func(r *mockAtomRepo) {},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionContentRequired,
		},
		{
			name:    "returns ErrRevisionDraftExists when draft already exists",
			atomID:  atomID,
			content: map[string]any{"question": "Duplicate draft?"},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetDraft", mock.Anything, atomID).Return(&AtomRevision{
					ID:               uuid.Must(uuid.NewV7()),
					AtomID:           atomID,
					VisibilityStatus: RevisionVisibilityDraft,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionDraftExists,
		},
		{
			name:    "returns error when atom not found",
			atomID:  uuid.Must(uuid.NewV7()),
			content: map[string]any{"question": "test"},
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrAtomNotFound)
			},
			setupRev:    func(r *mockRevisionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrAtomNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			revRepo := new(mockRevisionRepo)
			publisher := new(mockEventPublisher)
			tt.setupAtom(atomRepo)
			tt.setupRev(revRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, revRepo, publisher)
			revision, err := svc.SaveDraft(context.Background(), tt.atomID, tt.content, tt.rules, tt.metadata, createdBy)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, revision.ID, "ID should be a non-nil UUIDv7")
			assert.Equal(t, RevisionVisibilityDraft, revision.VisibilityStatus, "visibility must be draft")
			assert.Nil(t, revision.PublishedAt, "publishedAt must be nil for drafts")
			assert.Equal(t, 1, revision.RevisionNumber, "first revision should be number 1")
			assert.False(t, revision.CreatedAt.IsZero(), "created_at should be set")
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_SaveDraft_PublishesEvent(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
	}, nil)

	revRepo := new(mockRevisionRepo)
	revRepo.On("GetDraft", mock.Anything, atomID).Return(nil, nil)
	revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
	revRepo.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, revRepo, publisher)
	_, err := svc.SaveDraft(context.Background(), atomID,
		map[string]any{"question": "Draft event test?"}, nil, nil, createdBy)

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeAtomRevisionDrafted, event.EventType)
	assert.Contains(t, event.Payload, "atom_id", "missing atom_id in payload")
	assert.Contains(t, event.Payload, "revision_id", "missing revision_id in payload")
	assert.Contains(t, event.Payload, "revision_number", "missing revision_number in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
}

func TestAtomService_PromoteDraft(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	draftRevID := uuid.Must(uuid.NewV7())
	publishedRevID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		revisionID  uuid.UUID
		setupAtom   func(*mockAtomRepo)
		setupRev    func(*mockRevisionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:       "promotes draft to published with publishedAt set",
			revisionID: draftRevID,
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByID", mock.Anything, draftRevID).Return(&AtomRevision{
					ID:               draftRevID,
					AtomID:           atomID,
					RevisionNumber:   1,
					Content:          map[string]any{"question": "What is Go?"},
					VisibilityStatus: RevisionVisibilityDraft,
				}, nil)
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
				r.On("UpdateVisibility", mock.Anything, draftRevID, RevisionVisibilityPublished, mock.AnythingOfType("*time.Time")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:       "archives previously published revision when promoting",
			revisionID: draftRevID,
			setupAtom: func(r *mockAtomRepo) {
				r.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
					ID:       atomID,
					TenantID: tenantID,
				}, nil)
			},
			setupRev: func(r *mockRevisionRepo) {
				pubTime := time.Now().UTC()
				r.On("GetByID", mock.Anything, draftRevID).Return(&AtomRevision{
					ID:               draftRevID,
					AtomID:           atomID,
					RevisionNumber:   2,
					Content:          map[string]any{"question": "Updated"},
					VisibilityStatus: RevisionVisibilityDraft,
				}, nil)
				r.On("GetLatestPublished", mock.Anything, atomID).Return(&AtomRevision{
					ID:               publishedRevID,
					AtomID:           atomID,
					RevisionNumber:   1,
					VisibilityStatus: RevisionVisibilityPublished,
					PublishedAt:      &pubTime,
				}, nil)
				r.On("UpdateVisibility", mock.Anything, publishedRevID, RevisionVisibilityArchived, mock.AnythingOfType("*time.Time")).Return(nil)
				r.On("UpdateVisibility", mock.Anything, draftRevID, RevisionVisibilityPublished, mock.AnythingOfType("*time.Time")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:       "returns ErrRevisionNotDraft when revision is not in draft state",
			revisionID: publishedRevID,
			setupAtom:  func(r *mockAtomRepo) {},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByID", mock.Anything, publishedRevID).Return(&AtomRevision{
					ID:               publishedRevID,
					AtomID:           atomID,
					VisibilityStatus: RevisionVisibilityPublished,
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionNotDraft,
		},
		{
			name:       "returns error when revision not found",
			revisionID: uuid.Must(uuid.NewV7()),
			setupAtom:  func(r *mockAtomRepo) {},
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrRevisionNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrRevisionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			revRepo := new(mockRevisionRepo)
			publisher := new(mockEventPublisher)
			tt.setupAtom(atomRepo)
			tt.setupRev(revRepo)
			tt.setupEvents(publisher)

			svc := NewAtomService(atomRepo, revRepo, publisher)
			revision, err := svc.PromoteDraft(context.Background(), tt.revisionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, RevisionVisibilityPublished, revision.VisibilityStatus, "promoted revision must be published")
			assert.NotNil(t, revision.PublishedAt, "publishedAt must be set after promotion")
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_PromoteDraft_PublishesEvent(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	draftRevID := uuid.Must(uuid.NewV7())

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
	}, nil)

	revRepo := new(mockRevisionRepo)
	revRepo.On("GetByID", mock.Anything, draftRevID).Return(&AtomRevision{
		ID:               draftRevID,
		AtomID:           atomID,
		RevisionNumber:   1,
		Content:          map[string]any{"question": "Promote event test?"},
		VisibilityStatus: RevisionVisibilityDraft,
	}, nil)
	revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
	revRepo.On("UpdateVisibility", mock.Anything, draftRevID, RevisionVisibilityPublished, mock.AnythingOfType("*time.Time")).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, revRepo, publisher)
	_, err := svc.PromoteDraft(context.Background(), draftRevID)

	require.NoError(t, err)
	require.Len(t, publisher.Calls, 1, "Publish should be called once")
	call := publisher.Calls[0]
	topic := call.Arguments.Get(1).(string)
	event := call.Arguments.Get(2).(DomainEvent)

	assert.Equal(t, TopicAtomicEvents, topic, "wrong topic name")
	assert.Equal(t, EventTypeAtomRevisionPublished, event.EventType)
	assert.Contains(t, event.Payload, "atom_id", "missing atom_id in payload")
	assert.Contains(t, event.Payload, "revision_id", "missing revision_id in payload")
	assert.Contains(t, event.Payload, "revision_number", "missing revision_number in payload")
	assert.Contains(t, event.Payload, "tenant_id", "missing tenant_id in payload")
}

func TestAtomService_GetLatestPublishedRevision(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name     string
		setupRev func(*mockRevisionRepo)
		wantNil  bool
		wantErr  error
	}{
		{
			name: "returns latest published revision",
			setupRev: func(r *mockRevisionRepo) {
				now := time.Now().UTC()
				r.On("GetLatestPublished", mock.Anything, atomID).Return(&AtomRevision{
					ID:               uuid.Must(uuid.NewV7()),
					AtomID:           atomID,
					RevisionNumber:   3,
					VisibilityStatus: RevisionVisibilityPublished,
					PublishedAt:      &now,
				}, nil)
			},
		},
		{
			name: "returns nil when no published revisions exist",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
			},
			wantNil: true,
		},
		{
			name: "propagates repository error",
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetLatestPublished", mock.Anything, atomID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			revRepo := new(mockRevisionRepo)
			tt.setupRev(revRepo)

			svc := NewAtomService(new(mockAtomRepo), revRepo, new(mockEventPublisher))
			revision, err := svc.GetLatestPublishedRevision(context.Background(), atomID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, revision)
			} else {
				assert.NotNil(t, revision)
				assert.Equal(t, RevisionVisibilityPublished, revision.VisibilityStatus)
			}
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_GetRevisionByID(t *testing.T) {
	t.Parallel()

	revisionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name     string
		id       uuid.UUID
		setupRev func(*mockRevisionRepo)
		wantErr  error
	}{
		{
			name: "returns specific revision",
			id:   revisionID,
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByID", mock.Anything, revisionID).Return(&AtomRevision{
					ID:               revisionID,
					AtomID:           uuid.Must(uuid.NewV7()),
					RevisionNumber:   1,
					Content:          map[string]any{"question": "Test"},
					VisibilityStatus: RevisionVisibilityDraft,
				}, nil)
			},
		},
		{
			name: "propagates not-found error",
			id:   uuid.Must(uuid.NewV7()),
			setupRev: func(r *mockRevisionRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrRevisionNotFound)
			},
			wantErr: ErrRevisionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			revRepo := new(mockRevisionRepo)
			tt.setupRev(revRepo)

			svc := NewAtomService(new(mockAtomRepo), revRepo, new(mockEventPublisher))
			revision, err := svc.GetRevisionByID(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, revision)
			assert.Equal(t, tt.id, revision.ID)
			revRepo.AssertExpectations(t)
		})
	}
}

func TestAtomService_PublishRevision_ArchivesPrevious(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())
	previousRevID := uuid.Must(uuid.NewV7())
	pubTime := time.Now().UTC()

	atomRepo := new(mockAtomRepo)
	atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{
		ID:       atomID,
		TenantID: tenantID,
	}, nil)

	revRepo := new(mockRevisionRepo)
	// GetLatestPublished returns the currently published revision.
	revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(&AtomRevision{
		ID:               previousRevID,
		AtomID:           atomID,
		RevisionNumber:   1,
		VisibilityStatus: RevisionVisibilityPublished,
		PublishedAt:      &pubTime,
	}, nil)
	// Expect archiveCurrentPublished to call UpdateVisibility with archived status.
	revRepo.On("UpdateVisibility", mock.Anything, previousRevID, RevisionVisibilityArchived, &pubTime).Return(nil)
	revRepo.On("GetLatest", mock.Anything, atomID).Return(&AtomRevision{
		RevisionNumber: 1,
	}, nil)
	revRepo.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(nil)

	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewAtomService(atomRepo, revRepo, publisher)
	revision, err := svc.PublishRevision(context.Background(), atomID,
		map[string]any{"question": "New published revision"}, nil, nil, createdBy)

	require.NoError(t, err)
	assert.Equal(t, 2, revision.RevisionNumber, "should increment from previous")
	assert.Equal(t, RevisionVisibilityPublished, revision.VisibilityStatus)
	assert.NotNil(t, revision.PublishedAt)

	// Verify UpdateVisibility was called to archive the previous revision.
	revRepo.AssertCalled(t, "UpdateVisibility", mock.Anything, previousRevID, RevisionVisibilityArchived, &pubTime)
	revRepo.AssertExpectations(t)
}

func TestAtomService_ListAtomsByTopic(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		setupRepo func(*mockAtomRepo)
		wantCount int
		wantErr   error
	}{
		{
			name: "returns atoms for topic",
			setupRepo: func(r *mockAtomRepo) {
				r.On("ListByTopic", mock.Anything, topicID).Return([]*LearningAtom{
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, AtomType: AtomTypeMultipleChoice},
				}, nil)
			},
			wantCount: 1,
		},
		{
			name: "returns empty slice when no atoms for topic",
			setupRepo: func(r *mockAtomRepo) {
				r.On("ListByTopic", mock.Anything, topicID).Return([]*LearningAtom{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "propagates repository error",
			setupRepo: func(r *mockAtomRepo) {
				r.On("ListByTopic", mock.Anything, topicID).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			atomRepo := new(mockAtomRepo)
			tt.setupRepo(atomRepo)

			svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
			atoms, err := svc.ListAtomsByTopic(context.Background(), topicID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.Len(t, atoms, tt.wantCount)
			atomRepo.AssertExpectations(t)
		})
	}
}
