package atomic

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ AtomRepository     = (*mockAtomRepo)(nil)
	_ RevisionRepository = (*mockRevisionRepo)(nil)
	_ TopicRepository    = (*mockTopicRepo)(nil)
	_ EventPublisher     = (*mockEventPublisher)(nil)

	_ AssessmentSessionRepository = (*mockSessionRepo)(nil)
	_ AtomInteractionRepository   = (*mockInteractionRepo)(nil)
	_ LockedPathRepository        = (*mockPathRepo)(nil)
	_ StudyListRepository         = (*mockStudyListRepo)(nil)

	_ AnswerValidationRuleRepository = (*mockValidationRuleRepo)(nil)
	_ PresentationLayoutRepository   = (*mockLayoutRepo)(nil)
	_ ImportJobRepository            = (*mockImportJobRepo)(nil)
)

// ---------------------------------------------------------------------------
// mockAtomRepo
// ---------------------------------------------------------------------------

type mockAtomRepo struct{ mock.Mock }

func (m *mockAtomRepo) GetByID(ctx context.Context, id uuid.UUID) (*LearningAtom, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LearningAtom), args.Error(1)
}

func (m *mockAtomRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*LearningAtom, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*LearningAtom), args.Error(1)
}

func (m *mockAtomRepo) ListByTopic(ctx context.Context, topicID uuid.UUID) ([]*LearningAtom, error) {
	args := m.Called(ctx, topicID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*LearningAtom), args.Error(1)
}

func (m *mockAtomRepo) Save(ctx context.Context, atom *LearningAtom) error {
	return m.Called(ctx, atom).Error(0)
}

func (m *mockAtomRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockRevisionRepo
// ---------------------------------------------------------------------------

type mockRevisionRepo struct{ mock.Mock }

func (m *mockRevisionRepo) GetByAtomID(ctx context.Context, atomID uuid.UUID) ([]*AtomRevision, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AtomRevision), args.Error(1)
}

func (m *mockRevisionRepo) GetByID(ctx context.Context, id uuid.UUID) (*AtomRevision, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AtomRevision), args.Error(1)
}

func (m *mockRevisionRepo) GetLatest(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AtomRevision), args.Error(1)
}

func (m *mockRevisionRepo) GetLatestPublished(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AtomRevision), args.Error(1)
}

func (m *mockRevisionRepo) GetDraft(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AtomRevision), args.Error(1)
}

func (m *mockRevisionRepo) Append(ctx context.Context, revision *AtomRevision) error {
	return m.Called(ctx, revision).Error(0)
}

func (m *mockRevisionRepo) UpdateVisibility(ctx context.Context, id uuid.UUID, status RevisionVisibility, publishedAt *time.Time) error {
	return m.Called(ctx, id, status, publishedAt).Error(0)
}

// ---------------------------------------------------------------------------
// mockTopicRepo
// ---------------------------------------------------------------------------

type mockTopicRepo struct{ mock.Mock }

func (m *mockTopicRepo) GetByID(ctx context.Context, id uuid.UUID) (*TopicNode, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TopicNode), args.Error(1)
}

func (m *mockTopicRepo) GetTree(ctx context.Context, tenantID uuid.UUID) ([]*TopicNode, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*TopicNode), args.Error(1)
}

func (m *mockTopicRepo) Save(ctx context.Context, topic *TopicNode) error {
	return m.Called(ctx, topic).Error(0)
}

func (m *mockTopicRepo) Move(ctx context.Context, id uuid.UUID, newParentID *uuid.UUID) error {
	return m.Called(ctx, id, newParentID).Error(0)
}

func (m *mockTopicRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockEventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event DomainEvent) error {
	return m.Called(ctx, topic, event).Error(0)
}

// ---------------------------------------------------------------------------
// mockSessionRepo
// ---------------------------------------------------------------------------

type mockSessionRepo struct{ mock.Mock }

func (m *mockSessionRepo) GetByID(ctx context.Context, id uuid.UUID) (*AssessmentSession, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AssessmentSession), args.Error(1)
}

func (m *mockSessionRepo) ListByLearner(ctx context.Context, gcid uuid.UUID) ([]*AssessmentSession, error) {
	args := m.Called(ctx, gcid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AssessmentSession), args.Error(1)
}

