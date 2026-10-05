// question_lookup_adapter_test.go — unit coverage of the pg.QuestionLookupAdapter
// (W3.B.1). Uses a fake question-by-id resolver so the ErrNotFound→false mapping
// + error propagation are exercised without a live DB.
package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

type fakeQuestionResolver struct {
	exists map[string]bool
	err    error
	tenant string // records the tenantID it was asked
	atomID string // returned on the found Question (W3.B.2 Details)
	qType  question.QuestionType
	prompt string // returned on the found Question (Details — row label)
}

func (f *fakeQuestionResolver) GetByID(_ context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	f.tenant = tenantID
	if f.err != nil {
		return nil, nil, f.err
	}
	if f.exists[questionID] {
		return &question.Question{QuestionID: questionID, TenantID: tenantID, AtomID: f.atomID, Type: f.qType, Prompt: f.prompt}, nil, nil
	}
	return nil, nil, question.ErrNotFound
}

func TestQuestionLookupAdapter_Resolve_Found(t *testing.T) {
	t.Parallel()

	const qid = "01970000-0000-7000-b000-000000000001"
	f := &fakeQuestionResolver{exists: map[string]bool{qid: true}}
	a := NewQuestionLookupAdapter(f)

	exists, err := a.Resolve(context.Background(), testTenantID, qid)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !exists {
		t.Errorf("exists = false; want true")
	}
	if f.tenant != testTenantID {
		t.Errorf("resolver tenant = %q; want %q (tenant-scoped)", f.tenant, testTenantID)
	}
}

func TestQuestionLookupAdapter_Resolve_NotFound(t *testing.T) {
	t.Parallel()

	f := &fakeQuestionResolver{exists: map[string]bool{}}
	a := NewQuestionLookupAdapter(f)

	exists, err := a.Resolve(context.Background(), testTenantID, "01970000-0000-7000-b000-0000000000ff")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if exists {
		t.Errorf("exists = true; want false for missing question")
	}
}

func TestQuestionLookupAdapter_Resolve_InfraError(t *testing.T) {
	t.Parallel()

	f := &fakeQuestionResolver{err: errors.New("db down")}
	a := NewQuestionLookupAdapter(f)

	_, err := a.Resolve(context.Background(), testTenantID, "01970000-0000-7000-b000-000000000001")
	if err == nil {
		t.Fatalf("expected infra error to propagate")
	}
}

func TestQuestionLookupAdapter_Resolve_NilRepo(t *testing.T) {
	t.Parallel()

	a := NewQuestionLookupAdapter(nil)
	if _, err := a.Resolve(context.Background(), testTenantID, "x"); err == nil {
		t.Fatalf("expected fail-loud error when repo not wired")
	}
}

func TestQuestionLookupAdapter_Details_ReturnsAtomAndType(t *testing.T) {
	t.Parallel()

	const qid = "01970000-0000-7000-b000-000000000001"
	f := &fakeQuestionResolver{
		exists: map[string]bool{qid: true},
		atomID: "01970000-0000-7000-a000-000000000001",
		qType:  question.TypeOpenEnded,
		prompt: "Explain photosynthesis.",
	}
	a := NewQuestionLookupAdapter(f)

	atomID, qType, prompt, err := a.Details(context.Background(), testTenantID, qid)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if atomID != "01970000-0000-7000-a000-000000000001" {
		t.Errorf("atomID = %q; want resolved", atomID)
	}
	if qType != string(question.TypeOpenEnded) {
		t.Errorf("qType = %q; want %q", qType, string(question.TypeOpenEnded))
	}
	if prompt != "Explain photosynthesis." {
		t.Errorf("prompt = %q; want the question prompt", prompt)
	}
	if f.tenant != testTenantID {
		t.Errorf("resolver tenant = %q; want %q (tenant-scoped)", f.tenant, testTenantID)
	}
}

func TestQuestionLookupAdapter_Details_NotFoundFailsLoud(t *testing.T) {
	t.Parallel()

	f := &fakeQuestionResolver{exists: map[string]bool{}}
	a := NewQuestionLookupAdapter(f)

	if _, _, _, err := a.Details(context.Background(), testTenantID, "01970000-0000-7000-b000-0000000000ff"); err == nil {
		t.Fatalf("expected fail-loud error for a missing question (a bank item must resolve)")
	}
}

func TestQuestionLookupAdapter_Details_NilRepo(t *testing.T) {
	t.Parallel()

	a := NewQuestionLookupAdapter(nil)
	if _, _, _, err := a.Details(context.Background(), testTenantID, "x"); err == nil {
		t.Fatalf("expected fail-loud error when repo not wired")
	}
}
