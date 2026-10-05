// Package atom_variants_test exercises atom-type-specific authoring
// validators. Each variant is a value object attached to a LearningAtom
// (which the atom CRUD agent owns) — this package only validates the
// type-specific payload shape.
package atom_variants_test

import (
	"strings"
	"testing"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

const (
	atomID = "01970000-0000-7000-aaaa-000000000001"
)

func TestFlashcard_ValidatesFrontAndBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  av.FlashcardParams
		wantErr string
	}{
		{
			name:    "missing atom",
			params:  av.FlashcardParams{Front: "Q", Back: "A"},
			wantErr: "atom",
		},
		{
			name:    "missing front",
			params:  av.FlashcardParams{AtomID: atomID, Back: "A"},
			wantErr: "front",
		},
		{
			name:    "missing back",
			params:  av.FlashcardParams{AtomID: atomID, Front: "Q"},
			wantErr: "back",
		},
		{
			name:    "front whitespace only",
			params:  av.FlashcardParams{AtomID: atomID, Front: "   ", Back: "A"},
			wantErr: "front",
		},
		{
			name:    "front too long",
			params:  av.FlashcardParams{AtomID: atomID, Front: strings.Repeat("x", 1025), Back: "A"},
			wantErr: "front",
		},
		{
			name:    "back too long",
			params:  av.FlashcardParams{AtomID: atomID, Front: "Q", Back: strings.Repeat("x", 4097)},
			wantErr: "back",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := av.NewFlashcard(tc.params)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q; want contains %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestFlashcard_HappyPath(t *testing.T) {
	t.Parallel()

	f, err := av.NewFlashcard(av.FlashcardParams{
		AtomID: atomID,
		Front:  "What is photosynthesis?",
		Back:   "Conversion of light energy into chemical energy by plants.",
	})
	if err != nil {
		t.Fatalf("NewFlashcard unexpected: %v", err)
	}
	if f.Type() != av.VariantTypeFlashcard {
		t.Errorf("Type = %s; want flashcard", f.Type())
	}
	if f.Front != "What is photosynthesis?" {
		t.Errorf("Front = %q; want trimmed input", f.Front)
	}
	if f.AtomID != atomID {
		t.Errorf("AtomID roundtrip mismatch")
	}
	if f.PublishedAt.IsZero() {
		t.Errorf("PublishedAt zero; want set")
	}
}
