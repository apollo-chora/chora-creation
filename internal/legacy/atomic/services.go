package atomic

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AtomService encapsulates business logic for LearningAtom management.
type AtomService struct {
	atomRepo     AtomRepository
	revisionRepo RevisionRepository
	publisher    EventPublisher
}

// NewAtomService creates a new AtomService with the required port dependencies.
func NewAtomService(
	atomRepo AtomRepository,
	revisionRepo RevisionRepository,
	publisher EventPublisher,
) *AtomService {
	return &AtomService{
		atomRepo:     atomRepo,
		revisionRepo: revisionRepo,
		publisher:    publisher,
	}
}

// CreateAtom validates and persists a new LearningAtom, then publishes
// an AtomCreated event.
func (s *AtomService) CreateAtom(ctx context.Context, atom *LearningAtom) (*LearningAtom, error) {
	if !ValidAtomTypes[atom.AtomType] {
		return nil, ErrInvalidAtomType
	}
	if atom.Difficulty < 1 || atom.Difficulty > 5 {
		return nil, ErrInvalidDifficulty
	}

	atom.ID = uuid.Must(uuid.NewV7())
	atom.Status = AtomStatusDraft
	now := time.Now().UTC()
	atom.CreatedAt = now
	atom.UpdatedAt = now

	if err := s.atomRepo.Save(ctx, atom); err != nil {
		return nil, fmt.Errorf("save atom: %w", err)
	}

	event := NewDomainEvent(
		EventTypeAtomCreated,
		atom.TenantID,
		atom.ID,
		AggregateTypeLearningAtom,
		map[string]any{
			"atom_id":    atom.ID.String(),
			"tenant_id":  atom.TenantID.String(),
			"atom_type":  string(atom.AtomType),
			"created_by": atom.CreatedBy.String(),
		},
	)
	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return nil, fmt.Errorf("publish atom created event: %w", err)
	}

	return atom, nil
}

// GetAtom retrieves a LearningAtom by ID. Returns ErrAtomNotFound if
// the atom does not exist.
func (s *AtomService) GetAtom(ctx context.Context, id uuid.UUID) (*LearningAtom, error) {
	atom, err := s.atomRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get atom: %w", err)
	}
	return atom, nil
}

// UpdateAtomMetadata updates non-content metadata fields (tags, difficulty,
// language, status). Content changes go through PublishRevision.
func (s *AtomService) UpdateAtomMetadata(ctx context.Context, atom *LearningAtom) (*LearningAtom, error) {
	existing, err := s.atomRepo.GetByID(ctx, atom.ID)
	if err != nil {
		return nil, fmt.Errorf("get atom for update: %w", err)
	}
	if existing.IsArchived() {
		return nil, ErrAtomAlreadyArchived
	}

	if atom.Difficulty < 1 || atom.Difficulty > 5 {
		return nil, ErrInvalidDifficulty
	}

	// Track changed fields for event payload.
	var changedFields []string
	if existing.Difficulty != atom.Difficulty {
		changedFields = append(changedFields, "difficulty")
	}
	if string(existing.AtomType) != string(atom.AtomType) {
		changedFields = append(changedFields, "atom_type")
	}
	if existing.LanguageCode != atom.LanguageCode {
		changedFields = append(changedFields, "language_code")
	}

	atom.UpdatedAt = time.Now().UTC()
	if err := s.atomRepo.Save(ctx, atom); err != nil {
		return nil, fmt.Errorf("save atom metadata: %w", err)
	}

	event := NewDomainEvent(
		EventTypeAtomUpdated,
		atom.TenantID,
		atom.ID,
		AggregateTypeLearningAtom,
		map[string]any{
			"atom_id":        atom.ID.String(),
			"tenant_id":      atom.TenantID.String(),
			"atom_type":      string(atom.AtomType),
			"changed_fields": changedFields,
		},
	)
	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return nil, fmt.Errorf("publish atom updated event: %w", err)
	}

	return atom, nil
}

