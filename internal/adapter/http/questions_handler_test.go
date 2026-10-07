// questions_handler_test.go — HTTP-layer tests for the question authoring
// endpoints mounted under /api/atoms/{atom_id}/questions[...]. Mirrors the
// handler_test.go fixture (in-memory atom repo + a fake QuestionRepository).
//
// Per P3-B in /Users/daleleung/.claude/plans/golden-hopping-owl.md:
//   - POST   /api/atoms/{atom_id}/questions             — manual create (201)
//   - PATCH  /api/atoms/{atom_id}/questions/{q_id}      — edit -> AppendRevision (200)
//   - GET    /api/atoms/{atom_id}/questions/{q_id}      — admin AUTHOR projection (200)
//
// Error envelope codes match the gateway convention:
//
//	CREATION_QUESTION_INVALID_TYPE, CREATION_QUESTION_PAYLOAD_INVALID,
//	CREATION_QUESTION_DUPLICATE, CREATION_QUESTION_NOT_FOUND.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// fakeQuestionRepository — in-memory port stub for the test. One question
// per atom (D2); soft delete only; revisions held in-memory list.
type fakeQuestionRepository struct {
	mu        sync.Mutex
	questions map[string]*question.Question           // questionID -> q
	revisions map[string][]*question.QuestionRevision // questionID -> rev list

	saveErr   error
	getErr    error
	appendErr error
	softErr   error
}

func newFakeQRepo() *fakeQuestionRepository {
	return &fakeQuestionRepository{
		questions: map[string]*question.Question{},
		revisions: map[string][]*question.QuestionRevision{},
	}
}

func (r *fakeQuestionRepository) Save(_ context.Context, q *question.Question, rev *question.QuestionRevision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	// Enforce D2 — 1 atom = 1 non-deleted question.
	for _, existing := range r.questions {
		if existing.AtomID == q.AtomID && existing.DeletedAt == nil {
			return question.ErrAtomHasQuestion
		}
	}
	q.LatestRevisionID = rev.RevisionID
	r.questions[q.QuestionID] = q
	r.revisions[q.QuestionID] = []*question.QuestionRevision{rev}
	return nil
}

func (r *fakeQuestionRepository) GetByAtomID(_ context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, nil, r.getErr
	}
	for _, q := range r.questions {
		if q.AtomID == atomID && q.TenantID == tenantID && q.DeletedAt == nil {
			revs := r.revisions[q.QuestionID]
			if len(revs) == 0 {
				return nil, nil, question.ErrNotFound
			}
			return q, revs[len(revs)-1], nil
		}
	}
	return nil, nil, question.ErrNotFound
}

func (r *fakeQuestionRepository) GetByID(_ context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, nil, r.getErr
	}
	q, ok := r.questions[questionID]
	if !ok || q.TenantID != tenantID || q.DeletedAt != nil {
		return nil, nil, question.ErrNotFound
	}
	revs := r.revisions[q.QuestionID]
	if len(revs) == 0 {
		return nil, nil, question.ErrNotFound
	}
	return q, revs[len(revs)-1], nil
}

func (r *fakeQuestionRepository) AppendRevision(_ context.Context, rev *question.QuestionRevision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendErr != nil {
		return r.appendErr
	}
	prev := r.revisions[rev.QuestionID]
	rev.RevisionNumber = len(prev) + 1
	r.revisions[rev.QuestionID] = append(prev, rev)
	if q, ok := r.questions[rev.QuestionID]; ok {
		q.LatestRevisionID = rev.RevisionID
		q.Revision = rev.RevisionNumber
		q.UpdatedAt = time.Now().UTC()
		// Reflect the latest payload onto the parent for round-trip GET.
		if rev.MCQPayload != nil {
			q.MCQ = rev.MCQPayload
		}
		if rev.OEPayload != nil {
			q.OE = rev.OEPayload
		}
		if rev.Prompt != "" {
			q.Prompt = rev.Prompt
		}
	}
	return nil
}

func (r *fakeQuestionRepository) SoftDelete(_ context.Context, tenantID, questionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.softErr != nil {
		return r.softErr
	}
	q, ok := r.questions[questionID]
	if !ok || q.TenantID != tenantID {
		return question.ErrNotFound
	}
	now := time.Now().UTC()
	q.DeletedAt = &now
	q.UpdatedAt = now
	return nil
}

// SearchQuestions — default fake impl returns an empty page. Test-specific
// behaviour overrides via searchableQRepo in question_search_test.go.
func (r *fakeQuestionRepository) SearchQuestions(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
	return []question.SearchResult{}, 0, nil
}

// -----------------------------------------------------------------------------
// Test server helpers
// -----------------------------------------------------------------------------

