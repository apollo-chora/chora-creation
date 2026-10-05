// clone.go — ADR-199 clone-as-variant (Wave 2). Clone builds a NEW draft
// LearningAtom from a source atom, owned by the caller, copying the source's
// content and stamping cloned_from_atom_id provenance. The source is never
// mutated (append-only respected — the clone is a distinct aggregate).
package atom

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ClonedTitlePrefix is prepended to the source title when the caller supplies
// no title override.
const ClonedTitlePrefix = "Copy of "

// CloneParams is the input envelope for Clone.
type CloneParams struct {
	// Source is the atom being cloned (already loaded + tenant-scoped by the
	// caller). Required.
	Source *LearningAtom
	// TenantID + Gcid are the CALLER's — the clone is owned by the caller, not
	// the source author. Required.
	TenantID string
	Gcid     string
	// TitleOverride optionally replaces the default "Copy of {source title}".
	TitleOverride string
}

// Clone returns a new DRAFT LearningAtom copying the source's content, owned by
// the caller, with ClonedFromAtomID set to the source atom_id. CourseID is
// intentionally NOT inherited — a clone is an independent variant.
func Clone(p CloneParams) (*LearningAtom, error) {
	if p.Source == nil {
		return nil, errors.New("atom.Clone: source atom is required")
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("atom.Clone: tenant_id is required")
	}
	if strings.TrimSpace(p.Gcid) == "" {
		return nil, errors.New("atom.Clone: gcid is required")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("atom.Clone: uuidv7: %w", err)
	}

	title := strings.TrimSpace(p.TitleOverride)
	if title == "" {
		title = ClonedTitlePrefix + p.Source.Title
	}

	mode := p.Source.Mode
	if !mode.Valid() {
		mode = ModeStraightUp
	}

	now := time.Now().UTC()
	return &LearningAtom{
		AtomID:            id.String(),
		TenantID:          p.TenantID,
		Gcid:              p.Gcid,
		Title:             title,
		Body:              p.Source.Body,
		Tags:              append([]string(nil), p.Source.Tags...),
		Mode:              mode,
		Status:            StatusDraft,
		Revision:          1,
		CreatedAt:         now,
		UpdatedAt:         now,
		QuestionType:      p.Source.QuestionType,
		Difficulty:        p.Source.Difficulty,
		Stem:              p.Source.Stem,
		Subject:           p.Source.Subject,
		CognitiveLevel:    p.Source.CognitiveLevel,
		ImdaDimensionTags: append([]ImdaDimTag(nil), p.Source.ImdaDimensionTags...),
		MediaAssets:       append([]MediaAsset(nil), p.Source.MediaAssets...),
		ClonedFromAtomID:  p.Source.AtomID,
	}, nil
}
