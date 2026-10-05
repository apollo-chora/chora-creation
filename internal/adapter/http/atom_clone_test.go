// atom_clone_test.go — RED-first end-to-end coverage of
// POST /api/atoms/{atom_id}/clone (ADR-199 clone-as-variant, Wave 2).
// Seeds a source atom + its MCQ question in the in-memory repos, then asserts
// the clone is a NEW draft atom owned by the caller with provenance + a cloned
// question.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	cloneSrcID     = "01970000-0000-7000-8000-0000000000c1"
	cloneSrcAuthor = "01970000-0000-7000-9000-0000000000c9" // NOT the caller (gcidA)
)

// newCloneServer seeds a source atom (+ optional MCQ question) into fresh
// in-memory repos and returns the wired router.
func newCloneServer(t *testing.T, seedQuestion bool, srcTenant string) (http.Handler, *fakeQuestionRepository) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	src := &atom.LearningAtom{
		AtomID: cloneSrcID, TenantID: srcTenant, Gcid: cloneSrcAuthor,
		Title: "Original Q", Body: "body", Tags: []string{"algebra"},
		Mode: atom.ModeStraightUp, Status: atom.StatusPublished,
		QuestionType: "mcq", Difficulty: 3, Stem: "What is 2+2?",
		Subject: "math", CognitiveLevel: atom.CognitiveLevel("application"),
	}
	if err := atomRepo.Save(context.Background(), src); err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	qRepo := newFakeQRepo()
	if seedQuestion {
		q, err := question.New(question.NewParams{
			TenantID: srcTenant, AtomID: cloneSrcID, AuthorGcid: cloneSrcAuthor,
			Type: question.TypeMCQ, Prompt: "What is 2+2?", SourceType: atom.SourceManual,
			MCQ: &question.MCQPayload{Options: []question.MCQOption{
				{OptionID: "o1", Label: "4", IsCorrect: true},
				{OptionID: "o2", Label: "5"},
			}},
		})
		if err != nil {
			t.Fatalf("seed question.New: %v", err)
		}
		rev, err := question.NewRevision(q, q.Prompt, q.MCQ, nil, cloneSrcAuthor, atom.SourceManual)
		if err != nil {
			t.Fatalf("seed NewRevision: %v", err)
		}
		q.LatestRevisionID = rev.RevisionID
		if err := qRepo.Save(context.Background(), q, rev); err != nil {
			t.Fatalf("seed question save: %v", err)
		}
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})
	return srv, qRepo
}

func TestCloneAtom_201_DraftVariantOwnedByCaller_WithClonedQuestion(t *testing.T) {
	t.Parallel()
	srv, qRepo := newCloneServer(t, true, tenantA)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+cloneSrcID+"/clone", map[string]any{}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	newID, _ := got["atom_id"].(string)
	if newID == "" || newID == cloneSrcID {
		t.Fatalf("clone atom_id = %q; want fresh id", newID)
	}
	if got["status"] != "draft" {
		t.Errorf("status = %v; want draft", got["status"])
	}
	if got["gcid"] != gcidA {
		t.Errorf("gcid = %v; want caller %s", got["gcid"], gcidA)
	}
	if got["title"] != "Copy of Original Q" {
		t.Errorf("title = %v; want 'Copy of Original Q'", got["title"])
	}
	if got["cloned_from_atom_id"] != cloneSrcID {
		t.Errorf("cloned_from_atom_id = %v; want source %s", got["cloned_from_atom_id"], cloneSrcID)
	}
	// The question was cloned onto the new atom.
	clonedQ, _, qerr := qRepo.GetByAtomID(context.Background(), tenantA, newID)
	if qerr != nil || clonedQ == nil {
		t.Fatalf("expected a cloned question on the new atom; got err=%v", qerr)
	}
	if string(clonedQ.Type) != "mcq" {
		t.Errorf("cloned question type = %q; want mcq", clonedQ.Type)
	}
}

func TestCloneAtom_TitleOverride(t *testing.T) {
	t.Parallel()
	srv, _ := newCloneServer(t, true, tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+cloneSrcID+"/clone",
		map[string]any{"title": "Variant A"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["title"] != "Variant A" {
		t.Errorf("title = %v; want override 'Variant A'", got["title"])
	}
}

func TestCloneAtom_AtomOnly_NoQuestion_201(t *testing.T) {
	t.Parallel()
	// Source atom with NO question row (seed-atom case) still clones the atom.
	srv, _ := newCloneServer(t, false, tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+cloneSrcID+"/clone", map[string]any{}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201 (atom-only clone)", w.Code, w.Body.String())
	}
}

func TestCloneAtom_EmptyStemSource_DerivesStemFromQuestionPrompt_201(t *testing.T) {
	t.Parallel()
	// REGRESSION (CHO-1917 follow-up): an atom may carry an EMPTY top-level Stem
	// while its prompt lives on the question — the live trigger is OE atoms
	// (their prompt is in oe_payload, not atom.Stem). The clone must still
	// satisfy ValidatePhase1 (stem required) by deriving the canonical stem from
	// the source question's prompt, NOT 400 "stem is required". (Type-agnostic;
	// reproduced here with an MCQ question for payload simplicity.)
	atomRepo := inmem.NewAtomRepository()
	src := &atom.LearningAtom{
		AtomID: cloneSrcID, TenantID: tenantA, Gcid: cloneSrcAuthor,
		Title: "Stemless Original", Body: "body",
		Mode: atom.ModeStraightUp, Status: atom.StatusPublished,
		QuestionType: "mcq", Difficulty: 2, Stem: "", // empty atom-level stem
	}
	if err := atomRepo.Save(context.Background(), src); err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	qRepo := newFakeQRepo()
	q, err := question.New(question.NewParams{
		TenantID: tenantA, AtomID: cloneSrcID, AuthorGcid: cloneSrcAuthor,
		Type: question.TypeMCQ, Prompt: "Derived prompt?", SourceType: atom.SourceManual,
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "o1", Label: "yes", IsCorrect: true},
			{OptionID: "o2", Label: "no"},
		}},
	})
	if err != nil {
		t.Fatalf("seed question.New: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, q.MCQ, nil, cloneSrcAuthor, atom.SourceManual)
	if err != nil {
		t.Fatalf("seed NewRevision: %v", err)
	}
	q.LatestRevisionID = rev.RevisionID
	if err := qRepo.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("seed question save: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+cloneSrcID+"/clone", map[string]any{}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201 (stem derived from question prompt)", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got["stem"] != "Derived prompt?" {
		t.Errorf("clone stem = %v; want derived from question prompt 'Derived prompt?'", got["stem"])
	}
}

func TestCloneAtom_SourceNotFound_404(t *testing.T) {
	t.Parallel()
	srv, _ := newCloneServer(t, true, tenantA)
	missing := "01970000-0000-7000-8000-0000000000ff"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+missing+"/clone", map[string]any{}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d; want 404 for missing source", w.Code)
	}
}

func TestCloneAtom_CrossTenantSource_404(t *testing.T) {
	t.Parallel()
	// Source seeded under a DIFFERENT tenant → caller (tenantA) cannot see it.
	otherTenant := "01970000-0000-7000-8000-0000000000z9"
	srv, _ := newCloneServer(t, true, otherTenant)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+cloneSrcID+"/clone", map[string]any{}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d; want 404 for cross-tenant source", w.Code)
	}
}

func TestCloneAtom_OnlyPOST_405(t *testing.T) {
	t.Parallel()
	srv, _ := newCloneServer(t, true, tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/"+cloneSrcID+"/clone", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d; want 405 on GET /clone", w.Code)
	}
}
