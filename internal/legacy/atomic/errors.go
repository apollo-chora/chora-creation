package atomic

import "errors"

// Sentinel errors for the Atomic domain.
// Error codes use the ATOM_ prefix per error-handling conventions.
var (
	// ErrAtomNotFound is returned when a LearningAtom cannot be found.
	ErrAtomNotFound = errors.New("ATOM_NOT_FOUND")

	// ErrAtomAlreadyArchived is returned when attempting to archive an
	// already-archived atom.
	ErrAtomAlreadyArchived = errors.New("ATOM_ALREADY_ARCHIVED")

	// ErrRevisionContentRequired is returned when a revision is created
	// without content.
	ErrRevisionContentRequired = errors.New("ATOM_REVISION_CONTENT_REQUIRED")

	// ErrTopicNodeNotFound is returned when a TopicNode cannot be found.
	ErrTopicNodeNotFound = errors.New("ATOM_TOPIC_NOT_FOUND")

	// ErrTopicNodeCircularRef is returned when a topic move would create
	// a circular parent reference.
	ErrTopicNodeCircularRef = errors.New("ATOM_TOPIC_CIRCULAR_REF")

	// ErrInvalidAtomType is returned when the atom_type is not in the
	// allowed set.
	ErrInvalidAtomType = errors.New("ATOM_INVALID_TYPE")

	// ErrInvalidDifficulty is returned when difficulty is outside the
	// valid range (1-5).
	ErrInvalidDifficulty = errors.New("ATOM_INVALID_DIFFICULTY")

	// ErrRevisionImmutable is returned when someone attempts to update
	// or delete an AtomRevision. Revisions are APPEND-ONLY per
	// ddd-enforcement rule #4.
	ErrRevisionImmutable = errors.New("ATOM_REVISION_IMMUTABLE")

	// ErrTopicNameRequired is returned when a topic name is empty.
	ErrTopicNameRequired = errors.New("ATOM_TOPIC_NAME_REQUIRED")

	// ErrTopicNameTooLong is returned when a topic name exceeds 255 characters.
	ErrTopicNameTooLong = errors.New("ATOM_TOPIC_NAME_TOO_LONG")

	// ErrRevisionNotFound is returned when an AtomRevision cannot be found
	// (either no revisions exist for the atom, or a specific revision ID
	// was requested and does not exist).
	ErrRevisionNotFound = errors.New("ATOM_REVISION_NOT_FOUND")

	// ErrRevisionDraftExists is returned when creating a draft revision
	// but one already exists for the atom (max 1 draft per atom).
	ErrRevisionDraftExists = errors.New("ATOM_REVISION_DRAFT_EXISTS")

	// ErrRevisionNotDraft is returned when attempting to publish a revision
	// that is not in draft state.
	ErrRevisionNotDraft = errors.New("ATOM_REVISION_NOT_DRAFT")
)

// Assessment session errors.
var (
	// ErrSessionNotFound is returned when an AssessmentSession cannot be found.
	ErrSessionNotFound = errors.New("ATOM_SESSION_NOT_FOUND")

	// ErrSessionAlreadyStarted is returned when attempting to start a
	// session that is already in progress.
	ErrSessionAlreadyStarted = errors.New("ATOM_SESSION_ALREADY_STARTED")

	// ErrSessionAlreadySubmitted is returned when attempting to modify a
	// session that has already been submitted.
	ErrSessionAlreadySubmitted = errors.New("ATOM_SESSION_ALREADY_SUBMITTED")

	// ErrSessionNotActive is returned when an operation requires an active
	// session but the session is in a different state.
	ErrSessionNotActive = errors.New("ATOM_SESSION_NOT_ACTIVE")

	// ErrSessionTimeExpired is returned when the session time limit has
	// been exceeded.
	ErrSessionTimeExpired = errors.New("ATOM_SESSION_TIME_EXPIRED")

	// ErrInvalidSessionType is returned when the session type is not in
	// the allowed set.
	ErrInvalidSessionType = errors.New("ATOM_INVALID_SESSION_TYPE")

	// ErrInvalidStructureMode is returned when the structure mode is not
	// in the allowed set.
	ErrInvalidStructureMode = errors.New("ATOM_INVALID_STRUCTURE_MODE")
)

