package atomic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errRepoFailure = errors.New("repository failure")

// ---------------------------------------------------------------------------
// Model helpers
// ---------------------------------------------------------------------------

func TestAtomRevision_IsPublished(t *testing.T) {
	t.Parallel()

	assert.True(t, (&AtomRevision{VisibilityStatus: RevisionVisibilityPublished}).IsPublished())
	assert.False(t, (&AtomRevision{VisibilityStatus: RevisionVisibilityDraft}).IsPublished())
	assert.False(t, (&AtomRevision{VisibilityStatus: RevisionVisibilityArchived}).IsPublished())
}

func TestSessionStatus_IsValid(t *testing.T) {
	t.Parallel()

	assert.True(t, SessionStatusNotStarted.IsValid())
	assert.True(t, SessionStatusActive.IsValid())
	assert.True(t, SessionStatusPaused.IsValid())
	assert.True(t, SessionStatusSubmitted.IsValid())
	assert.True(t, SessionStatusGraded.IsValid())
	assert.False(t, SessionStatus("unknown").IsValid())
}

func TestStudyListVisibility_IsValid(t *testing.T) {
	t.Parallel()

	assert.True(t, StudyListVisibilityPrivate.IsValid())
	assert.True(t, StudyListVisibilityShared.IsValid())
	assert.True(t, StudyListVisibilityPublic.IsValid())
	assert.False(t, StudyListVisibility("unknown").IsValid())
}

// ---------------------------------------------------------------------------
// validation.go helpers
// ---------------------------------------------------------------------------

func TestExtractAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		answer map[string]any
		want   any
	}{
		{"value key", map[string]any{"value": "v"}, "v"},
		{"selected_option key", map[string]any{"selected_option": 2}, 2},
		{"text key", map[string]any{"text": "hello"}, "hello"},
		{"code key", map[string]any{"code": "print(1)"}, "print(1)"},
		{"answers slice first element", map[string]any{"answers": []any{"a", "b"}}, "a"},
		{"answers empty slice", map[string]any{"answers": []any{}}, nil},
		{"answers not a slice", map[string]any{"answers": "x"}, nil},
		{"no known keys", map[string]any{"other": 1}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractAnswer(tt.answer))
		})
	}
}

func TestToFloat64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"float64", 1.5, 1.5, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", 3, 3.0, true},
		{"int64", int64(4), 4.0, true},
		{"json number", json.Number("5.5"), 5.5, true},
		{"json number invalid", json.Number("abc"), 0, false},
		{"string not numeric", "nope", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := toFloat64(tt.in)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 0.0001)
			}
		})
	}
}

func TestToStringSlice(t *testing.T) {
	t.Parallel()

	got, ok := toStringSlice([]string{"a", "b"})
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, got)

	got, ok = toStringSlice([]any{"x", "y"})
	require.True(t, ok)
	assert.Equal(t, []string{"x", "y"}, got)

	_, ok = toStringSlice([]any{"x", 42})
	assert.False(t, ok)

	_, ok = toStringSlice("not-a-slice")
	assert.False(t, ok)
}

func TestValidateRegex_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("expected not a map", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRegex, Expected: "^a+$"}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": "aaa"}))
	})

	t.Run("answer value missing", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRegex, Expected: map[string]any{"pattern": "^a+$"}}
		assert.False(t, ValidateAnswer(rule, map[string]any{}))
	})

	t.Run("pattern not a string", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRegex, Expected: map[string]any{"pattern": 42}}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": "aaa"}))
	})
}

func TestValidateRange_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("expected not a map", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRange, Expected: 5}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": 5}))
	})

	t.Run("answer value missing", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRange, Expected: map[string]any{"min": 1}}
		assert.False(t, ValidateAnswer(rule, map[string]any{}))
	})

	t.Run("numeric string answer parsed", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRange, Expected: map[string]any{"min": 1, "max": 100}}
		assert.True(t, ValidateAnswer(rule, map[string]any{"text": "78.54"}))
	})

	t.Run("non-numeric string answer", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRange, Expected: map[string]any{"min": 1, "max": 100}}
		assert.False(t, ValidateAnswer(rule, map[string]any{"text": "abc"}))
	})

	t.Run("non-numeric non-string answer", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleRange, Expected: map[string]any{"min": 1, "max": 100}}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": true}))
	})
}

func TestValidateKeyword_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("expected not a map", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleKeyword, Expected: "kw"}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": "kw"}))
	})

	t.Run("keywords missing", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleKeyword, Expected: map[string]any{}}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": "kw"}))
	})

	t.Run("keywords not a slice", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleKeyword, Expected: map[string]any{"keywords": 42}}
		assert.False(t, ValidateAnswer(rule, map[string]any{"value": "kw"}))
	})

	t.Run("answer value missing", func(t *testing.T) {
		t.Parallel()
		rule := AnswerValidationRule{RuleType: ValidationRuleKeyword, Expected: map[string]any{"keywords": []any{"kw"}}}
		assert.False(t, ValidateAnswer(rule, map[string]any{}))
	})
}

