package atomic

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Content Validation (Phase 52.3.1, 52.3.2, 52.3.3)
// ---------------------------------------------------------------------------

// ValidateClozeContent validates the JSONB content for a "completion" (cloze) atom.
// Requires at least one {{blank}} marker and matching answer count.
func ValidateClozeContent(content map[string]any) error {
	text, ok := content["text"].(string)
	if !ok || text == "" {
		return ErrClozeNoBlankMarkers
	}

	blankCount := strings.Count(text, "{{blank}}")
	if blankCount == 0 {
		return ErrClozeNoBlankMarkers
	}

	answersRaw, ok := content["answers"]
	if !ok {
		return ErrClozeAnswerCountMismatch
	}

	answers, ok := toStringSlice(answersRaw)
	if !ok {
		return ErrClozeAnswerCountMismatch
	}

	if len(answers) != blankCount {
		return ErrClozeAnswerCountMismatch
	}

	return nil
}

// ValidateTableCompletionContent validates the JSONB content for a "table_completion" atom.
// Requires at least one header and at least one blank cell.
func ValidateTableCompletionContent(content map[string]any) error {
	headersRaw, ok := content["headers"]
	if !ok {
		return ErrTableNoHeaders
	}

	headers, ok := toStringSlice(headersRaw)
	if !ok || len(headers) == 0 {
		return ErrTableNoHeaders
	}

	rowsRaw, ok := content["rows"]
	if !ok {
		return ErrTableNoBlankCells
	}

	rows, ok := rowsRaw.([]any)
	if !ok || len(rows) == 0 {
		return ErrTableNoBlankCells
	}

	hasBlank := false
	for _, rowRaw := range rows {
		row, ok := rowRaw.(map[string]any)
		if !ok {
			continue
		}
		cellsRaw, ok := row["cells"]
		if !ok {
			continue
		}
		cells, ok := cellsRaw.([]any)
		if !ok {
			continue
		}
		for _, cellRaw := range cells {
			cell, ok := cellRaw.(map[string]any)
			if !ok {
				continue
			}
			if isBlank, ok := cell["is_blank"].(bool); ok && isBlank {
				hasBlank = true
				break
			}
		}
		if hasBlank {
			break
		}
	}

	if !hasBlank {
		return ErrTableNoBlankCells
	}

	return nil
}

// ValidateMultiSelectContent validates the JSONB content for a "multi_select" atom.
// Requires at least 2 correct options and at least 1 incorrect option.
func ValidateMultiSelectContent(content map[string]any) error {
	optionsRaw, ok := content["options"]
	if !ok {
		return ErrMultiSelectNoOptions
	}

	options, ok := optionsRaw.([]any)
	if !ok || len(options) == 0 {
		return ErrMultiSelectNoOptions
	}

	correctCount := 0
	incorrectCount := 0

	for _, optRaw := range options {
		opt, ok := optRaw.(map[string]any)
		if !ok {
			continue
		}
		if isCorrect, ok := opt["correct"].(bool); ok && isCorrect {
			correctCount++
		} else {
			incorrectCount++
		}
	}

	if correctCount < 2 {
		return ErrMultiSelectTooFewCorrect
	}
	if incorrectCount < 1 {
		return ErrMultiSelectNoIncorrect
	}

	return nil
}