func newQuestionServer(t *testing.T) (http.Handler, *fakeQuestionRepository, *inmem.AtomRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()

	// Seed an MCQ atom we can hang questions off.
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA,
		Gcid:     gcidA,
		Title:    "Phyllis MCQ atom",
		Body:     "Body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save seed atom: %v", err)
	}

	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})
	return router, qRepo, atomRepo, a.AtomID
}

// authedJSON builds an AUTHOR request — the identity every authoring test here
// intends. CHO-2254: the roles ride the canonical mesh header the gateway
// already populates (servicemesh.HeaderUserRoles); before that story this
// handler never read them, so the header was absent and every route was open.
func authedJSON(method, path string, body any) *http.Request {
	r := learnerJSON(method, path, body)
	r.Header.Set(servicemesh.HeaderUserRoles, "author,learner")
	return r
}

// learnerJSON builds the same request as a NON-author: a plain learner, exactly
// what a real learner-only JWT resolves to on the mesh. CHO-2254 — the answer
// key must be unreachable through this identity.
func learnerJSON(method, path string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(servicemesh.HeaderUserRoles, "learner")
	return r
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/questions — manual create
// -----------------------------------------------------------------------------

func TestCreateQuestion_MCQ_Returns201WithQuestion(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which is a Scrum role?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "Product Owner", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "Project Manager", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	q, _ := got["question"].(map[string]any)
	if q == nil {
		t.Fatalf("response missing `question` block: %s", w.Body.String())
	}
	if q["type"] != "mcq" {
		t.Errorf("type = %v; want mcq", q["type"])
	}
	if q["atom_id"] != atomID {
		t.Errorf("atom_id mismatch: got %v want %s", q["atom_id"], atomID)
	}
}

func TestCreateQuestion_OE_Returns201WithQuestion(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "OE atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})

	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain story points.",
		"oe_payload": map[string]any{
			"model_answer": "Story points capture complexity + risk + effort.",
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	q, _ := got["question"].(map[string]any)
	if q["type"] != "oe" {
		t.Errorf("type = %v; want oe", q["type"])
	}
}

// E2E-BE-OE-RUBRIC (P1 smoke #2 2026-05-17) — chora-contracts
// creation-questions.yaml §OEPayload.rubric is `array` of RubricCriterion.
// Go decoder must match. Pre-fix the wire shape `rubric:[]` 400s with
// `cannot unmarshal array into Go struct field createOEPayload.oe_payload.rubric
// of type httpadapter.createRubric`.
func TestCreateQuestion_OE_RubricAsArray_PerOpenAPISpec(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "OE atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})

	// Spec-conformant array body — `rubric` is a flat array of RubricCriterion,
	// each with `title` + `weight` (0..1 float). NOT wrapped in a `criteria`
	// object.
	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain photosynthesis.",
		"oe_payload": map[string]any{
			"model_answer": "Chlorophyll converts light to chemical energy.",
			"rubric": []map[string]any{
				{"criterion_id": "c-mechanism", "title": "names mechanism", "weight": 0.5},
				{"criterion_id": "c-outcome", "title": "describes outcome", "weight": 0.5},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201 (spec-conformant array rubric)", w.Code, w.Body.String())
	}
	// Sanity — rubric persisted, both criteria survive the boundary mapping.
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	q, _ := got["question"].(map[string]any)
	oe, _ := q["oe"].(map[string]any)
	if oe == nil {
		t.Fatalf("expected oe payload on response; got %v", q)
	}
	rb, _ := oe["rubric"].(map[string]any)
	if rb == nil {
		t.Fatalf("expected rubric on oe payload; got %v (domain wraps array in {criteria:[]})", oe)
	}
	crits, _ := rb["criteria"].([]any)
	if len(crits) != 2 {
		t.Errorf("rubric.criteria: want 2 criteria, got %d (full=%v)", len(crits), rb)
	}
}

// Empty rubric MUST also be accepted — FE workaround at c52d9e79 strips
// empty arrays today; after the fix the strip becomes a no-op (`rubric:[]`
// just means no criteria yet).
func TestCreateQuestion_OE_EmptyRubricArray_StillAccepted(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "OE atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})

	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain x.",
		"oe_payload": map[string]any{
			"model_answer": "Some answer.",
			"rubric":       []map[string]any{}, // empty array — no criteria yet
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d body=%s; want 201 (empty rubric array is valid per OpenAPI)", w.Code, w.Body.String())
	}
}

func TestCreateQuestion_RejectsTypeMismatchWithAtom(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	// atom is MCQ but request is OE
	body := map[string]any{
		"type":   "oe",
		"prompt": "stem",
		"oe_payload": map[string]any{
			"model_answer": "answer",
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 400 or 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "CREATION_QUESTION_INVALID_TYPE") {
		t.Errorf("expected CREATION_QUESTION_INVALID_TYPE in body; got %s", w.Body.String())
	}
}

func TestCreateQuestion_RejectsMissingPayload(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "stem",
		// mcq_payload omitted
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 400 or 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "CREATION_QUESTION_PAYLOAD_INVALID") {
		t.Errorf("expected CREATION_QUESTION_PAYLOAD_INVALID; got %s", w.Body.String())
	}
}

func TestCreateQuestion_404OnMissingParentAtom(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newQuestionServer(t)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "stem",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
				{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/01970000-0000-7000-8000-deadbeefcafe/questions", body))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

func TestCreateQuestion_409OnDuplicateAtomQuestion(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "stem",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
				{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
			},
		},
	}
	// First call succeeds.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	// Second call fails with 409 due to D2 enforcement.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second create: status = %d; want 409", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "CREATION_QUESTION_DUPLICATE") {
		t.Errorf("expected CREATION_QUESTION_DUPLICATE; got %s", w2.Body.String())
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/atoms/{atom_id}/questions/{question_id} — append revision
// -----------------------------------------------------------------------------

func TestPatchQuestion_AppendsNewRevision(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	// Create
	createBody := map[string]any{
		"type":   "mcq",
		"prompt": "v1",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
				{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	q := got["question"].(map[string]any)
	qID := q["question_id"].(string)

	// Patch — bump prompt.
	patchBody := map[string]any{
		"prompt": "v2",
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, patchBody))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s; want 200", w2.Code, w2.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &patched)
	pq := patched["question"].(map[string]any)
	if pq["prompt"] != "v2" {
		t.Errorf("prompt = %v; want v2", pq["prompt"])
	}
	revBlk, _ := patched["revision"].(map[string]any)
	if revBlk == nil {
		t.Errorf("response missing revision block: %s", w2.Body.String())
	}
}

// CHO-1974 — editing a saved question's TEXT (the PATCH carries mcq_payload/
// options but OMITS image_url/answer_image_url, exactly as the FE editor does)
// must CARRY FORWARD the prior revision's image refs, not silently drop them.
// RED until buildPatchPayloadFromRequest inherits the previous payload's images.
func TestPatchQuestion_OmittedImagesCarriedForward_MCQ(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	stemGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png"
	ansGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/ans.png"
	createBody := map[string]any{
		"type":   "mcq",
		"prompt": "v1",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
			{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
		}},
		"image_url":        stemGS,
		"answer_image_url": ansGS,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)

	// FE-style edit: prompt + options only; NO image fields in the PATCH body.
	patchBody := map[string]any{
		"prompt": "v2 — fixed a typo",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
			{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
		}},
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, patchBody))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w2.Code, w2.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &patched)
	rev, _ := patched["revision"].(map[string]any)
	if rev == nil {
		t.Fatalf("response missing revision block: %s", w2.Body.String())
	}
	mcqP, _ := rev["mcq_payload"].(map[string]any)
	if mcqP == nil {
		t.Fatalf("revision missing mcq_payload: %s", w2.Body.String())
	}
	if got := mcqP["image_url"]; got != stemGS {
		t.Errorf("question image dropped on save-revision: image_url=%v; want %s", got, stemGS)
	}
	if got := mcqP["answer_image_url"]; got != ansGS {
		t.Errorf("model-answer image dropped on save-revision: answer_image_url=%v; want %s", got, ansGS)
	}
}

// CHO-1974 — OE parity: an OE atom's stem/model-answer images must also survive
// a text-only save-revision.
func TestPatchQuestion_OmittedImagesCarriedForward_OE(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{TenantID: tenantA, Gcid: gcidA, Title: "OE img atom", Body: "b", Mode: atom.ModeStraightUp})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo, QuestionRepository: qRepo})
	atomID := a.AtomID

	stemGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/oe/stem.png"
	ansGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/oe/ans.png"
	createBody := map[string]any{
		"type":             "oe",
		"prompt":           "Explain photosynthesis.",
		"oe_payload":       map[string]any{"model_answer": "Light + CO2 + water -> glucose + O2."},
		"image_url":        stemGS,
		"answer_image_url": ansGS,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)

	patchBody := map[string]any{
		"prompt":     "Explain photosynthesis in plants.",
		"oe_payload": map[string]any{"model_answer": "Light + CO2 + water -> glucose + O2."},
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, patchBody))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w2.Code, w2.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &patched)
	rev, _ := patched["revision"].(map[string]any)
	if rev == nil {
		t.Fatalf("response missing revision block: %s", w2.Body.String())
	}
	oeP, _ := rev["oe_payload"].(map[string]any)
	if oeP == nil {
		t.Fatalf("revision missing oe_payload: %s", w2.Body.String())
	}
	if got := oeP["image_url"]; got != stemGS {
		t.Errorf("OE stem image dropped on save-revision: image_url=%v; want %s", got, stemGS)
	}
	if got := oeP["answer_image_url"]; got != ansGS {
		t.Errorf("OE model-answer image dropped on save-revision: answer_image_url=%v; want %s", got, ansGS)
	}
}

