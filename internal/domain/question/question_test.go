// Question aggregate tests — RED first. These tests assert the §2.2 spec of
// docs/m14/cr-question-authoring-design-2026-05-15.md and the 16-value enum
// from §2.1 / migration 0005_question_type_enum.up.sql.
package question_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	atomA   = "01970000-0000-7000-8000-000000000010"
	authorA = "01970000-0000-7000-9000-000000000001"
)

func validMCQ() *question.MCQPayload {
	return &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt-1", Label: "A", IsCorrect: false, Explainer: "Wrong because reason."},
			{OptionID: "opt-2", Label: "B", IsCorrect: true, Explainer: "Right because reason."},
		},
		XPOnCorrect:  10,
		TimerSeconds: 60,
	}
}

func validOE() *question.OEPayload {
	return &question.OEPayload{
		ModelAnswer: "A model answer to the prompt.",
	}
}

// -----------------------------------------------------------------------------
// QuestionType — enum coverage
// -----------------------------------------------------------------------------

func TestQuestionType_Enabled_OnlyMCQAndOE(t *testing.T) {
	t.Parallel()
	if !question.TypeMCQ.Enabled() {
		t.Errorf("TypeMCQ should be Enabled()")
	}
	if !question.TypeOpenEnded.Enabled() {
		t.Errorf("TypeOpenEnded should be Enabled()")
	}
	reserved := []question.QuestionType{
		question.TypeReservedCodeExecution,
		question.TypeReservedCompletion,
		question.TypeReservedDragDrop,
		question.TypeReservedFillBlank,
		question.TypeReservedMatching,
		question.TypeReservedMultiSelect,
		question.TypeReservedMultimedia,
		question.TypeReservedOral,
		question.TypeReservedOrdering,
		question.TypeReservedPeerGraded,
		question.TypeReservedShortAnswer,
		question.TypeReservedSimulation,
		question.TypeReservedTableCompletion,
		question.TypeReservedTrueFalse,
	}
	for _, rt := range reserved {
		if rt.Enabled() {
			t.Errorf("%q should NOT be Enabled() — reserved", string(rt))
		}
	}
}

func TestQuestionType_Valid_AllSixteen(t *testing.T) {
	t.Parallel()
	all := []question.QuestionType{
		question.TypeMCQ,
		question.TypeOpenEnded,
		question.TypeReservedCodeExecution,
		question.TypeReservedCompletion,
		question.TypeReservedDragDrop,
		question.TypeReservedFillBlank,
		question.TypeReservedMatching,
		question.TypeReservedMultiSelect,
		question.TypeReservedMultimedia,
		question.TypeReservedOral,
		question.TypeReservedOrdering,
		question.TypeReservedPeerGraded,
		question.TypeReservedShortAnswer,
		question.TypeReservedSimulation,
		question.TypeReservedTableCompletion,
		question.TypeReservedTrueFalse,
	}
	for _, tp := range all {
		if !tp.Valid() {
			t.Errorf("%q should be Valid()", string(tp))
		}
	}
	if question.QuestionType("not_a_type").Valid() {
		t.Errorf("garbage type should not be Valid()")
	}
}

func TestQuestionType_WireFormatMatchesMigration(t *testing.T) {
	t.Parallel()
	// Wire-format strings must match the chora_creation.question_type ENUM
	// authored in migration 0005_question_type_enum.up.sql.
	cases := map[question.QuestionType]string{
		question.TypeMCQ:                   "mcq",
		question.TypeOpenEnded:             "oe",
		question.TypeReservedCodeExecution: "reserved_code_execution",
		question.TypeReservedTrueFalse:     "reserved_true_false",
	}
	for tp, want := range cases {
		if string(tp) != want {
			t.Errorf("wire format = %q; want %q", string(tp), want)
		}
	}
}

// -----------------------------------------------------------------------------
// New — constructor invariants
// -----------------------------------------------------------------------------

