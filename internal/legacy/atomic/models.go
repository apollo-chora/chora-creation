package atomic

import (
	"time"

	"github.com/google/uuid"
)

// AtomType enumerates the supported learning atom types.
type AtomType string

const (
	AtomTypeMultipleChoice  AtomType = "multiple_choice"
	AtomTypeFillBlank       AtomType = "fill_blank"
	AtomTypeTrueFalse       AtomType = "true_false"
	AtomTypeShortAnswer     AtomType = "short_answer"
	AtomTypeMatching        AtomType = "matching"
	AtomTypeOrdering        AtomType = "ordering"
	AtomTypeCode            AtomType = "code"
	AtomTypeEssay           AtomType = "essay"
	AtomTypeMultimedia      AtomType = "multimedia"
	AtomTypeSimulation      AtomType = "simulation"
	AtomTypeCompletion      AtomType = "completion"
	AtomTypeTableCompletion AtomType = "table_completion"
	AtomTypeMultiSelect     AtomType = "multi_select"
)

// ValidAtomTypes is the set of all valid atom types.
var ValidAtomTypes = map[AtomType]bool{
	AtomTypeMultipleChoice:  true,
	AtomTypeFillBlank:       true,
	AtomTypeTrueFalse:       true,
	AtomTypeShortAnswer:     true,
	AtomTypeMatching:        true,
	AtomTypeOrdering:        true,
	AtomTypeCode:            true,
	AtomTypeEssay:           true,
	AtomTypeMultimedia:      true,
	AtomTypeSimulation:      true,
	AtomTypeCompletion:      true,
	AtomTypeTableCompletion: true,
	AtomTypeMultiSelect:     true,
}

// AtomStatus enumerates the lifecycle states of a LearningAtom.
type AtomStatus string

const (
	AtomStatusDraft     AtomStatus = "draft"
	AtomStatusPublished AtomStatus = "published"
	AtomStatusArchived  AtomStatus = "archived"
)

// LearningAtom is the PRIMARY AGGREGATE ROOT of the entire Chora platform.
// All higher-order constructs (LockedPath, AssessmentSession, DailyDose) are
// collection aggregates that QUERY atoms — they do not own them.
// See: ADR-005, SP-02 (Atom-Centric Architecture).
type LearningAtom struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	AtomType     AtomType   `json:"atom_type"`
	Difficulty   int        `json:"difficulty"`
	LanguageCode string     `json:"language_code"`
	Tags         []string   `json:"tags"`
	Status       AtomStatus `json:"status"`
	CreatedBy    uuid.UUID  `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`

	// LatestRevision is the most recent published AtomRevision, populated
	// by read queries (GetByID, ListByTenant) via LEFT JOIN. Nil when no
	// published revision exists. Not persisted — derived at query time.
	LatestRevision *AtomRevision `json:"latest_revision,omitempty"`
}

// IsValid checks that the atom has a valid type and difficulty range.
func (a *LearningAtom) IsValid() bool {
	if !ValidAtomTypes[a.AtomType] {
		return false
	}
	if a.Difficulty < 1 || a.Difficulty > 5 {
		return false
	}
	return true
}

// IsArchived returns true if the atom has been archived.
func (a *LearningAtom) IsArchived() bool {
	return a.Status == AtomStatusArchived
}

// IsDeleted returns true if the atom has been soft-deleted.
func (a *LearningAtom) IsDeleted() bool {
	return a.DeletedAt != nil
}

// ValidationRuleType enumerates the supported answer validation strategies.
type ValidationRuleType string

const (
	ValidationRuleExactMatch ValidationRuleType = "exact_match"
	ValidationRuleRegex      ValidationRuleType = "regex"
	ValidationRuleRange      ValidationRuleType = "range"
	ValidationRuleKeyword    ValidationRuleType = "keyword"
	ValidationRuleManual     ValidationRuleType = "manual"
	ValidationRuleLLMGraded  ValidationRuleType = "llm_graded"
)

// AnswerValidationRule is a value object embedded in AtomRevision.
// It defines how a learner's answer is evaluated.
type AnswerValidationRule struct {
	RuleType      ValidationRuleType `json:"rule_type"`
	Expected      any                `json:"expected"`
	Tolerance     *float64           `json:"tolerance,omitempty"`
	CaseSensitive bool               `json:"case_sensitive"`
}

// RevisionVisibility enumerates the lifecycle states of an AtomRevision.
// Content is immutable (append-only), but visibility_status is a mutable
// lifecycle field. Transitions: draft → published → archived. withdrawn is terminal.
type RevisionVisibility string

