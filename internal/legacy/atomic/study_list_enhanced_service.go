package atomic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StudyListEnhancedService provides the CHO-581 enhanced StudyList operations:
// CreateStudyList, UpdateStudyList, GetByShareCode, GenerateShareCode,
// DeleteStudyList. This service extends the existing StudyListService with
// richer method signatures suitable for Phase 57.4.
type StudyListEnhancedService struct {
	lists  StudyListRepository
	events EventPublisher
}

// NewStudyListEnhancedService creates a new service with the required port
// dependencies.
func NewStudyListEnhancedService(lists StudyListRepository, events EventPublisher) *StudyListEnhancedService {
	return &StudyListEnhancedService{lists: lists, events: events}
}

// CreateStudyList validates and persists a new StudyList with all fields
// specified explicitly.
func (s *StudyListEnhancedService) CreateStudyList(
	ctx context.Context,
	tenantID, gcid uuid.UUID,
	title, description string,
	atomIDs []uuid.UUID,
	visibility StudyListVisibility,
) (*StudyList, error) {
	if title == "" {
		return nil, ErrStudyListTitleRequired
	}

	if visibility == "" {
		visibility = StudyListVisibilityPrivate
	}

	if atomIDs == nil {
		atomIDs = []uuid.UUID{}
	}

	now := time.Now().UTC()
	list := &StudyList{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		GCID:        gcid,
		Title:       title,
		Description: description,
		AtomIDs:     atomIDs,
		Visibility:  visibility,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.lists.Save(ctx, list); err != nil {
		return nil, fmt.Errorf("save study list: %w", err)
	}

	event := NewDomainEvent(
		EventTypeStudyListCreated,
		tenantID,
		list.ID,
		AggregateTypeStudyList,
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     tenantID.String(),
			"gcid":          gcid.String(),
			"title":         title,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return list, nil
}

// UpdateStudyList updates a StudyList's metadata and atom list.
func (s *StudyListEnhancedService) UpdateStudyList(
	ctx context.Context,
	id uuid.UUID,
	title, description string,
	atomIDs []uuid.UUID,
	visibility StudyListVisibility,
) (*StudyList, error) {
	if title == "" {
		return nil, ErrStudyListTitleRequired
	}

	existing, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	existing.Title = title
	existing.Description = description
	existing.AtomIDs = atomIDs
	existing.Visibility = visibility
	existing.UpdatedAt = time.Now().UTC()

	if err := s.lists.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("update study list: %w", err)
	}

	event := NewDomainEvent(
		EventTypeStudyListUpdated,
		existing.TenantID,
		existing.ID,
		AggregateTypeStudyList,
		map[string]any{
			"study_list_id": existing.ID.String(),
			"tenant_id":     existing.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return existing, nil
}

// GetByShareCode retrieves a study list by its share code. Returns
// ErrInvalidShareCode if the code is empty.
func (s *StudyListEnhancedService) GetByShareCode(ctx context.Context, shareCode string) (*StudyList, error) {
	if shareCode == "" {
		return nil, ErrInvalidShareCode
	}

	list, err := s.lists.GetByShareCode(ctx, shareCode)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// GenerateShareCode generates a 12-character alphanumeric share code for a
// study list. If the list already has a share code, it is returned as-is.
// Also promotes the visibility to "shared" if currently "private".
func (s *StudyListEnhancedService) GenerateShareCode(ctx context.Context, id uuid.UUID) (string, error) {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return "", err
	}

	// Return existing code if already present.
	if list.ShareCode != "" {
		return list.ShareCode, nil
	}

	// Generate a random 6-byte hex share code (12 chars alphanumeric).
	codeBytes := make([]byte, 6)
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
		AggregateTypeStudyList,
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
			"share_code":    shareCode,
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return shareCode, nil
}

// DeleteStudyList soft-deletes a study list.
func (s *StudyListEnhancedService) DeleteStudyList(ctx context.Context, id uuid.UUID) error {
	list, err := s.lists.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if err := s.lists.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("soft delete study list: %w", err)
	}

	event := NewDomainEvent(
		EventTypeStudyListDeleted,
		list.TenantID,
		list.ID,
		AggregateTypeStudyList,
		map[string]any{
			"study_list_id": list.ID.String(),
			"tenant_id":     list.TenantID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicAtomicEvents, event)

	return nil
}