// ArchiveAtom soft-archives a LearningAtom by setting its status to archived
// and calling SoftDelete per DDD enforcement rule #5.
func (s *AtomService) ArchiveAtom(ctx context.Context, id uuid.UUID) error {
	atom, err := s.atomRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get atom for archive: %w", err)
	}
	if atom.IsArchived() {
		return ErrAtomAlreadyArchived
	}

	atom.Status = AtomStatusArchived
	atom.UpdatedAt = time.Now().UTC()
	if err := s.atomRepo.Save(ctx, atom); err != nil {
		return fmt.Errorf("save archived atom: %w", err)
	}

	if err := s.atomRepo.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete atom: %w", err)
	}

	event := NewDomainEvent(
		EventTypeAtomArchived,
		atom.TenantID,
		atom.ID,
		AggregateTypeLearningAtom,
		map[string]any{
			"atom_id":   atom.ID.String(),
			"tenant_id": atom.TenantID.String(),
		},
	)
	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return fmt.Errorf("publish atom archived event: %w", err)
	}

	return nil
}

// PublishRevision appends a new published AtomRevision to the atom.
// Content is required. Archives the previously published revision (if any)
// and updates LearningAtom's status. Revisions are APPEND-ONLY — content
// cannot be updated or deleted per ddd-enforcement rule #4.
func (s *AtomService) PublishRevision(ctx context.Context, atomID uuid.UUID, content map[string]any, rules []AnswerValidationRule, metadata map[string]any, createdBy uuid.UUID) (*AtomRevision, error) {
	if len(content) == 0 {
		return nil, ErrRevisionContentRequired
	}

	atom, err := s.atomRepo.GetByID(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get atom for revision: %w", err)
	}

	// Archive the currently published revision (if any).
	if err := s.archiveCurrentPublished(ctx, atomID); err != nil {
		return nil, err
	}

	nextNumber, err := s.nextRevisionNumber(ctx, atomID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	revision := &AtomRevision{
		ID:               uuid.Must(uuid.NewV7()),
		AtomID:           atomID,
		RevisionNumber:   nextNumber,
		Content:          content,
		ValidationRules:  rules,
		VisibilityStatus: RevisionVisibilityPublished,
		Metadata:         metadata,
		PublishedAt:      &now,
		CreatedByGCID:    createdBy,
		CreatedAt:        now,
	}

	if err := s.revisionRepo.Append(ctx, revision); err != nil {
		return nil, fmt.Errorf("append revision: %w", err)
	}

	s.publishRevisionEvent(ctx, atom.TenantID, atomID, revision)
	return revision, nil
}

// SaveDraft creates a draft revision that is not yet visible to learners.
// Only one draft per atom is allowed — returns ErrRevisionDraftExists if
// a draft already exists. Content is immutable after creation.
func (s *AtomService) SaveDraft(ctx context.Context, atomID uuid.UUID, content map[string]any, rules []AnswerValidationRule, metadata map[string]any, createdBy uuid.UUID) (*AtomRevision, error) {
	if len(content) == 0 {
		return nil, ErrRevisionContentRequired
	}

	atom, err := s.atomRepo.GetByID(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get atom for draft: %w", err)
	}

	// Enforce max-1-draft constraint.
	existing, err := s.revisionRepo.GetDraft(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("check existing draft: %w", err)
	}
	if existing != nil {
		return nil, ErrRevisionDraftExists
	}

	nextNumber, err := s.nextRevisionNumber(ctx, atomID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	revision := &AtomRevision{
		ID:               uuid.Must(uuid.NewV7()),
		AtomID:           atomID,
		RevisionNumber:   nextNumber,
		Content:          content,
		ValidationRules:  rules,
		VisibilityStatus: RevisionVisibilityDraft,
		Metadata:         metadata,
		CreatedByGCID:    createdBy,
		CreatedAt:        now,
	}

	if err := s.revisionRepo.Append(ctx, revision); err != nil {
		return nil, fmt.Errorf("append draft: %w", err)
	}

	event := NewDomainEvent(
		EventTypeAtomRevisionDrafted,
		atom.TenantID,
		atomID,
		AggregateTypeLearningAtom,
		map[string]any{
			"atom_id":         atomID.String(),
			"revision_id":     revision.ID.String(),
			"revision_number": nextNumber,
			"tenant_id":       atom.TenantID.String(),
		},
	)
	_ = s.publisher.Publish(ctx, TopicAtomicEvents, event)

	return revision, nil
}

// PromoteDraft transitions a draft revision to published. Archives the
// previously published revision (if any). Returns ErrRevisionNotDraft if
// the revision is not in draft state.
func (s *AtomService) PromoteDraft(ctx context.Context, revisionID uuid.UUID) (*AtomRevision, error) {
	revision, err := s.revisionRepo.GetByID(ctx, revisionID)
	if err != nil {
		return nil, fmt.Errorf("get revision for promote: %w", err)
	}

	if !revision.IsDraft() {
		return nil, ErrRevisionNotDraft
	}

	atom, err := s.atomRepo.GetByID(ctx, revision.AtomID)
	if err != nil {
		return nil, fmt.Errorf("get atom for promote: %w", err)
	}

	// Archive the currently published revision (if any).
	if err := s.archiveCurrentPublished(ctx, revision.AtomID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.revisionRepo.UpdateVisibility(ctx, revisionID, RevisionVisibilityPublished, &now); err != nil {
		return nil, fmt.Errorf("update visibility: %w", err)
	}

	revision.VisibilityStatus = RevisionVisibilityPublished
	revision.PublishedAt = &now

	s.publishRevisionEvent(ctx, atom.TenantID, revision.AtomID, revision)
	return revision, nil
}

// GetLatestPublishedRevision returns the most recent published revision.
func (s *AtomService) GetLatestPublishedRevision(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error) {
	revision, err := s.revisionRepo.GetLatestPublished(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get latest published revision: %w", err)
	}
	return revision, nil
}

// GetRevisionByID retrieves a specific revision by its UUID.
func (s *AtomService) GetRevisionByID(ctx context.Context, id uuid.UUID) (*AtomRevision, error) {
	revision, err := s.revisionRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get revision by id: %w", err)
	}
	return revision, nil
}

// nextRevisionNumber calculates the next revision number for an atom.
func (s *AtomService) nextRevisionNumber(ctx context.Context, atomID uuid.UUID) (int, error) {
	latest, err := s.revisionRepo.GetLatest(ctx, atomID)
	if err != nil {
		return 0, fmt.Errorf("get latest revision: %w", err)
	}
	if latest != nil {
		return latest.RevisionNumber + 1, nil
	}
	return 1, nil
}

// archiveCurrentPublished archives the currently published revision for an atom.
func (s *AtomService) archiveCurrentPublished(ctx context.Context, atomID uuid.UUID) error {
	current, err := s.revisionRepo.GetLatestPublished(ctx, atomID)
	if err != nil {
		return fmt.Errorf("get current published: %w", err)
	}
	if current != nil {
		if err := s.revisionRepo.UpdateVisibility(ctx, current.ID, RevisionVisibilityArchived, current.PublishedAt); err != nil {
			return fmt.Errorf("archive previous revision: %w", err)
		}
	}
	return nil
}

// publishRevisionEvent publishes the atom.revision.published domain event.
func (s *AtomService) publishRevisionEvent(ctx context.Context, tenantID, atomID uuid.UUID, revision *AtomRevision) {
	event := NewDomainEvent(
		EventTypeAtomRevisionPublished,
		tenantID,
		atomID,
		AggregateTypeLearningAtom,
		map[string]any{
			"atom_id":         atomID.String(),
			"revision_id":     revision.ID.String(),
			"revision_number": revision.RevisionNumber,
			"tenant_id":       tenantID.String(),
		},
	)
	_ = s.publisher.Publish(ctx, TopicAtomicEvents, event)
}

// ListAtomsByTenant retrieves all non-deleted atoms for a given tenant.
func (s *AtomService) ListAtomsByTenant(ctx context.Context, tenantID uuid.UUID) ([]*LearningAtom, error) {
	atoms, err := s.atomRepo.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list atoms by tenant: %w", err)
	}
	return atoms, nil
}

// GetLatestRevision returns the most recent AtomRevision for the given atom.
func (s *AtomService) GetLatestRevision(ctx context.Context, atomID uuid.UUID) (*AtomRevision, error) {
	revision, err := s.revisionRepo.GetLatest(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get latest revision: %w", err)
	}
	return revision, nil
}

// GetRevisionsByAtomID retrieves all revisions for a given atom, ordered by
// revision_number ascending.
func (s *AtomService) GetRevisionsByAtomID(ctx context.Context, atomID uuid.UUID) ([]*AtomRevision, error) {
	revisions, err := s.revisionRepo.GetByAtomID(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get revisions by atom: %w", err)
	}
	return revisions, nil
}

// RecordValidation publishes an answer.validated event after a learner's
// answer has been checked. This is a fire-and-forget event — validation
// result is returned synchronously, the event triggers async side effects.
func (s *AtomService) RecordValidation(ctx context.Context, atomID, revisionID, tenantID, gcid uuid.UUID, correct bool, atomType string, difficulty int, ruleType string, timeSpentSeconds *int, sessionID *uuid.UUID) error {
	payload := map[string]any{
		"atom_id":     atomID.String(),
		"revision_id": revisionID.String(),
		"tenant_id":   tenantID.String(),
		"gcid":        gcid.String(),
		"correct":     correct,
		"atom_type":   atomType,
		"difficulty":  difficulty,
		"rule_type":   ruleType,
	}
	if timeSpentSeconds != nil {
		payload["time_spent_seconds"] = *timeSpentSeconds
	}
	if sessionID != nil {
		payload["session_id"] = sessionID.String()
	}

	event := NewDomainEvent(
		EventTypeAnswerValidated,
		tenantID,
		atomID,
		AggregateTypeAtomCompletion,
		payload,
	)
	event.GCID = &gcid

	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return fmt.Errorf("publish answer validated event: %w", err)
	}
	return nil
}