// CHO-1974 — an EXPLICIT empty image_url ("") is the Remove affordance and must
// CLEAR the image, NOT carry the prior one forward (guards the nil-vs-"" split:
// omitted ⇒ keep, "" ⇒ clear).
func TestPatchQuestion_EmptyImageStringClears_MCQ(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	stemGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png"
	createBody := map[string]any{
		"type":   "mcq",
		"prompt": "v1",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
			{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
		}},
		"image_url": stemGS,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)

	patchBody := map[string]any{
		"prompt": "v2",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "x", "label": "A", "is_correct": true, "explainer": "right"},
			{"option_id": "y", "label": "B", "is_correct": false, "explainer": "wrong"},
		}},
		"image_url": "", // Remove affordance
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, patchBody))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w2.Code, w2.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &patched)
	rev, _ := patched["revision"].(map[string]any)
	if rev == nil {
		t.Fatalf("response missing revision block: %s", w2.Body.String())
	}
	if mcqP, _ := rev["mcq_payload"].(map[string]any); mcqP != nil {
		if got, ok := mcqP["image_url"]; ok && got == stemGS {
			t.Errorf("explicit clear carried the old image forward: image_url=%v", got)
		}
	}
}

// -----------------------------------------------------------------------------
// GET /api/atoms/{atom_id}/questions/{question_id} — author projection
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// CHO-2254 (SECURITY) — the author question doors are author-only.
//
// getQuestion is the "AUTHOR projection (full)": it returns is_correct +
// explainer. Before this story chora-creation read roles NOWHERE (its only role
// use was SET LOCAL chora.user_roles for RLS, and the live `questions` table
// carries only tenant_isolation), the gateway proxies the path verbatim, and the
// learner is handed question_id in their own atom payload — so any learner in
// the tenant could read the answer key for every MCQ, bypassing
// LearnerProjection, the learnerSafeMCQOptions allow-list and the BFF strip.
// -----------------------------------------------------------------------------

