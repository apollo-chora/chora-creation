// questions_delete_test.go — HTTP-layer tests for the DELETE + GET error
// branches of /api/atoms/{atom_id}/questions/{question_id} and the PATCH
// fail-loud paths. Complements questions_handler_test.go's happy paths; same
// router fixture and fakes.
package httpadapter_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createQuestionID posts a minimal MCQ and returns its question_id.
func createQuestionID(t *testing.T, srv http.Handler, atomID string) string {
	t.Helper()
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which is a Scrum role?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"label": "Product Owner", "is_correct": true},
				{"label": "Project Manager", "is_correct": false},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed question: status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("seed question: invalid json: %v", err)
	}
	q, _ := got["question"].(map[string]any)
	qid, _ := q["question_id"].(string)
	if qid == "" {
		t.Fatalf("seed question: question_id missing: %s", w.Body.String())
	}
	return qid
}

// -----------------------------------------------------------------------------
// DELETE /api/atoms/{atom_id}/questions/{question_id}
// -----------------------------------------------------------------------------

func TestDeleteQuestion_Returns204(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete, "/api/atoms/"+atomID+"/questions/"+qid, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s; want 204", w.Code, w.Body.String())
	}

	// A subsequent GET must now 404 (soft-deleted).
	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qid, nil))
	if wGet.Code != http.StatusNotFound {
		t.Fatalf("GET after delete: status = %d; want 404", wGet.Code)
	}
}

func TestDeleteQuestion_NotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// A question that lives under a DIFFERENT atom → 404 (no cross-atom leak).
func TestDeleteQuestion_WrongAtom_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/questions/"+qid, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

func TestDeleteQuestion_RepoGetError_500(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)
	qRepo.getErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

func TestDeleteQuestion_SoftDeleteError_500(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)
	qRepo.softErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodDelete, "/api/atoms/"+atomID+"/questions/"+qid, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/atoms/{atom_id}/questions/{question_id} — error branches
// -----------------------------------------------------------------------------

func TestGetQuestion_NotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

func TestGetQuestion_RepoError_500(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)
	qRepo.getErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

func TestGetQuestion_WrongAtom_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/questions/"+qid, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/atoms/{atom_id}/questions/{question_id} — fail-loud branches
// -----------------------------------------------------------------------------

func TestPatchQuestion_InvalidJSON_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0", "not-an-object"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

func TestPatchQuestion_NotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-0000000beef0",
		map[string]any{"prompt": "updated"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

func TestPatchQuestion_WrongAtom_404(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/questions/"+qid,
		map[string]any{"prompt": "updated"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

func TestPatchQuestion_AppendError_500(t *testing.T) {
	t.Parallel()
	srv, qRepo, _, atomID := newQuestionServer(t)
	qid := createQuestionID(t, srv, atomID)
	qRepo.appendErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+atomID+"/questions/"+qid,
		map[string]any{"prompt": "updated prompt"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}
