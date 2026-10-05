package atom_variants_test

import (
	"strings"
	"testing"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

func TestDragDrop_RequiresFields(t *testing.T) {
	t.Parallel()

	validLeft := []av.DragDropItem{{ID: "L1", Label: "Cat"}, {ID: "L2", Label: "Dog"}}
	validRight := []av.DragDropItem{{ID: "R1", Label: "Meow"}, {ID: "R2", Label: "Bark"}}
	validMap := []av.DragDropMapping{{LeftID: "L1", RightID: "R1"}, {LeftID: "L2", RightID: "R2"}}

	tests := []struct {
		name    string
		params  av.DragDropParams
		wantErr string
	}{
		{
			name:    "missing atom",
			params:  av.DragDropParams{LeftItems: validLeft, RightItems: validRight, Mappings: validMap},
			wantErr: "atom",
		},
		{
			name:    "no left items",
			params:  av.DragDropParams{AtomID: atomID, RightItems: validRight, Mappings: validMap},
			wantErr: "left",
		},
		{
			name:    "no right items",
			params:  av.DragDropParams{AtomID: atomID, LeftItems: validLeft, Mappings: validMap},
			wantErr: "right",
		},
		{
			name: "no mappings",
			params: av.DragDropParams{
				AtomID: atomID, LeftItems: validLeft, RightItems: validRight,
				Mappings: []av.DragDropMapping{},
			},
			wantErr: "mapping",
		},
		{
			name: "duplicate left ids",
			params: av.DragDropParams{
				AtomID: atomID,
				LeftItems: []av.DragDropItem{
					{ID: "L1", Label: "A"}, {ID: "L1", Label: "Dup"},
				},
				RightItems: validRight, Mappings: validMap,
			},
			wantErr: "duplicate",
		},
		{
			name: "mapping references unknown left",
			params: av.DragDropParams{
				AtomID: atomID, LeftItems: validLeft, RightItems: validRight,
				Mappings: []av.DragDropMapping{{LeftID: "Lx", RightID: "R1"}},
			},
			wantErr: "left",
		},
		{
			name: "mapping references unknown right",
			params: av.DragDropParams{
				AtomID: atomID, LeftItems: validLeft, RightItems: validRight,
				Mappings: []av.DragDropMapping{{LeftID: "L1", RightID: "Rx"}},
			},
			wantErr: "right",
		},
		{
			name: "item with empty id",
			params: av.DragDropParams{
				AtomID:     atomID,
				LeftItems:  []av.DragDropItem{{ID: "", Label: "A"}},
				RightItems: []av.DragDropItem{{ID: "R1", Label: "B"}},
				Mappings:   []av.DragDropMapping{{LeftID: "L1", RightID: "R1"}},
			},
			wantErr: "id",
		},
		{
			name: "item with empty label",
			params: av.DragDropParams{
				AtomID:     atomID,
				LeftItems:  []av.DragDropItem{{ID: "L1", Label: ""}},
				RightItems: []av.DragDropItem{{ID: "R1", Label: "B"}},
				Mappings:   []av.DragDropMapping{{LeftID: "L1", RightID: "R1"}},
			},
			wantErr: "label",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := av.NewDragDrop(tc.params)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q; want contains %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDragDrop_HappyPath(t *testing.T) {
	t.Parallel()

	d, err := av.NewDragDrop(av.DragDropParams{
		AtomID:     atomID,
		LeftItems:  []av.DragDropItem{{ID: "L1", Label: "Cat"}, {ID: "L2", Label: "Dog"}},
		RightItems: []av.DragDropItem{{ID: "R1", Label: "Meow"}, {ID: "R2", Label: "Bark"}},
		Mappings:   []av.DragDropMapping{{LeftID: "L1", RightID: "R1"}, {LeftID: "L2", RightID: "R2"}},
	})
	if err != nil {
		t.Fatalf("NewDragDrop unexpected: %v", err)
	}
	if d.Type() != av.VariantTypeDragDrop {
		t.Errorf("Type = %s; want drag-drop-matching", d.Type())
	}
	if len(d.Mappings) != 2 {
		t.Errorf("Mappings len = %d; want 2", len(d.Mappings))
	}
}