func (m *mockSessionRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*AssessmentSession, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AssessmentSession), args.Error(1)
}

func (m *mockSessionRepo) Save(ctx context.Context, session *AssessmentSession) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) Update(ctx context.Context, session *AssessmentSession) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockInteractionRepo
// ---------------------------------------------------------------------------

type mockInteractionRepo struct{ mock.Mock }

func (m *mockInteractionRepo) Append(ctx context.Context, interaction *AtomInteraction) error {
	return m.Called(ctx, interaction).Error(0)
}

func (m *mockInteractionRepo) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]*AtomInteraction, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AtomInteraction), args.Error(1)
}

func (m *mockInteractionRepo) ListByLearner(ctx context.Context, gcid uuid.UUID, atomID uuid.UUID) ([]*AtomInteraction, error) {
	args := m.Called(ctx, gcid, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AtomInteraction), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockPathRepo
// ---------------------------------------------------------------------------

type mockPathRepo struct{ mock.Mock }

func (m *mockPathRepo) GetByID(ctx context.Context, id uuid.UUID) (*LockedPath, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LockedPath), args.Error(1)
}

func (m *mockPathRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*LockedPath, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*LockedPath), args.Error(1)
}

func (m *mockPathRepo) Save(ctx context.Context, path *LockedPath) error {
	return m.Called(ctx, path).Error(0)
}

func (m *mockPathRepo) Update(ctx context.Context, path *LockedPath) error {
	return m.Called(ctx, path).Error(0)
}

func (m *mockPathRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockStudyListRepo
// ---------------------------------------------------------------------------

type mockStudyListRepo struct{ mock.Mock }

func (m *mockStudyListRepo) GetByID(ctx context.Context, id uuid.UUID) (*StudyList, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*StudyList), args.Error(1)
}

func (m *mockStudyListRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*StudyList, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*StudyList), args.Error(1)
}

func (m *mockStudyListRepo) GetByShareCode(ctx context.Context, shareCode string) (*StudyList, error) {
	args := m.Called(ctx, shareCode)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*StudyList), args.Error(1)
}

func (m *mockStudyListRepo) Save(ctx context.Context, list *StudyList) error {
	return m.Called(ctx, list).Error(0)
}

func (m *mockStudyListRepo) Update(ctx context.Context, list *StudyList) error {
	return m.Called(ctx, list).Error(0)
}

func (m *mockStudyListRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockValidationRuleRepo (Phase 52.3)
// ---------------------------------------------------------------------------

type mockValidationRuleRepo struct{ mock.Mock }

func (m *mockValidationRuleRepo) Save(ctx context.Context, rule *AnswerValidationRuleEntity) error {
	return m.Called(ctx, rule).Error(0)
}

func (m *mockValidationRuleRepo) ListByAtom(ctx context.Context, atomID uuid.UUID) ([]*AnswerValidationRuleEntity, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*AnswerValidationRuleEntity), args.Error(1)
}

func (m *mockValidationRuleRepo) GetByID(ctx context.Context, id uuid.UUID) (*AnswerValidationRuleEntity, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AnswerValidationRuleEntity), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLayoutRepo (Phase 52.3)
// ---------------------------------------------------------------------------

type mockLayoutRepo struct{ mock.Mock }

func (m *mockLayoutRepo) Upsert(ctx context.Context, layout *PresentationLayout) error {
	return m.Called(ctx, layout).Error(0)
}

func (m *mockLayoutRepo) GetByAtom(ctx context.Context, atomID uuid.UUID) (*PresentationLayout, error) {
	args := m.Called(ctx, atomID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*PresentationLayout), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockImportJobRepo (Phase 52.3)
// ---------------------------------------------------------------------------

type mockImportJobRepo struct{ mock.Mock }

func (m *mockImportJobRepo) Save(ctx context.Context, job *ImportJob) error {
	return m.Called(ctx, job).Error(0)
}

func (m *mockImportJobRepo) GetByID(ctx context.Context, id uuid.UUID) (*ImportJob, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ImportJob), args.Error(1)
}

func (m *mockImportJobRepo) Update(ctx context.Context, job *ImportJob) error {
	return m.Called(ctx, job).Error(0)
}
