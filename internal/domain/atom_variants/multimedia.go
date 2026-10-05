package atom_variants

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	multimediaQuestionMaxLen = 1024
	multimediaAnswerMaxLen   = 512
)

// Multimedia is a video-question-answer atom payload.
type Multimedia struct {
	VariantID     string      `json:"variant_id"`
	AtomID        string      `json:"atom_id"`
	VariantType   VariantType `json:"type"`
	VideoURL      string      `json:"video_url"`
	Question      string      `json:"question"`
	CorrectAnswer string      `json:"correct_answer"`
	ThumbnailURL  string      `json:"thumbnail_url,omitempty"`
	PublishedAt   time.Time   `json:"published_at"`
}

// MultimediaParams is the constructor input.
type MultimediaParams struct {
	AtomID        string
	VideoURL      string
	Question      string
	CorrectAnswer string
	ThumbnailURL  string
}

// NewMultimedia constructs a validated Multimedia variant.
func NewMultimedia(p MultimediaParams) (*Multimedia, error) {
	if err := requireAtom(p.AtomID); err != nil {
		return nil, err
	}
	if err := requireHTTPS(p.VideoURL, "video_url", false); err != nil {
		return nil, err
	}
	if err := requireHTTPS(p.ThumbnailURL, "thumbnail_url", true); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(p.Question)
	if q == "" {
		return nil, errors.New("question is required")
	}
	if len(q) > multimediaQuestionMaxLen {
		return nil, fmt.Errorf("question too long: %d > %d", len(q), multimediaQuestionMaxLen)
	}
	a := strings.TrimSpace(p.CorrectAnswer)
	if a == "" {
		return nil, errors.New("correct_answer is required")
	}
	if len(a) > multimediaAnswerMaxLen {
		return nil, fmt.Errorf("correct_answer too long: %d > %d", len(a), multimediaAnswerMaxLen)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	return &Multimedia{
		VariantID:     id.String(),
		AtomID:        p.AtomID,
		VariantType:   VariantTypeMultimedia,
		VideoURL:      p.VideoURL,
		Question:      q,
		CorrectAnswer: a,
		ThumbnailURL:  p.ThumbnailURL,
		PublishedAt:   time.Now().UTC(),
	}, nil
}

// Type implements Variant.
func (m *Multimedia) Type() VariantType { return VariantTypeMultimedia }

// GetAtomID implements Variant.
func (m *Multimedia) GetAtomID() string { return m.AtomID }

// GetPublishedAt implements Variant.
func (m *Multimedia) GetPublishedAt() time.Time { return m.PublishedAt }
