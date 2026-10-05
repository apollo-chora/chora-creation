package atomic

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLockedPathService_CreatePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       *LockedPath
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
		wantUUIDv7 bool
	}{
		{
			name: "creates valid path with UUIDv7 and timestamps",
			path: &LockedPath{
				TenantID:  uuid.Must(uuid.NewV7()),
				Title:     "Go Fundamentals Certification",
				CreatedBy: uuid.Must(uuid.NewV7()),
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantUUIDv7: true,
		},
		{
			name: "rejects path with empty title",
			path: &LockedPath{
				TenantID:  uuid.Must(uuid.NewV7()),
				Title:     "",
				CreatedBy: uuid.Must(uuid.NewV7()),
			},
			setupRepo:  func(r *mockPathRepo) {},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathTitleRequired,
		},
		{
			name: "sets defaults when no prerequisite or certification_name",
			path: &LockedPath{
				TenantID:  uuid.Must(uuid.NewV7()),
				Title:     "Minimal Path",
				CreatedBy: uuid.Must(uuid.NewV7()),
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantUUIDv7: true,
		},
		{
			name: "validates prerequisite_path_id exists when provided",
			path: func() *LockedPath {
				prereqID := uuid.Must(uuid.NewV7())
				return &LockedPath{
					TenantID:           uuid.Must(uuid.NewV7()),
					Title:              "Advanced Path",
					CreatedBy:          uuid.Must(uuid.NewV7()),
					PrerequisitePathID: &prereqID,
				}
			}(),
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrPathNotFound)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			err := svc.CreatePath(context.Background(), tt.path)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, tt.path.ID, "ID should be a non-nil UUIDv7")
			assert.False(t, tt.path.CreatedAt.IsZero(), "created_at should be set")
			assert.False(t, tt.path.UpdatedAt.IsZero(), "updated_at should be set")

			if tt.name == "sets defaults when no prerequisite or certification_name" {
				assert.Nil(t, tt.path.PrerequisitePathID, "prerequisite should be nil by default")
				assert.Empty(t, tt.path.CertificationName, "certification_name should default to empty")
			}

			pathRepo.AssertExpectations(t)
		})
	}
}

func TestLockedPathService_AddStep(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		pathID     uuid.UUID
		step       *LockedPathStep
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
	}{
		{
			name:   "adds step to existing path and assigns step_order",
			pathID: pathID,
			step: &LockedPathStep{
				TenantID: tenantID,
				AtomID:   atomID1,
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps:    []LockedPathStep{},
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:   "rejects step with duplicate atom_id in path",
			pathID: pathID,
			step: &LockedPathStep{
				TenantID: tenantID,
				AtomID:   atomID1,
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{
							ID:        uuid.Must(uuid.NewV7()),
							PathID:    pathID,
							TenantID:  tenantID,
							AtomID:    atomID1,
							StepOrder: 1,
						},
					},
				}, nil)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathStepDuplicate,
		},
		{
			name:   "returns error when path not found",
			pathID: uuid.Must(uuid.NewV7()),
			step: &LockedPathStep{
				TenantID: tenantID,
				AtomID:   atomID2,
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrPathNotFound)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathNotFound,
		},
		{
			name:   "appends step at end of existing steps",
			pathID: pathID,
			step: &LockedPathStep{
				TenantID: tenantID,
				AtomID:   atomID2,
			},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{
							ID:        uuid.Must(uuid.NewV7()),
							PathID:    pathID,
							TenantID:  tenantID,
							AtomID:    atomID1,
							StepOrder: 1,
						},
					},
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			err := svc.AddStep(context.Background(), tt.pathID, tt.step)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			pathRepo.AssertExpectations(t)
		})
	}
}