const (
	RevisionVisibilityDraft     RevisionVisibility = "draft"
	RevisionVisibilityPublished RevisionVisibility = "published"
	RevisionVisibilityArchived  RevisionVisibility = "archived"
	RevisionVisibilityWithdrawn RevisionVisibility = "withdrawn"
)

// AtomRevision is a child entity of LearningAtom. Content (Content,
// ValidationRules, Metadata) is APPEND-ONLY per ddd-enforcement rule #4.
// VisibilityStatus is a mutable lifecycle field (draft → published → archived).
// RevisionNumber is monotonically increasing within an atom.
type AtomRevision struct {
	ID               uuid.UUID              `json:"id"`
	AtomID           uuid.UUID              `json:"atom_id"`
	RevisionNumber   int                    `json:"revision_number"`
	Content          map[string]any         `json:"content"`
	ValidationRules  []AnswerValidationRule `json:"validation_rules"`
	VisibilityStatus RevisionVisibility     `json:"visibility_status"`
	Metadata         map[string]any         `json:"metadata,omitempty"`
	PublishedAt      *time.Time             `json:"published_at,omitempty"`
	CreatedByGCID    uuid.UUID              `json:"created_by_gcid"`
	CreatedAt        time.Time              `json:"created_at"`
	// NOTE: Content is IMMUTABLE (append-only). VisibilityStatus is mutable
	// (lifecycle transitions only). No UpdatedAt, no DeletedAt.
}

// IsDraft returns true if the revision is in draft state.
func (r *AtomRevision) IsDraft() bool {
	return r.VisibilityStatus == RevisionVisibilityDraft
}

// IsPublished returns true if the revision is published.
func (r *AtomRevision) IsPublished() bool {
	return r.VisibilityStatus == RevisionVisibilityPublished
}

// TopicNode is a knowledge graph node forming a tree structure.
// Used for organizing atoms and driving discovery mode traversal.
type TopicNode struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	Name      string     `json:"name"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	SortOrder int        `json:"sort_order"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// IsRoot returns true if this topic node has no parent.
func (t *TopicNode) IsRoot() bool {
	return t.ParentID == nil
}

// IsDeleted returns true if this topic node has been soft-deleted.
func (t *TopicNode) IsDeleted() bool {
	return t.DeletedAt != nil
}

// ---------------------------------------------------------------------------
// Assessment Engine — Enums
// ---------------------------------------------------------------------------

// SessionType determines the UX flow and grading rules.
type SessionType string

const (
	SessionTypeStraightUpExam SessionType = "straight_up_exam"
	SessionTypeDailyDose      SessionType = "daily_dose"
	SessionTypeMockExam       SessionType = "mock_exam"
	SessionTypePracticeSet    SessionType = "practice_set"
	SessionTypeWordleQuiz     SessionType = "wordle_quiz"
	SessionTypeAtomPlaylist   SessionType = "atom_playlist"
)

// ValidSessionTypes is the set of all valid session types.
var ValidSessionTypes = map[SessionType]bool{
	SessionTypeStraightUpExam: true,
	SessionTypeDailyDose:      true,
	SessionTypeMockExam:       true,
	SessionTypePracticeSet:    true,
	SessionTypeWordleQuiz:     true,
	SessionTypeAtomPlaylist:   true,
}

// IsValid returns true if the session type is in the allowed set.
func (s SessionType) IsValid() bool {
	return ValidSessionTypes[s]
}

// SessionStatus is the lifecycle state of an assessment session.
type SessionStatus string

const (
	SessionStatusNotStarted SessionStatus = "not_started"
	SessionStatusActive     SessionStatus = "active"
	SessionStatusPaused     SessionStatus = "paused"
	SessionStatusSubmitted  SessionStatus = "submitted"
	SessionStatusGraded     SessionStatus = "graded"
)

// ValidSessionStatuses is the set of all valid session statuses.
var ValidSessionStatuses = map[SessionStatus]bool{
	SessionStatusNotStarted: true,
	SessionStatusActive:     true,
	SessionStatusPaused:     true,
	SessionStatusSubmitted:  true,
	SessionStatusGraded:     true,
}

// IsValid returns true if the session status is in the allowed set.
func (s SessionStatus) IsValid() bool {
	return ValidSessionStatuses[s]
}

// DeliveryMode is the bimodal delivery approach.
type DeliveryMode string

const (
	DeliveryModeStraightUp     DeliveryMode = "straight_up"
	DeliveryModeGraphDiscovery DeliveryMode = "graph_discovery"
)

