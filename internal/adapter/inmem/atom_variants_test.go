// Package inmem_test exercises the in-memory VariantRepository implementation.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

const variantAtomA = "01970000-0000-7000-aaaa-000000000001"

// unknownVariant exercises the default branch of Save for a Variant whose
// concrete type is not one of the known payloads.
type unknownVariant struct{ atomID string }

func (v *unknownVariant) Type() av.VariantType      { return av.VariantType("unknown") }
func (v *unknownVariant) GetAtomID() string         { return v.atomID }
func (v *unknownVariant) GetPublishedAt() time.Time { return time.Now().UTC() }

func TestVariantRepository_SaveAndCount_KnownTypes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewVariantRepository()

	fc, err := av.NewFlashcard(av.FlashcardParams{AtomID: variantAtomA, Front: "f", Back: "b"})
	if err != nil {
		t.Fatalf("NewFlashcard: %v", err)
	}
	ce, err := av.NewCodeEditor(av.CodeEditorParams{AtomID: variantAtomA, Language: "go", Solution: "package main",
		TestCases: []av.CodeTestCase{{Name: "t", Input: "in", ExpectedOutput: "out"}}})
	if err != nil {
		t.Fatalf("NewCodeEditor: %v", err)
	}
	dd, err := av.NewDragDrop(av.DragDropParams{
		AtomID:     variantAtomA,
		LeftItems:  []av.DragDropItem{{ID: "l1", Label: "L1"}},
		RightItems: []av.DragDropItem{{ID: "r1", Label: "R1"}},
		Mappings:   []av.DragDropMapping{{LeftID: "l1", RightID: "r1"}},
	})
	if err != nil {
		t.Fatalf("NewDragDrop: %v", err)
	}
	mm, err := av.NewMultimedia(av.MultimediaParams{
		AtomID: variantAtomA, VideoURL: "https://example.com/v.mp4",
		Question: "q", CorrectAnswer: "a",
	})
	if err != nil {
		t.Fatalf("NewMultimedia: %v", err)
	}

	for _, v := range []av.Variant{fc, ce, dd, mm} {
		if err := r.Save(ctx, v); err != nil {
			t.Fatalf("Save(%T): %v", v, err)
		}
	}
	if got := r.Count(); got != 4 {
		t.Errorf("Count = %d; want 4", got)
	}
}

func TestVariantRepository_Save_UnknownConcreteType(t *testing.T) {
	t.Parallel()

	r := inmem.NewVariantRepository()
	if err := r.Save(context.Background(), &unknownVariant{atomID: variantAtomA}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := r.Count(); got != 1 {
		t.Errorf("Count = %d; want 1", got)
	}
}

func TestVariantRepository_Count_Empty(t *testing.T) {
	t.Parallel()

	r := inmem.NewVariantRepository()
	if got := r.Count(); got != 0 {
		t.Errorf("Count on empty repo = %d; want 0", got)
	}
}
