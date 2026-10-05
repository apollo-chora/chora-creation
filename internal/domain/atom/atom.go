// Package atom is the LearningAtom aggregate of the Content Creation domain.
//
// LearningAtom is the PRIMARY AGGREGATE ROOT of the Chora platform per
// .claude/rules/ddd-enforcement.md and CLAUDE.md §1. Collections in other
// domains query atoms but never own them.
//
// Cross-domain references are UUIDs (no FK). Cross-DB queries are forbidden;
// inter-domain side effects flow via Pub/Sub events (deferred — Tier 2).
//
// This package is dependency-free w.r.t. infrastructure (hexagonal: domain
// at the centre, adapters depend on domain, never the reverse).
package atom

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Enums
// -----------------------------------------------------------------------------

// Mode declares whether an atom is delivered straight-up (linear cert path)
// or via the graph-based discovery experience.
type Mode string

const (
	ModeStraightUp Mode = "straight-up"
	ModeGraphBased Mode = "graph-based"
)

func (m Mode) Valid() bool {
	switch m {
	case ModeStraightUp, ModeGraphBased:
		return true
	}
	return false
}

// Status is the lifecycle state of a LearningAtom. Transitions:
//
//	draft -> published -> archived
//
// Archived is reached via SoftDelete (sets deleted_at) per ddd-enforcement
// aggregate invariant #5. Hard delete is forbidden.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — Question type alias + Bloom + IMDA + Media types
//
// Per docs/m13/atom-phase1-execution-plan-2026-05-17.md §0 + §1 (user
// locked 2026-05-17). `QuestionType` is the rename of `AtomType` (Decision
// #2). `AtomType` remains as a type alias for one release cycle so existing
// callers (legacy, http handler, grpc adapter, test fixtures) keep
// compiling without per-callsite migration.
// -----------------------------------------------------------------------------

// QuestionType is the renamed-AtomType per ADR-156 Decision #2. Values are
// the wire-compatible lowercase forms preserved from the legacy AtomType.
// AtomType is a Go type alias of QuestionType (see phyllis.go), so the two
// identifiers are interchangeable at compile time.
type QuestionType = AtomType

// QuestionType constants — new identifiers per plan §1. These reference the
// existing TypeMCQ / TypeFlashcard / ... constants (phyllis.go) so a single
// underlying enum value is shared across the alias pair.
const (
	QuestionTypeMCQ       = TypeMCQ
	QuestionTypeFlashcard = TypeFlashcard
	QuestionTypeVideo     = TypeVideo
	QuestionTypeEssay     = TypeEssay
	QuestionTypeOutline   = TypeOutline
)

// -----------------------------------------------------------------------------
// CognitiveLevel — Bloom 6-enum per ADR-156 Decision #3.
// Optional initially; mandatory after curriculum onboarding lands.
// -----------------------------------------------------------------------------

type CognitiveLevel string

const (
	CognitiveLevelKnowledge     CognitiveLevel = "knowledge"
	CognitiveLevelComprehension CognitiveLevel = "comprehension"
	CognitiveLevelApplication   CognitiveLevel = "application"
	CognitiveLevelAnalysis      CognitiveLevel = "analysis"
	CognitiveLevelSynthesis     CognitiveLevel = "synthesis"
	CognitiveLevelEvaluation    CognitiveLevel = "evaluation"
)