// ListAtomsByTopic retrieves all non-deleted atoms associated with a topic.
func (s *AtomService) ListAtomsByTopic(ctx context.Context, topicID uuid.UUID) ([]*LearningAtom, error) {
	atoms, err := s.atomRepo.ListByTopic(ctx, topicID)
	if err != nil {
		return nil, fmt.Errorf("list atoms by topic: %w", err)
	}
	return atoms, nil
}

// TopicService encapsulates business logic for TopicNode management.
type TopicService struct {
	topicRepo TopicRepository
	publisher EventPublisher
}

// NewTopicService creates a new TopicService with the required port dependencies.
func NewTopicService(topicRepo TopicRepository, publisher EventPublisher) *TopicService {
	return &TopicService{topicRepo: topicRepo, publisher: publisher}
}

// validateTopicName checks that a topic name is non-empty and within length limits.
func validateTopicName(name string) error {
	if name == "" {
		return ErrTopicNameRequired
	}
	if len(name) > 255 {
		return ErrTopicNameTooLong
	}
	return nil
}

// CreateTopic validates and persists a new TopicNode, then publishes
// a TopicNodeCreated event.
func (s *TopicService) CreateTopic(ctx context.Context, topic *TopicNode) (*TopicNode, error) {
	if err := validateTopicName(topic.Name); err != nil {
		return nil, err
	}

	topic.ID = uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	topic.CreatedAt = now
	topic.UpdatedAt = now

	if err := s.topicRepo.Save(ctx, topic); err != nil {
		return nil, fmt.Errorf("save topic: %w", err)
	}

	var parentIDStr *string
	if topic.ParentID != nil {
		s := topic.ParentID.String()
		parentIDStr = &s
	}

	event := NewDomainEvent(
		EventTypeTopicNodeCreated,
		topic.TenantID,
		topic.ID,
		AggregateTypeTopicNode,
		map[string]any{
			"topic_id":  topic.ID.String(),
			"tenant_id": topic.TenantID.String(),
			"name":      topic.Name,
			"parent_id": parentIDStr,
		},
	)
	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return nil, fmt.Errorf("publish topic created event: %w", err)
	}

	return topic, nil
}