// StructureMode defines the 4-tier assessment hierarchy.
type StructureMode string

const (
	StructureModeFlat              StructureMode = "flat"
	StructureModeSectionsOnly      StructureMode = "sections_only"
	StructureModePapersOnly        StructureMode = "papers_only"
	StructureModePapersAndSections StructureMode = "papers_and_sections"
)

// NavigationMode constrains movement between papers/sections.
type NavigationMode string

const (
	NavigationModeFree       NavigationMode = "free"
	NavigationModeSequential NavigationMode = "sequential"
)

// GradingMode determines how answers are graded (Phase 14: deterministic + manual only).
type GradingMode string

const (
	GradingModeDeterministic GradingMode = "deterministic"
	GradingModeManual        GradingMode = "manual"
)

// StudyListVisibility controls access level.
type StudyListVisibility string

const (
	StudyListVisibilityPrivate StudyListVisibility = "private"
	StudyListVisibilityShared  StudyListVisibility = "shared"
	StudyListVisibilityPublic  StudyListVisibility = "public"
)

// ValidStudyListVisibilities is the set of all valid study list visibilities.
var ValidStudyListVisibilities = map[StudyListVisibility]bool{
	StudyListVisibilityPrivate: true,
	StudyListVisibilityShared:  true,
	StudyListVisibilityPublic:  true,
}

// IsValid returns true if the visibility is in the allowed set.
func (v StudyListVisibility) IsValid() bool {
	return ValidStudyListVisibilities[v]
}

// ---------------------------------------------------------------------------
// Assessment Engine — Entities
// ---------------------------------------------------------------------------

