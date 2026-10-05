package atomic

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Phase 52.3.1 — Cloze (Completion) Validation Tests
// ---------------------------------------------------------------------------

func TestValidateClozeContent_Valid(t *testing.T) {
	content := map[string]any{
		"text":    "The {{blank}} jumped over the {{blank}}.",
		"answers": []any{"cow", "moon"},
	}
	err := ValidateClozeContent(content)
	assert.NoError(t, err)
}

func TestValidateClozeContent_NoBlankMarkers(t *testing.T) {
	content := map[string]any{
		"text":    "No blanks here.",
		"answers": []any{"a"},
	}
	err := ValidateClozeContent(content)
	assert.ErrorIs(t, err, ErrClozeNoBlankMarkers)
}

func TestValidateClozeContent_MissingText(t *testing.T) {
	content := map[string]any{
		"answers": []any{"a"},
	}
	err := ValidateClozeContent(content)
	assert.ErrorIs(t, err, ErrClozeNoBlankMarkers)
}

func TestValidateClozeContent_AnswerCountMismatch(t *testing.T) {
	content := map[string]any{
		"text":    "The {{blank}} jumped over the {{blank}}.",
		"answers": []any{"cow"}, // only 1 answer for 2 blanks
	}
	err := ValidateClozeContent(content)
	assert.ErrorIs(t, err, ErrClozeAnswerCountMismatch)
}

func TestValidateClozeContent_MissingAnswers(t *testing.T) {
	content := map[string]any{
		"text": "The {{blank}} is missing answers.",
	}
	err := ValidateClozeContent(content)
	assert.ErrorIs(t, err, ErrClozeAnswerCountMismatch)
}

// ---------------------------------------------------------------------------
// Phase 52.3.2 — Table Completion Validation Tests
// ---------------------------------------------------------------------------

func TestValidateTableCompletionContent_Valid(t *testing.T) {
	content := map[string]any{
		"headers": []any{"Name", "Value"},
		"rows": []any{
			map[string]any{
				"cells": []any{
					map[string]any{"value": "Hydrogen", "is_blank": false},
					map[string]any{"value": "", "is_blank": true, "correct_value": "1"},
				},
			},
		},
	}
	err := ValidateTableCompletionContent(content)
	assert.NoError(t, err)
}

func TestValidateTableCompletionContent_NoHeaders(t *testing.T) {
	content := map[string]any{
		"rows": []any{
			map[string]any{
				"cells": []any{
					map[string]any{"value": "", "is_blank": true, "correct_value": "1"},
				},
			},
		},
	}
	err := ValidateTableCompletionContent(content)
	assert.ErrorIs(t, err, ErrTableNoHeaders)
}

func TestValidateTableCompletionContent_EmptyHeaders(t *testing.T) {
	content := map[string]any{
		"headers": []any{},
		"rows": []any{
			map[string]any{
				"cells": []any{
					map[string]any{"value": "", "is_blank": true, "correct_value": "1"},
				},
			},
		},
	}
	err := ValidateTableCompletionContent(content)
	assert.ErrorIs(t, err, ErrTableNoHeaders)
}

func TestValidateTableCompletionContent_NoBlankCells(t *testing.T) {
	content := map[string]any{
		"headers": []any{"Name", "Value"},
		"rows": []any{
			map[string]any{
				"cells": []any{
					map[string]any{"value": "Hydrogen", "is_blank": false},
					map[string]any{"value": "1", "is_blank": false},
				},
			},
		},
	}
	err := ValidateTableCompletionContent(content)
	assert.ErrorIs(t, err, ErrTableNoBlankCells)
}

// ---------------------------------------------------------------------------
// Phase 52.3.3 — Multi-Select Validation Tests
// ---------------------------------------------------------------------------

func TestValidateMultiSelectContent_Valid(t *testing.T) {
	content := map[string]any{
		"question": "Which are primary colors?",
		"options": []any{
			map[string]any{"text": "Red", "correct": true},
			map[string]any{"text": "Blue", "correct": true},
			map[string]any{"text": "Green", "correct": false},
		},
	}
	err := ValidateMultiSelectContent(content)
	assert.NoError(t, err)
}