// GetTopicTree retrieves the full topic tree for a tenant.
func (s *TopicService) GetTopicTree(ctx context.Context, tenantID uuid.UUID) ([]*TopicNode, error) {
	tree, err := s.topicRepo.GetTree(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get topic tree: %w", err)
	}
	return tree, nil
}

// GetTopic retrieves a single TopicNode by its ID.
func (s *TopicService) GetTopic(ctx context.Context, id uuid.UUID) (*TopicNode, error) {
	topic, err := s.topicRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get topic: %w", err)
	}
	return topic, nil
}

// UpdateTopic validates and updates a TopicNode's metadata.
func (s *TopicService) UpdateTopic(ctx context.Context, topic *TopicNode) (*TopicNode, error) {
	if err := validateTopicName(topic.Name); err != nil {
		return nil, err
	}

	existing, err := s.topicRepo.GetByID(ctx, topic.ID)
	if err != nil {
		return nil, fmt.Errorf("get topic for update: %w", err)
	}

	existing.Name = topic.Name
	existing.UpdatedAt = time.Now().UTC()

	if err := s.topicRepo.Save(ctx, existing); err != nil {
		return nil, fmt.Errorf("save topic: %w", err)
	}

	return existing, nil
}

// DeleteTopic soft-deletes a TopicNode.
func (s *TopicService) DeleteTopic(ctx context.Context, id uuid.UUID) error {
	if _, err := s.topicRepo.GetByID(ctx, id); err != nil {
		return fmt.Errorf("get topic for delete: %w", err)
	}

	if err := s.topicRepo.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete topic: %w", err)
	}

	return nil
}