// seedQuestion creates an MCQ as an author and returns its id.
func seedQuestion(t *testing.T, srv http.Handler, atomID string) string {
	t.Helper()
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "x", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "y", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	return got["question"].(map[string]any)["question_id"].(string)
}

func TestGetQuestion_NonAuthor_Forbidden_AndNoAnswerKey(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, learnerJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("a learner reading the AUTHOR projection must be 403; got %d", w.Code)
	}
	// Belt-and-braces: not one byte of the key may ride the refusal.
	if b := w.Body.String(); strings.Contains(b, "is_correct") || strings.Contains(b, "explainer") ||
		strings.Contains(b, "PO owns backlog priority") {
		t.Fatalf("the refusal leaked the answer key: %s", b)
	}
}

// The writes on this same dispatch are author-only too — an ungated PATCH would
// let a learner rewrite the answer key itself (the CHO-2233 class, in creation).
func TestQuestionWrites_NonAuthor_Forbidden(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	// create targets a DIFFERENT, question-less atom on purpose: reusing atomID
	// would 409 on the 1-atom-1-question rule (D2) and the test would pass on an
	// invariant that has nothing to do with authz.
	_, _, _, freshAtomID := newQuestionServer(t)

	cases := map[string]*http.Request{
		"create": learnerJSON(http.MethodPost, "/api/atoms/"+freshAtomID+"/questions", map[string]any{
			"type": "mcq", "prompt": "learner-authored?",
			"mcq_payload": map[string]any{"options": []map[string]any{
				{"option_id": "a", "label": "A", "is_correct": true},
				{"option_id": "b", "label": "B", "is_correct": false}}},
		}),
		"patch": learnerJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, map[string]any{
			"prompt": "rewritten by a learner",
		}),
		"delete": learnerJSON(http.MethodDelete, "/api/atoms/"+atomID+"/questions/"+qID, nil),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s by a learner must be 403; got %d %s", name, w.Code, w.Body.String())
			}
		})
	}
}

// Fail-CLOSED: a request carrying NO roles header at all (a caller that never
// marshalled mesh claims) must be refused, never admitted by omission.
func TestGetQuestion_NoRolesHeader_Forbidden(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	r := learnerJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil)
	r.Header.Del(servicemesh.HeaderUserRoles)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("absent roles header must fail CLOSED (403); got %d", w.Code)
	}
}