func TestValidateMultiSelectContent_TooFewCorrect(t *testing.T) {
	content := map[string]any{
		"question": "Pick the right one.",
		"options": []any{
			map[string]any{"text": "A", "correct": true}, // only 1 correct
			map[string]any{"text": "B", "correct": false},
		},
	}
	err := ValidateMultiSelectContent(content)
	assert.ErrorIs(t, err, ErrMultiSelectTooFewCorrect)
}

func TestValidateMultiSelectContent_NoIncorrect(t *testing.T) {
	content := map[string]any{
		"question": "All correct.",
		"options": []any{
			map[string]any{"text": "A", "correct": true},
			map[string]any{"text": "B", "correct": true},
		},
	}
	err := ValidateMultiSelectContent(content)
	assert.ErrorIs(t, err, ErrMultiSelectNoIncorrect)
}

func TestValidateMultiSelectContent_NoOptions(t *testing.T) {
	content := map[string]any{
		"question": "No options provided.",
	}
	err := ValidateMultiSelectContent(content)
	assert.ErrorIs(t, err, ErrMultiSelectNoOptions)
}

// ---------------------------------------------------------------------------
// ValidateAtomContent Dispatch Tests
// ---------------------------------------------------------------------------

func TestValidateAtomContent_CompletionType(t *testing.T) {
	content := map[string]any{
		"text":    "The {{blank}} is.",
		"answers": []any{"answer"},
	}
	err := ValidateAtomContent(AtomTypeCompletion, content)
	assert.NoError(t, err)
}

func TestValidateAtomContent_TableCompletionType(t *testing.T) {
	content := map[string]any{
		"headers": []any{"H1"},
		"rows": []any{
			map[string]any{
				"cells": []any{
					map[string]any{"value": "", "is_blank": true, "correct_value": "x"},
				},
			},
		},
	}
	err := ValidateAtomContent(AtomTypeTableCompletion, content)
	assert.NoError(t, err)
}

func TestValidateAtomContent_MultiSelectType(t *testing.T) {
	content := map[string]any{
		"question": "Q",
		"options": []any{
			map[string]any{"text": "A", "correct": true},
			map[string]any{"text": "B", "correct": true},
			map[string]any{"text": "C", "correct": false},
		},
	}
	err := ValidateAtomContent(AtomTypeMultiSelect, content)
	assert.NoError(t, err)
}