// Locked path errors.
var (
	// ErrPathNotFound is returned when a LockedPath cannot be found.
	ErrPathNotFound = errors.New("ATOM_PATH_NOT_FOUND")

	// ErrPathPrerequisiteIncomplete is returned when a learner attempts
	// to start a path whose prerequisite path has not been completed.
	ErrPathPrerequisiteIncomplete = errors.New("ATOM_PATH_PREREQUISITE_INCOMPLETE")

	// ErrPathStepNotFound is returned when a LockedPathStep cannot be found.
	ErrPathStepNotFound = errors.New("ATOM_PATH_STEP_NOT_FOUND")

	// ErrPathStepDuplicate is returned when an atom is added to a path
	// that already contains it.
	ErrPathStepDuplicate = errors.New("ATOM_PATH_STEP_DUPLICATE")

	// ErrPathTitleRequired is returned when a locked path title is empty.
	ErrPathTitleRequired = errors.New("ATOM_PATH_TITLE_REQUIRED")
)

// Study list errors.
var (
	// ErrStudyListNotFound is returned when a StudyList cannot be found.
	ErrStudyListNotFound = errors.New("ATOM_STUDY_LIST_NOT_FOUND")

	// ErrShareCodeNotFound is returned when a study list share code does
	// not match any existing study list.
	ErrShareCodeNotFound = errors.New("ATOM_SHARE_CODE_NOT_FOUND")

	// ErrStudyListTitleRequired is returned when a study list title is empty.
	ErrStudyListTitleRequired = errors.New("ATOM_STUDY_LIST_TITLE_REQUIRED")

	// ErrInteractionInvalid is returned when an AtomInteraction fails
	// domain validation.
	ErrInteractionInvalid = errors.New("ATOM_INTERACTION_INVALID")

	// ErrInvalidShareCode is returned when a share code is empty or invalid.
	ErrInvalidShareCode = errors.New("ATOM_INVALID_SHARE_CODE")

	// ErrStudyListAccessDenied is returned when the caller lacks permission
	// to access the study list.
	ErrStudyListAccessDenied = errors.New("ATOM_STUDY_LIST_ACCESS_DENIED")
)

// Extended quiz content validation errors (Phase 52.3).
var (
	// ErrClozeNoBlankMarkers is returned when a cloze (completion) atom has
	// no {{blank}} markers in its text.
	ErrClozeNoBlankMarkers = errors.New("ATOM_CLOZE_NO_BLANK_MARKERS")

	// ErrClozeAnswerCountMismatch is returned when the number of answers
	// does not match the number of {{blank}} markers.
	ErrClozeAnswerCountMismatch = errors.New("ATOM_CLOZE_ANSWER_COUNT_MISMATCH")

	// ErrTableNoHeaders is returned when a table_completion atom has no headers.
	ErrTableNoHeaders = errors.New("ATOM_TABLE_NO_HEADERS")

	// ErrTableNoBlankCells is returned when a table_completion atom has
	// no blank cells.
	ErrTableNoBlankCells = errors.New("ATOM_TABLE_NO_BLANK_CELLS")

	// ErrMultiSelectNoOptions is returned when a multi_select atom has
	// no options.
	ErrMultiSelectNoOptions = errors.New("ATOM_MULTI_SELECT_NO_OPTIONS")

	// ErrMultiSelectTooFewCorrect is returned when a multi_select atom has
	// fewer than 2 correct options.
	ErrMultiSelectTooFewCorrect = errors.New("ATOM_MULTI_SELECT_TOO_FEW_CORRECT")

	// ErrMultiSelectNoIncorrect is returned when a multi_select atom has
	// no incorrect options.
	ErrMultiSelectNoIncorrect = errors.New("ATOM_MULTI_SELECT_NO_INCORRECT")
)

// Extended quiz service errors (Phase 52.3).
var (
	// ErrValidationRuleNotFound is returned when an AnswerValidationRuleEntity
	// cannot be found.
	ErrValidationRuleNotFound = errors.New("ATOM_VALIDATION_RULE_NOT_FOUND")

	// ErrInvalidValidationRuleType is returned when a validation rule type
	// is not in the allowed set.
	ErrInvalidValidationRuleType = errors.New("ATOM_INVALID_VALIDATION_RULE_TYPE")

	// ErrLayoutNotFound is returned when a PresentationLayout cannot be found.
	ErrLayoutNotFound = errors.New("ATOM_LAYOUT_NOT_FOUND")

	// ErrInvalidLayoutType is returned when a layout type is not in the
	// allowed set.
	ErrInvalidLayoutType = errors.New("ATOM_INVALID_LAYOUT_TYPE")

	// ErrImportJobNotFound is returned when an ImportJob cannot be found.
	ErrImportJobNotFound = errors.New("ATOM_IMPORT_JOB_NOT_FOUND")

	// ErrInvalidImportFormat is returned when a document import format is
	// not supported.
	ErrInvalidImportFormat = errors.New("ATOM_INVALID_IMPORT_FORMAT")

	// ErrNoRulesForMarking is returned when the auto-marking engine has
	// no active validation rules for an atom.
	ErrNoRulesForMarking = errors.New("ATOM_NO_RULES_FOR_MARKING")
)