// The instructor arm of the canonical predicate: chora-creation must mirror the
// BFF's HasAuthorRole (author|instructor|admin|owner|tenant_admin), which already
// gates correct_option_id on question_payload. Diverging would make the two
// answer-key doors disagree.
func TestGetQuestion_InstructorAllowed_MirrorsBFFPredicate(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	r := learnerJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil)
	r.Header.Set(servicemesh.HeaderUserRoles, "instructor,learner")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("instructor is an author-role per the BFF predicate; got %d %s", w.Code, w.Body.String())
	}
}

// The tenant-admin arm. The accept route (POST .../question-jobs/{job}/accept)
// is NOT gated, so an `admin` can mint a question; without this arm the very
// next read 403s and the atom editor renders empty — the generated content
// looks lost.
func TestGetQuestion_AdminAllowed_MirrorsBFFPredicate(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	r := learnerJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil)
	r.Header.Set(servicemesh.HeaderUserRoles, "admin")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin must read back what the ungated accept route let it create; got %d %s", w.Code, w.Body.String())
	}
}

// The other half of the gate: a non-authoring role is still refused, so the
// widening did not open the answer key to the whole tenant.
func TestGetQuestion_AuditorRefused(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qID := seedQuestion(t, srv, atomID)

	r := learnerJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil)
	r.Header.Set(servicemesh.HeaderUserRoles, "auditor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("auditor must not read the answer key; got %d %s", w.Code, w.Body.String())
	}
}

func TestGetQuestionByID_ReturnsAuthorProjection_FullPayload(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "x", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "y", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	qID := got["question"].(map[string]any)["question_id"].(string)

	// GET by id
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", w2.Code, w2.Body.String())
	}
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	q := fetched["question"].(map[string]any)
	if q["question_id"] != qID {
		t.Errorf("question_id roundtrip mismatch: got %v want %s", q["question_id"], qID)
	}
	mcq, _ := q["mcq"].(map[string]any)
	if mcq == nil {
		t.Fatalf("expected mcq payload on author projection; got %s", w2.Body.String())
	}
	options, _ := mcq["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected 2 options; got %v", options)
	}
	// Author projection MUST include is_correct + explainer.
	first := options[0].(map[string]any)
	if _, ok := first["is_correct"]; !ok {
		t.Errorf("author projection missing is_correct: %v", first)
	}
	if _, ok := first["explainer"]; !ok {
		t.Errorf("author projection missing explainer: %v", first)
	}
}

// fakeDownloadSigner is a test double for ports.AtomMediaDownloadSigner.
type fakeDownloadSigner struct {
	fn func(ctx context.Context, gsURI string) (ports.SignedDownloadURL, error)
}

func (f fakeDownloadSigner) SignDownloadURL(ctx context.Context, gsURI string) (ports.SignedDownloadURL, error) {
	return f.fn(ctx, gsURI)
}

// seedMCQAtom seeds an MCQ atom and returns a router wired with the given
// (optionally nil) download signer plus the atom id.
func seedMCQAtomWithSigner(t *testing.T, signer ports.AtomMediaDownloadSigner) (http.Handler, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "Img atom", Body: "b", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save atom: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo, AtomMediaDownloadSigner: signer,
	})
	return srv, a.AtomID
}

// CHO-1638 — the AUTHOR re-open projection (GET /questions/{id}) must mint
// durable gs:// image refs to fresh signed GET URLs (an <img> can't carry a
// bearer JWT; a published atom stores the canonical gs:// ref). RED before
// getQuestion learns to mint.
func TestGetQuestionByID_MintsDurableImageRefs(t *testing.T) {
	t.Parallel()
	var signedURIs []string
	signer := fakeDownloadSigner{fn: func(_ context.Context, gsURI string) (ports.SignedDownloadURL, error) {
		signedURIs = append(signedURIs, gsURI)
		return ports.SignedDownloadURL{URL: "https://signed.example/" + gsURI, ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
	}}
	srv, atomID := seedMCQAtomWithSigner(t, signer)

	stemGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png"
	ansGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/ans.png"
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Identify the condensation stage.",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "a", "label": "Condensation", "is_correct": true, "explainer": "Yes"},
				{"option_id": "b", "label": "Evaporation", "is_correct": false, "explainer": "No"},
			},
		},
		"image_url":        stemGS,
		"answer_image_url": ansGS,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w2.Code, w2.Body.String())
	}
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	mcq := fetched["question"].(map[string]any)["mcq"].(map[string]any)
	gotImg, _ := mcq["image_url"].(string)
	gotAns, _ := mcq["answer_image_url"].(string)
	if !strings.HasPrefix(gotImg, "https://signed.example/") {
		t.Errorf("question image_url not minted: got %q", gotImg)
	}
	if !strings.HasPrefix(gotAns, "https://signed.example/") {
		t.Errorf("answer_image_url not minted: got %q", gotAns)
	}
	if strings.HasPrefix(gotImg, "gs://") || strings.HasPrefix(gotAns, "gs://") {
		t.Errorf("raw gs:// leaked to the wire: img=%q ans=%q", gotImg, gotAns)
	}
	// Author projection still carries is_correct + explainer.
	first := mcq["options"].([]any)[0].(map[string]any)
	if _, ok := first["is_correct"]; !ok {
		t.Errorf("author projection lost is_correct")
	}
	if len(signedURIs) != 2 {
		t.Errorf("expected 2 mint calls (stem+answer); got %d: %v", len(signedURIs), signedURIs)
	}
}