func TestNew_Valid_MCQ(t *testing.T) {
	t.Parallel()
	q, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "Which of these are right?",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if q.QuestionID == "" {
		t.Errorf("QuestionID was empty; expected UUIDv7")
	}
	if len(q.QuestionID) != 36 || q.QuestionID[14] != '7' {
		t.Errorf("QuestionID = %q; want UUIDv7", q.QuestionID)
	}
	if q.Type != question.TypeMCQ {
		t.Errorf("Type = %q; want mcq", string(q.Type))
	}
	if q.SourceType != atom.SourceManual {
		t.Errorf("SourceType = %q; want manual", string(q.SourceType))
	}
	if q.DeletedAt != nil {
		t.Errorf("DeletedAt = %v; want nil on creation", q.DeletedAt)
	}
}

func TestNew_Valid_OE(t *testing.T) {
	t.Parallel()
	q, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeOpenEnded,
		Prompt:     "Explain why story points beat hours.",
		SourceType: atom.SourceManual,
		OE:         validOE(),
	})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if q.Type != question.TypeOpenEnded {
		t.Errorf("Type = %q; want oe", string(q.Type))
	}
}

func TestNew_RejectsReservedType(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeReservedShortAnswer,
		Prompt:     "x",
		SourceType: atom.SourceManual,
	})
	if !errors.Is(err, question.ErrTypeNotEnabled) {
		t.Errorf("err = %v; want ErrTypeNotEnabled", err)
	}
}

func TestNew_RejectsInvalidType(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.QuestionType("not_real"),
		Prompt:     "x",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if !errors.Is(err, question.ErrInvalidType) {
		t.Errorf("err = %v; want ErrInvalidType", err)
	}
}

func TestNew_RejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "   ", // whitespace only
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for empty prompt; got nil")
	}
}

func TestNew_RejectsOversizedPrompt(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("a", 4097)
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     tooLong,
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for prompt > 4096 chars; got nil")
	}
}

func TestNew_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   "",
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for missing tenant_id; got nil")
	}
}

func TestNew_RejectsMissingAtomID(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     "",
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for missing atom_id; got nil")
	}
}

func TestNew_RejectsMissingAuthor(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: "",
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for missing author gcid; got nil")
	}
}

func TestNew_RejectsInvalidSourceType(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceType("not_real"),
		MCQ:        validMCQ(),
	})
	if err == nil {
		t.Errorf("expected error for invalid source_type; got nil")
	}
}

func TestNew_RejectsMissingPayload(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceManual,
		// no MCQ payload — should reject (type=mcq requires MCQ)
	})
	if err == nil {
		t.Errorf("expected error for missing MCQ payload on type=mcq; got nil")
	}
}

func TestNew_RejectsMixedPayload(t *testing.T) {
	t.Parallel()
	_, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		AuthorGcid: authorA,
		Type:       question.TypeMCQ,
		Prompt:     "p",
		SourceType: atom.SourceManual,
		MCQ:        validMCQ(),
		OE:         validOE(),
	})
	if err == nil {
		t.Errorf("expected error for mixed MCQ+OE payload; got nil")
	}
}

// -----------------------------------------------------------------------------
// SoftDelete
// -----------------------------------------------------------------------------

func TestIsActive_TrueWhenNotDeleted(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	if !q.IsActive() {
		t.Errorf("IsActive() = false; want true for fresh question")
	}
	q.SoftDelete()
	if q.IsActive() {
		t.Errorf("IsActive() = true; want false after SoftDelete")
	}
}

func TestSoftDelete_IdempotentAndSetsDeletedAt(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	q.SoftDelete()
	if q.DeletedAt == nil {
		t.Fatalf("DeletedAt was nil after SoftDelete; want non-nil")
	}
	firstDel := *q.DeletedAt
	time.Sleep(1 * time.Millisecond)
	// Idempotent: second call should not change DeletedAt.
	q.SoftDelete()
	if q.DeletedAt == nil || !q.DeletedAt.Equal(firstDel) {
		t.Errorf("SoftDelete not idempotent — DeletedAt changed: was %v now %v", firstDel, q.DeletedAt)
	}
}

