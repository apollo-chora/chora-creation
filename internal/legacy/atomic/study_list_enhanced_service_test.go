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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type testStudyListEnhancedDeps struct {
	svc       *StudyListEnhancedService
	repo      *mockStudyListRepo
	publisher *mockEventPublisher
}

func newTestStudyListEnhancedService() testStudyListEnhancedDeps {
	r := &mockStudyListRepo{}
	ep := &mockEventPublisher{}
	return testStudyListEnhancedDeps{
		svc:       NewStudyListEnhancedService(r, ep),
		repo:      r,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestStudyListEnhancedService_CreateStudyList
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_CreateStudyList(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		tenantID     uuid.UUID
		gcid         uuid.UUID
		title        string
		description  string
		atomIDs      []uuid.UUID
		visibility   StudyListVisibility
		setupMocks   func(d testStudyListEnhancedDeps)
		wantErr      error
		assertResult func(t *testing.T, got *StudyList)
	}{
		{
			name:        "creates study list with atoms and visibility",
			tenantID:    tenantID,
			gcid:        gcid,
			title:       "My Study List",
			description: "A test list",
			atomIDs:     []uuid.UUID{atomID1, atomID2},
			visibility:  StudyListVisibilityShared,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
				d.publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudyList) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, tenantID, got.TenantID)
				assert.Equal(t, gcid, got.GCID)
				assert.Equal(t, "My Study List", got.Title)
				assert.Equal(t, "A test list", got.Description)
				assert.Len(t, got.AtomIDs, 2)
				assert.Equal(t, StudyListVisibilityShared, got.Visibility)
				assert.False(t, got.CreatedAt.IsZero())
				assert.False(t, got.UpdatedAt.IsZero())
			},
		},
		{
			name:       "defaults visibility to private",
			tenantID:   tenantID,
			gcid:       gcid,
			title:      "Default Visibility",
			atomIDs:    []uuid.UUID{},
			visibility: "",
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
				d.publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudyList) {
				assert.Equal(t, StudyListVisibilityPrivate, got.Visibility)
			},
		},
		{
			name:       "rejects empty title",
			tenantID:   tenantID,
			gcid:       gcid,
			title:      "",
			atomIDs:    []uuid.UUID{},
			visibility: StudyListVisibilityPrivate,
			setupMocks: func(d testStudyListEnhancedDeps) {},
			wantErr:    ErrStudyListTitleRequired,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestStudyListEnhancedService()
			tc.setupMocks(d)

			got, err := d.svc.CreateStudyList(context.Background(), tc.tenantID, tc.gcid, tc.title, tc.description, tc.atomIDs, tc.visibility)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			if assert.NotNil(t, got) && tc.assertResult != nil {
				tc.assertResult(t, got)
			}
			d.repo.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStudyListEnhancedService_UpdateStudyList
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_UpdateStudyList(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	listID := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		title        string
		description  string
		atomIDs      []uuid.UUID
		visibility   StudyListVisibility
		setupMocks   func(d testStudyListEnhancedDeps)
		wantErr      error
		assertResult func(t *testing.T, got *StudyList)
	}{
		{
			name:        "updates all fields",
			id:          listID,
			title:       "Updated Title",
			description: "Updated desc",
			atomIDs:     []uuid.UUID{atomID1},
			visibility:  StudyListVisibilityPublic,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, listID).Return(&StudyList{
					ID:         listID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "Old Title",
					Visibility: StudyListVisibilityPrivate,
					CreatedAt:  time.Now().UTC().Add(-time.Hour),
				}, nil)
				d.repo.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
				d.publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudyList) {
				assert.Equal(t, "Updated Title", got.Title)
				assert.Equal(t, "Updated desc", got.Description)
				assert.Equal(t, StudyListVisibilityPublic, got.Visibility)
				assert.Len(t, got.AtomIDs, 1)
			},
		},
		{
			name:       "rejects empty title",
			id:         listID,
			title:      "",
			atomIDs:    []uuid.UUID{},
			visibility: StudyListVisibilityPrivate,
			setupMocks: func(d testStudyListEnhancedDeps) {},
			wantErr:    ErrStudyListTitleRequired,
		},
		{
			name:       "returns error for non-existent list",
			id:         uuid.Must(uuid.NewV7()),
			title:      "Valid Title",
			atomIDs:    []uuid.UUID{},
			visibility: StudyListVisibilityPrivate,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			wantErr: ErrStudyListNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestStudyListEnhancedService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateStudyList(context.Background(), tc.id, tc.title, tc.description, tc.atomIDs, tc.visibility)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			if assert.NotNil(t, got) && tc.assertResult != nil {
				tc.assertResult(t, got)
			}
			d.repo.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStudyListEnhancedService_GetByShareCode
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_GetByShareCode(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	listID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		shareCode  string
		setupMocks func(d testStudyListEnhancedDeps)
		wantErr    error
	}{
		{
			name:      "returns study list by share code",
			shareCode: "abc123xyz456",
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByShareCode", mock.Anything, "abc123xyz456").Return(&StudyList{
					ID:         listID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "Shared List",
					Visibility: StudyListVisibilityShared,
					ShareCode:  "abc123xyz456",
				}, nil)
			},
		},
		{
			name:       "returns error for invalid share code",
			shareCode:  "",
			setupMocks: func(d testStudyListEnhancedDeps) {},
			wantErr:    ErrInvalidShareCode,
		},
		{
			name:      "returns error when not found",
			shareCode: "nonexistent12",
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByShareCode", mock.Anything, "nonexistent12").Return(nil, ErrStudyListNotFound)
			},
			wantErr: ErrStudyListNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestStudyListEnhancedService()
			tc.setupMocks(d)

			got, err := d.svc.GetByShareCode(context.Background(), tc.shareCode)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tc.shareCode, got.ShareCode)
			d.repo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStudyListEnhancedService_GenerateShareCode
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_GenerateShareCode(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	listID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupMocks func(d testStudyListEnhancedDeps)
		wantErr    error
		wantCode   bool
	}{
		{
			name: "generates a 12-char share code",
			id:   listID,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, listID).Return(&StudyList{
					ID:         listID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				d.repo.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(nil)
				d.publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantCode: true,
		},
		{
			name: "returns existing share code if already present",
			id:   listID,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, listID).Return(&StudyList{
					ID:         listID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "My List",
					Visibility: StudyListVisibilityShared,
					ShareCode:  "existingcode",
				}, nil)
			},
			wantCode: true,
		},
		{
			name: "returns error for non-existent list",
			id:   uuid.Must(uuid.NewV7()),
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			wantErr: ErrStudyListNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestStudyListEnhancedService()
			tc.setupMocks(d)

			code, err := d.svc.GenerateShareCode(context.Background(), tc.id)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, code)
			if tc.wantCode {
				assert.True(t, len(code) >= 12, "share code should be at least 12 characters")
			}
			d.repo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStudyListEnhancedService_DeleteStudyList
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_DeleteStudyList(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	listID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupMocks func(d testStudyListEnhancedDeps)
		wantErr    error
	}{
		{
			name: "soft-deletes study list",
			id:   listID,
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, listID).Return(&StudyList{
					ID:         listID,
					TenantID:   tenantID,
					GCID:       gcid,
					Title:      "To Delete",
					Visibility: StudyListVisibilityPrivate,
				}, nil)
				d.repo.On("SoftDelete", mock.Anything, listID).Return(nil)
				d.publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "returns error for non-existent list",
			id:   uuid.Must(uuid.NewV7()),
			setupMocks: func(d testStudyListEnhancedDeps) {
				d.repo.On("GetByID", mock.Anything, mock.Anything).Return(nil, ErrStudyListNotFound)
			},
			wantErr: ErrStudyListNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestStudyListEnhancedService()
			tc.setupMocks(d)

			err := d.svc.DeleteStudyList(context.Background(), tc.id)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			d.repo.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}