// A transient https ref (draft atom, pre-publish) must pass through unchanged
// and the signer must NOT be invoked for it.
func TestGetQuestionByID_PassesThroughTransientHTTPS(t *testing.T) {
	t.Parallel()
	called := false
	signer := fakeDownloadSigner{fn: func(_ context.Context, gsURI string) (ports.SignedDownloadURL, error) {
		called = true
		return ports.SignedDownloadURL{URL: "https://should-not-be-used/", ExpiresAt: time.Now()}, nil
	}}
	srv, atomID := seedMCQAtomWithSigner(t, signer)
	transient := "https://storage.googleapis.com/chora-ai-assist-images-dev/x.png?X-Goog-Expires=604800"
	body := map[string]any{
		"type":   "mcq",
		"prompt": "p",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "a", "label": "A", "is_correct": true, "explainer": "y"},
				{"option_id": "b", "label": "B", "is_correct": false, "explainer": "n"},
			},
		},
		"image_url": transient,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	mcq := fetched["question"].(map[string]any)["mcq"].(map[string]any)
	if mcq["image_url"] != transient {
		t.Errorf("transient https should pass through unchanged: got %v", mcq["image_url"])
	}
	if called {
		t.Errorf("signer must NOT be called for a transient https ref")
	}
}

// On mint failure the durable ref is dropped (never a raw gs:// on the wire)
// and the endpoint still returns 200.
func TestGetQuestionByID_MintFailureDropsRef_Still200(t *testing.T) {
	t.Parallel()
	signer := fakeDownloadSigner{fn: func(_ context.Context, gsURI string) (ports.SignedDownloadURL, error) {
		return ports.SignedDownloadURL{}, ports.ErrAtomMediaDownloadSignerNotWired
	}}
	srv, atomID := seedMCQAtomWithSigner(t, signer)
	body := map[string]any{
		"type":   "mcq",
		"prompt": "p",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "a", "label": "A", "is_correct": true, "explainer": "y"},
				{"option_id": "b", "label": "B", "is_correct": false, "explainer": "n"},
			},
		},
		"image_url": "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get must still be 200 on mint failure; got %d %s", w2.Code, w2.Body.String())
	}
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	mcq := fetched["question"].(map[string]any)["mcq"].(map[string]any)
	if v, present := mcq["image_url"]; present {
		if s, _ := v.(string); strings.HasPrefix(s, "gs://") {
			t.Errorf("raw gs:// leaked after mint failure: %q", s)
		}
	}
}

func TestGetQuestionByID_404OnMissing(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-aaaaaaaaaaaa", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "CREATION_QUESTION_NOT_FOUND") {
		t.Errorf("expected CREATION_QUESTION_NOT_FOUND; got %s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// LearningAtom DELETE cascade to attached Question — ddd-enforcement #8
//
// Per .claude/rules/ddd-enforcement.md Aggregate Invariant #8 — "Cascade
// soft-delete within same aggregate (parent → children)". LearningAtom +
// Question are 1:1 by design (D2 invariant enforced by fakeQuestionRepository
// + production QuestionRepo). Deleting the atom MUST cascade soft-delete to
// the attached question; otherwise the question row is orphaned in pg with
// no parent atom to reach it through.
//
// Filed under a508f184 FE atom CRUD gap + 1052db87 Infra Cloud Armor unblock.
// -----------------------------------------------------------------------------

func TestDeleteAtom_CascadesSoftDeleteToAttachedQuestion(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)

	// Author a question on the atom so the cascade has a target.
	createBody := map[string]any{
		"type":   "mcq",
		"prompt": "Cascade smoke",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"label": "A", "is_correct": true, "explainer": "right"},
				{"label": "B", "is_correct": false, "explainer": "wrong"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed POST /questions = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var seedResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &seedResp)
	qWire, _ := seedResp["question"].(map[string]any)
	questionID, _ := qWire["question_id"].(string)
	if questionID == "" {
		t.Fatalf("seed: question_id missing in response body=%s", w.Body.String())
	}

	// Pre-condition — question is reachable via the repo before the atom DELETE.
	if _, _, err := qRepo.GetByID(context.Background(), tenantA, questionID); err != nil {
		t.Fatalf("pre-condition: GetByID returns %v; want question reachable", err)
	}
	if _, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID); err != nil {
		t.Fatalf("pre-condition: GetByAtomID returns %v; want question reachable", err)
	}

	// DELETE the atom.
	wDel := httptest.NewRecorder()
	srv.ServeHTTP(wDel, authedJSON(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if wDel.Code != http.StatusNoContent && wDel.Code != http.StatusOK {
		t.Fatalf("DELETE /api/atoms/{id} = %d body=%s; want 204 or 200", wDel.Code, wDel.Body.String())
	}

	// Post-condition — the attached question is also soft-deleted (cascade
	// from parent → child). fakeQuestionRepository.GetByID filters out
	// rows with DeletedAt != nil, so a ErrNotFound here means the cascade
	// landed.
	if _, _, err := qRepo.GetByID(context.Background(), tenantA, questionID); err == nil {
		t.Errorf("cascade: GetByID(questionID) returned nil err; want ErrNotFound (question should be soft-deleted along with parent atom)")
	}
	if _, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID); err == nil {
		t.Errorf("cascade: GetByAtomID(atomID) returned nil err; want ErrNotFound (no live question reachable through deleted atom)")
	}
}

// Guard — deleting an atom that has NO attached question must still succeed
// (no cascade target, no crash, no error).
func TestDeleteAtom_NoQuestion_NoCascadeFailure(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Errorf("DELETE /api/atoms/{id} (no question) = %d body=%s; want 204 (cascade no-op is OK)", w.Code, w.Body.String())
	}
}