// MoveTopic changes the parent of a TopicNode, validating that the move
// does not create a circular reference.
func (s *TopicService) MoveTopic(ctx context.Context, id uuid.UUID, newParentID *uuid.UUID) error {
	topic, err := s.topicRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get topic for move: %w", err)
	}

	// Prevent circular references: a node cannot become its own descendant.
	if newParentID != nil && *newParentID == id {
		return ErrTopicNodeCircularRef
	}

	oldParentID := topic.ParentID

	if err := s.topicRepo.Move(ctx, id, newParentID); err != nil {
		return fmt.Errorf("move topic: %w", err)
	}

	var oldParentIDStr *string
	if oldParentID != nil {
		s := oldParentID.String()
		oldParentIDStr = &s
	}

	event := NewDomainEvent(
		EventTypeTopicNodeMoved,
		topic.TenantID,
		topic.ID,
		AggregateTypeTopicNode,
		map[string]any{
			"topic_id":      topic.ID.String(),
			"tenant_id":     topic.TenantID.String(),
			"old_parent_id": oldParentIDStr,
			"new_parent_id": newParentID,
		},
	)
	if err := s.publisher.Publish(ctx, TopicAtomicEvents, event); err != nil {
		return fmt.Errorf("publish topic moved event: %w", err)
	}

	return nil
}

// Assessment Engine services are in separate files:
// - assessment_session_service.go
// - locked_path_service.go
// - study_list_service.go
