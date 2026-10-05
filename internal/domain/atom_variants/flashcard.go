package atom_variants

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	flashcardFrontMaxLen = 1024
	flashcardBackMaxLen  = 4096
)

// Flashcard payload — front-face prompt + back-face reveal.
type Flashcard struct {
	VariantID   string      `json:"variant_id"`
	AtomID      string      `json:"atom_id"`
	VariantType VariantType `json:"type"`
	Front       string      `json:"front"`
	Back        string      `json:"back"`
	PublishedAt time.Time   `json:"published_at"`
}

// FlashcardParams is the constructor input.
type FlashcardParams struct {
	AtomID string
	Front  string
	Back   string
}

// NewFlashcard constructs a validated Flashcard with a UUIDv7 id.
func NewFlashcard(p FlashcardParams) (*Flashcard, error) {
	if err := requireAtom(p.AtomID); err != nil {
		return nil, err
	}
	front := strings.TrimSpace(p.Front)
	if front == "" {
		return nil, errors.New("front is required")
	}
	if len(front) > flashcardFrontMaxLen {
		return nil, fmt.Errorf("front too long: %d > %d", len(front), flashcardFrontMaxLen)
	}
	back := strings.TrimSpace(p.Back)
	if back == "" {
		return nil, errors.New("back is required")
	}
	if len(back) > flashcardBackMaxLen {
		return nil, fmt.Errorf("back too long: %d > %d", len(back), flashcardBackMaxLen)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	return &Flashcard{
		VariantID:   id.String(),
		AtomID:      p.AtomID,
		VariantType: VariantTypeFlashcard,
		Front:       front,
		Back:        back,
		PublishedAt: time.Now().UTC(),
	}, nil
}

// Type implements Variant.
func (f *Flashcard) Type() VariantType { return VariantTypeFlashcard }

// GetAtomID implements Variant.
func (f *Flashcard) GetAtomID() string { return f.AtomID }

// GetPublishedAt implements Variant.
func (f *Flashcard) GetPublishedAt() time.Time { return f.PublishedAt }
