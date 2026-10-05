// QuestionRevision tests — RED first. Asserts §2.2 invariants:
//   - Exactly ONE of MCQPayload / OEPayload is non-nil
//   - Payload type matches the parent question.Type
//   - revision_number is monotonic
//   - NewRevision calls payload.Validate() before constructing
package question_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func newMCQQ(t *testing.T) *question.Question {
	t.Helper()
	q, err := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeMCQ, Prompt: "p",
		SourceType: atom.SourceManual, MCQ: validMCQ(),
	})
	if err != nil {
		t.Fatalf("seed MCQ question: %v", err)
	}
	return q
}

func newOEQ(t *testing.T) *question.Question {
	t.Helper()
	q, err := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomA, AuthorGcid: authorA,
		Type: question.TypeOpenEnded, Prompt: "p",
		SourceType: atom.SourceManual, OE: validOE(),
	})
	if err != nil {
		t.Fatalf("seed OE question: %v", err)
	}
	return q
}

func TestNewRevision_OK_MCQ(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	rev, err := question.NewRevision(q, "Revised prompt", validMCQ(), nil, authorA, atom.SourceManual)
	if err != nil {
		t.Fatalf("NewRevision unexpected error: %v", err)
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("RevisionNumber = %d; want 1 (first rev)", rev.RevisionNumber)
	}
	if rev.QuestionID != q.QuestionID {
		t.Errorf("QuestionID = %q; want %q", rev.QuestionID, q.QuestionID)
	}
	if rev.MCQPayload == nil {
		t.Errorf("MCQPayload was nil; expected populated")
	}
	if rev.OEPayload != nil {
		t.Errorf("OEPayload was non-nil on MCQ revision")
	}
}

func TestNewRevision_OK_OE(t *testing.T) {
	t.Parallel()
	q := newOEQ(t)
	rev, err := question.NewRevision(q, "Revised prompt", nil, validOE(), authorA, atom.SourceManual)
	if err != nil {
		t.Fatalf("NewRevision unexpected error: %v", err)
	}
	if rev.OEPayload == nil {
		t.Errorf("OEPayload was nil; expected populated")
	}
	if rev.MCQPayload != nil {
		t.Errorf("MCQPayload was non-nil on OE revision")
	}
}

func TestNewRevision_PayloadTypeMatchesQuestion(t *testing.T) {
	t.Parallel()
	// MCQ question with OE payload — must reject.
	q := newMCQQ(t)
	_, err := question.NewRevision(q, "p", nil, validOE(), authorA, atom.SourceManual)
	if !errors.Is(err, question.ErrPayloadTypeMismatch) {
		t.Errorf("err = %v; want ErrPayloadTypeMismatch", err)
	}

	// OE question with MCQ payload — must reject.
	q2 := newOEQ(t)
	_, err = question.NewRevision(q2, "p", validMCQ(), nil, authorA, atom.SourceManual)
	if !errors.Is(err, question.ErrPayloadTypeMismatch) {
		t.Errorf("err = %v; want ErrPayloadTypeMismatch (oe + mcq payload)", err)
	}
}

func TestNewRevision_RejectsMixedPayloads(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	_, err := question.NewRevision(q, "p", validMCQ(), validOE(), authorA, atom.SourceManual)
	if err == nil {
		t.Errorf("expected error for both MCQ + OE payloads; got nil")
	}
}

func TestNewRevision_RejectsBothNilPayloads(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	_, err := question.NewRevision(q, "p", nil, nil, authorA, atom.SourceManual)
	if err == nil {
		t.Errorf("expected error for both payloads nil; got nil")
	}
}

func TestNewRevision_MonotonicRevisionNumber(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	rev1, _ := question.NewRevision(q, "p1", validMCQ(), nil, authorA, atom.SourceManual)
	q.LatestRevisionID = rev1.RevisionID

	// Simulate that the persistence layer would set the parent.Revision counter
	// — the domain helper must read the prior revision number from the parent.
	if rev1.RevisionNumber != 1 {
		t.Fatalf("rev1.RevisionNumber = %d; want 1", rev1.RevisionNumber)
	}

	// Now advance — the contract is that we pass q after the parent has been
	// updated by ApplyUpdate (which bumps a Revision counter).
	q2, rev2, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     ptr("p2"),
		MCQ:        validMCQ(),
		AuthorGcid: authorA,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	_ = q2
	if rev2.RevisionNumber != 2 {
		t.Errorf("rev2.RevisionNumber = %d; want 2 (monotonic)", rev2.RevisionNumber)
	}
}

func TestNewRevision_RejectsMissingAuthor(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	_, err := question.NewRevision(q, "p", validMCQ(), nil, "  ", atom.SourceManual)
	if err == nil {
		t.Errorf("expected error for missing authored_by; got nil")
	}
}

func TestNewRevision_RejectsInvalidSourceType(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	_, err := question.NewRevision(q, "p", validMCQ(), nil, authorA, atom.SourceType("garbage"))
	if err == nil {
		t.Errorf("expected error for invalid source_type; got nil")
	}
}

func TestNewRevision_RejectsNilParent(t *testing.T) {
	t.Parallel()
	_, err := question.NewRevision(nil, "p", validMCQ(), nil, authorA, atom.SourceManual)
	if err == nil {
		t.Errorf("expected error for nil parent question; got nil")
	}
}

func TestNewRevision_CallsValidate(t *testing.T) {
	t.Parallel()
	q := newMCQQ(t)
	bad := &question.MCQPayload{
		Options: []question.MCQOption{
			// No option marked correct → violates exactly-one-correct (explainer
			// is now optional per ADR-189, so it can't be the rejection trigger).
			{OptionID: "o1", Label: "A", IsCorrect: false, Explainer: "w"},
			{OptionID: "o2", Label: "B", IsCorrect: false, Explainer: "w"},
		},
	}
	_, err := question.NewRevision(q, "p", bad, nil, authorA, atom.SourceManual)
	if err == nil {
		t.Errorf("NewRevision did not call payload.Validate; expected error for bad payload; got nil")
	}
}

func ptr[T any](v T) *T { return &v }