// Guard — deleting an atom whose attached question was ALREADY soft-deleted
// (e.g., user manually deleted the question first via the per-question DELETE)
// must still succeed; cascade is idempotent.
func TestDeleteAtom_QuestionAlreadyDeleted_StillSucceeds(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)

	// Author a question + immediately soft-delete it via the per-question DELETE.
	createBody := map[string]any{
		"type":   "mcq",
		"prompt": "Pre-deleted",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"label": "A", "is_correct": true, "explainer": "x"},
				{"label": "B", "is_correct": false, "explainer": "y"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	var seedResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &seedResp)
	qWire, _ := seedResp["question"].(map[string]any)
	questionID, _ := qWire["question_id"].(string)

	if err := qRepo.SoftDelete(context.Background(), tenantA, questionID); err != nil {
		t.Fatalf("pre-step: soft-delete question failed: %v", err)
	}

	// Now DELETE the atom. Should succeed (idempotent cascade).
	wDel := httptest.NewRecorder()
	srv.ServeHTTP(wDel, authedJSON(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if wDel.Code != http.StatusNoContent && wDel.Code != http.StatusOK {
		t.Errorf("DELETE /api/atoms/{id} (question already deleted) = %d body=%s; want 204 (cascade is idempotent)", wDel.Code, wDel.Body.String())
	}
}

// -----------------------------------------------------------------------------
// CHO-2255 (SECURITY) — manual authoring must not leak the answer via the
// option id or the option ORDER.
//
// Measured live 2026-07-17 (4-option MCQs, chance = 25%):
//   - ai_assist, post-shuffle (since 2026-06-20): 31.6% correct-first — works.
//   - MANUAL, same period:                        95.0% correct-first (19/20).
//
// Authors type the answer first and nothing shuffled it, and the FE renders in
// payload order with a positional A/B/C/D marker — so "always pick A" won.
// -----------------------------------------------------------------------------

// createMCQAs posts a manual MCQ whose CORRECT option is deliberately FIRST and
// whose ids name the key, then returns the STORED options via the author read.
func createMCQAs(t *testing.T, srv http.Handler, atomID, prompt string) []map[string]any {
	t.Helper()
	body := map[string]any{
		"type": "mcq", "prompt": prompt,
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "opt_correct", "label": "the right one", "is_correct": true, "explainer": "yes"},
			{"option_id": "opt_distractor_1", "label": "wrong a", "is_correct": false},
			{"option_id": "opt_distractor_2", "label": "wrong b", "is_correct": false},
			{"option_id": "opt_distractor_3", "label": "wrong c", "is_correct": false},
		}},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	q := got["question"].(map[string]any)
	mcq := q["mcq"].(map[string]any)
	raw := mcq["options"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, o := range raw {
		out = append(out, o.(map[string]any))
	}
	return out
}

func TestCreateQuestion_Manual_MintsOpaqueOptionIDs(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	opts := createMCQAs(t, srv, atomID, "which is right?")

	if len(opts) != 4 {
		t.Fatalf("stored %d options; want 4", len(opts))
	}
	for _, o := range opts {
		id, _ := o["option_id"].(string)
		if id == "" {
			t.Fatalf("stored a blank option id")
		}
		low := strings.ToLower(id)
		if strings.Contains(low, "correct") || strings.Contains(low, "distractor") {
			t.Errorf("the caller's answer-naming id was stored verbatim: %q", id)
		}
	}
}