// -----------------------------------------------------------------------------
// ApplyUpdate — PATCH semantics; returns a NEW revision; does NOT mutate
// -----------------------------------------------------------------------------

func TestApplyUpdate_AppendsRevision_DoesNotMutate(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "Original prompt",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})

	// Build a first revision (revision 1) so ApplyUpdate has something to monotonically follow.
	rev1, err := question.NewRevision(q, q.Prompt, validMCQ(), nil, authorA, atom.SourceManual)
	if err != nil {
		t.Fatalf("seed rev1: %v", err)
	}
	q.LatestRevisionID = rev1.RevisionID

	// Update prompt + payload. ApplyUpdate must:
	//   (a) return a new revision with revision_number 2,
	//   (b) NOT mutate the original q.Prompt in place — the contract is that the
	//       returned *Question is the updated value and the caller persists both.
	updatedMCQ := validMCQ()
	updatedMCQ.Options[0].Explainer = "Updated explainer."

	newPrompt := "Updated prompt"
	upd, newRev, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &newPrompt,
		MCQ:        updatedMCQ,
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if newRev.RevisionNumber != 2 {
		t.Errorf("newRev.RevisionNumber = %d; want 2", newRev.RevisionNumber)
	}
	if upd.Prompt != newPrompt {
		t.Errorf("upd.Prompt = %q; want %q", upd.Prompt, newPrompt)
	}
	// Append-only: original q should be untouched.
	if q.Prompt != "Original prompt" {
		t.Errorf("original q.Prompt mutated to %q; want untouched 'Original prompt'", q.Prompt)
	}
	if upd == q {
		t.Errorf("ApplyUpdate returned same pointer; expected new struct (append-only)")
	}
}

func TestApplyUpdate_RejectsTypeChange(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	// Type immutable: passing OE payload on an MCQ question must reject.
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		OE:         validOE(),
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if !errors.Is(err, question.ErrPayloadTypeMismatch) {
		t.Errorf("err = %v; want ErrPayloadTypeMismatch", err)
	}
}

func TestApplyUpdate_RejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "Original",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	rev1, _ := question.NewRevision(q, q.Prompt, validMCQ(), nil, authorA, atom.SourceManual)
	q.LatestRevisionID = rev1.RevisionID

	empty := "   "
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &empty,
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for empty prompt; got nil")
	}
}

func TestApplyUpdate_RejectsOversizedPrompt(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "Original",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	rev1, _ := question.NewRevision(q, q.Prompt, validMCQ(), nil, authorA, atom.SourceManual)
	q.LatestRevisionID = rev1.RevisionID

	tooLong := strings.Repeat("z", 4097)
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &tooLong,
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for prompt > 4096; got nil")
	}
}

func TestApplyUpdate_RejectsInvalidSourceType(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	rev1, _ := question.NewRevision(q, q.Prompt, validMCQ(), nil, authorA, atom.SourceManual)
	q.LatestRevisionID = rev1.RevisionID

	np := "x"
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &np,
		AuthorGcid: authorA,
		SourceType: atom.SourceType("garbage"),
	})
	if err == nil {
		t.Errorf("expected error for invalid source_type; got nil")
	}
}

func TestApplyUpdate_RejectsMissingAuthor(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	rev1, _ := question.NewRevision(q, q.Prompt, validMCQ(), nil, authorA, atom.SourceManual)
	q.LatestRevisionID = rev1.RevisionID

	np := "x"
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &np,
		AuthorGcid: "",
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for missing author_gcid; got nil")
	}
}

func TestApplyUpdate_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	q.SoftDelete()

	newP := "x"
	_, _, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     &newP,
		MCQ:        validMCQ(),
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error on update of soft-deleted question; got nil")
	}
}
