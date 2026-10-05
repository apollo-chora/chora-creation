// Coverage-fill tests for the variant getters used by adapter code.
package atom_variants_test

import (
	"testing"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

const variantTestAtom = "01970000-0000-7000-bbbb-000000000001"

func TestVariant_Getters(t *testing.T) {
	t.Parallel()

	f, _ := av.NewFlashcard(av.FlashcardParams{AtomID: variantTestAtom, Front: "Q", Back: "A"})
	if f.GetAtomID() != variantTestAtom {
		t.Errorf("Flashcard.GetAtomID = %q", f.GetAtomID())
	}
	if f.GetPublishedAt().IsZero() {
		t.Errorf("Flashcard.GetPublishedAt zero")
	}
	if av.VariantType("flashcard").Valid() != true {
		t.Errorf("VariantType(flashcard).Valid() = false")
	}
	if av.VariantType("unknown").Valid() != false {
		t.Errorf("VariantType(unknown).Valid() = true")
	}

	c, _ := av.NewCodeEditor(av.CodeEditorParams{
		AtomID: variantTestAtom, Language: "go", StarterCode: "x", Solution: "y",
		TestCases: []av.CodeTestCase{{Name: "tc1", Input: "1", ExpectedOutput: "1"}},
	})
	if c.GetAtomID() != variantTestAtom {
		t.Errorf("CodeEditor.GetAtomID = %q", c.GetAtomID())
	}
	if c.GetPublishedAt().IsZero() {
		t.Errorf("CodeEditor.GetPublishedAt zero")
	}

	d, _ := av.NewDragDrop(av.DragDropParams{
		AtomID:     variantTestAtom,
		LeftItems:  []av.DragDropItem{{ID: "l1", Label: "a"}},
		RightItems: []av.DragDropItem{{ID: "r1", Label: "b"}},
		Mappings:   []av.DragDropMapping{{LeftID: "l1", RightID: "r1"}},
	})
	if d.GetAtomID() != variantTestAtom {
		t.Errorf("DragDrop.GetAtomID = %q", d.GetAtomID())
	}
	if d.GetPublishedAt().IsZero() {
		t.Errorf("DragDrop.GetPublishedAt zero")
	}

	m, _ := av.NewMultimedia(av.MultimediaParams{
		AtomID: variantTestAtom, VideoURL: "https://example.com/v.mp4",
		Question: "Q?", CorrectAnswer: "A",
	})
	if m.GetAtomID() != variantTestAtom {
		t.Errorf("Multimedia.GetAtomID = %q", m.GetAtomID())
	}
	if m.GetPublishedAt().IsZero() {
		t.Errorf("Multimedia.GetPublishedAt zero")
	}
}