// ---------------------------------------------------------------------------
// AtomService error paths
// ---------------------------------------------------------------------------

func TestAtomService_CreateAtom_PublishError(t *testing.T) {
	t.Parallel()

	atomRepo := new(mockAtomRepo)
	atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

	svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
	_, err := svc.CreateAtom(context.Background(), &LearningAtom{
		TenantID:   uuid.Must(uuid.NewV7()),
		AtomType:   AtomTypeMultipleChoice,
		Difficulty: 3,
		CreatedBy:  uuid.Must(uuid.NewV7()),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestAtomService_UpdateAtomMetadata_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	t.Run("tracks language change and publishes", func(t *testing.T) {
		t.Parallel()

		existing := &LearningAtom{ID: atomID, AtomType: AtomTypeMultipleChoice, Difficulty: 3, LanguageCode: "en", Status: AtomStatusDraft}
		updated := &LearningAtom{ID: atomID, AtomType: AtomTypeMultipleChoice, Difficulty: 3, LanguageCode: "de"}

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(existing, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
		publisher := new(mockEventPublisher)
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
		_, err := svc.UpdateAtomMetadata(context.Background(), updated)
		require.NoError(t, err)
		publisher.AssertExpectations(t)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		existing := &LearningAtom{ID: atomID, Difficulty: 3, Status: AtomStatusDraft}
		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(existing, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
		_, err := svc.UpdateAtomMetadata(context.Background(), &LearningAtom{ID: atomID, Difficulty: 3})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates publish error", func(t *testing.T) {
		t.Parallel()

		existing := &LearningAtom{ID: atomID, Difficulty: 3, Status: AtomStatusDraft}
		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(existing, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
		publisher := new(mockEventPublisher)
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
		_, err := svc.UpdateAtomMetadata(context.Background(), &LearningAtom{ID: atomID, Difficulty: 3})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAtomService_ArchiveAtom_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	t.Run("propagates get error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
		err := svc.ArchiveAtom(context.Background(), atomID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("rejects already archived atom", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID, Status: AtomStatusArchived}, nil)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
		err := svc.ArchiveAtom(context.Background(), atomID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrAtomAlreadyArchived)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID, Status: AtomStatusDraft}, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
		err := svc.ArchiveAtom(context.Background(), atomID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates soft delete error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID, Status: AtomStatusDraft}, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
		atomRepo.On("SoftDelete", mock.Anything, atomID).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), new(mockEventPublisher))
		err := svc.ArchiveAtom(context.Background(), atomID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates publish error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID, Status: AtomStatusDraft}, nil)
		atomRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LearningAtom")).Return(nil)
		atomRepo.On("SoftDelete", mock.Anything, atomID).Return(nil)
		publisher := new(mockEventPublisher)
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, new(mockRevisionRepo), publisher)
		err := svc.ArchiveAtom(context.Background(), atomID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAtomService_PublishRevision_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	content := map[string]any{"body": "text"}

	t.Run("propagates current published lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PublishRevision(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates archive previous revision error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		current := &AtomRevision{ID: uuid.Must(uuid.NewV7()), AtomID: atomID, RevisionNumber: 1}
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(current, nil)
		revRepo.On("UpdateVisibility", mock.Anything, current.ID, RevisionVisibilityArchived, mock.Anything).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PublishRevision(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates latest revision lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PublishRevision(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates append error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PublishRevision(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAtomService_SaveDraft_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	content := map[string]any{"body": "text"}

	t.Run("propagates existing draft lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetDraft", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.SaveDraft(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates latest revision lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetDraft", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.SaveDraft(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates append error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		revRepo := new(mockRevisionRepo)
		revRepo.On("GetDraft", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("GetLatest", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomRevision")).Return(errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.SaveDraft(context.Background(), atomID, content, nil, nil, uuid.Must(uuid.NewV7()))

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAtomService_PromoteDraft_ErrorPaths(t *testing.T) {
	t.Parallel()

	revisionID := uuid.Must(uuid.NewV7())
	atomID := uuid.Must(uuid.NewV7())
	draft := &AtomRevision{ID: revisionID, AtomID: atomID, VisibilityStatus: RevisionVisibilityDraft}

	t.Run("propagates atom lookup error", func(t *testing.T) {
		t.Parallel()

		revRepo := new(mockRevisionRepo)
		revRepo.On("GetByID", mock.Anything, revisionID).Return(draft, nil)
		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PromoteDraft(context.Background(), revisionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates current published lookup error", func(t *testing.T) {
		t.Parallel()

		revRepo := new(mockRevisionRepo)
		revRepo.On("GetByID", mock.Anything, revisionID).Return(draft, nil)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, errRepoFailure)
		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PromoteDraft(context.Background(), revisionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates update visibility error", func(t *testing.T) {
		t.Parallel()

		revRepo := new(mockRevisionRepo)
		revRepo.On("GetByID", mock.Anything, revisionID).Return(draft, nil)
		revRepo.On("GetLatestPublished", mock.Anything, atomID).Return(nil, nil)
		revRepo.On("UpdateVisibility", mock.Anything, revisionID, RevisionVisibilityPublished, mock.Anything).Return(errRepoFailure)
		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)

		svc := NewAtomService(atomRepo, revRepo, new(mockEventPublisher))
		_, err := svc.PromoteDraft(context.Background(), revisionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAtomService_RecordValidation(t *testing.T) {
	t.Parallel()

	t.Run("publishes answer validated event with optional fields", func(t *testing.T) {
		t.Parallel()

		publisher := new(mockEventPublisher)
		var captured DomainEvent
		publisher.On("Publish", mock.Anything, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).
			Run(func(args mock.Arguments) {
				captured = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewAtomService(new(mockAtomRepo), new(mockRevisionRepo), publisher)

		timeSpent := 42
		sessionID := uuid.Must(uuid.NewV7())
		atomID := uuid.Must(uuid.NewV7())
		revisionID := uuid.Must(uuid.NewV7())
		tenantID := uuid.Must(uuid.NewV7())
		gcid := uuid.Must(uuid.NewV7())

		err := svc.RecordValidation(context.Background(), atomID, revisionID, tenantID, gcid, true, "multiple_choice", 3, "exact_match", &timeSpent, &sessionID)

		require.NoError(t, err)
		assert.Equal(t, EventTypeAnswerValidated, captured.EventType)
		assert.Equal(t, 42, captured.Payload["time_spent_seconds"])
		assert.Equal(t, sessionID.String(), captured.Payload["session_id"])
		assert.Equal(t, true, captured.Payload["correct"])
		publisher.AssertExpectations(t)
	})

	t.Run("omits optional fields when nil", func(t *testing.T) {
		t.Parallel()

		publisher := new(mockEventPublisher)
		var captured DomainEvent
		publisher.On("Publish", mock.Anything, TopicAtomicEvents, mock.AnythingOfType("atomic.DomainEvent")).
			Run(func(args mock.Arguments) {
				captured = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewAtomService(new(mockAtomRepo), new(mockRevisionRepo), publisher)

		err := svc.RecordValidation(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), false, "code", 2, "regex", nil, nil)

		require.NoError(t, err)
		_, hasTime := captured.Payload["time_spent_seconds"]
		_, hasSession := captured.Payload["session_id"]
		assert.False(t, hasTime)
		assert.False(t, hasSession)
	})

	t.Run("propagates publish error", func(t *testing.T) {
		t.Parallel()

		publisher := new(mockEventPublisher)
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

		svc := NewAtomService(new(mockAtomRepo), new(mockRevisionRepo), publisher)

		err := svc.RecordValidation(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), true, "code", 2, "regex", nil, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

// ---------------------------------------------------------------------------
// TopicService error paths
// ---------------------------------------------------------------------------

func TestTopicService_CreateTopic_PublishError(t *testing.T) {
	t.Parallel()

	topicRepo := new(mockTopicRepo)
	topicRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(nil)
	publisher := new(mockEventPublisher)
	publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

	svc := NewTopicService(topicRepo, publisher)
	parentID := uuid.Must(uuid.NewV7())
	_, err := svc.CreateTopic(context.Background(), &TopicNode{
		TenantID: uuid.Must(uuid.NewV7()),
		Name:     "Biology",
		ParentID: &parentID,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestTopicService_UpdateTopic_SaveError(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	topicRepo := new(mockTopicRepo)
	topicRepo.On("GetByID", mock.Anything, topicID).Return(&TopicNode{ID: topicID, Name: "Old"}, nil)
	topicRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.TopicNode")).Return(errRepoFailure)

	svc := NewTopicService(topicRepo, new(mockEventPublisher))
	_, err := svc.UpdateTopic(context.Background(), &TopicNode{ID: topicID, Name: "New"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestTopicService_DeleteTopic_SoftDeleteError(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	topicRepo := new(mockTopicRepo)
	topicRepo.On("GetByID", mock.Anything, topicID).Return(&TopicNode{ID: topicID}, nil)
	topicRepo.On("SoftDelete", mock.Anything, topicID).Return(errRepoFailure)

	svc := NewTopicService(topicRepo, new(mockEventPublisher))
	err := svc.DeleteTopic(context.Background(), topicID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestTopicService_MoveTopic_ErrorPaths(t *testing.T) {
	t.Parallel()

	topicID := uuid.Must(uuid.NewV7())
	newParentID := uuid.Must(uuid.NewV7())

	t.Run("propagates move error", func(t *testing.T) {
		t.Parallel()

		topicRepo := new(mockTopicRepo)
		topicRepo.On("GetByID", mock.Anything, topicID).Return(&TopicNode{ID: topicID}, nil)
		topicRepo.On("Move", mock.Anything, topicID, &newParentID).Return(errRepoFailure)

		svc := NewTopicService(topicRepo, new(mockEventPublisher))
		err := svc.MoveTopic(context.Background(), topicID, &newParentID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates publish error", func(t *testing.T) {
		t.Parallel()

		topicRepo := new(mockTopicRepo)
		topicRepo.On("GetByID", mock.Anything, topicID).Return(&TopicNode{ID: topicID}, nil)
		topicRepo.On("Move", mock.Anything, topicID, &newParentID).Return(nil)
		publisher := new(mockEventPublisher)
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(errRepoFailure)

		svc := NewTopicService(topicRepo, publisher)
		err := svc.MoveTopic(context.Background(), topicID, &newParentID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

// ---------------------------------------------------------------------------
// AssessmentSessionService error paths
// ---------------------------------------------------------------------------

func TestAssessmentSessionService_CreateSession_DefaultsAndSaveError(t *testing.T) {
	t.Parallel()

	t.Run("applies default delivery and navigation modes", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
		events := new(mockEventPublisher)
		events.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), events)
		session := &AssessmentSession{
			TenantID:      uuid.Must(uuid.NewV7()),
			GCID:          uuid.Must(uuid.NewV7()),
			SessionType:   SessionTypePracticeSet,
			StructureMode: StructureModeFlat,
			AtomIDs:       []uuid.UUID{uuid.Must(uuid.NewV7())},
		}

		err := svc.CreateSession(context.Background(), session)

		require.NoError(t, err)
		assert.Equal(t, DeliveryModeStraightUp, session.DeliveryMode)
		assert.Equal(t, NavigationModeFree, session.PaperNavigation)
		assert.Equal(t, NavigationModeFree, session.SectionNavigation)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		session := &AssessmentSession{
			TenantID:      uuid.Must(uuid.NewV7()),
			GCID:          uuid.Must(uuid.NewV7()),
			SessionType:   SessionTypeMockExam,
			StructureMode: StructureModeFlat,
			AtomIDs:       []uuid.UUID{uuid.Must(uuid.NewV7())},
		}

		err := svc.CreateSession(context.Background(), session)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_ListByLearner(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())

	t.Run("returns sessions for learner", func(t *testing.T) {
		t.Parallel()

		want := []*AssessmentSession{{ID: uuid.Must(uuid.NewV7()), GCID: gcid}}
		sessions := new(mockSessionRepo)
		sessions.On("ListByLearner", mock.Anything, gcid).Return(want, nil)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		got, err := svc.ListByLearner(context.Background(), gcid)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("ListByLearner", mock.Anything, gcid).Return(nil, errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.ListByLearner(context.Background(), gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_StartSession_ErrorPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())

	t.Run("rejects unexpected status", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatus("weird")}, nil)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.StartSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSessionNotActive)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusNotStarted}, nil)
		sessions.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.StartSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_PauseSession_ErrorPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())

	t.Run("propagates get error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(nil, errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.PauseSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusActive}, nil)
		sessions.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.PauseSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_SubmitSession_ErrorPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())

	t.Run("propagates get error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(nil, errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.SubmitSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusActive}, nil)
		sessions.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.SubmitSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_GradeSession_ErrorPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())

	t.Run("propagates get error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(nil, errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.GradeSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates interactions list error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusSubmitted}, nil)
		interactions := new(mockInteractionRepo)
		interactions.On("ListBySession", mock.Anything, sessionID).Return(nil, errRepoFailure)

		svc := NewAssessmentSessionService(sessions, interactions, new(mockEventPublisher))
		_, err := svc.GradeSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("initializes grade config when nil and averages scores", func(t *testing.T) {
		t.Parallel()

		scoreA, scoreB := 0.8, 0.4
		sessions := new(mockSessionRepo)
		session := &AssessmentSession{ID: sessionID, Status: SessionStatusSubmitted}
		sessions.On("GetByID", mock.Anything, sessionID).Return(session, nil)
		sessions.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(nil)
		interactions := new(mockInteractionRepo)
		interactions.On("ListBySession", mock.Anything, sessionID).Return([]*AtomInteraction{
			{Score: &scoreA},
			{Score: &scoreB},
			{Score: nil},
		}, nil)
		events := new(mockEventPublisher)
		events.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		svc := NewAssessmentSessionService(sessions, interactions, events)
		got, err := svc.GradeSession(context.Background(), sessionID)

		require.NoError(t, err)
		assert.InDelta(t, 0.6, got.CombinedGradeConfig["calculated_score"], 0.0001)
		assert.Equal(t, 2, got.CombinedGradeConfig["interactions_count"])
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		sessions := new(mockSessionRepo)
		sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusSubmitted, CombinedGradeConfig: map[string]any{"grading_mode": "manual"}}, nil)
		sessions.On("Update", mock.Anything, mock.AnythingOfType("*atomic.AssessmentSession")).Return(errRepoFailure)

		svc := NewAssessmentSessionService(sessions, new(mockInteractionRepo), new(mockEventPublisher))
		_, err := svc.GradeSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAssessmentSessionService_RecordInteraction_AppendError(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())
	sessions := new(mockSessionRepo)
	sessions.On("GetByID", mock.Anything, sessionID).Return(&AssessmentSession{ID: sessionID, Status: SessionStatusActive}, nil)
	interactions := new(mockInteractionRepo)
	interactions.On("Append", mock.Anything, mock.AnythingOfType("*atomic.AtomInteraction")).Return(errRepoFailure)

	svc := NewAssessmentSessionService(sessions, interactions, new(mockEventPublisher))
	err := svc.RecordInteraction(context.Background(), &AtomInteraction{
		SessionID:     sessionID,
		TenantID:      uuid.Must(uuid.NewV7()),
		GCID:          uuid.Must(uuid.NewV7()),
		AtomID:        uuid.Must(uuid.NewV7()),
		AttemptNumber: 1,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestAssessmentSessionService_ListInteractions_Error(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())
	interactions := new(mockInteractionRepo)
	interactions.On("ListBySession", mock.Anything, sessionID).Return(nil, errRepoFailure)

	svc := NewAssessmentSessionService(new(mockSessionRepo), interactions, new(mockEventPublisher))
	_, err := svc.ListInteractions(context.Background(), sessionID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

// ---------------------------------------------------------------------------
// LockedPathService error paths
// ---------------------------------------------------------------------------

func TestLockedPathService_CreatePath_SaveError(t *testing.T) {
	t.Parallel()

	paths := new(mockPathRepo)
	paths.On("Save", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(errRepoFailure)

	svc := NewLockedPathService(paths, new(mockEventPublisher))
	err := svc.CreatePath(context.Background(), &LockedPath{Title: "Path", TenantID: uuid.Must(uuid.NewV7())})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestLockedPathService_ListPaths(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns paths for tenant", func(t *testing.T) {
		t.Parallel()

		want := []*LockedPath{{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID}}
		paths := new(mockPathRepo)
		paths.On("ListByTenant", mock.Anything, tenantID).Return(want, nil)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		got, err := svc.ListPaths(context.Background(), tenantID)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		paths := new(mockPathRepo)
		paths.On("ListByTenant", mock.Anything, tenantID).Return(nil, errRepoFailure)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		_, err := svc.ListPaths(context.Background(), tenantID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestLockedPathService_AddStep_UpdateError(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	paths := new(mockPathRepo)
	paths.On("GetByID", mock.Anything, pathID).Return(&LockedPath{ID: pathID, TenantID: uuid.Must(uuid.NewV7())}, nil)
	paths.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(errRepoFailure)

	svc := NewLockedPathService(paths, new(mockEventPublisher))
	err := svc.AddStep(context.Background(), pathID, &LockedPathStep{AtomID: uuid.Must(uuid.NewV7())})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestLockedPathService_RemoveStep_ErrorPaths(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	stepID := uuid.Must(uuid.NewV7())

	t.Run("propagates get error", func(t *testing.T) {
		t.Parallel()

		paths := new(mockPathRepo)
		paths.On("GetByID", mock.Anything, pathID).Return(nil, errRepoFailure)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		err := svc.RemoveStep(context.Background(), pathID, stepID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		paths := new(mockPathRepo)
		paths.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
			ID:       pathID,
			TenantID: uuid.Must(uuid.NewV7()),
			Steps:    []LockedPathStep{{ID: stepID, StepOrder: 1}},
		}, nil)
		paths.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(errRepoFailure)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		err := svc.RemoveStep(context.Background(), pathID, stepID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestLockedPathService_ReorderSteps_ErrorPaths(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	stepID := uuid.Must(uuid.NewV7())

	t.Run("rejects unknown step id", func(t *testing.T) {
		t.Parallel()

		paths := new(mockPathRepo)
		paths.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
			ID:    pathID,
			Steps: []LockedPathStep{{ID: stepID, StepOrder: 1}},
		}, nil)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		err := svc.ReorderSteps(context.Background(), pathID, []uuid.UUID{uuid.Must(uuid.NewV7())})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrPathStepNotFound)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		paths := new(mockPathRepo)
		paths.On("GetByID", mock.Anything, pathID).Return(&LockedPath{
			ID:       pathID,
			TenantID: uuid.Must(uuid.NewV7()),
			Steps:    []LockedPathStep{{ID: stepID, StepOrder: 1}},
		}, nil)
		paths.On("Update", mock.Anything, mock.AnythingOfType("*atomic.LockedPath")).Return(errRepoFailure)

		svc := NewLockedPathService(paths, new(mockEventPublisher))
		err := svc.ReorderSteps(context.Background(), pathID, []uuid.UUID{stepID})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestLockedPathService_DeletePath_SoftDeleteError(t *testing.T) {
	t.Parallel()

	pathID := uuid.Must(uuid.NewV7())
	paths := new(mockPathRepo)
	paths.On("GetByID", mock.Anything, pathID).Return(&LockedPath{ID: pathID, TenantID: uuid.Must(uuid.NewV7())}, nil)
	paths.On("SoftDelete", mock.Anything, pathID).Return(errRepoFailure)

	svc := NewLockedPathService(paths, new(mockEventPublisher))
	err := svc.DeletePath(context.Background(), pathID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

// ---------------------------------------------------------------------------
// StudyListService error paths
// ---------------------------------------------------------------------------

func TestStudyListService_CreateList_SaveError(t *testing.T) {
	t.Parallel()

	lists := new(mockStudyListRepo)
	lists.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	err := svc.CreateList(context.Background(), &StudyList{Title: "List", TenantID: uuid.Must(uuid.NewV7())})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListService_ListByTenant(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns study lists for tenant", func(t *testing.T) {
		t.Parallel()

		want := []*StudyList{{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID}}
		lists := new(mockStudyListRepo)
		lists.On("ListByTenant", mock.Anything, tenantID).Return(want, nil)

		svc := NewStudyListService(lists, new(mockEventPublisher))
		got, err := svc.ListByTenant(context.Background(), tenantID)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		lists := new(mockStudyListRepo)
		lists.On("ListByTenant", mock.Anything, tenantID).Return(nil, errRepoFailure)

		svc := NewStudyListService(lists, new(mockEventPublisher))
		_, err := svc.ListByTenant(context.Background(), tenantID)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestStudyListService_UpdateList_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, Title: "Old"}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	err := svc.UpdateList(context.Background(), &StudyList{ID: listID, Title: "New"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListService_AddAtoms_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, AtomIDs: []uuid.UUID{}}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	err := svc.AddAtoms(context.Background(), listID, []uuid.UUID{uuid.Must(uuid.NewV7())})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListService_RemoveAtoms_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	atomID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, AtomIDs: []uuid.UUID{atomID}}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	err := svc.RemoveAtoms(context.Background(), listID, []uuid.UUID{atomID})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListService_DeleteList_SoftDeleteError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID}, nil)
	lists.On("SoftDelete", mock.Anything, listID).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	err := svc.DeleteList(context.Background(), listID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListService_ShareList_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, Visibility: StudyListVisibilityPrivate}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListService(lists, new(mockEventPublisher))
	_, err := svc.ShareList(context.Background(), listID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

// ---------------------------------------------------------------------------
// StudyListEnhancedService error paths
// ---------------------------------------------------------------------------

func TestStudyListEnhancedService_CreateStudyList_DefaultsAndSaveError(t *testing.T) {
	t.Parallel()

	t.Run("defaults visibility to private", func(t *testing.T) {
		t.Parallel()

		lists := new(mockStudyListRepo)
		var saved *StudyList
		lists.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).
			Run(func(args mock.Arguments) {
				saved = args.Get(1).(*StudyList)
			}).Return(nil)
		events := new(mockEventPublisher)
		events.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		svc := NewStudyListEnhancedService(lists, events)
		got, err := svc.CreateStudyList(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "Title", "desc", nil, "")

		require.NoError(t, err)
		assert.Equal(t, StudyListVisibilityPrivate, got.Visibility)
		assert.NotNil(t, saved.AtomIDs)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		lists := new(mockStudyListRepo)
		lists.On("Save", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

		svc := NewStudyListEnhancedService(lists, new(mockEventPublisher))
		_, err := svc.CreateStudyList(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "Title", "desc", nil, StudyListVisibilityPublic)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestStudyListEnhancedService_UpdateStudyList_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, Title: "Old"}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListEnhancedService(lists, new(mockEventPublisher))
	_, err := svc.UpdateStudyList(context.Background(), listID, "New", "desc", nil, StudyListVisibilityPrivate)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListEnhancedService_GenerateShareCode_UpdateError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID, Visibility: StudyListVisibilityPrivate}, nil)
	lists.On("Update", mock.Anything, mock.AnythingOfType("*atomic.StudyList")).Return(errRepoFailure)

	svc := NewStudyListEnhancedService(lists, new(mockEventPublisher))
	_, err := svc.GenerateShareCode(context.Background(), listID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestStudyListEnhancedService_DeleteStudyList_SoftDeleteError(t *testing.T) {
	t.Parallel()

	listID := uuid.Must(uuid.NewV7())
	lists := new(mockStudyListRepo)
	lists.On("GetByID", mock.Anything, listID).Return(&StudyList{ID: listID}, nil)
	lists.On("SoftDelete", mock.Anything, listID).Return(errRepoFailure)

	svc := NewStudyListEnhancedService(lists, new(mockEventPublisher))
	err := svc.DeleteStudyList(context.Background(), listID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

// ---------------------------------------------------------------------------
// Extended quiz content validators — malformed input branches
// ---------------------------------------------------------------------------

func TestValidateClozeContent_AnswersNotSlice(t *testing.T) {
	t.Parallel()

	err := ValidateClozeContent(map[string]any{
		"text":    "Fill {{blank}} here",
		"answers": 42,
	})
	assert.ErrorIs(t, err, ErrClozeAnswerCountMismatch)
}

func TestValidateTableCompletionContent_MalformedRows(t *testing.T) {
	t.Parallel()

	headers := map[string]any{"headers": []any{"H1"}}

	t.Run("rows not a slice", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": "nope"}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("empty rows", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": []any{}}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("non-map row is skipped", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": []any{"bad-row"}}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("row without cells is skipped", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": []any{map[string]any{}}}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("cells not a slice is skipped", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": []any{map[string]any{"cells": "bad"}}}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("non-map cell is skipped", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": []any{"H1"}, "rows": []any{map[string]any{"cells": []any{"bad"}}}}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoBlankCells)
	})

	t.Run("headers not a string slice", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"headers": 42}
		assert.ErrorIs(t, ValidateTableCompletionContent(content), ErrTableNoHeaders)
	})

	_ = headers
}

func TestValidateMultiSelectContent_MalformedOptions(t *testing.T) {
	t.Parallel()

	t.Run("options not a slice", func(t *testing.T) {
		t.Parallel()
		assert.ErrorIs(t, ValidateMultiSelectContent(map[string]any{"options": "bad"}), ErrMultiSelectNoOptions)
	})

	t.Run("non-map option is skipped, leaving no incorrect", func(t *testing.T) {
		t.Parallel()
		content := map[string]any{"options": []any{
			map[string]any{"correct": true},
			map[string]any{"correct": true},
			"bad-option",
		}}
		// Production skips non-map options entirely, so two correct options
		// with a non-map slot yield zero incorrect -> ErrMultiSelectNoIncorrect.
		assert.ErrorIs(t, ValidateMultiSelectContent(content), ErrMultiSelectNoIncorrect)
	})
}

// ---------------------------------------------------------------------------
// Extended quiz services — repository error paths
// ---------------------------------------------------------------------------

func TestAnswerValidationRuleService_CreateRule_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	t.Run("propagates atom lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewAnswerValidationRuleService(new(mockValidationRuleRepo), atomRepo, new(mockEventPublisher))
		_, err := svc.CreateRule(context.Background(), &AnswerValidationRuleEntity{AtomID: atomID, RuleType: ExtValRuleExactMatch})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		ruleRepo := new(mockValidationRuleRepo)
		ruleRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.AnswerValidationRuleEntity")).Return(errRepoFailure)

		svc := NewAnswerValidationRuleService(ruleRepo, atomRepo, new(mockEventPublisher))
		_, err := svc.CreateRule(context.Background(), &AnswerValidationRuleEntity{AtomID: atomID, RuleType: ExtValRuleExactMatch})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestAnswerValidationRuleService_ListRules_Error(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	ruleRepo := new(mockValidationRuleRepo)
	ruleRepo.On("ListByAtom", mock.Anything, atomID).Return(nil, errRepoFailure)

	svc := NewAnswerValidationRuleService(ruleRepo, new(mockAtomRepo), new(mockEventPublisher))
	_, err := svc.ListRules(context.Background(), atomID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestLayoutService_SetLayout_ErrorPaths(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())

	t.Run("propagates atom lookup error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(nil, errRepoFailure)

		svc := NewLayoutService(new(mockLayoutRepo), atomRepo)
		_, err := svc.SetLayout(context.Background(), &PresentationLayout{AtomID: atomID, LayoutType: LayoutTypeCard})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})

	t.Run("propagates upsert error", func(t *testing.T) {
		t.Parallel()

		atomRepo := new(mockAtomRepo)
		atomRepo.On("GetByID", mock.Anything, atomID).Return(&LearningAtom{ID: atomID}, nil)
		layoutRepo := new(mockLayoutRepo)
		layoutRepo.On("Upsert", mock.Anything, mock.AnythingOfType("*atomic.PresentationLayout")).Return(errRepoFailure)

		svc := NewLayoutService(layoutRepo, atomRepo)
		_, err := svc.SetLayout(context.Background(), &PresentationLayout{AtomID: atomID, LayoutType: LayoutTypeCard})

		require.Error(t, err)
		assert.ErrorIs(t, err, errRepoFailure)
	})
}

func TestLayoutService_GetLayout_Error(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	layoutRepo := new(mockLayoutRepo)
	layoutRepo.On("GetByAtom", mock.Anything, atomID).Return(nil, errRepoFailure)

	svc := NewLayoutService(layoutRepo, new(mockAtomRepo))
	_, err := svc.GetLayout(context.Background(), atomID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestAutoMarkingService_MarkAnswer_ListError(t *testing.T) {
	t.Parallel()

	atomID := uuid.Must(uuid.NewV7())
	ruleRepo := new(mockValidationRuleRepo)
	ruleRepo.On("ListByAtom", mock.Anything, atomID).Return(nil, errRepoFailure)

	svc := NewAutoMarkingService(ruleRepo, new(mockEventPublisher))
	_, err := svc.MarkAnswer(context.Background(), atomID, map[string]any{"value": "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestEvaluateRule(t *testing.T) {
	t.Parallel()

	rule := func(ruleType ExtendedValidationRuleType, params map[string]any) *AnswerValidationRuleEntity {
		return &AnswerValidationRuleEntity{ID: uuid.Must(uuid.NewV7()), TenantID: uuid.Must(uuid.NewV7()), RuleType: ruleType, Parameters: params}
	}

	tests := []struct {
		name        string
		rule        *AnswerValidationRuleEntity
		answerStr   string
		answer      map[string]any
		wantCorrect bool
	}{
		{"case insensitive no match", rule(ExtValRuleCaseInsensitive, map[string]any{"expected": "Paris"}), "london", map[string]any{"value": "london"}, false},
		{"regex invalid pattern", rule(ExtValRuleRegex, map[string]any{"pattern": "[unclosed"}), "abc", map[string]any{"value": "abc"}, false},
		{"regex no match", rule(ExtValRuleRegex, map[string]any{"pattern": "^z+$"}), "abc", map[string]any{"value": "abc"}, false},
		{"numeric range answer not a number", rule(ExtValRuleNumericRange, map[string]any{"min": 1.0}), "abc", map[string]any{"value": true}, false},
		{"numeric range below minimum", rule(ExtValRuleNumericRange, map[string]any{"min": 10.0}), "5", map[string]any{"value": 5.0}, false},
		{"numeric range above maximum", rule(ExtValRuleNumericRange, map[string]any{"max": 10.0}), "15", map[string]any{"value": 15.0}, false},
		{"numeric range no bounds defined", rule(ExtValRuleNumericRange, map[string]any{}), "5", map[string]any{"value": 5.0}, false},
		{"set contains invalid expected_set", rule(ExtValRuleSetContains, map[string]any{"expected_set": 42}), "a", map[string]any{"value": "a"}, false},
		{"set contains single value fallback match", rule(ExtValRuleSetContains, map[string]any{"expected_set": []any{"go"}}), "go", map[string]any{"value": "go"}, true},
		{"set contains missing item", rule(ExtValRuleSetContains, map[string]any{"expected_set": []any{"go", "rust"}}), "go", map[string]any{"value": "go"}, false},
		{"ordered list invalid expected_order", rule(ExtValRuleOrderedList, map[string]any{"expected_order": 42}), "a", map[string]any{"value": "a"}, false},
		{"ordered list answer not a list", rule(ExtValRuleOrderedList, map[string]any{"expected_order": []any{"a"}}), "a", map[string]any{"value": "a"}, false},
		{"ordered list length mismatch", rule(ExtValRuleOrderedList, map[string]any{"expected_order": []any{"a", "b"}}), "a", map[string]any{"value": []any{"a"}}, false},
		{"ordered list position mismatch", rule(ExtValRuleOrderedList, map[string]any{"expected_order": []any{"a", "b"}}), "b,a", map[string]any{"value": []any{"b", "a"}}, false},
		{"ordered list correct", rule(ExtValRuleOrderedList, map[string]any{"expected_order": []any{"a", "b"}}), "a,b", map[string]any{"value": []any{"a", "b"}}, true},
		{"set contains all present", rule(ExtValRuleSetContains, map[string]any{"expected_set": []any{"a", "b"}}), "a,b", map[string]any{"value": []any{"A", "B", "C"}}, true},
		{"semantic similarity placeholder", rule(ExtValRuleSemanticSimilarity, map[string]any{}), "anything", map[string]any{"value": "anything"}, true},
		{"unknown rule type", rule("unknown_type", map[string]any{}), "x", map[string]any{"value": "x"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			correct, _, feedback := evaluateRule(tt.rule, tt.answerStr, tt.answer)
			assert.Equal(t, tt.wantCorrect, correct)
			assert.NotEmpty(t, feedback)
		})
	}
}

func TestDocumentImportService_CreateImportJob_SaveError(t *testing.T) {
	t.Parallel()

	importRepo := new(mockImportJobRepo)
	importRepo.On("Save", mock.Anything, mock.AnythingOfType("*atomic.ImportJob")).Return(errRepoFailure)

	svc := NewDocumentImportService(importRepo, new(mockEventPublisher))
	_, err := svc.CreateImportJob(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "file.pdf", ImportJobFormatPDF)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}

func TestDocumentImportService_GetImportJob_Error(t *testing.T) {
	t.Parallel()

	jobID := uuid.Must(uuid.NewV7())
	importRepo := new(mockImportJobRepo)
	importRepo.On("GetByID", mock.Anything, jobID).Return(nil, errRepoFailure)

	svc := NewDocumentImportService(importRepo, new(mockEventPublisher))
	_, err := svc.GetImportJob(context.Background(), jobID)

	require.Error(t, err)
	assert.ErrorIs(t, err, errRepoFailure)
}