// The order bug. Deterministic assertion is impossible for one shuffled
// question, so drive many: with the answer typed FIRST every time, a working
// shuffle must not leave it first every time.
func TestCreateQuestion_Manual_ShufflesOptionOrder(t *testing.T) {
	t.Parallel()
	const n = 24
	first := 0
	for i := 0; i < n; i++ {
		srv, _, _, atomID := newQuestionServer(t)
		opts := createMCQAs(t, srv, atomID, "q")
		// The answer moves with its fields — find it by its label.
		for pos, o := range opts {
			if o["label"] == "the right one" {
				if pos == 0 {
					first++
				}
				break
			}
		}
	}
	// Chance is 1-in-4. Pre-fix this was 24/24. Allow generous slack for a
	// legitimately random run while still failing loud on "never shuffled".
	if first > n/2 {
		t.Errorf("the correct option was stored FIRST in %d/%d manual creates — chance is ~%d; manual authoring is not being shuffled",
			first, n, n/4)
	}
}

// -----------------------------------------------------------------------------
// CHO-2255 (SECURITY) — the SAME two leaks on the PATCH twin.
//
// The original fix landed on createQuestion's builder only. There are THREE
// author/agent write paths into mcq_payload, not two:
//
//	candidate_normalizer.go     (AI ingest)  — mints + shuffles
//	buildPayloadFromRequest     (POST)       — mints + shuffles
//	buildPatchPayloadFromRequest(PATCH)      — did NEITHER
//
// So every leak POST closed, PATCH re-opened: an edit stored the caller's
// answer-naming id verbatim and restored the author's correct-first order.
// That also makes any data backfill decay — one edit re-poisons the row.
// Re-minting on PATCH is safe because patchQuestion already re-emits the
// atom_index upsert with the effective payload (fail-loud), so the grading key
// crossing into chora_consumption follows the new ids.
// -----------------------------------------------------------------------------

// patchMCQAs creates a benign MCQ, then PATCHes it with the correct option
// FIRST and ids that name the key — exactly what an author's editor sends on a
// read-modify-write save. Returns the STORED options via the patch response.
func patchMCQAs(t *testing.T, srv http.Handler, atomID string) []map[string]any {
	t.Helper()
	createBody := map[string]any{
		"type": "mcq", "prompt": "v1",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"label": "seed a", "is_correct": true, "explainer": "yes"},
			{"label": "seed b", "is_correct": false},
		}},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	qID := got["question"].(map[string]any)["question_id"].(string)

	patchBody := map[string]any{
		"prompt": "v2",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "opt_correct", "label": "the right one", "is_correct": true, "explainer": "yes"},
			{"option_id": "opt_distractor_1", "label": "wrong a", "is_correct": false},
			{"option_id": "opt_distractor_2", "label": "wrong b", "is_correct": false},
			{"option_id": "opt_distractor_3", "label": "wrong c", "is_correct": false},
		}},
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, patchBody))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w2.Code, w2.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &patched)
	mcq := patched["question"].(map[string]any)["mcq"].(map[string]any)
	raw := mcq["options"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, o := range raw {
		out = append(out, o.(map[string]any))
	}
	return out
}

func TestPatchQuestion_MintsOpaqueOptionIDs(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	opts := patchMCQAs(t, srv, atomID)

	if len(opts) != 4 {
		t.Fatalf("stored %d options; want 4", len(opts))
	}
	for _, o := range opts {
		id, _ := o["option_id"].(string)
		if id == "" {
			t.Fatalf("stored a blank option id")
		}
		low := strings.ToLower(id)
		if strings.Contains(low, "correct") || strings.Contains(low, "distractor") {
			t.Errorf("PATCH stored the caller's answer-naming id verbatim: %q", id)
		}
	}
}

// Same shape as the create-path shuffle test: one shuffled question cannot be
// asserted deterministically, so drive many with the answer typed FIRST every
// time. A working shuffle must not leave it first every time.
func TestPatchQuestion_ShufflesOptionOrder(t *testing.T) {
	t.Parallel()
	const n = 24
	first := 0
	for i := 0; i < n; i++ {
		srv, _, _, atomID := newQuestionServer(t)
		opts := patchMCQAs(t, srv, atomID)
		// The answer moves with its fields — find it by its label.
		for pos, o := range opts {
			if o["label"] == "the right one" {
				if pos == 0 {
					first++
				}
				break
			}
		}
	}
	if first > n/2 {
		t.Errorf("the correct option was stored FIRST in %d/%d manual PATCHes — chance is ~%d; the PATCH path is not being shuffled",
			first, n, n/4)
	}
}
