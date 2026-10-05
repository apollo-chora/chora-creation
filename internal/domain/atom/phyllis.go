// Phyllis MVP extension of the LearningAtom aggregate root.
//
// Adds course-bound atom CRUD + append-only AtomRevision history per:
//   - docs/m13/phyllis-mvp-2026-05-08.md §5.3 (Atom CRUD agent)
//   - Comic Ch6 P12 (AI Assist generates 10 MCQ in editor)
//   - .claude/rules/ddd-enforcement.md (LearningAtom = primary aggregate root;
//     AtomRevision is append-only — never UPDATE or DELETE in place)
//
// The new types and methods coexist with the original M10 skeleton (atom.go)
// without breaking the existing /api/atoms surface — they add a parallel
// course-bound surface that the Phyllis MVP HTTP handlers expose.
package atom

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// AtomType — Phyllis MVP atom flavours per docs/m13/phyllis-mvp-2026-05-08.md
//
// These are a deliberately narrower vocabulary than the OpenAPI spec's
// AtomType enum (MULTIPLE_CHOICE, FILL_BLANK, ...) because the MVP only ships
// MCQ + flashcard + video + essay + outline. The full enum is the domain's
// long-term target; the MVP set is what Phyllis demos.
// -----------------------------------------------------------------------------

type AtomType string

const (
	TypeMCQ       AtomType = "mcq"
	TypeFlashcard AtomType = "flashcard"
	TypeVideo     AtomType = "video"
	TypeEssay     AtomType = "essay"
	TypeOutline   AtomType = "outline"
)

// AllAtomTypes is the canonical vocabulary — the ONE enumerable source every
// other layer is pinned to (CHO-2178). The wire contract, the outbox encoder
// and the authoring guard all iterate this list in their tests, so adding a
// flavour here WITHOUT giving it a chora.creation.v1.AtomType value breaks the
// build. Before that pin existed, `outline` sat in this vocabulary with no
// wire representation: the outbox marshal failed, atom.published.v1 never
// fired, and the atom stayed invisible to every other domain — permanently,
// and with an HTTP 200 in the author's face.
var AllAtomTypes = []AtomType{
	TypeMCQ,
	TypeFlashcard,
	TypeVideo,
	TypeEssay,
	TypeOutline,
}

