package atomic

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LockedPathService manages locked path lifecycle.
type LockedPathService struct {
	paths  LockedPathRepository
	events EventPublisher
}

// NewLockedPathService creates a new LockedPathService with the required
// port dependencies.
func NewLockedPathService(paths LockedPathRepository, events EventPublisher) *LockedPathService {
	return &LockedPathService{paths: paths, events: events}
}

// CreatePath validates and persists a new LockedPath.
func (s *LockedPathService) CreatePath(ctx context.Context, path *LockedPath) error {
	if path.Title == "" {
		return fmt.Errorf("create path: %w", ErrPathTitleRequired)
	}

	// Validate prerequisite path exists when provided.
	if path.PrerequisitePathID != nil {
		if _, err := s.paths.GetByID(ctx, *path.PrerequisitePathID); err != nil {
			return fmt.Errorf("validate prerequisite path: %w", err)
		}
	}

	path.ID = uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	path.CreatedAt = now
	path.UpdatedAt = now

	if err := s.paths.Save(ctx, path); err != nil {
		return fmt.Errorf("save path: %w", err)
	}

	event := NewDomainEvent(
		"path.created",
		path.TenantID,
		path.ID,
		AggregateTypeLockedPath,
		map[string]any{
			"path_id":   path.ID.String(),
			"tenant_id": path.TenantID.String(),
			"title":     path.Title,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// GetPath retrieves a LockedPath by ID.
func (s *LockedPathService) GetPath(ctx context.Context, id uuid.UUID) (*LockedPath, error) {
	path, err := s.paths.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get path: %w", err)
	}
	return path, nil
}

// ListPaths retrieves all locked paths for a tenant.
func (s *LockedPathService) ListPaths(ctx context.Context, tenantID uuid.UUID) ([]*LockedPath, error) {
	paths, err := s.paths.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	return paths, nil
}

// AddStep adds a step to a locked path.
func (s *LockedPathService) AddStep(ctx context.Context, pathID uuid.UUID, step *LockedPathStep) error {
	path, err := s.paths.GetByID(ctx, pathID)
	if err != nil {
		return fmt.Errorf("get path for add step: %w", err)
	}

	// Check no duplicate atom_id in existing steps.
	for _, existing := range path.Steps {
		if existing.AtomID == step.AtomID {
			return fmt.Errorf("add step: %w", ErrPathStepDuplicate)
		}
	}

	step.ID = uuid.Must(uuid.NewV7())
	step.PathID = pathID
	step.TenantID = path.TenantID
	step.StepOrder = len(path.Steps) + 1

	now := time.Now().UTC()
	step.CreatedAt = now
	step.UpdatedAt = now

	path.Steps = append(path.Steps, *step)
	path.UpdatedAt = now

	if err := s.paths.Update(ctx, path); err != nil {
		return fmt.Errorf("update path with new step: %w", err)
	}

	event := NewDomainEvent(
		"path.step.added",
		path.TenantID,
		path.ID,
		AggregateTypeLockedPath,
		map[string]any{
			"path_id": path.ID.String(),
			"step_id": step.ID.String(),
			"atom_id": step.AtomID.String(),
			"order":   step.StepOrder,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// RemoveStep removes a step from a locked path.
func (s *LockedPathService) RemoveStep(ctx context.Context, pathID uuid.UUID, stepID uuid.UUID) error {
	path, err := s.paths.GetByID(ctx, pathID)
	if err != nil {
		return fmt.Errorf("get path for remove step: %w", err)
	}

	// Find step index.
	idx := -1
	for i, step := range path.Steps {
		if step.ID == stepID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("remove step: %w", ErrPathStepNotFound)
	}

	// Remove step from slice.
	path.Steps = append(path.Steps[:idx], path.Steps[idx+1:]...)

	// Reorder remaining steps (1-indexed sequential).
	for i := range path.Steps {
		path.Steps[i].StepOrder = i + 1
	}

	path.UpdatedAt = time.Now().UTC()

	if err := s.paths.Update(ctx, path); err != nil {
		return fmt.Errorf("update path after step removal: %w", err)
	}

	event := NewDomainEvent(
		"path.step.removed",
		path.TenantID,
		path.ID,
		AggregateTypeLockedPath,
		map[string]any{
			"path_id": path.ID.String(),
			"step_id": stepID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// ReorderSteps reorders steps within a locked path.
func (s *LockedPathService) ReorderSteps(ctx context.Context, pathID uuid.UUID, stepIDs []uuid.UUID) error {
	path, err := s.paths.GetByID(ctx, pathID)
	if err != nil {
		return fmt.Errorf("get path for reorder: %w", err)
	}

	// Build a map of existing steps for fast lookup.
	stepMap := make(map[uuid.UUID]LockedPathStep, len(path.Steps))
	for _, step := range path.Steps {
		stepMap[step.ID] = step
	}

	// Validate all provided stepIDs exist and count matches.
	if len(stepIDs) != len(path.Steps) {
		return fmt.Errorf("reorder steps: %w", ErrPathStepNotFound)
	}

	reordered := make([]LockedPathStep, 0, len(stepIDs))
	for i, sid := range stepIDs {
		step, ok := stepMap[sid]
		if !ok {
			return fmt.Errorf("reorder steps: %w", ErrPathStepNotFound)
		}
		step.StepOrder = i + 1
		reordered = append(reordered, step)
	}

	path.Steps = reordered
	path.UpdatedAt = time.Now().UTC()

	if err := s.paths.Update(ctx, path); err != nil {
		return fmt.Errorf("update path after reorder: %w", err)
	}

	event := NewDomainEvent(
		"path.steps.reordered",
		path.TenantID,
		path.ID,
		AggregateTypeLockedPath,
		map[string]any{
			"path_id": path.ID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// DeletePath soft-deletes a locked path.
func (s *LockedPathService) DeletePath(ctx context.Context, id uuid.UUID) error {
	path, err := s.paths.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get path for delete: %w", err)
	}

	if err := s.paths.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete path: %w", err)
	}

	event := NewDomainEvent(
		"path.deleted",
		path.TenantID,
		path.ID,
		AggregateTypeLockedPath,
		map[string]any{
			"path_id":   path.ID.String(),
			"tenant_id": path.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}
