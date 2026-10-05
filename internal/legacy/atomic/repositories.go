package atomic

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AtomRepository defines the data access interface for LearningAtom.
type AtomRepository interface {
	// GetByID retrieves a single atom by its ID.
	// Returns ErrAtomNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*LearningAtom, error)

	// ListByTenant retrieves all non-deleted atoms for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*LearningAtom, error)

	// ListByTopic retrieves all non-deleted atoms associated with a topic.
	ListByTopic(ctx context.Context, topicID uuid.UUID) ([]*LearningAtom, error)

	// Save persists a LearningAtom (insert or update).
	Save(ctx context.Context, atom *LearningAtom) error

	// SoftDelete marks an atom as deleted by setting deleted_at.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// RevisionRepository defines the data access interface for AtomRevision.
// Content is APPEND-ONLY per ddd-enforcement rule #4. VisibilityStatus is
// a mutable lifecycle field (draft → published → archived).
type RevisionRepository interface {
	// GetByAtomID retrieves all revisions for a given atom, ordered by
	// revision_number ascending. Includes all visibility states.
	GetByAtomID(ctx context.Context, atomID uuid.UUID) ([]*AtomRevision, error)

	// GetByID retrieves a single revision by its UUID.
	// Returns ErrRevisionNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*AtomRevision, error)

	// GetLatest retrieves the most recent revision for an atom (any status).
	// Returns nil, nil if no revisions exist.
	GetLatest(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error)

	// GetLatestPublished retrieves the most recent published revision for an atom.
	// Returns nil, nil if no published revisions exist.
	GetLatestPublished(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error)

	// GetDraft retrieves the current draft revision for an atom (max 1).
	// Returns nil, nil if no draft exists.
	GetDraft(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error)

	// Append persists a new immutable revision. Content cannot be changed
	// after creation.
	Append(ctx context.Context, revision *AtomRevision) error

	// UpdateVisibility transitions a revision's lifecycle state.
	// Only visibility_status and published_at are mutable — content is immutable.
	UpdateVisibility(ctx context.Context, id uuid.UUID, status RevisionVisibility, publishedAt *time.Time) error
}

// TopicRepository defines the data access interface for TopicNode.
type TopicRepository interface {
	// GetByID retrieves a single topic node by its ID.
	// Returns ErrTopicNodeNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*TopicNode, error)

	// GetTree retrieves the full topic tree for a tenant.
	GetTree(ctx context.Context, tenantID uuid.UUID) ([]*TopicNode, error)

	// Save persists a TopicNode (insert or update).
	Save(ctx context.Context, topic *TopicNode) error

	// Move changes the parent_id of a topic node.
	Move(ctx context.Context, id uuid.UUID, newParentID *uuid.UUID) error

	// SoftDelete marks a topic node as deleted by setting deleted_at.
	// Cascades soft-delete to child nodes within the same aggregate.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// ---------------------------------------------------------------------------
// Assessment Engine Repositories
// ---------------------------------------------------------------------------

// AssessmentSessionRepository manages AssessmentSession persistence.
// Sessions are accessed through their own aggregate root (collection aggregate).
type AssessmentSessionRepository interface {
	// GetByID retrieves a single session by its ID.
	// Returns ErrSessionNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*AssessmentSession, error)

	// ListByLearner retrieves all non-deleted sessions for a learner (GCID).
	ListByLearner(ctx context.Context, gcid uuid.UUID) ([]*AssessmentSession, error)

	// ListByTenant retrieves all non-deleted sessions for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*AssessmentSession, error)

	// Save persists a new AssessmentSession.
	Save(ctx context.Context, session *AssessmentSession) error

	// Update persists changes to an existing AssessmentSession.
	Update(ctx context.Context, session *AssessmentSession) error

	// SoftDelete marks a session as deleted by setting deleted_at.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// AtomInteractionRepository manages append-only AtomInteraction records.
// Interactions are NEVER updated or deleted per ddd-enforcement rule #4.
type AtomInteractionRepository interface {
	// Append persists a new immutable interaction record.
	Append(ctx context.Context, interaction *AtomInteraction) error

	// ListBySession retrieves all interactions for a given session.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]*AtomInteraction, error)

	// ListByLearner retrieves all interactions for a learner on a specific atom.
	ListByLearner(ctx context.Context, gcid uuid.UUID, atomID uuid.UUID) ([]*AtomInteraction, error)
}

// LockedPathRepository manages LockedPath persistence.
// LockedPaths are collection aggregates — they query atoms, not own them.
type LockedPathRepository interface {
	// GetByID retrieves a single locked path by its ID.
	// Returns ErrLockedPathNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*LockedPath, error)

	// ListByTenant retrieves all non-deleted locked paths for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*LockedPath, error)

	// Save persists a new LockedPath.
	Save(ctx context.Context, path *LockedPath) error

	// Update persists changes to an existing LockedPath.
	Update(ctx context.Context, path *LockedPath) error

	// SoftDelete marks a locked path as deleted by setting deleted_at.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// StudyListRepository manages StudyList persistence.
// StudyLists are collection aggregates — they query atoms, not own them.
type StudyListRepository interface {
	// GetByID retrieves a single study list by its ID.
	// Returns ErrStudyListNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*StudyList, error)

	// ListByTenant retrieves all non-deleted study lists for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*StudyList, error)

	// GetByShareCode retrieves a study list by its unique share code.
	// Returns ErrStudyListNotFound if not found.
	GetByShareCode(ctx context.Context, shareCode string) (*StudyList, error)

	// Save persists a new StudyList.
	Save(ctx context.Context, list *StudyList) error

	// Update persists changes to an existing StudyList.
	Update(ctx context.Context, list *StudyList) error

	// SoftDelete marks a study list as deleted by setting deleted_at.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// ---------------------------------------------------------------------------
// Extended Quiz Repositories (Phase 52.3)
// ---------------------------------------------------------------------------

// AnswerValidationRuleRepository manages AnswerValidationRuleEntity persistence.
type AnswerValidationRuleRepository interface {
	// Save persists a new AnswerValidationRuleEntity.
	Save(ctx context.Context, rule *AnswerValidationRuleEntity) error

	// ListByAtom retrieves all active validation rules for an atom,
	// ordered by priority ascending.
	ListByAtom(ctx context.Context, atomID uuid.UUID) ([]*AnswerValidationRuleEntity, error)

	// GetByID retrieves a single validation rule by its ID.
	// Returns ErrValidationRuleNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*AnswerValidationRuleEntity, error)
}

// PresentationLayoutRepository manages PresentationLayout persistence.
type PresentationLayoutRepository interface {
	// Upsert creates or updates the presentation layout for an atom.
	Upsert(ctx context.Context, layout *PresentationLayout) error

	// GetByAtom retrieves the presentation layout for an atom.
	// Returns ErrLayoutNotFound if not found.
	GetByAtom(ctx context.Context, atomID uuid.UUID) (*PresentationLayout, error)
}

// ImportJobRepository manages ImportJob persistence.
type ImportJobRepository interface {
	// Save persists a new ImportJob.
	Save(ctx context.Context, job *ImportJob) error

	// GetByID retrieves an import job by its ID.
	// Returns ErrImportJobNotFound if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*ImportJob, error)

	// Update persists changes to an existing ImportJob.
	Update(ctx context.Context, job *ImportJob) error
}