func (t AtomType) Valid() bool {
	for _, v := range AllAtomTypes {
		if t == v {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// SourceType — provenance of an AtomRevision
// -----------------------------------------------------------------------------

type SourceType string

const (
	SourceManual            SourceType = "manual"
	SourceAIAssist          SourceType = "ai_assist"
	SourceCommunityAtomBank SourceType = "community_atom_bank"
)

func (s SourceType) Valid() bool {
	switch s {
	case SourceManual, SourceAIAssist, SourceCommunityAtomBank:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// AppendOnlyRevision — Phyllis MVP append-only revision body
//
// Distinct from the legacy AtomRevision (revision.go) which models the
// content-hash + validation-rule oriented snapshot used by the original
// admin OpenAPI. Phyllis MVP cares about authoring-time provenance:
// who wrote it, was it AI-assisted, what was the screening verdict.
// -----------------------------------------------------------------------------

type AppendOnlyRevision struct {
	RevisionID     string            `json:"revision_id"`
	AtomID         string            `json:"atom_id"`
	RevisionNumber int               `json:"revision_number"`
	Body           string            `json:"body"`
	AuthoredBy     string            `json:"authored_by"`
	AuthoredAt     time.Time         `json:"authored_at"`
	SourceType     SourceType        `json:"source_type"`
	SourceMetadata map[string]string `json:"source_metadata,omitempty"`
}

// -----------------------------------------------------------------------------
// LearningAtom Phyllis-MVP fields + methods
//
// The aggregate root extends in-place (see atom.go for the struct field
// additions): course_id, atom_type, difficulty, and an append-only revision
// history are added. Existing fields (TenantID, Gcid, Title, Status, Mode)
// remain untouched so the legacy /api/atoms surface keeps working.
// -----------------------------------------------------------------------------

// CurrentRevision returns the latest non-deleted append-only revision, or nil
// if none exists. Per ddd-enforcement: revisions are never UPDATEd or DELETEd
// in place; soft-delete is on the parent aggregate only.
func (a *LearningAtom) CurrentRevision() *AppendOnlyRevision {
	if len(a.RevisionHistoryList) == 0 {
		return nil
	}
	r := a.RevisionHistoryList[len(a.RevisionHistoryList)-1]
	clone := *r
	if r.SourceMetadata != nil {
		clone.SourceMetadata = cloneMap(r.SourceMetadata)
	}
	return &clone
}

// RevisionHistory returns a defensive copy of the full revision history in
// chronological (RevisionNumber-asc) order. Append-only invariant means
// callers cannot mutate the returned slice's elements to mutate the aggregate.
func (a *LearningAtom) RevisionHistory() []*AppendOnlyRevision {
	out := make([]*AppendOnlyRevision, len(a.RevisionHistoryList))
	for i, r := range a.RevisionHistoryList {
		clone := *r
		clone.SourceMetadata = cloneMap(r.SourceMetadata)
		out[i] = &clone
	}
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// -----------------------------------------------------------------------------
// NewBound — Phyllis MVP constructor
//
// Creates a fresh LearningAtom in DRAFT status bound to a course, with the
// first AtomRevision auto-attached.
// -----------------------------------------------------------------------------

type NewBoundParams struct {
	TenantID       string
	Gcid           string
	CourseID       string
	Title          string
	Body           string
	AtomType       AtomType
	Difficulty     int
	Tags           []string
	SourceType     SourceType
	SourceMetadata map[string]string
}

// NewBound constructs a LearningAtom + first AppendOnlyRevision in one call.
func NewBound(p NewBoundParams) (*LearningAtom, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.Gcid) == "" {
		return nil, errors.New("gcid is required")
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, errors.New("course_id is required")
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	if len(title) > 256 {
		return nil, fmt.Errorf("title too long: %d > 256", len(title))
	}
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	if !p.AtomType.Valid() {
		return nil, fmt.Errorf("invalid atom_type: %q", string(p.AtomType))
	}
	if p.Difficulty < 0 || p.Difficulty > 5 {
		return nil, fmt.Errorf("difficulty out of range [0..5]: %d", p.Difficulty)
	}
	if !p.SourceType.Valid() {
		return nil, fmt.Errorf("invalid source_type: %q", string(p.SourceType))
	}

	atomID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	revID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}

	now := time.Now().UTC()
	tags := append([]string(nil), p.Tags...)

	first := &AppendOnlyRevision{
		RevisionID:     revID.String(),
		AtomID:         atomID.String(),
		RevisionNumber: 1,
		Body:           body,
		AuthoredBy:     p.Gcid,
		AuthoredAt:     now,
		SourceType:     p.SourceType,
		SourceMetadata: cloneMap(p.SourceMetadata),
	}

	a := &LearningAtom{
		AtomID:              atomID.String(),
		TenantID:            p.TenantID,
		Gcid:                p.Gcid,
		CourseID:            p.CourseID,
		Title:               title,
		Body:                body,
		Tags:                tags,
		Mode:                ModeStraightUp,
		Status:              StatusDraft,
		Revision:            1,
		QuestionType:        p.AtomType, // ADR-156 Decision #2: field renamed; param keeps legacy name for one cycle
		Difficulty:          p.Difficulty,
		CreatedAt:           now,
		UpdatedAt:           now,
		RevisionHistoryList: []*AppendOnlyRevision{first},
	}
	return a, nil
}

// -----------------------------------------------------------------------------
// AppendRevision — append-only invariant per ddd-enforcement #4
// -----------------------------------------------------------------------------

type AppendRevisionParams struct {
	Body           string
	AuthoredBy     string
	SourceType     SourceType
	SourceMetadata map[string]string
}

// AppendRevision appends a new revision to the atom. Returns a defensive
// copy of the appended revision. Refuses to operate on a soft-deleted atom.
func (a *LearningAtom) AppendRevision(p AppendRevisionParams) (*AppendOnlyRevision, error) {
	if a.DeletedAt != nil {
		return nil, errors.New("cannot append revision to soft-deleted atom")
	}
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	if strings.TrimSpace(p.AuthoredBy) == "" {
		return nil, errors.New("authored_by is required")
	}
	if !p.SourceType.Valid() {
		return nil, fmt.Errorf("invalid source_type: %q", string(p.SourceType))
	}

	revID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}

	// Ensure monotonic AuthoredAt vs prior revision (clock-skew defence).
	now := time.Now().UTC()
	if len(a.RevisionHistoryList) > 0 {
		prev := a.RevisionHistoryList[len(a.RevisionHistoryList)-1]
		if !now.After(prev.AuthoredAt) {
			now = prev.AuthoredAt.Add(time.Microsecond)
		}
	}

	r := &AppendOnlyRevision{
		RevisionID:     revID.String(),
		AtomID:         a.AtomID,
		RevisionNumber: len(a.RevisionHistoryList) + 1,
		Body:           body,
		AuthoredBy:     p.AuthoredBy,
		AuthoredAt:     now,
		SourceType:     p.SourceType,
		SourceMetadata: cloneMap(p.SourceMetadata),
	}
	a.RevisionHistoryList = append(a.RevisionHistoryList, r)
	a.Body = body
	a.Revision = r.RevisionNumber
	a.UpdatedAt = now

	clone := *r
	clone.SourceMetadata = cloneMap(r.SourceMetadata)
	return &clone, nil
}
