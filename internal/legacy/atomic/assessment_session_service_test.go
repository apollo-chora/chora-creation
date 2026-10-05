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

// ---------------------------------------------------------------------------
// Helper: build a service wired to fresh mocks
// ---------------------------------------------------------------------------

func newSessionTestService() (*AssessmentSessionService, *mockSessionRepo, *mockInteractionRepo, *mockEventPublisher) {
	sessionRepo := new(mockSessionRepo)
	interactionRepo := new(mockInteractionRepo)
	eventPub := new(mockEventPublisher)
	svc := NewAssessmentSessionService(sessionRepo, interactionRepo, eventPub)
	return svc, sessionRepo, interactionRepo, eventPub
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_CreateSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_CreateSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())
	atomID2 := uuid.Must(uuid.NewV7())
	revID1 := uuid.Must(uuid.NewV7())
	revID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		session     *AssessmentSession
		setupRepo   func(*mockSessionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
		wantStatus  SessionStatus
		wantUUIDv7  bool
	}{
		{
			name: "creates valid flat session with UUIDv7 and not_started status",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionTypePracticeSet,
				DeliveryMode:    DeliveryModeStraightUp,
				StructureMode:   StructureModeFlat,
				AtomIDs:         []uuid.UUID{atomID1, atomID2},
				AtomRevisionIDs: []uuid.UUID{revID1, revID2},
			},
			setupRepo: func(r *mockSessionRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusNotStarted,
			wantUUIDv7: true,
		},
		{
			name: "creates valid papers_and_sections session with nested papers and sections",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionTypeStraightUpExam,
				DeliveryMode:    DeliveryModeStraightUp,
				StructureMode:   StructureModePapersAndSections,
				AtomIDs:         []uuid.UUID{atomID1, atomID2},
				AtomRevisionIDs: []uuid.UUID{revID1, revID2},
				Papers: []AssessmentPaper{
					{
						TenantID:        tenantID,
						PaperNumber:     1,
						Title:           "Paper 1",
						TotalPoints:     50,
						AtomIDs:         []uuid.UUID{atomID1},
						AtomRevisionIDs: []uuid.UUID{revID1},
					},
				},
				Sections: []AssessmentSection{
					{
						TenantID:        tenantID,
						SectionNumber:   1,
						Title:           "Section A",
						TotalPoints:     25,
						AtomIDs:         []uuid.UUID{atomID1},
						AtomRevisionIDs: []uuid.UUID{revID1},
					},
				},
			},
			setupRepo: func(r *mockSessionRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusNotStarted,
			wantUUIDv7: true,
		},
		{
			name: "creates valid atom_playlist session",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionTypeAtomPlaylist,
				DeliveryMode:    DeliveryModeGraphDiscovery,
				StructureMode:   StructureModeFlat,
				AtomIDs:         []uuid.UUID{atomID1},
				AtomRevisionIDs: []uuid.UUID{revID1},
			},
			setupRepo: func(r *mockSessionRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusNotStarted,
			wantUUIDv7: true,
		},
		{
			name: "rejects invalid session type",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionType("invalid_type"),
				DeliveryMode:    DeliveryModeStraightUp,
				StructureMode:   StructureModeFlat,
				AtomIDs:         []uuid.UUID{atomID1},
				AtomRevisionIDs: []uuid.UUID{revID1},
			},
			setupRepo:   func(r *mockSessionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrInvalidSessionType,
		},
		{
			name: "rejects invalid structure mode",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionTypePracticeSet,
				DeliveryMode:    DeliveryModeStraightUp,
				StructureMode:   StructureMode("invalid_mode"),
				AtomIDs:         []uuid.UUID{atomID1},
				AtomRevisionIDs: []uuid.UUID{revID1},
			},
			setupRepo:   func(r *mockSessionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrInvalidStructureMode,
		},
		{
			name: "rejects session with empty atom_ids",
			session: &AssessmentSession{
				TenantID:      tenantID,
				GCID:          gcid,
				SessionType:   SessionTypePracticeSet,
				DeliveryMode:  DeliveryModeStraightUp,
				StructureMode: StructureModeFlat,
				AtomIDs:       []uuid.UUID{},
			},
			setupRepo:   func(r *mockSessionRepo) {},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrInteractionInvalid,
		},
		{
			name: "pins atom_revision_ids at creation time",
			session: &AssessmentSession{
				TenantID:        tenantID,
				GCID:            gcid,
				SessionType:     SessionTypeMockExam,
				DeliveryMode:    DeliveryModeStraightUp,
				StructureMode:   StructureModeFlat,
				AtomIDs:         []uuid.UUID{atomID1, atomID2},
				AtomRevisionIDs: []uuid.UUID{revID1, revID2},
			},
			setupRepo: func(r *mockSessionRepo) {
				r.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusNotStarted,
			wantUUIDv7: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, _, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupEvents(eventPub)

			err := svc.CreateSession(context.Background(), tt.session)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, tt.session.ID, "ID should be a non-nil UUIDv7")
			assert.Equal(t, tt.wantStatus, tt.session.Status, "new session should start as not_started")
			assert.False(t, tt.session.CreatedAt.IsZero(), "created_at should be set")
			assert.False(t, tt.session.UpdatedAt.IsZero(), "updated_at should be set")

			if tt.name == "pins atom_revision_ids at creation time" {
				assert.Len(t, tt.session.AtomRevisionIDs, 2, "revision IDs should be pinned")
				assert.Equal(t, revID1, tt.session.AtomRevisionIDs[0])
				assert.Equal(t, revID2, tt.session.AtomRevisionIDs[1])
			}

			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_StartSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_StartSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		sessionID   uuid.UUID
		setupRepo   func(*mockSessionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
		wantStatus  SessionStatus
	}{
		{
			name:      "starts a not_started session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:            uuid.Must(uuid.NewV7()),
					TenantID:      tenantID,
					GCID:          gcid,
					SessionType:   SessionTypePracticeSet,
					StructureMode: StructureModeFlat,
					Status:        SessionStatusNotStarted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusActive,
		},
		{
			name:      "resumes a paused session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:         uuid.Must(uuid.NewV7()),
					TenantID:   tenantID,
					GCID:       gcid,
					Status:     SessionStatusPaused,
					PauseCount: 2,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusActive,
		},
		{
			name:      "rejects starting an already active session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusActive,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionAlreadyStarted,
		},
		{
			name:      "rejects starting an already submitted session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusSubmitted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionAlreadySubmitted,
		},
		{
			name:      "returns error when session not found",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrSessionNotFound)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, _, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupEvents(eventPub)

			result, err := svc.StartSession(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status, "session should be active")
			assert.NotNil(t, result.StartedAt, "started_at should be set")
			assert.False(t, result.StartedAt.IsZero(), "started_at should not be zero")

			if tt.name == "resumes a paused session" {
				assert.Equal(t, 2, result.PauseCount, "pause_count should remain unchanged on resume")
			}

			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_PauseSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_PauseSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	startedAt := time.Now().UTC()

	tests := []struct {
		name        string
		sessionID   uuid.UUID
		setupRepo   func(*mockSessionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
		wantStatus  SessionStatus
		wantPause   int
	}{
		{
			name:      "pauses an active session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:         uuid.Must(uuid.NewV7()),
					TenantID:   tenantID,
					GCID:       gcid,
					Status:     SessionStatusActive,
					StartedAt:  &startedAt,
					PauseCount: 0,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusPaused,
			wantPause:  1,
		},
		{
			name:      "rejects pausing a not_started session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusNotStarted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionNotActive,
		},
		{
			name:      "rejects pausing a submitted session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusSubmitted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionAlreadySubmitted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, _, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupEvents(eventPub)

			result, err := svc.PauseSession(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status, "session should be paused")
			assert.Equal(t, tt.wantPause, result.PauseCount, "pause_count should be incremented")
			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_SubmitSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_SubmitSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	startedAt := time.Now().UTC()

	tests := []struct {
		name        string
		sessionID   uuid.UUID
		setupRepo   func(*mockSessionRepo)
		setupEvents func(*mockEventPublisher)
		wantErr     error
		wantStatus  SessionStatus
	}{
		{
			name:      "submits an active session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:        uuid.Must(uuid.NewV7()),
					TenantID:  tenantID,
					GCID:      gcid,
					Status:    SessionStatusActive,
					StartedAt: &startedAt,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusSubmitted,
		},
		{
			name:      "rejects submitting a not_started session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusNotStarted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionNotActive,
		},
		{
			name:      "rejects submitting an already submitted session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusSubmitted,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupEvents: func(p *mockEventPublisher) {},
			wantErr:     ErrSessionAlreadySubmitted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, _, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupEvents(eventPub)

			result, err := svc.SubmitSession(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status, "session should be submitted")
			assert.NotNil(t, result.SubmittedAt, "submitted_at should be set")
			assert.False(t, result.SubmittedAt.IsZero(), "submitted_at should not be zero")
			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_GradeSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_GradeSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	startedAt := time.Now().UTC()
	submittedAt := time.Now().UTC()

	tests := []struct {
		name             string
		sessionID        uuid.UUID
		setupRepo        func(*mockSessionRepo)
		setupInteraction func(*mockInteractionRepo)
		setupEvents      func(*mockEventPublisher)
		wantErr          error
		wantStatus       SessionStatus
	}{
		{
			name:      "grades a submitted session with deterministic mode",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:          uuid.Must(uuid.NewV7()),
					TenantID:    tenantID,
					GCID:        gcid,
					SessionType: SessionTypePracticeSet,
					Status:      SessionStatusSubmitted,
					StartedAt:   &startedAt,
					SubmittedAt: &submittedAt,
					CombinedGradeConfig: map[string]any{
						"grading_mode": string(GradingModeDeterministic),
					},
					AtomIDs: []uuid.UUID{uuid.Must(uuid.NewV7())},
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupInteraction: func(r *mockInteractionRepo) {
				score := 1.0
				interactions := []*AtomInteraction{
					{
						ID:            uuid.Must(uuid.NewV7()),
						IsCorrect:     true,
						Score:         &score,
						AttemptNumber: 1,
					},
				}
				r.On("ListBySession", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(interactions, nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusGraded,
		},
		{
			name:      "grades a submitted session with manual mode",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:          uuid.Must(uuid.NewV7()),
					TenantID:    tenantID,
					GCID:        gcid,
					SessionType: SessionTypeStraightUpExam,
					Status:      SessionStatusSubmitted,
					StartedAt:   &startedAt,
					SubmittedAt: &submittedAt,
					CombinedGradeConfig: map[string]any{
						"grading_mode": string(GradingModeManual),
					},
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
			},
			setupInteraction: func(r *mockInteractionRepo) {},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantStatus: SessionStatusGraded,
		},
		{
			name:      "rejects grading an active session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusActive,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupInteraction: func(r *mockInteractionRepo) {},
			setupEvents:      func(p *mockEventPublisher) {},
			wantErr:          ErrSessionNotActive,
		},
		{
			name:      "rejects grading an already graded session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:       uuid.Must(uuid.NewV7()),
					TenantID: tenantID,
					GCID:     gcid,
					Status:   SessionStatusGraded,
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
			setupInteraction: func(r *mockInteractionRepo) {},
			setupEvents:      func(p *mockEventPublisher) {},
			wantErr:          ErrSessionAlreadySubmitted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, interactionRepo, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupInteraction(interactionRepo)
			tt.setupEvents(eventPub)

			result, err := svc.GradeSession(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status, "session should be graded")
			assert.NotNil(t, result.GradedAt, "graded_at should be set")
			assert.False(t, result.GradedAt.IsZero(), "graded_at should not be zero")
			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_RecordInteraction
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_RecordInteraction(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	atomID := uuid.Must(uuid.NewV7())
	revisionID := uuid.Must(uuid.NewV7())
	startedAt := time.Now().UTC()

	tests := []struct {
		name             string
		interaction      *AtomInteraction
		setupRepo        func(*mockSessionRepo)
		setupInteraction func(*mockInteractionRepo)
		setupEvents      func(*mockEventPublisher)
		wantErr          error
	}{
		{
			name: "records a valid atom interaction",
			interaction: &AtomInteraction{
				TenantID:      tenantID,
				GCID:          gcid,
				SessionID:     sessionID,
				AtomID:        atomID,
				RevisionID:    revisionID,
				Answer:        map[string]any{"choice": "A"},
				IsCorrect:     true,
				TimeSpentMs:   5000,
				AttemptNumber: 1,
			},
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:        sessionID,
					TenantID:  tenantID,
					GCID:      gcid,
					Status:    SessionStatusActive,
					StartedAt: &startedAt,
				}
				r.On("GetByID", mock.Anything, sessionID).Return(session, nil)
			},
			setupInteraction: func(r *mockInteractionRepo) {
				r.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomInteraction")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "rejects interaction with invalid session_id",
			interaction: &AtomInteraction{
				TenantID:      tenantID,
				GCID:          gcid,
				SessionID:     uuid.Must(uuid.NewV7()),
				AtomID:        atomID,
				RevisionID:    revisionID,
				Answer:        map[string]any{"choice": "B"},
				AttemptNumber: 1,
			},
			setupRepo: func(r *mockSessionRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrSessionNotFound)
			},
			setupInteraction: func(r *mockInteractionRepo) {},
			setupEvents:      func(p *mockEventPublisher) {},
			wantErr:          ErrSessionNotFound,
		},
		{
			name: "rejects interaction with attempt_number less than 1",
			interaction: &AtomInteraction{
				TenantID:      tenantID,
				GCID:          gcid,
				SessionID:     sessionID,
				AtomID:        atomID,
				RevisionID:    revisionID,
				Answer:        map[string]any{"choice": "C"},
				AttemptNumber: 0,
			},
			setupRepo:        func(r *mockSessionRepo) {},
			setupInteraction: func(r *mockInteractionRepo) {},
			setupEvents:      func(p *mockEventPublisher) {},
			wantErr:          ErrInteractionInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, interactionRepo, eventPub := newSessionTestService()
			tt.setupRepo(sessionRepo)
			tt.setupInteraction(interactionRepo)
			tt.setupEvents(eventPub)

			err := svc.RecordInteraction(context.Background(), tt.interaction)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, tt.interaction.ID, "interaction ID should be a non-nil UUIDv7")
			assert.False(t, tt.interaction.CreatedAt.IsZero(), "created_at should be set")
			interactionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_GetSession
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_GetSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	atomID1 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name      string
		sessionID uuid.UUID
		setupRepo func(*mockSessionRepo)
		wantErr   error
	}{
		{
			name:      "returns session with papers and sections",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				session := &AssessmentSession{
					ID:            uuid.Must(uuid.NewV7()),
					TenantID:      tenantID,
					GCID:          gcid,
					SessionType:   SessionTypeStraightUpExam,
					StructureMode: StructureModePapersAndSections,
					Status:        SessionStatusNotStarted,
					AtomIDs:       []uuid.UUID{atomID1},
					Papers: []AssessmentPaper{
						{
							ID:          uuid.Must(uuid.NewV7()),
							TenantID:    tenantID,
							PaperNumber: 1,
							Title:       "Paper 1",
							TotalPoints: 100,
						},
					},
					Sections: []AssessmentSection{
						{
							ID:            uuid.Must(uuid.NewV7()),
							TenantID:      tenantID,
							SectionNumber: 1,
							Title:         "Section A",
							TotalPoints:   50,
						},
					},
				}
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(session, nil)
			},
		},
		{
			name:      "returns error for non-existent session",
			sessionID: uuid.Must(uuid.NewV7()),
			setupRepo: func(r *mockSessionRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, sessionRepo, _, _ := newSessionTestService()
			tt.setupRepo(sessionRepo)

			result, err := svc.GetSession(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.NotEmpty(t, result.Papers, "should include papers")
			assert.NotEmpty(t, result.Sections, "should include sections")
			sessionRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAssessmentSessionService_ListInteractions
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_ListInteractions(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name             string
		sessionID        uuid.UUID
		setupInteraction func(*mockInteractionRepo)
		wantCount        int
		wantErr          error
	}{
		{
			name:      "returns interactions for a session",
			sessionID: sessionID,
			setupInteraction: func(r *mockInteractionRepo) {
				score1 := 1.0
				score2 := 0.5
				interactions := []*AtomInteraction{
					{
						ID:            uuid.Must(uuid.NewV7()),
						TenantID:      tenantID,
						GCID:          gcid,
						SessionID:     sessionID,
						AtomID:        uuid.Must(uuid.NewV7()),
						RevisionID:    uuid.Must(uuid.NewV7()),
						IsCorrect:     true,
						Score:         &score1,
						AttemptNumber: 1,
						TimeSpentMs:   3000,
						CreatedAt:     time.Now().UTC(),
					},
					{
						ID:            uuid.Must(uuid.NewV7()),
						TenantID:      tenantID,
						GCID:          gcid,
						SessionID:     sessionID,
						AtomID:        uuid.Must(uuid.NewV7()),
						RevisionID:    uuid.Must(uuid.NewV7()),
						IsCorrect:     false,
						Score:         &score2,
						AttemptNumber: 1,
						TimeSpentMs:   7500,
						CreatedAt:     time.Now().UTC(),
					},
				}
				r.On("ListBySession", mock.Anything, sessionID).Return(interactions, nil)
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, _, interactionRepo, _ := newSessionTestService()
			tt.setupInteraction(interactionRepo)

			result, err := svc.ListInteractions(context.Background(), tt.sessionID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, result, tt.wantCount, "should return expected number of interactions")
			interactionRepo.AssertExpectations(t)
		})
	}
}