// Valid returns true iff c is a recognised Bloom 6-enum value. The empty
// string is NOT Valid — but Phase 1 treats empty as "unset" at the
// aggregate level (see ValidatePhase1).
func (c CognitiveLevel) Valid() bool {
	switch c {
	case CognitiveLevelKnowledge, CognitiveLevelComprehension,
		CognitiveLevelApplication, CognitiveLevelAnalysis,
		CognitiveLevelSynthesis, CognitiveLevelEvaluation:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// ImdaDimTag — IMDA Model AI Governance Framework 4-dimension tag per
// ADR-156 Decision #6 + ADR-141 canonical labels.
// -----------------------------------------------------------------------------

type ImdaDimTag string

const (
	ImdaDimAccountability         ImdaDimTag = "accountability"
	ImdaDimTransparency           ImdaDimTag = "transparency"
	ImdaDimSafetyRobustness       ImdaDimTag = "safety_robustness"
	ImdaDimFairnessHumanOversight ImdaDimTag = "fairness_human_oversight"
)

// Valid returns true iff i is one of the 4 canonical IMDA labels. The
// pre-ADR-141 v1 labels (internal_governance, risk_levels, ...) are
// DEPRECATED and intentionally NOT accepted here — callers that still
// emit them should canonicalise via chora-governance imda.Canonicalise().
func (i ImdaDimTag) Valid() bool {
	switch i {
	case ImdaDimAccountability, ImdaDimTransparency,
		ImdaDimSafetyRobustness, ImdaDimFairnessHumanOversight:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// MediaAsset — embedded media on a LearningAtom per ADR-156 Decision #5.
// Phase 1 supports ≤1 image entry; audio/video deferred to Phase 4.
// -----------------------------------------------------------------------------

type MediaAsset struct {
	// Type is the asset kind. Phase 1 supports only "image".
	Type string `json:"type"`
	// URL is the canonical asset URL (typically gs://chora-atom-media-{env}/...).
	URL string `json:"url"`
	// AltText is the WCAG accessibility alt text (≤280 chars).
	AltText string `json:"alt_text,omitempty"`
	// MIME is the media MIME type. Phase 1: image/jpeg | image/png | image/webp.
	MIME string `json:"mime"`
	// SizeBytes is the asset size in bytes (tenant-cap enforced server-side).
	SizeBytes int64 `json:"size_bytes"`
}

// supportedImageMIMEs is the Phase 1 MIME allowlist for image type assets.
var supportedImageMIMEs = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

// Validate enforces Phase 1 invariants on a single MediaAsset. Phase 1
// supports only the image type with a small MIME allowlist; SizeBytes must
// be > 0; URL must be present.
func (m MediaAsset) Validate() error {
	if m.Type != "image" {
		return fmt.Errorf("media_asset type %q unsupported in Phase 1 (image only)", m.Type)
	}
	if strings.TrimSpace(m.URL) == "" {
		return errors.New("media_asset url is required")
	}
	if _, ok := supportedImageMIMEs[m.MIME]; !ok {
		return fmt.Errorf("media_asset mime %q unsupported (Phase 1: image/jpeg|image/png|image/webp)", m.MIME)
	}
	if m.SizeBytes <= 0 {
		return fmt.Errorf("media_asset size_bytes %d must be > 0", m.SizeBytes)
	}
	if len(m.AltText) > 280 {
		return fmt.Errorf("media_asset alt_text > 280 chars (got %d)", len(m.AltText))
	}
	return nil
}

// -----------------------------------------------------------------------------
// LearningAtom aggregate root
// -----------------------------------------------------------------------------

// LearningAtom is the aggregate root. All mutations go through methods on
// this type; never expose mutator helpers from sibling packages.
//
// Phyllis MVP (Comic Ch6 P12, docs/m13/phyllis-mvp-2026-05-08.md §5.3) adds:
//   - CourseID: course-bound atom (cross-domain reference, no FK; validated
//     via Pub/Sub events per ddd-enforcement #3)
//   - AtomType / QuestionType: MVP atom flavour (mcq | flashcard | video |
//     essay | outline). Renamed from AtomType per ADR-156 Decision #2;
//     AtomType remains a type alias for one release cycle.
//   - Difficulty: 0..5 difficulty level
//   - RevisionHistoryList: append-only history of AppendOnlyRevisions
//
// ADR-156 Phase 1 (user locked 2026-05-17) adds:
//   - Stem: canonical question prompt (required, ≤4096) — Decision structural.
//   - Title relaxed to optional (≤256) — Decision #1.
//   - Subject: free-text ≤64 — Decision #4.
//   - CognitiveLevel: Bloom 6-enum — Decision #3.
//   - ImdaDimensionTags: IMDA 4-enum array (max 4) — Decision #6.
//   - MediaAssets: ≤1 image entry — Decision #5.
//   - AuthorNote: private free-text — distinct from title.
type LearningAtom struct {
	AtomID    string     `json:"atom_id"`
	TenantID  string     `json:"tenant_id"`
	Gcid      string     `json:"gcid"`
	Title     string     `json:"title,omitempty"`
	Body      string     `json:"body"`
	Tags      []string   `json:"tags"`
	Mode      Mode       `json:"mode"`
	Status    Status     `json:"status"`
	Revision  int        `json:"revision"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`

	// Phyllis MVP (course-bound) extensions — see phyllis.go.
	CourseID            string                `json:"course_id,omitempty"`
	QuestionType        QuestionType          `json:"question_type,omitempty"`
	Difficulty          int                   `json:"difficulty,omitempty"`
	RevisionHistoryList []*AppendOnlyRevision `json:"-"`

	// ADR-156 Phase 1 fields (user locked 2026-05-17).
	Stem              string         `json:"stem"`
	Subject           string         `json:"subject,omitempty"`
	CognitiveLevel    CognitiveLevel `json:"cognitive_level,omitempty"`
	ImdaDimensionTags []ImdaDimTag   `json:"imda_dimension_tags,omitempty"`
	AuthorNote        string         `json:"author_note,omitempty"`
	MediaAssets       []MediaAsset   `json:"media_assets,omitempty"`

	// ADR-199 clone-as-variant provenance (Wave 2) — the source atom_id this
	// atom was cloned from; empty for originals. Persisted in
	// learning_atoms.cloned_from_atom_id (migration 0027).
	ClonedFromAtomID string `json:"cloned_from_atom_id,omitempty"`

	// ADR-229 WS-1 reuse-consent flag: who may REUSE this atom (pickers,
	// snapshots, quiz arming — playback untouched). Author-chosen, default
	// private (consent-first). Persisted in learning_atoms.reuse_visibility
	// (migration 0029); decoupled from sharing's license/promotion/grants.
	ReuseVisibility ReuseVisibility `json:"reuse_visibility"`

	// ADR-229 Amendment A1 (CHO-2132) — singleton orphan edition markers
	// (migration 0030). Non-empty OrphanedFromAtomID marks this atom as the
	// immutable orphan edition minted when the original was withdrawn while
	// consumed: exactly ONE orphan exists per (orphaned_from_atom_id,
	// orphaned_source_revision_id) — DB-enforced by a partial UNIQUE index.
	// Orphans are FROZEN (every mutation returns ErrOrphanFrozen),
	// reuse_visibility=private FOREVER, and reachable only via repointed
	// grants. Evolving one = fork via Clone().
	OrphanedFromAtomID       string     `json:"orphaned_from_atom_id,omitempty"`
	OrphanedSourceRevisionID string     `json:"orphaned_source_revision_id,omitempty"`
	OrphanedAt               *time.Time `json:"orphaned_at,omitempty"`
}

// IsOrphan reports whether this atom is a frozen orphan edition (ADR-229 A1).
func (a *LearningAtom) IsOrphan() bool {
	return a != nil && a.OrphanedFromAtomID != ""
}

// ReuseVisibility is the ADR-229 author-consent audience for atom REUSE.
type ReuseVisibility string

// The three consent audiences. friends stays hidden in A+ authoring until
// the social-graph friends audience un-hides (ADR-229 Amendment A1.4).
const (
	ReusePrivate ReuseVisibility = "private"
	ReuseFriends ReuseVisibility = "friends"
	ReuseTenant  ReuseVisibility = "tenant"
)

// ParseReuseVisibility validates a wire label into a ReuseVisibility.
func ParseReuseVisibility(s string) (ReuseVisibility, error) {
	switch ReuseVisibility(strings.TrimSpace(s)) {
	case ReusePrivate:
		return ReusePrivate, nil
	case ReuseFriends:
		return ReuseFriends, nil
	case ReuseTenant:
		return ReuseTenant, nil
	default:
		return "", errors.New("reuse_visibility must be private, friends, or tenant")
	}
}

// ErrNotAuthor is returned when a non-author attempts an author-only
// mutation (the reuse-consent flag is the author's alone, ADR-229).
var ErrNotAuthor = errors.New("only the atom author may change this")

// ChangeReuseVisibility sets the author's reuse-consent audience. Author-only
// (ErrNotAuthor otherwise); soft-deleted atoms refuse mutation; a same-value
// change reports changed=false so callers emit no event for a no-op.
func (a *LearningAtom) ChangeReuseVisibility(actorGCID string, v ReuseVisibility) (bool, error) {
	if _, err := ParseReuseVisibility(string(v)); err != nil {
		return false, err
	}
	if a.IsOrphan() {
		// ADR-229 A1.2 — orphan editions are private FOREVER; widening one
		// would re-expose withdrawn content past the author's consent.
		return false, ErrOrphanFrozen
	}
	if a.DeletedAt != nil {
		return false, errors.New("cannot update soft-deleted atom")
	}
	if strings.TrimSpace(actorGCID) == "" || actorGCID != a.Gcid {
		return false, ErrNotAuthor
	}
	if a.ReuseVisibility == v {
		return false, nil
	}
	a.ReuseVisibility = v
	a.UpdatedAt = time.Now().UTC()
	return true, nil
}

// AtomType field alias accessor (backwards-compat shim) — the QuestionType
// field replaces AtomType per Decision #2; the AtomType type alias keeps
// existing call-sites valid. The .AtomType STRUCT-FIELD accessor is
// provided via the AtomType() / SetAtomType helpers below for callers that
// were using `.AtomType` as a struct field selector. Since QuestionType is
// the canonical struct field, callers should migrate to `.QuestionType`;
// the AtomType() accessor preserves read access during the migration.

// AtomType returns the QuestionType. Kept as a method to ease the
// transition from `a.AtomType` (struct field) → `a.QuestionType` (renamed
// struct field) — callers can do `a.QuestionType` directly; this accessor
// keeps semantic clarity in legacy paths that referenced the old name.
func (a *LearningAtom) AtomType() QuestionType { return a.QuestionType }

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID string
	Gcid     string
	Title    string
	Body     string
	Tags     []string
	Mode     Mode
}

// New constructs a fresh LearningAtom in DRAFT status. Returns an error if
// inputs violate aggregate invariants.
func New(p NewParams) (*LearningAtom, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.Gcid) == "" {
		return nil, errors.New("gcid is required")
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	if len(title) > 256 {
		return nil, fmt.Errorf("title too long: %d > 256", len(title))
	}
	body := strings.TrimSpace(p.Body)
	if !p.Mode.Valid() {
		return nil, fmt.Errorf("invalid mode: %q", string(p.Mode))
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}

	tags := append([]string(nil), p.Tags...) // defensive copy

	now := time.Now().UTC()
	return &LearningAtom{
		AtomID:          id.String(),
		TenantID:        p.TenantID,
		Gcid:            p.Gcid,
		Title:           title,
		Body:            body,
		Tags:            tags,
		Mode:            p.Mode,
		Status:          StatusDraft,
		Revision:        1,
		ReuseVisibility: ReusePrivate, // ADR-229: consent-first default
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// UpdateParams is the partial-update payload (PATCH semantics).
type UpdateParams struct {
	Title *string
	Body  *string
	Tags  *[]string
	Mode  *Mode
}

// ApplyUpdate mutates the atom according to UpdateParams, bumps the revision,
// and updates UpdatedAt. Refuses to touch a soft-deleted atom.
func (a *LearningAtom) ApplyUpdate(p UpdateParams) error {
	if a.IsOrphan() {
		// ADR-229 A1.2 — orphan editions are immutable; evolving one is a
		// fork via Clone().
		return ErrOrphanFrozen
	}
	if a.DeletedAt != nil {
		return errors.New("cannot update soft-deleted atom")
	}
	if p.Title != nil {
		title := strings.TrimSpace(*p.Title)
		if title == "" {
			return errors.New("title cannot be empty")
		}
		if len(title) > 256 {
			return fmt.Errorf("title too long: %d > 256", len(title))
		}
		a.Title = title
	}
	if p.Body != nil {
		a.Body = strings.TrimSpace(*p.Body)
	}
	if p.Tags != nil {
		a.Tags = append([]string(nil), (*p.Tags)...)
	}
	if p.Mode != nil {
		if !p.Mode.Valid() {
			return fmt.Errorf("invalid mode: %q", string(*p.Mode))
		}
		a.Mode = *p.Mode
	}
	a.Revision++
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// SoftDelete sets DeletedAt + status=archived. Idempotent: a no-op on already-
// soft-deleted atoms.
//
// Per ddd-enforcement invariant #5, this is a soft delete. Hard delete is
// reserved for crypto-shred / cold archive (not implemented in this skeleton).
func (a *LearningAtom) SoftDelete() error {
	if a.IsOrphan() {
		// ADR-229 A1.2 — consumed-continuity is ABSOLUTE: nobody (not even
		// the author) may archive an orphan edition out from under the
		// consumers repointed onto it.
		return ErrOrphanFrozen
	}
	if a.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	a.DeletedAt = &now
	a.Status = StatusArchived
	a.UpdatedAt = now
	return nil
}

// Publish transitions the atom DRAFT -> PUBLISHED.
//
// Idempotent: re-publishing an already-PUBLISHED atom is a no-op success (per
// A22.Q3 idempotent-friendly convention — single-tap forgiving CTA on the FE).
// Refuses to publish a soft-deleted (archived) atom; returns ErrAtomArchived
// in that case. The caller is responsible for verifying that at least one
// QuestionRevision exists before invoking Publish — the atom aggregate does
// not own the Question repository.
//
// Per .claude/rules/ddd-enforcement.md aggregate invariants #1 + #4:
//   - LearningAtom is the primary aggregate root; revisions are append-only.
//   - The lifecycle transition (DRAFT -> PUBLISHED) is a property of the atom,
//     not the revision — revisions are already immutable at the moment they
//     are appended via QuestionsHandler.createQuestion / patchQuestion (so
//     A22 Option α applies: no separate revision-publish step).
func (a *LearningAtom) Publish() error {
	if a.IsOrphan() {
		// ADR-229 A1.2 — orphans are born published and FROZEN; a publish
		// call means an authoring flow targeted an orphan, which must fail
		// loud rather than silently no-op (the orphan is nobody's draft).
		return ErrOrphanFrozen
	}
	if a.DeletedAt != nil {
		return ErrAtomArchived
	}
	if a.Status == StatusPublished {
		// Idempotent re-publish — no state mutation, no UpdatedAt bump.
		return nil
	}
	a.Status = StatusPublished
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// ErrAtomArchived is returned by Publish when called on a soft-deleted atom.
var ErrAtomArchived = errors.New("atom is archived (soft-deleted); cannot publish")

// IsActive returns true iff the atom is not soft-deleted.
func (a *LearningAtom) IsActive() bool { return a.DeletedAt == nil }

// ListFilter is the query filter for repository List operations.
type ListFilter struct {
	Status Status // empty = no filter
	Query  string // case-insensitive title substring; empty = no filter (entity-picker search)
	Limit  int    // 0 = default (100)
	Offset int
}

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — aggregate validation + display label
// -----------------------------------------------------------------------------

// maxStemLength is the OpenAPI-declared cap on the canonical question stem
// per chora-contracts/openapi/creation-admin.yaml LearningAtom.stem.
const maxStemLength = 4096

// maxTitleLength is the cap on the optional display title.
const maxTitleLength = 256

// maxSubjectLength is the cap on the optional subject (Decision #4).
const maxSubjectLength = 64

// maxImdaTags is the OpenAPI maxItems for imda_dimension_tags. Mirrors
// chora-contracts ADR-141 canonical 4-dim taxonomy.
const maxImdaTags = 4

// maxMediaAssets is the Phase 1 cap on media_assets per Decision #5.
// Audio/video deferred to Phase 4.
const maxMediaAssets = 1

// ValidatePhase1 enforces the ADR-156 Phase 1 aggregate-level invariants:
//
//   - stem: required, non-empty after trim, ≤4096 chars
//   - title: optional; ≤256 chars when present (Decision #1)
//   - subject: optional; ≤64 chars when present (Decision #4)
//   - cognitive_level: optional; when present must be one of the 6 Bloom
//     values (Decision #3)
//   - imda_dimension_tags: ≤4 entries; each must be a canonical IMDA label
//     per ADR-141 (Decision #6)
//   - media_assets: ≤1 entry in Phase 1; each entry passes MediaAsset.Validate
//     (Decision #5)
//
// Callers (HTTP handler createAtom/patchAtom; gRPC server) invoke this on
// the aggregate AFTER applying request fields and BEFORE persisting. The
// handler is responsible for emitting a 400 with the validation message
// when this returns an error.
func (a *LearningAtom) ValidatePhase1() error {
	stem := strings.TrimSpace(a.Stem)
	if stem == "" {
		return errors.New("stem is required (≥1 char after trim)")
	}
	if len(a.Stem) > maxStemLength {
		return fmt.Errorf("stem too long: %d > %d", len(a.Stem), maxStemLength)
	}
	if len(a.Title) > maxTitleLength {
		return fmt.Errorf("title too long: %d > %d", len(a.Title), maxTitleLength)
	}
	if len(a.Subject) > maxSubjectLength {
		return fmt.Errorf("subject too long: %d > %d", len(a.Subject), maxSubjectLength)
	}
	if a.CognitiveLevel != "" && !a.CognitiveLevel.Valid() {
		return fmt.Errorf("invalid cognitive_level: %q (Bloom 6-enum required)", a.CognitiveLevel)
	}
	if n := len(a.ImdaDimensionTags); n > maxImdaTags {
		return fmt.Errorf("too many imda_dimension_tags: %d > %d", n, maxImdaTags)
	}
	for i, tag := range a.ImdaDimensionTags {
		if !tag.Valid() {
			return fmt.Errorf("imda_dimension_tags[%d]: invalid label %q (ADR-141 canonical labels required)", i, tag)
		}
	}
	if n := len(a.MediaAssets); n > maxMediaAssets {
		return fmt.Errorf("too many media_assets: %d > %d (Phase 1 supports ≤%d image entry)", n, maxMediaAssets, maxMediaAssets)
	}
	for i, m := range a.MediaAssets {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("media_assets[%d]: %w", i, err)
		}
	}
	return nil
}

// DisplayLabel returns the FE-facing display string for an atom: title when
// non-empty, else the stem (cap at maxTitleLength for layout sanity).
// Centralised here so all surfaces produce the same shape per Decision #1.
func (a *LearningAtom) DisplayLabel() string {
	if t := strings.TrimSpace(a.Title); t != "" {
		return t
	}
	s := strings.TrimSpace(a.Stem)
	if len(s) > maxTitleLength {
		// Hard truncate at maxTitleLength so layout assumptions hold. UI
		// can apply its own ellipsis on top.
		return s[:maxTitleLength]
	}
	return s
}