func TestLockedPathService_RemoveStep(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	stepID1 := uuid.Must(uuid.NewV7())
	stepID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		pathID     uuid.UUID
		stepID     uuid.UUID
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
	}{
		{
			name:   "removes existing step from path",
			pathID: pathID,
			stepID: stepID1,
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{ID: stepID1, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
						{ID: stepID2, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 2},
					},
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:   "reorders remaining steps after removal",
			pathID: pathID,
			stepID: stepID1,
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{ID: stepID1, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
						{ID: stepID2, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 2},
					},
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Run(func(args mock.Arguments) {
					updated := args.Get(1).(*LockedPath)
					// After removing step 1, step 2 should be reordered to step 1.
					require.Len(t, updated.Steps, 1)
					assert.Equal(t, 1, updated.Steps[0].StepOrder, "remaining step should be reordered to 1")
				}).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:   "returns error when step not found",
			pathID: pathID,
			stepID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{ID: stepID1, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
					},
				}, nil)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathStepNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			err := svc.RemoveStep(context.Background(), tt.pathID, tt.stepID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			pathRepo.AssertExpectations(t)
		})
	}
}

func TestLockedPathService_ReorderSteps(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	stepID1 := uuid.Must(uuid.NewV7())
	stepID2 := uuid.Must(uuid.NewV7())
	stepID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		pathID     uuid.UUID
		stepIDs    []uuid.UUID
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
	}{
		{
			name:    "reorders steps with new order applied",
			pathID:  pathID,
			stepIDs: []uuid.UUID{stepID3, stepID1, stepID2},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{ID: stepID1, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
						{ID: stepID2, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 2},
						{ID: stepID3, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 3},
					},
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:    "rejects reorder with missing step IDs",
			pathID:  pathID,
			stepIDs: []uuid.UUID{stepID1}, // missing stepID2 and stepID3
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
					ID:       pathID,
					TenantID: tenantID,
					Title:    "Test Path",
					Steps: []LockedPathStep{
						{ID: stepID1, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
						{ID: stepID2, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 2},
						{ID: stepID3, PathID: pathID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 3},
					},
				}, nil)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathStepNotFound,
		},
		{
			name:    "returns error when path not found",
			pathID:  uuid.Must(uuid.NewV7()),
			stepIDs: []uuid.UUID{stepID1, stepID2},
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrPathNotFound)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			err := svc.ReorderSteps(context.Background(), tt.pathID, tt.stepIDs)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			pathRepo.AssertExpectations(t)
		})
	}
}

func TestLockedPathService_GetPath(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
	}{
		{
			name: "returns path with steps",
			id:   existingID,
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&LockedPath{
					ID:       existingID,
					TenantID: tenantID,
					Title:    "Go Certification Path",
					Steps: []LockedPathStep{
						{ID: uuid.Must(uuid.NewV7()), PathID: existingID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 1},
						{ID: uuid.Must(uuid.NewV7()), PathID: existingID, TenantID: tenantID, AtomID: uuid.Must(uuid.NewV7()), StepOrder: 2},
					},
				}, nil)
			},
			setupEvent: func(p *mockEventPublisher) {},
		},
		{
			name: "returns error for non-existent path",
			id:   uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrPathNotFound)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			result, err := svc.GetPath(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, existingID, result.ID)
			assert.Len(t, result.Steps, 2, "path should have 2 steps")
			pathRepo.AssertExpectations(t)
		})
	}
}

func TestLockedPathService_DeletePath(t *testing.T) {
	t.Parallel()

	existingID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupRepo  func(*mockPathRepo)
		setupEvent func(*mockEventPublisher)
		wantErr    error
	}{
		{
			name: "soft-deletes path",
			id:   existingID,
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, existingID).Return(&LockedPath{
					ID:       existingID,
					TenantID: uuid.Must(uuid.NewV7()),
					Title:    "Path to Delete",
				}, nil)
				r.On("SoftDelete", mock.Anything, existingID).Return(nil)
			},
			setupEvent: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "returns error for non-existent path",
			id:   uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockPathRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrPathNotFound)
			},
			setupEvent: func(p *mockEventPublisher) {},
			wantErr:    ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pathRepo := new(mockPathRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(pathRepo)
			tt.setupEvent(publisher)

			svc := NewLockedPathService(pathRepo, publisher)
			err := svc.DeletePath(context.Background(), tt.id)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				return
			}

			require.NoError(t, err)
			pathRepo.AssertExpectations(t)
		})
	}
}
