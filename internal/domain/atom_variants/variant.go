// Package atom_variants holds atom-type-specific authoring value objects.
//
// Each LearningAtom (owned by the atom CRUD agent) MAY have one variant
// payload attached describing its interactive content shape. This package
// defines the four variants supported in this iteration:
//
//   - flashcard            (front + back)
//   - code-editor          (language + starter + tests + solution)
//   - drag-drop-matching   (left items + right items + correct mappings)
//   - multimedia           (video URL + question + correct answer + thumbnail)
//
// Variants are value objects: immutable once published. Re-publishing emits
// a new variant via Pub/Sub event chora.creation.atom.variant.published.v1.
//
// Hexagonal: dependency-free w.r.t. infrastructure. Validation lives here;
// persistence + event publishing live in adapter packages.
package atom_variants

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// VariantType is the canonical kebab-case variant identifier surfaced in
// API + event payloads.
type VariantType string

const (
	VariantTypeFlashcard  VariantType = "flashcard"
	VariantTypeCodeEditor VariantType = "code-editor"
	VariantTypeDragDrop   VariantType = "drag-drop-matching"
	VariantTypeMultimedia VariantType = "multimedia"
)

// Valid reports whether v is one of the four supported variant types.
func (v VariantType) Valid() bool {
	switch v {
	case VariantTypeFlashcard, VariantTypeCodeEditor, VariantTypeDragDrop, VariantTypeMultimedia:
		return true
	}
	return false
}

// Variant is the common interface implemented by all variant payloads.
type Variant interface {
	Type() VariantType
	GetAtomID() string
	GetPublishedAt() time.Time
}

// -----------------------------------------------------------------------------
// Shared validation helpers
// -----------------------------------------------------------------------------

func requireAtom(atomID string) error {
	if strings.TrimSpace(atomID) == "" {
		return errors.New("atom_id is required")
	}
	return nil
}

// requireHTTPS validates that s is a syntactically valid HTTPS URL. Empty
// inputs are accepted iff allowEmpty is true.
func requireHTTPS(s, field string, allowEmpty bool) error {
	if s == "" {
		if allowEmpty {
			return nil
		}
		return errors.New(field + " is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New(field + " is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New(field + " must use https scheme")
	}
	if u.Host == "" {
		return errors.New(field + " is not a valid URL")
	}
	return nil
}