func TestValidateAtomContent_OtherTypesNoValidation(t *testing.T) {
	err := ValidateAtomContent(AtomTypeMultipleChoice, map[string]any{"any": "thing"})
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Phase 52.3.4 — AnswerValidationRuleService Tests
// ---------------------------------------------------------------------------

func TestAnswerValidationRuleService_CreateRule(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	atomRepo := &mockAtomRepo{}
	pub := &mockEventPublisher{}

	svc := NewAnswerValidationRuleService(ruleRepo, atomRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	atomRepo.On("GetByID", ctx, atomID).Return(&LearningAtom{
		ID: atomID, TenantID: tenantID,
	}, nil)
	ruleRepo.On("Save", ctx, mock.AnythingOfType("*atomic.AnswerValidationRuleEntity")).Return(nil)

	rule := &AnswerValidationRuleEntity{
		TenantID: tenantID,
		AtomID:   atomID,
		RuleType: ExtValRuleExactMatch,
		Parameters: map[string]any{
			"expected": "42",
		},
		Priority: 1,
	}

	created, err := svc.CreateRule(ctx, rule)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)
	assert.True(t, created.IsActive)
	assert.Equal(t, ExtValRuleExactMatch, created.RuleType)
}

func TestAnswerValidationRuleService_CreateRule_InvalidType(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	atomRepo := &mockAtomRepo{}
	pub := &mockEventPublisher{}

	svc := NewAnswerValidationRuleService(ruleRepo, atomRepo, pub)

	rule := &AnswerValidationRuleEntity{
		RuleType: "invalid_type",
	}

	_, err := svc.CreateRule(ctx, rule)
	assert.ErrorIs(t, err, ErrInvalidValidationRuleType)
}

func TestAnswerValidationRuleService_ListRules(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	atomRepo := &mockAtomRepo{}
	pub := &mockEventPublisher{}

	svc := NewAnswerValidationRuleService(ruleRepo, atomRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	rules := []*AnswerValidationRuleEntity{
		{ID: uuid.Must(uuid.NewV7()), AtomID: atomID, RuleType: ExtValRuleExactMatch, Priority: 1},
	}

	ruleRepo.On("ListByAtom", ctx, atomID).Return(rules, nil)

	result, err := svc.ListRules(ctx, atomID)
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

// ---------------------------------------------------------------------------
// Phase 52.3.5 — LayoutService Tests
// ---------------------------------------------------------------------------

func TestLayoutService_SetLayout(t *testing.T) {
	ctx := context.Background()
	layoutRepo := &mockLayoutRepo{}
	atomRepo := &mockAtomRepo{}

	svc := NewLayoutService(layoutRepo, atomRepo)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	atomRepo.On("GetByID", ctx, atomID).Return(&LearningAtom{
		ID: atomID, TenantID: tenantID,
	}, nil)
	layoutRepo.On("Upsert", ctx, mock.AnythingOfType("*atomic.PresentationLayout")).Return(nil)

	layout := &PresentationLayout{
		TenantID:   tenantID,
		AtomID:     atomID,
		LayoutType: LayoutTypeCard,
		Config: map[string]any{
			"theme":     "dark",
			"font_size": 16,
		},
		IsDefault: true,
	}

	result, err := svc.SetLayout(ctx, layout)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, result.ID)
	assert.Equal(t, LayoutTypeCard, result.LayoutType)
}

func TestLayoutService_SetLayout_InvalidType(t *testing.T) {
	ctx := context.Background()
	layoutRepo := &mockLayoutRepo{}
	atomRepo := &mockAtomRepo{}

	svc := NewLayoutService(layoutRepo, atomRepo)

	layout := &PresentationLayout{
		LayoutType: "invalid",
	}

	_, err := svc.SetLayout(ctx, layout)
	assert.ErrorIs(t, err, ErrInvalidLayoutType)
}

func TestLayoutService_GetLayout(t *testing.T) {
	ctx := context.Background()
	layoutRepo := &mockLayoutRepo{}
	atomRepo := &mockAtomRepo{}

	svc := NewLayoutService(layoutRepo, atomRepo)

	atomID := uuid.Must(uuid.NewV7())
	expected := &PresentationLayout{
		ID:         uuid.Must(uuid.NewV7()),
		AtomID:     atomID,
		LayoutType: LayoutTypeFullscreen,
	}

	layoutRepo.On("GetByAtom", ctx, atomID).Return(expected, nil)

	result, err := svc.GetLayout(ctx, atomID)
	require.NoError(t, err)
	assert.Equal(t, expected.ID, result.ID)
}

// ---------------------------------------------------------------------------
// Phase 52.3.6 — AutoMarkingService Tests
// ---------------------------------------------------------------------------

func TestAutoMarkingService_MarkAnswer_ExactMatch(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	ruleID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       ruleID,
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleExactMatch,
			Parameters: map[string]any{
				"expected": "42",
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "42"})
	require.NoError(t, err)
	assert.True(t, result.Correct)
	assert.Equal(t, 1.0, result.Score)
	assert.NotNil(t, result.MatchedRuleID)
	assert.Equal(t, ruleID, *result.MatchedRuleID)
}

func TestAutoMarkingService_MarkAnswer_CaseInsensitive(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       uuid.Must(uuid.NewV7()),
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleCaseInsensitive,
			Parameters: map[string]any{
				"expected": "Hello World",
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "hello world"})
	require.NoError(t, err)
	assert.True(t, result.Correct)
}

