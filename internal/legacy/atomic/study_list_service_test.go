package atomic

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestStudyListService_CreateList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		list             *StudyList
		setupRepo        func(*mockStudyListRepo)
		setupEvents      func(*mockEventPublisher)
		wantErr          error
		wantVisibility   StudyListVisibility
		wantUUIDv7       bool
		wantEmptyAtomIDs bool
	}{
		{
			name: "creates valid list with private visibility by default",
			list: &StudyList{
				TenantID: uuid.Must(uuid.NewV7()),
				GCID:     uuid.Must(uuid.NewV7()),
				Title:    "My Study List",
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantVisibility: StudyListVisibilityPrivate,
			wantUUIDv7:     true,
		},
		{
			name: "creates list with custom visibility",
			list: &StudyList{
				TenantID:   uuid.Must(uuid.NewV7()),
				GCID:       uuid.Must(uuid.NewV7()),
				Title:      "Shared Study List",
				Visibility: StudyListVisibilityPublic,
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantVisibility: StudyListVisibilityPublic,
			wantUUIDv7:     true,
		},
		{
			name: "rejects list with empty title",
			list: &StudyList{
				TenantID: uuid.Must(uuid.NewV7()),
				GCID:     uuid.Must(uuid.NewV7()),
				Title:    "",
			},
			setupRepo:   func(r *mockStudyListRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListTitleRequired,
		},
		{
			name: "assigns UUIDv7 and timestamps",
			list: &StudyList{
				TenantID: uuid.Must(uuid.NewV7()),
				GCID:     uuid.Must(uuid.NewV7()),
				Title:    "Timestamped List",
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantVisibility: StudyListVisibilityPrivate,
			wantUUIDv7:     true,
		},
		{
			name: "initializes empty atom_ids if not provided",
			list: &StudyList{
				TenantID: uuid.Must(uuid.NewV7()),
				GCID:     uuid.Must(uuid.NewV7()),
				Title:    "Empty Atoms List",
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantVisibility:   StudyListVisibilityPrivate,
			wantUUIDv7:       true,
			wantEmptyAtomIDs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			err := svc.CreateList(context.Background(), tt.list)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, tt.list.ID, "ID should be a non-nil UUIDv7")
			assert.Equal(t, tt.wantVisibility, tt.list.Visibility, "visibility should match expected")
			assert.False(t, tt.list.CreatedAt.IsZero(), "created_at should be set")
			assert.False(t, tt.list.UpdatedAt.IsZero(), "updated_at should be set")

			if tt.wantEmptyAtomIDs {
				assert.NotNil(t, tt.list.AtomIDs, "atom_ids should be initialized")
				assert.Empty(t, tt.list.AtomIDs, "atom_ids should be empty")
			}

			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_GetList(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		id        uuid.UUID
		setupRepo func(*mockStudyListRepo)
		wantErr   error
	}{
		{
			name: "returns list with atom_ids",
			id:   existingID,
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					AtomIDs:    []uuid.UUID{atomID1, atomID2},
					Visibility: StudyListVisibilityPrivate,
				}, nil)
			},
		},
		{
			name: "returns error for non-existent list",
			id:   uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			wantErr: ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)

			svc := NewStudyListService(listRepo, publisher)
			result, err := svc.GetList(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, existingID, result.ID)
			assert.Len(t, result.AtomIDs, 2, "should have 2 atom IDs")
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_UpdateList(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		list        *StudyList
		setupRepo   func(*mockStudyListRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name: "updates title and description",
			list: &StudyList{
				ID:          existingID,
				TenantID:    tenantID,
				GCID:        gcid,
				Title:       "Updated Title",
				Description: "Updated Description",
				Visibility:  StudyListVisibilityPrivate,
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "Old Title",
					Visibility: StudyListVisibilityPrivate,
					CreatedAt:  time.Now().UTC().Add(-time.Hour),
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "transitions visibility from private to shared",
			list: &StudyList{
				ID:         existingID,
				TenantID:   tenantID,
				GCID:       gcid,
				Title:      "Shared List",
				Visibility: StudyListVisibilityShared,
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "Shared List",
					Visibility: StudyListVisibilityPrivate,
					CreatedAt:  time.Now().UTC().Add(-time.Hour),
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "rejects update with empty title",
			list: &StudyList{
				ID:       existingID,
				TenantID: tenantID,
				GCID:     gcid,
				Title:    "",
			},
			setupRepo:   func(r *mockStudyListRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListTitleRequired,
		},
		{
			name: "returns error for non-existent list",
			list: &StudyList{
				ID:       uuid.Must(uuid.NewV7()),
				TenantID: tenantID,
				GCID:     gcid,
				Title:    "Valid Title",
			},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			err := svc.UpdateList(context.Background(), tt.list)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.False(t, tt.list.UpdatedAt.IsZero(), "updated_at should be refreshed")
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_AddAtoms(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())
	atomID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		listID      uuid.UUID
		atomIDs     []uuid.UUID
		setupRepo   func(*mockStudyListRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:    "adds atom IDs to existing list",
			listID:  existingID,
			atomIDs: []uuid.UUID{atomID2, atomID3},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					AtomIDs:    []uuid.UUID{atomID1},
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:    "deduplicates atom IDs",
			listID:  existingID,
			atomIDs: []uuid.UUID{atomID1, atomID2, atomID1},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					AtomIDs:    []uuid.UUID{atomID1},
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:    "returns error for non-existent list",
			listID:  uuid.Must(uuid.NewV7()),
			atomIDs: []uuid.UUID{atomID1},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			err := svc.AddAtoms(context.Background(), tt.listID, tt.atomIDs)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_RemoveAtoms(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())
	atomID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		listID      uuid.UUID
		atomIDs     []uuid.UUID
		setupRepo   func(*mockStudyListRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:    "removes atom IDs from list",
			listID:  existingID,
			atomIDs: []uuid.UUID{atomID2},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					AtomIDs:    []uuid.UUID{atomID1, atomID2, atomID3},
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:    "handles removing non-existent atoms gracefully",
			listID:  existingID,
			atomIDs: []uuid.UUID{uuid.Must(uuid.NewV7())},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					AtomIDs:    []uuid.UUID{atomID1, atomID2},
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:    "returns error for non-existent list",
			listID:  uuid.Must(uuid.NewV7()),
			atomIDs: []uuid.UUID{atomID1},
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			err := svc.RemoveAtoms(context.Background(), tt.listID, tt.atomIDs)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_ShareList(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		listID      uuid.UUID
		setupRepo   func(*mockStudyListRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
		wantCode    bool
	}{
		{
			name:   "generates share code for a list",
			listID: existingID,
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantCode: true,
		},
		{
			name:   "returns existing share code if already shared",
			listID: existingID,
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					Visibility: StudyListVisibilityShared,
					ShareCode:  "existing-code-abc123",
				}, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantCode:    true,
		},
		{
			name:   "returns error for non-existent list",
			listID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			code, err := svc.ShareList(context.Background(), tt.listID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Empty(t, code)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, code, "share code should be non-empty")
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_GetByShareCode(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		shareCode string
		setupRepo func(*mockStudyListRepo)
		wantErr   error
	}{
		{
			name:      "returns list by share code",
			shareCode: "valid-share-code",
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByShareCode", mock.Anything, "valid-share-code").Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "Shared List",
					Visibility: StudyListVisibilityShared,
					ShareCode:  "valid-share-code",
				}, nil)
			},
		},
		{
			name:      "returns error for invalid share code",
			shareCode: "nonexistent-code",
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByShareCode", mock.Anything, "nonexistent-code").Return(nil, ErrShareCodeNotFound)
			},
			wantErr: ErrShareCodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)

			svc := NewStudyListService(listRepo, publisher)
			result, err := svc.GetByShareCode(context.Background(), tt.shareCode)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.shareCode, result.ShareCode)
			listRepo.AssertExpectations(t)
		})
	}
}

func TestStudyListService_DeleteList(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		listID      uuid.UUID
		setupRepo   func(*mockStudyListRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
	}{
		{
			name:   "soft-deletes list",
			listID: existingID,
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&StudyList{
					ID:         existingID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "To Delete",
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				r.On("SoftDelete", mock.Anything, existingID).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:   "returns error for non-existent list",
			listID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockStudyListRepo) {
				r.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrStudyListNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listRepo := new(mockStudyListRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(listRepo)
			tt.setupEvents(publisher)

			svc := NewStudyListService(listRepo, publisher)
			err := svc.DeleteList(context.Background(), tt.listID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			listRepo.AssertExpectations(t)
		})
	}
}