// AssessmentSession is a COLLECTION AGGREGATE that queries LearningAtoms.
// It does NOT own atoms — it pins atom_revision_ids for version locking.
// Supports 4-tier hierarchy: Session → Paper → Section → Atom.
// See: SP-02 (Atom-Centric), ddd-enforcement rule #1.
type AssessmentSession struct {
	ID                  uuid.UUID      `json:"id"`
	TenantID            uuid.UUID      `json:"tenant_id"`
	GCID                uuid.UUID      `json:"gcid"`
	SessionType         SessionType    `json:"session_type"`
	DeliveryMode        DeliveryMode   `json:"delivery_mode"`
	StructureMode       StructureMode  `json:"structure_mode"`
	Status              SessionStatus  `json:"status"`
	AtomQuery           map[string]any `json:"atom_query,omitempty"`
	AtomIDs             []uuid.UUID    `json:"atom_ids"`
	AtomRevisionIDs     []uuid.UUID    `json:"atom_revision_ids"`
	TimeLimitMs         *int           `json:"time_limit_ms,omitempty"`
	TimeRemainingMs     *int           `json:"time_remaining_ms,omitempty"`
	PauseCount          int            `json:"pause_count"`
	AnswersSnapshot     map[string]any `json:"answers_snapshot,omitempty"`
	PaperNavigation     NavigationMode `json:"paper_navigation"`
	SectionNavigation   NavigationMode `json:"section_navigation"`
	TotalPoints         *int           `json:"total_points,omitempty"`
	AtomPointsMap       map[string]any `json:"atom_points_map,omitempty"`
	CombinedGradeConfig map[string]any `json:"combined_grade_config,omitempty"`
	StartedAt           *time.Time     `json:"started_at,omitempty"`
	SubmittedAt         *time.Time     `json:"submitted_at,omitempty"`
	GradedAt            *time.Time     `json:"graded_at,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           *time.Time     `json:"deleted_at,omitempty"`

	// Child entities (loaded when fetching a full session).
	Papers   []AssessmentPaper   `json:"papers,omitempty"`
	Sections []AssessmentSection `json:"sections,omitempty"`
}

// AssessmentPaper is a child entity of AssessmentSession.
// Accessed only through the AssessmentSession aggregate root
// (ddd-enforcement rule #2).
type AssessmentPaper struct {
	ID                  uuid.UUID      `json:"id"`
	AssessmentSessionID uuid.UUID      `json:"assessment_session_id"`
	TenantID            uuid.UUID      `json:"tenant_id"`
	PaperNumber         int            `json:"paper_number"`
	PaperCode           string         `json:"paper_code,omitempty"`
	Title               string         `json:"title"`
	Instructions        string         `json:"instructions,omitempty"`
	TimeLimitMs         *int           `json:"time_limit_ms,omitempty"`
	TimeRemainingMs     *int           `json:"time_remaining_ms,omitempty"`
	TotalPoints         int            `json:"total_points"`
	Status              SessionStatus  `json:"status"`
	AllowReturn         bool           `json:"allow_return"`
	AtomIDs             []uuid.UUID    `json:"atom_ids"`
	AtomRevisionIDs     []uuid.UUID    `json:"atom_revision_ids"`
	AtomPointsMap       map[string]any `json:"atom_points_map,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// AssessmentSection is a child entity of AssessmentSession (optionally
// nested under AssessmentPaper). Accessed only through the
// AssessmentSession aggregate root (ddd-enforcement rule #2).
type AssessmentSection struct {
	ID                  uuid.UUID      `json:"id"`
	AssessmentSessionID uuid.UUID      `json:"assessment_session_id"`
	AssessmentPaperID   *uuid.UUID     `json:"assessment_paper_id,omitempty"`
	TenantID            uuid.UUID      `json:"tenant_id"`
	SectionNumber       int            `json:"section_number"`
	Title               string         `json:"title"`
	Instructions        string         `json:"instructions,omitempty"`
	AtomIDs             []uuid.UUID    `json:"atom_ids"`
	AtomRevisionIDs     []uuid.UUID    `json:"atom_revision_ids"`
	AtomPointsMap       map[string]any `json:"atom_points_map,omitempty"`
	TotalPoints         int            `json:"total_points"`
	TimeLimitMs         *int           `json:"time_limit_ms,omitempty"`
	IsSequential        bool           `json:"is_sequential"`
	AllowBacktrack      bool           `json:"allow_backtrack"`
	ShuffleAtoms        bool           `json:"shuffle_atoms"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// AtomInteraction is an APPEND-ONLY record of a learner's response to an atom.
// Never updated or deleted — forms an immutable audit trail.
// See: ddd-enforcement rule #4 (append-only).
type AtomInteraction struct {
	ID            uuid.UUID      `json:"id"`
	TenantID      uuid.UUID      `json:"tenant_id"`
	GCID          uuid.UUID      `json:"gcid"`
	SessionID     uuid.UUID      `json:"session_id"`
	AtomID        uuid.UUID      `json:"atom_id"`
	RevisionID    uuid.UUID      `json:"revision_id"`
	Answer        map[string]any `json:"answer"`
	IsCorrect     bool           `json:"is_correct"`
	Score         *float64       `json:"score,omitempty"`
	TimeSpentMs   int            `json:"time_spent_ms"`
	AttemptNumber int            `json:"attempt_number"`
	CreatedAt     time.Time      `json:"created_at"`
	// NOTE: No UpdatedAt, no DeletedAt — APPEND-ONLY (ddd-enforcement rule #4).
}

// LockedPath is a COLLECTION AGGREGATE — an ordered atom sequence for certification.
// It QUERIES atoms, does NOT own them. Resolves latest published AtomRevision
// at query time. See: SP-02 (Atom-Centric), ddd-enforcement rule #1.
type LockedPath struct {
	ID                 uuid.UUID  `json:"id"`
	TenantID           uuid.UUID  `json:"tenant_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description,omitempty"`
	CertificationName  string     `json:"certification_name,omitempty"`
	PrerequisitePathID *uuid.UUID `json:"prerequisite_path_id,omitempty"`
	CreatedBy          uuid.UUID  `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty"`

	// Child entities.
	Steps []LockedPathStep `json:"steps,omitempty"`
}

// LockedPathStep is a child entity of LockedPath.
// Accessed only through the LockedPath aggregate root
// (ddd-enforcement rule #2).
type LockedPathStep struct {
	ID         uuid.UUID `json:"id"`
	PathID     uuid.UUID `json:"path_id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	AtomID     uuid.UUID `json:"atom_id"`
	StepOrder  int       `json:"step_order"`
	IsRequired bool      `json:"is_required"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// StudyList is a curated, shareable collection of atoms (collection aggregate).
// Always resolves latest published AtomRevision — NO revision pinning.
// See: SP-02 (Atom-Centric), ddd-enforcement rule #1.
type StudyList struct {
	ID          uuid.UUID           `json:"id"`
	TenantID    uuid.UUID           `json:"tenant_id"`
	GCID        uuid.UUID           `json:"gcid"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	AtomIDs     []uuid.UUID         `json:"atom_ids"`
	Visibility  StudyListVisibility `json:"visibility"`
	ShareCode   string              `json:"share_code,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	DeletedAt   *time.Time          `json:"deleted_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Extended Quiz Types (Phase 52.3)
// ---------------------------------------------------------------------------

// ExtendedValidationRuleType enumerates configurable per-atom validation
// rule strategies for the auto-marking engine.
type ExtendedValidationRuleType = string

const (
	ExtValRuleExactMatch         ExtendedValidationRuleType = "exact_match"
	ExtValRuleCaseInsensitive    ExtendedValidationRuleType = "case_insensitive"
	ExtValRuleRegex              ExtendedValidationRuleType = "regex"
	ExtValRuleNumericRange       ExtendedValidationRuleType = "numeric_range"
	ExtValRuleSetContains        ExtendedValidationRuleType = "set_contains"
	ExtValRuleOrderedList        ExtendedValidationRuleType = "ordered_list"
	ExtValRuleSemanticSimilarity ExtendedValidationRuleType = "semantic_similarity"
)

// ValidExtendedValidationRuleTypes is the set of all valid extended validation rule types.
var ValidExtendedValidationRuleTypes = map[ExtendedValidationRuleType]bool{
	ExtValRuleExactMatch:         true,
	ExtValRuleCaseInsensitive:    true,
	ExtValRuleRegex:              true,
	ExtValRuleNumericRange:       true,
	ExtValRuleSetContains:        true,
	ExtValRuleOrderedList:        true,
	ExtValRuleSemanticSimilarity: true,
}

// AnswerValidationRuleEntity is a persistent, configurable validation rule
// associated with an atom. Used by the auto-marking engine (Phase 52.3.4/52.3.6).
type AnswerValidationRuleEntity struct {
	ID         uuid.UUID                  `json:"id"`
	TenantID   uuid.UUID                  `json:"tenant_id"`
	AtomID     uuid.UUID                  `json:"atom_id"`
	RuleType   ExtendedValidationRuleType `json:"rule_type"`
	Parameters map[string]any             `json:"parameters"`
	Priority   int                        `json:"priority"`
	IsActive   bool                       `json:"is_active"`
	CreatedAt  time.Time                  `json:"created_at"`
	UpdatedAt  time.Time                  `json:"updated_at"`
}

// LayoutType enumerates the supported presentation layout types.
type LayoutType = string

const (
	LayoutTypeCard       LayoutType = "card"
	LayoutTypeFullscreen LayoutType = "fullscreen"
	LayoutTypeInline     LayoutType = "inline"
	LayoutTypeSplit      LayoutType = "split"
)

// ValidLayoutTypes is the set of all valid layout types.
var ValidLayoutTypes = map[LayoutType]bool{
	LayoutTypeCard:       true,
	LayoutTypeFullscreen: true,
	LayoutTypeInline:     true,
	LayoutTypeSplit:      true,
}

// PresentationLayout stores per-atom UI layout configuration (Phase 52.3.5).
type PresentationLayout struct {
	ID         uuid.UUID      `json:"id"`
	TenantID   uuid.UUID      `json:"tenant_id"`
	AtomID     uuid.UUID      `json:"atom_id"`
	LayoutType LayoutType     `json:"layout_type"`
	Config     map[string]any `json:"config"`
	IsDefault  bool           `json:"is_default"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// MarkingResult is the result of the auto-marking engine evaluating a learner's answer.
type MarkingResult struct {
	Correct       bool       `json:"correct"`
	Score         float64    `json:"score"`
	Feedback      string     `json:"feedback"`
	MatchedRuleID *uuid.UUID `json:"matched_rule_id,omitempty"`
}

// ImportJobFormat enumerates the supported document import formats.
type ImportJobFormat string

const (
	ImportJobFormatPDF  ImportJobFormat = "pdf"
	ImportJobFormatDOCX ImportJobFormat = "docx"
	ImportJobFormatTXT  ImportJobFormat = "txt"
)

// ImportJobStatus enumerates the lifecycle states of a document import job.
type ImportJobStatus string

const (
	ImportJobStatusPending    ImportJobStatus = "pending"
	ImportJobStatusProcessing ImportJobStatus = "processing"
	ImportJobStatusCompleted  ImportJobStatus = "completed"
	ImportJobStatusFailed     ImportJobStatus = "failed"
)

// ImportJob tracks an asynchronous document-to-atom import job (Phase 52.3.7).
type ImportJob struct {
	ID            uuid.UUID       `json:"id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	UploaderGCID  uuid.UUID       `json:"uploader_gcid"`
	FileReference string          `json:"file_reference"`
	Format        ImportJobFormat `json:"format"`
	Status        ImportJobStatus `json:"status"`
	AtomCount     int             `json:"atom_count"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}