func TestAutoMarkingService_MarkAnswer_Regex(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       uuid.Must(uuid.NewV7()),
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleRegex,
			Parameters: map[string]any{
				"pattern": `^\d{3}-\d{4}$`,
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "123-4567"})
	require.NoError(t, err)
	assert.True(t, result.Correct)
}

func TestAutoMarkingService_MarkAnswer_NumericRange(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       uuid.Must(uuid.NewV7()),
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleNumericRange,
			Parameters: map[string]any{
				"min": float64(10),
				"max": float64(20),
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": float64(15)})
	require.NoError(t, err)
	assert.True(t, result.Correct)
}

func TestAutoMarkingService_MarkAnswer_NoRules(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{}, nil)

	_, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "42"})
	assert.ErrorIs(t, err, ErrNoRulesForMarking)
}

func TestAutoMarkingService_MarkAnswer_Incorrect(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       uuid.Must(uuid.NewV7()),
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleExactMatch,
			Parameters: map[string]any{
				"expected": "42",
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "wrong"})
	require.NoError(t, err)
	assert.False(t, result.Correct)
	assert.Equal(t, float64(0), result.Score)
}

func TestAutoMarkingService_MarkAnswer_SemanticSimilarity(t *testing.T) {
	ctx := context.Background()
	ruleRepo := &mockValidationRuleRepo{}
	pub := &mockEventPublisher{}

	svc := NewAutoMarkingService(ruleRepo, pub)

	atomID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	ruleRepo.On("ListByAtom", ctx, atomID).Return([]*AnswerValidationRuleEntity{
		{
			ID:       uuid.Must(uuid.NewV7()),
			TenantID: tenantID,
			AtomID:   atomID,
			RuleType: ExtValRuleSemanticSimilarity,
			Parameters: map[string]any{
				"threshold": 0.8,
			},
			Priority: 1,
			IsActive: true,
		},
	}, nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	result, err := svc.MarkAnswer(ctx, atomID, map[string]any{"value": "any answer"})
	require.NoError(t, err)
	// Placeholder always returns correct.
	assert.True(t, result.Correct)
}

// ---------------------------------------------------------------------------
// Phase 52.3.7 — DocumentImportService Tests
// ---------------------------------------------------------------------------

func TestDocumentImportService_CreateImportJob(t *testing.T) {
	ctx := context.Background()
	importRepo := &mockImportJobRepo{}
	pub := &mockEventPublisher{}

	svc := NewDocumentImportService(importRepo, pub)

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	importRepo.On("Save", ctx, mock.AnythingOfType("*atomic.ImportJob")).Return(nil)
	pub.On("Publish", ctx, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).Return(nil)

	job, err := svc.CreateImportJob(ctx, tenantID, gcid, "gs://bucket/file.pdf", ImportJobFormatPDF)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, job.ID)
	assert.Equal(t, ImportJobStatusPending, job.Status)
	assert.Equal(t, ImportJobFormatPDF, job.Format)
	assert.Equal(t, "gs://bucket/file.pdf", job.FileReference)
}

func TestDocumentImportService_CreateImportJob_InvalidFormat(t *testing.T) {
	ctx := context.Background()
	importRepo := &mockImportJobRepo{}
	pub := &mockEventPublisher{}

	svc := NewDocumentImportService(importRepo, pub)

	_, err := svc.CreateImportJob(ctx, uuid.New(), uuid.New(), "file.xyz", "xyz")
	assert.ErrorIs(t, err, ErrInvalidImportFormat)
}

func TestDocumentImportService_GetImportJob(t *testing.T) {
	ctx := context.Background()
	importRepo := &mockImportJobRepo{}
	pub := &mockEventPublisher{}

	svc := NewDocumentImportService(importRepo, pub)

	jobID := uuid.Must(uuid.NewV7())
	expected := &ImportJob{
		ID:     jobID,
		Status: ImportJobStatusProcessing,
	}

	importRepo.On("GetByID", ctx, jobID).Return(expected, nil)

	result, err := svc.GetImportJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobID, result.ID)
}