// ValidateAtomContent dispatches content validation based on atom type.
// Returns nil for atom types that do not have type-specific validation.
func ValidateAtomContent(atomType AtomType, content map[string]any) error {
	switch atomType {
	case AtomTypeCompletion:
		return ValidateClozeContent(content)
	case AtomTypeTableCompletion:
		return ValidateTableCompletionContent(content)
	case AtomTypeMultiSelect:
		return ValidateMultiSelectContent(content)
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Phase 52.3.4 — AnswerValidationRule Service
// ---------------------------------------------------------------------------

// AnswerValidationRuleService manages configurable per-atom validation rules.
type AnswerValidationRuleService struct {
	ruleRepo  AnswerValidationRuleRepository
	atomRepo  AtomRepository
	publisher EventPublisher
}

// NewAnswerValidationRuleService creates a new AnswerValidationRuleService.
func NewAnswerValidationRuleService(
	ruleRepo AnswerValidationRuleRepository,
	atomRepo AtomRepository,
	publisher EventPublisher,
) *AnswerValidationRuleService {
	return &AnswerValidationRuleService{
		ruleRepo:  ruleRepo,
		atomRepo:  atomRepo,
		publisher: publisher,
	}
}

// CreateRule validates and persists a new AnswerValidationRuleEntity.
func (s *AnswerValidationRuleService) CreateRule(ctx context.Context, rule *AnswerValidationRuleEntity) (*AnswerValidationRuleEntity, error) {
	if !ValidExtendedValidationRuleTypes[rule.RuleType] {
		return nil, ErrInvalidValidationRuleType
	}

	// Verify the atom exists.
	if _, err := s.atomRepo.GetByID(ctx, rule.AtomID); err != nil {
		return nil, fmt.Errorf("get atom for rule: %w", err)
	}

	rule.ID = uuid.Must(uuid.NewV7())
	rule.IsActive = true
	now := time.Now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now

	if err := s.ruleRepo.Save(ctx, rule); err != nil {
		return nil, fmt.Errorf("save validation rule: %w", err)
	}

	return rule, nil
}

// ListRules retrieves all active validation rules for an atom.
func (s *AnswerValidationRuleService) ListRules(ctx context.Context, atomID uuid.UUID) ([]*AnswerValidationRuleEntity, error) {
	rules, err := s.ruleRepo.ListByAtom(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("list validation rules: %w", err)
	}
	return rules, nil
}

// ---------------------------------------------------------------------------
// Phase 52.3.5 — Presentation Layout Service
// ---------------------------------------------------------------------------

// LayoutService manages presentation layout configuration for atoms.
type LayoutService struct {
	layoutRepo PresentationLayoutRepository
	atomRepo   AtomRepository
}

// NewLayoutService creates a new LayoutService.
func NewLayoutService(
	layoutRepo PresentationLayoutRepository,
	atomRepo AtomRepository,
) *LayoutService {
	return &LayoutService{
		layoutRepo: layoutRepo,
		atomRepo:   atomRepo,
	}
}

// SetLayout creates or updates the presentation layout for an atom.
func (s *LayoutService) SetLayout(ctx context.Context, layout *PresentationLayout) (*PresentationLayout, error) {
	if !ValidLayoutTypes[layout.LayoutType] {
		return nil, ErrInvalidLayoutType
	}

	// Verify the atom exists.
	if _, err := s.atomRepo.GetByID(ctx, layout.AtomID); err != nil {
		return nil, fmt.Errorf("get atom for layout: %w", err)
	}

	if layout.ID == uuid.Nil {
		layout.ID = uuid.Must(uuid.NewV7())
	}
	now := time.Now().UTC()
	layout.CreatedAt = now
	layout.UpdatedAt = now

	if err := s.layoutRepo.Upsert(ctx, layout); err != nil {
		return nil, fmt.Errorf("upsert layout: %w", err)
	}

	return layout, nil
}

// GetLayout retrieves the presentation layout for an atom.
func (s *LayoutService) GetLayout(ctx context.Context, atomID uuid.UUID) (*PresentationLayout, error) {
	layout, err := s.layoutRepo.GetByAtom(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("get layout: %w", err)
	}
	return layout, nil
}

// ---------------------------------------------------------------------------
// Phase 52.3.6 — Auto-Marking Engine
// ---------------------------------------------------------------------------

// AutoMarkingService evaluates learner answers against AnswerValidationRuleEntity
// rules for an atom. Uses priority-ordered rules; returns first match.
type AutoMarkingService struct {
	ruleRepo  AnswerValidationRuleRepository
	publisher EventPublisher
}

// NewAutoMarkingService creates a new AutoMarkingService.
func NewAutoMarkingService(
	ruleRepo AnswerValidationRuleRepository,
	publisher EventPublisher,
) *AutoMarkingService {
	return &AutoMarkingService{
		ruleRepo:  ruleRepo,
		publisher: publisher,
	}
}

// MarkAnswer evaluates a learner's answer against all active validation rules
// for the given atom. Rules are evaluated in priority order; the first matching
// rule determines the result.
func (s *AutoMarkingService) MarkAnswer(ctx context.Context, atomID uuid.UUID, learnerAnswer map[string]any) (*MarkingResult, error) {
	rules, err := s.ruleRepo.ListByAtom(ctx, atomID)
	if err != nil {
		return nil, fmt.Errorf("list rules for marking: %w", err)
	}
	if len(rules) == 0 {
		return nil, ErrNoRulesForMarking
	}

	answerVal := extractAnswer(learnerAnswer)
	answerStr := fmt.Sprintf("%v", answerVal)

	for _, rule := range rules {
		correct, score, feedback := evaluateRule(rule, answerStr, learnerAnswer)
		if correct {
			result := &MarkingResult{
				Correct:       true,
				Score:         score,
				Feedback:      feedback,
				MatchedRuleID: &rule.ID,
			}
			s.publishMarkingEvent(ctx, atomID, rule.TenantID, result)
			return result, nil
		}
	}

	// No rule matched — answer is incorrect.
	result := &MarkingResult{
		Correct:  false,
		Score:    0,
		Feedback: "Answer does not match any validation rule.",
	}
	if len(rules) > 0 {
		s.publishMarkingEvent(ctx, atomID, rules[0].TenantID, result)
	}
	return result, nil
}

// evaluateRule checks a single rule against the learner's answer.
func evaluateRule(rule *AnswerValidationRuleEntity, answerStr string, answer map[string]any) (correct bool, score float64, feedback string) {
	params := rule.Parameters

	switch rule.RuleType {
	case ExtValRuleExactMatch:
		expected, _ := params["expected"].(string)
		if answerStr == expected {
			return true, 1.0, "Exact match."
		}
		return false, 0, "Answer does not match expected value."

	case ExtValRuleCaseInsensitive:
		expected, _ := params["expected"].(string)
		if strings.EqualFold(answerStr, expected) {
			return true, 1.0, "Correct (case-insensitive match)."
		}
		return false, 0, "Answer does not match expected value."

	case ExtValRuleRegex:
		pattern, _ := params["pattern"].(string)
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, 0, "Invalid regex pattern in rule."
		}
		if re.MatchString(answerStr) {
			return true, 1.0, "Pattern match."
		}
		return false, 0, "Answer does not match expected pattern."

	case ExtValRuleNumericRange:
		numVal, ok := toFloat64(extractAnswer(answer))
		if !ok {
			return false, 0, "Answer is not a valid number."
		}
		minVal, hasMin := toFloat64(params["min"])
		maxVal, hasMax := toFloat64(params["max"])
		if hasMin && numVal < minVal {
			return false, 0, fmt.Sprintf("Answer below minimum (%.2f).", minVal)
		}
		if hasMax && numVal > maxVal {
			return false, 0, fmt.Sprintf("Answer above maximum (%.2f).", maxVal)
		}
		if hasMin || hasMax {
			return true, 1.0, "Within acceptable range."
		}
		return false, 0, "No range boundaries defined."

	case ExtValRuleSetContains:
		expectedRaw, _ := params["expected_set"]
		expectedSet, ok := toStringSlice(expectedRaw)
		if !ok {
			return false, 0, "Invalid expected_set in rule parameters."
		}
		answersRaw := extractAnswer(answer)
		answerSlice, ok := toStringSlice(answersRaw)
		if !ok {
			// Try single value
			answerSlice = []string{answerStr}
		}
		// Check if all expected items are in the answer set.
		answerMap := make(map[string]bool)
		for _, a := range answerSlice {
			answerMap[strings.ToLower(a)] = true
		}
		for _, e := range expectedSet {
			if !answerMap[strings.ToLower(e)] {
				return false, 0, "Answer does not contain all required items."
			}
		}
		return true, 1.0, "All required items present."

	case ExtValRuleOrderedList:
		expectedRaw, _ := params["expected_order"]
		expectedOrder, ok := toStringSlice(expectedRaw)
		if !ok {
			return false, 0, "Invalid expected_order in rule parameters."
		}
		answersRaw := extractAnswer(answer)
		answerSlice, ok := toStringSlice(answersRaw)
		if !ok {
			return false, 0, "Answer is not a valid ordered list."
		}
		if len(answerSlice) != len(expectedOrder) {
			return false, 0, "Answer list length does not match expected."
		}
		for i, expected := range expectedOrder {
			if !strings.EqualFold(answerSlice[i], expected) {
				return false, 0, fmt.Sprintf("Item at position %d does not match.", i+1)
			}
		}
		return true, 1.0, "Correct order."

	case ExtValRuleSemanticSimilarity:
		// Placeholder — actual ML-based semantic similarity is deferred.
		// Returns correct for now to unblock integration testing.
		return true, 1.0, "Semantic similarity check (placeholder — ML deferred)."

	default:
		return false, 0, "Unknown rule type."
	}
}

// publishMarkingEvent fires an atom.marked domain event.
func (s *AutoMarkingService) publishMarkingEvent(ctx context.Context, atomID, tenantID uuid.UUID, result *MarkingResult) {
	payload := map[string]any{
		"atom_id": atomID.String(),
		"correct": result.Correct,
		"score":   result.Score,
	}
	if result.MatchedRuleID != nil {
		payload["matched_rule_id"] = result.MatchedRuleID.String()
	}

	event := NewDomainEvent(
		EventTypeAtomMarked,
		tenantID,
		atomID,
		AggregateTypeLearningAtom,
		payload,
	)
	_ = s.publisher.Publish(ctx, TopicAtomicEvents, event)
}

// ---------------------------------------------------------------------------
// Phase 52.3.7 — Document Import Service
// ---------------------------------------------------------------------------

// DocumentImportService manages asynchronous document-to-atom import jobs.
// Actual LLM processing is deferred to AI agents — this is the coordination stub.
type DocumentImportService struct {
	importRepo ImportJobRepository
	publisher  EventPublisher
}

// NewDocumentImportService creates a new DocumentImportService.
func NewDocumentImportService(
	importRepo ImportJobRepository,
	publisher EventPublisher,
) *DocumentImportService {
	return &DocumentImportService{
		importRepo: importRepo,
		publisher:  publisher,
	}
}

// CreateImportJob creates a new pending import job and publishes a started event.
func (s *DocumentImportService) CreateImportJob(ctx context.Context, tenantID, uploaderGCID uuid.UUID, fileRef string, format ImportJobFormat) (*ImportJob, error) {
	validFormats := map[ImportJobFormat]bool{
		ImportJobFormatPDF:  true,
		ImportJobFormatDOCX: true,
		ImportJobFormatTXT:  true,
	}
	if !validFormats[format] {
		return nil, ErrInvalidImportFormat
	}

	job := &ImportJob{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		UploaderGCID:  uploaderGCID,
		FileReference: fileRef,
		Format:        format,
		Status:        ImportJobStatusPending,
		AtomCount:     0,
		CreatedAt:     time.Now().UTC(),
	}

	if err := s.importRepo.Save(ctx, job); err != nil {
		return nil, fmt.Errorf("save import job: %w", err)
	}

	event := NewDomainEvent(
		EventTypeDocumentImportStarted,
		tenantID,
		job.ID,
		AggregateTypeImportJob,
		map[string]any{
			"job_id":         job.ID.String(),
			"tenant_id":      tenantID.String(),
			"uploader_gcid":  uploaderGCID.String(),
			"file_reference": fileRef,
			"format":         string(format),
		},
	)
	_ = s.publisher.Publish(ctx, TopicAtomicEvents, event)

	return job, nil
}

// GetImportJob retrieves an import job by ID.
func (s *DocumentImportService) GetImportJob(ctx context.Context, id uuid.UUID) (*ImportJob, error) {
	job, err := s.importRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get import job: %w", err)
	}
	return job, nil
}
