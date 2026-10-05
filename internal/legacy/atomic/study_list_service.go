package atomic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StudyListService manages study list lifecycle.
type StudyListService struct {
	lists  StudyListRepository
	events EventPublisher
}

// NewStudyListService creates a new StudyListService with the required
// port dependencies.
func NewStudyListService(lists StudyListRepository, events EventPublisher) *StudyListService {
	return &StudyListService{lists: lists, events: events}
}

// CreateList validates and persists a new StudyList.
func (s *StudyListService) CreateList(ctx context.Context, list *StudyList) error {
	if list.Title == "" {
		return fmt.Errorf("create study list: %w", ErrStudyListTitleRequired)
	}

	list.ID = uuid.Must(uuid.NewV7())

	if list.Visibility == "" {
		list.Visibility = StudyListVisibilityPrivate
	}

	if list.AtomIDs == nil {
		list.AtomIDs = []uuid.UUID{}
	}

	now := time.Now().UTC()
	list.CreatedAt = now
	list.UpdatedAt = now

	if err := s.lists.Save(ctx, list); err != nil {
		return fmt.Errorf("save study list: %w", err)
	}

	event := NewDomainEvent(
		"study_list.created",
		list.TenantID,
		list.ID,
		"StudyList",
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
			"gcid":          list.GCID.String(),
			"title":         list.Title,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// ListByTenant retrieves all non-deleted study lists for a tenant.
func (s *StudyListService) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*StudyList, error) {
	lists, err := s.lists.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list study lists by tenant: %w", err)
	}
	return lists, nil
}

// GetList retrieves a StudyList by ID.
func (s *StudyListService) GetList(ctx context.Context, id uuid.UUID) (*StudyList, error) {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get study list: %w", err)
	}
	return list, nil
}

// UpdateList updates a StudyList's metadata.
func (s *StudyListService) UpdateList(ctx context.Context, list *StudyList) error {
	if list.Title == "" {
		return fmt.Errorf("update study list: %w", ErrStudyListTitleRequired)
	}

	existing, err := s.lists.GetByID(ctx, list.ID)
	if err != nil {
		return fmt.Errorf("get study list for update: %w", err)
	}

	existing.Title = list.Title
	existing.Description = list.Description
	existing.Visibility = list.Visibility
	existing.UpdatedAt = time.Now().UTC()

	if err := s.lists.Update(ctx, existing); err != nil {
		return fmt.Errorf("update study list: %w", err)
	}

	// Propagate UpdatedAt back to the caller's struct.
	list.UpdatedAt = existing.UpdatedAt

	event := NewDomainEvent(
		"study_list.updated",
		existing.TenantID,
		existing.ID,
		"StudyList",
		map[string]any{
			"study_list_id": existing.ID.String(),
			"tenant_id":     existing.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// AddAtoms adds atoms to a study list.
func (s *StudyListService) AddAtoms(ctx context.Context, id uuid.UUID, atomIDs []uuid.UUID) error {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get study list for add atoms: %w", err)
	}

	// Build a set of existing atom IDs for deduplication.
	seen := make(map[uuid.UUID]bool, len(list.AtomIDs))
	for _, aid := range list.AtomIDs {
		seen[aid] = true
	}

	for _, aid := range atomIDs {
		if !seen[aid] {
			list.AtomIDs = append(list.AtomIDs, aid)
			seen[aid] = true
		}
	}

	list.UpdatedAt = time.Now().UTC()

	if err := s.lists.Update(ctx, list); err != nil {
		return fmt.Errorf("update study list atoms: %w", err)
	}

	event := NewDomainEvent(
		"study_list.atoms_added",
		list.TenantID,
		list.ID,
		"StudyList",
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// RemoveAtoms removes atoms from a study list.
func (s *StudyListService) RemoveAtoms(ctx context.Context, id uuid.UUID, atomIDs []uuid.UUID) error {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get study list for remove atoms: %w", err)
	}

	// Build a set of IDs to remove.
	toRemove := make(map[uuid.UUID]bool, len(atomIDs))
	for _, aid := range atomIDs {
		toRemove[aid] = true
	}

	// Filter out the removed IDs.
	filtered := make([]uuid.UUID, 0, len(list.AtomIDs))
	for _, aid := range list.AtomIDs {
		if !toRemove[aid] {
			filtered = append(filtered, aid)
		}
	}
	list.AtomIDs = filtered

	list.UpdatedAt = time.Now().UTC()

	if err := s.lists.Update(ctx, list); err != nil {
		return fmt.Errorf("update study list atoms: %w", err)
	}

	event := NewDomainEvent(
		"study_list.atoms_removed",
		list.TenantID,
		list.ID,
		"StudyList",
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// DeleteList soft-deletes a study list.
func (s *StudyListService) DeleteList(ctx context.Context, id uuid.UUID) error {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get study list for delete: %w", err)
	}

	if err := s.lists.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete study list: %w", err)
	}

	event := NewDomainEvent(
		"study_list.deleted",
		list.TenantID,
		list.ID,
		"StudyList",
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}

// ShareList generates a share code for a study list.
func (s *StudyListService) ShareList(ctx context.Context, id uuid.UUID) (string, error) {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get study list for share: %w", err)
	}

	// If already shared with an existing code, return it.
	if list.ShareCode != "" {
		return list.ShareCode, nil
	}

	// Generate a random 16-byte hex share code (32 chars).
	codeBytes := make([]byte, 16)
	if _, err := rand.Read(codeBytes); err != nil {
		return "", fmt.Errorf("generate share code: %w", err)
	}
	shareCode := hex.EncodeToString(codeBytes)

	list.ShareCode = shareCode
	if list.Visibility == StudyListVisibilityPrivate {
		list.Visibility = StudyListVisibilityShared
	}
	list.UpdatedAt = time.Now().UTC()

	if err := s.lists.Update(ctx, list); err != nil {
		return "", fmt.Errorf("update study list share code: %w", err)
	}

	event := NewDomainEvent(
		"study_list.shared",
		list.TenantID,
		list.ID,
		"StudyList",
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
			"share_code":    shareCode,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return shareCode, nil
}

// GetByShareCode retrieves a study list by its share code.
func (s *StudyListService) GetByShareCode(ctx context.Context, shareCode string) (*StudyList, error) {
	list, err := s.lists.GetByShareCode(ctx, shareCode)
	if err != nil {
		return nil, fmt.Errorf("get study list by share code: %w", err)
	}
	return list, nil
}
