package atom_variants

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	dragDropMaxItems    = 32
	dragDropLabelMaxLen = 256
)

// DragDropItem is a labelled draggable / drop-target token.
type DragDropItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// DragDropMapping is one correct (left -> right) pair.
type DragDropMapping struct {
	LeftID  string `json:"left_id"`
	RightID string `json:"right_id"`
}

// DragDrop is a drag-drop matching atom payload.
type DragDrop struct {
	VariantID   string            `json:"variant_id"`
	AtomID      string            `json:"atom_id"`
	VariantType VariantType       `json:"type"`
	LeftItems   []DragDropItem    `json:"left_items"`
	RightItems  []DragDropItem    `json:"right_items"`
	Mappings    []DragDropMapping `json:"mappings"`
	PublishedAt time.Time         `json:"published_at"`
}

// DragDropParams is the constructor input.
type DragDropParams struct {
	AtomID     string
	LeftItems  []DragDropItem
	RightItems []DragDropItem
	Mappings   []DragDropMapping
}

// NewDragDrop constructs a validated DragDrop with a UUIDv7 id.
func NewDragDrop(p DragDropParams) (*DragDrop, error) {
	if err := requireAtom(p.AtomID); err != nil {
		return nil, err
	}
	leftIDs, err := validateItems(p.LeftItems, "left")
	if err != nil {
		return nil, err
	}
	rightIDs, err := validateItems(p.RightItems, "right")
	if err != nil {
		return nil, err
	}
	if len(p.Mappings) == 0 {
		return nil, errors.New("at least one mapping is required")
	}
	if len(p.Mappings) > dragDropMaxItems {
		return nil, fmt.Errorf("too many mappings: %d > %d", len(p.Mappings), dragDropMaxItems)
	}
	for i, m := range p.Mappings {
		if _, ok := leftIDs[m.LeftID]; !ok {
			return nil, fmt.Errorf("mapping[%d]: left_id %q not found in left_items", i, m.LeftID)
		}
		if _, ok := rightIDs[m.RightID]; !ok {
			return nil, fmt.Errorf("mapping[%d]: right_id %q not found in right_items", i, m.RightID)
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	return &DragDrop{
		VariantID:   id.String(),
		AtomID:      p.AtomID,
		VariantType: VariantTypeDragDrop,
		LeftItems:   append([]DragDropItem(nil), p.LeftItems...),
		RightItems:  append([]DragDropItem(nil), p.RightItems...),
		Mappings:    append([]DragDropMapping(nil), p.Mappings...),
		PublishedAt: time.Now().UTC(),
	}, nil
}

// validateItems checks that items are non-empty, ids unique within the
// slice, and labels non-blank within len cap. Returns the id-set on success.
func validateItems(items []DragDropItem, side string) (map[string]struct{}, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("%s_items must contain at least one item", side)
	}
	if len(items) > dragDropMaxItems {
		return nil, fmt.Errorf("too many %s_items: %d > %d", side, len(items), dragDropMaxItems)
	}
	seen := make(map[string]struct{}, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.ID) == "" {
			return nil, fmt.Errorf("%s_items[%d]: id is required", side, i)
		}
		if _, dup := seen[it.ID]; dup {
			return nil, fmt.Errorf("%s_items[%d]: duplicate id %q", side, i, it.ID)
		}
		seen[it.ID] = struct{}{}
		if strings.TrimSpace(it.Label) == "" {
			return nil, fmt.Errorf("%s_items[%d]: label is required", side, i)
		}
		if len(it.Label) > dragDropLabelMaxLen {
			return nil, fmt.Errorf("%s_items[%d]: label too long: %d > %d", side, i, len(it.Label), dragDropLabelMaxLen)
		}
	}
	return seen, nil
}

// Type implements Variant.
func (d *DragDrop) Type() VariantType { return VariantTypeDragDrop }

// GetAtomID implements Variant.
func (d *DragDrop) GetAtomID() string { return d.AtomID }

// GetPublishedAt implements Variant.
func (d *DragDrop) GetPublishedAt() time.Time { return d.PublishedAt }
