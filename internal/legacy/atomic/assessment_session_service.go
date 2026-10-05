package atomic

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ValidStructureModes is the set of all valid structure modes.
var ValidStructureModes = map[StructureMode]bool{
	StructureModeFlat:              true,
	StructureModeSectionsOnly:      true,
	StructureModePapersOnly:        true,
	StructureModePapersAndSections: true,
}

// AssessmentSessionService manages assessment session lifecycle.
type AssessmentSessionService struct {
	sessions     AssessmentSessionRepository
	interactions AtomInteractionRepository
	events       EventPublisher
}

// NewAssessmentSessionService creates a new AssessmentSessionService with the
// required port dependencies.
func NewAssessmentSessionService(
	sessions AssessmentSessionRepository,
	interactions AtomInteractionRepository,
	events EventPublisher,
) *AssessmentSessionService {
	return &AssessmentSessionService{
		sessions:     sessions,
		interactions: interactions,
		events:       events,
	}
}

// CreateSession validates and persists a new AssessmentSession.
func (s *AssessmentSessionService) CreateSession(ctx context.Context, session *AssessmentSession) error {
	// Validate session type.
	if !session.SessionType.IsValid() {
		return fmt.Errorf("invalid session type %q: %w", session.SessionType, ErrInvalidSessionType)
	}

	// Validate structure mode.
	if !ValidStructureModes[session.StructureMode] {
		return fmt.Errorf("invalid structure mode %q: %w", session.StructureMode, ErrInvalidStructureMode)
	}

	// Reject empty atom_ids.
	if len(session.AtomIDs) == 0 {
		return fmt.Errorf("atom_ids must not be empty: %w", ErrInteractionInvalid)
	}

	// Assign UUIDv7.
	session.ID = uuid.Must(uuid.NewV7())

	// Set initial status.
	session.Status = SessionStatusNotStarted

	// Set defaults for navigation if not specified.
	if session.DeliveryMode == "" {
		session.DeliveryMode = DeliveryModeStraightUp
	}
	if session.PaperNavigation == "" {
		session.PaperNavigation = NavigationModeFree
	}
	if session.SectionNavigation == "" {
		session.SectionNavigation = NavigationModeFree
	}

	// Set timestamps.
	now := time.Now().UTC()
	session.CreatedAt = now
	session.UpdatedAt = now

	// Persist the session.
	if err := s.sessions.Save(ctx, session); err != nil {
		return fmt.Errorf("save assessment session: %w", err)
	}

	// Publish event (fire-and-forget).
	event := NewDomainEvent(
		"assessment.created",
		session.TenantID,
		session.ID,
		AggregateTypeAssessmentSession,
		map[string]any{
			"session_id":   session.ID.String(),
			"tenant_id":    session.TenantID.String(),
			"gcid":         session.GCID.String(),
			"session_type": string(session.SessionType),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// GetSession retrieves an AssessmentSession by ID.
func (s *AssessmentSessionService) GetSession(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	session, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get assessment session: %w", err)
	}
	return session, nil
}

// ListByLearner retrieves all sessions for a learner.
func (s *AssessmentSessionService) ListByLearner(ctx context.Context, gcid uuid.UUID) ([]*AssessmentSession, error) {
	sessions, err := s.sessions.ListByLearner(ctx, gcid)
	if err != nil {
		return nil, fmt.Errorf("list sessions by learner: %w", err)
	}
	return sessions, nil
}

// StartSession transitions a session from not_started to active, or resumes
// a paused session.
func (s *AssessmentSessionService) StartSession(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	session, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get session for start: %w", err)
	}

	switch session.Status {
	case SessionStatusNotStarted:
		session.Status = SessionStatusActive
		now := time.Now().UTC()
		session.StartedAt = &now
	case SessionStatusPaused:
		session.Status = SessionStatusActive
		// If StartedAt was never set (edge case), set it now on resume.
		if session.StartedAt == nil {
			now := time.Now().UTC()
			session.StartedAt = &now
		}
	case SessionStatusActive:
		return nil, ErrSessionAlreadyStarted
	case SessionStatusSubmitted, SessionStatusGraded:
		return nil, ErrSessionAlreadySubmitted
	default:
		return nil, fmt.Errorf("unexpected session status %q: %w", session.Status, ErrSessionNotActive)
	}

	session.UpdatedAt = time.Now().UTC()
	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("update session for start: %w", err)
	}

	// Publish event.
	event := NewDomainEvent(
		EventTypeAssessmentStarted,
		session.TenantID,
		session.ID,
		AggregateTypeAssessmentSession,
		map[string]any{
			"session_id": session.ID.String(),
			"tenant_id":  session.TenantID.String(),
			"gcid":       session.GCID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return session, nil
}

// PauseSession transitions an active session to paused.
func (s *AssessmentSessionService) PauseSession(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	session, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get session for pause: %w", err)
	}

	switch session.Status {
	case SessionStatusActive:
		// OK — can pause.
	case SessionStatusSubmitted, SessionStatusGraded:
		return nil, ErrSessionAlreadySubmitted
	default:
		return nil, ErrSessionNotActive
	}

	session.Status = SessionStatusPaused
	session.PauseCount++
	session.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("update session for pause: %w", err)
	}

	// Publish event (fire-and-forget).
	event := NewDomainEvent(
		"assessment.paused",
		session.TenantID,
		session.ID,
		AggregateTypeAssessmentSession,
		map[string]any{
			"session_id":  session.ID.String(),
			"tenant_id":   session.TenantID.String(),
			"pause_count": session.PauseCount,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return session, nil
}

// SubmitSession transitions a session to submitted.
func (s *AssessmentSessionService) SubmitSession(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	session, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get session for submit: %w", err)
	}

	switch session.Status {
	case SessionStatusActive:
		// OK — can submit.
	case SessionStatusSubmitted, SessionStatusGraded:
		return nil, ErrSessionAlreadySubmitted
	default:
		return nil, ErrSessionNotActive
	}

	now := time.Now().UTC()
	session.Status = SessionStatusSubmitted
	session.SubmittedAt = &now
	session.UpdatedAt = now

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("update session for submit: %w", err)
	}

	// Publish event.
	event := NewDomainEvent(
		EventTypeAssessmentSubmitted,
		session.TenantID,
		session.ID,
		AggregateTypeAssessmentSession,
		map[string]any{
			"session_id": session.ID.String(),
			"tenant_id":  session.TenantID.String(),
			"gcid":       session.GCID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return session, nil
}

// GradeSession grades a submitted session.
func (s *AssessmentSessionService) GradeSession(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	session, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get session for grade: %w", err)
	}

	switch session.Status {
	case SessionStatusSubmitted:
		// OK — can grade.
	case SessionStatusGraded:
		return nil, ErrSessionAlreadySubmitted
	default:
		return nil, ErrSessionNotActive
	}

	// Determine grading mode from config.
	gradingMode := GradingModeDeterministic
	if cfg, ok := session.CombinedGradeConfig["grading_mode"]; ok {
		if mode, ok := cfg.(string); ok {
			gradingMode = GradingMode(mode)
		}
	}

	// For deterministic mode, calculate scores from interactions.
	if gradingMode == GradingModeDeterministic {
		interactions, err := s.interactions.ListBySession(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("list interactions for grading: %w", err)
		}

		var totalScore float64
		var count int
		for _, interaction := range interactions {
			if interaction.Score != nil {
				totalScore += *interaction.Score
				count++
			}
		}

		if count > 0 {
			avgScore := totalScore / float64(count)
			if session.CombinedGradeConfig == nil {
				session.CombinedGradeConfig = make(map[string]any)
			}
			session.CombinedGradeConfig["calculated_score"] = avgScore
			session.CombinedGradeConfig["interactions_count"] = count
		}
	}
	// For manual mode, just transition the status.

	now := time.Now().UTC()
	session.Status = SessionStatusGraded
	session.GradedAt = &now
	session.UpdatedAt = now

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("update session for grade: %w", err)
	}

	// Publish event.
	event := NewDomainEvent(
		EventTypeAssessmentGraded,
		session.TenantID,
		session.ID,
		AggregateTypeAssessmentSession,
		map[string]any{
			"session_id": session.ID.String(),
			"tenant_id":  session.TenantID.String(),
			"gcid":       session.GCID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return session, nil
}

// RecordInteraction appends an AtomInteraction to a session.
func (s *AssessmentSessionService) RecordInteraction(ctx context.Context, interaction *AtomInteraction) error {
	// Validate attempt_number.
	if interaction.AttemptNumber < 1 {
		return fmt.Errorf("attempt_number must be >= 1: %w", ErrInteractionInvalid)
	}

	// Validate that the session exists and is active.
	_, err := s.sessions.GetByID(ctx, interaction.SessionID)
	if err != nil {
		return fmt.Errorf("get session for interaction: %w", err)
	}

	// Assign UUIDv7 and timestamp.
	interaction.ID = uuid.Must(uuid.NewV7())
	interaction.CreatedAt = time.Now().UTC()

	// Append the interaction (immutable, append-only).
	if err := s.interactions.Append(ctx, interaction); err != nil {
		return fmt.Errorf("append interaction: %w", err)
	}

	// Publish event (fire-and-forget).
	event := NewDomainEvent(
		EventTypeInteractionRecorded,
		interaction.TenantID,
		interaction.ID,
		AggregateTypeAtomInteraction,
		map[string]any{
			"interaction_id": interaction.ID.String(),
			"session_id":     interaction.SessionID.String(),
			"atom_id":        interaction.AtomID.String(),
			"tenant_id":      interaction.TenantID.String(),
			"gcid":           interaction.GCID.String(),
			"is_correct":     interaction.IsCorrect,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// ListInteractions retrieves all interactions for a session.
func (s *AssessmentSessionService) ListInteractions(ctx context.Context, sessionID uuid.UUID) ([]*AtomInteraction, error) {
	interactions, err := s.interactions.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list interactions by session: %w", err)
	}
	return interactions, nil
}
