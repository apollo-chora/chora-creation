// question_jobs_error_branches_test.go — HTTP-layer tests for the fail-loud
// error branches of POST /api/atoms/{atom_id}/question-jobs (ai_draft + manual
// dispatch) and GET /api/atoms/{atom_id}/question-jobs/{job_id}. Complements
// the happy-path tests in question_jobs_handler_test.go; same router fixture
// and fakes.
package httpadapter_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// invalid JSON body → 400 CREATION_JOB_INVALID_BODY.
func TestCreateQuestionJob_InvalidJSON_400(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	// A bare JSON string fails to decode into the struct request shape.
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", "not-an-object"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

// type=ai_draft with an unknown question_type → 400 CREATION_JOB_UNKNOWN_TYPE.
func TestCreateQuestionJob_InvalidQuestionType_400(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs",
		map[string]any{"type": "ai_draft", "question_type": "true_false", "prompt": "p"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

// type=ai_draft without a prompt → 400 CREATION_JOB_INVALID_BODY.
func TestCreateQuestionJob_MissingPrompt_400(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs",
		map[string]any{"type": "ai_draft", "question_type": "mcq"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

// An unknown job type → 400 CREATION_JOB_UNKNOWN_TYPE.
func TestCreateQuestionJob_UnknownType_400(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs",
		map[string]any{"type": "crystal_ball", "question_type": "mcq", "prompt": "p"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

// ai_draft against a missing parent atom → 404 CREATION_ATOM_NOT_FOUND.
func TestCreateQuestionJob_AtomNotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, _ := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/question-jobs",
		map[string]any{"type": "ai_draft", "question_type": "mcq", "prompt": "p"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// jobRepo.Create failing → 500 CREATION_JOB_INTERNAL (no refund: ai_draft is
// not charged upfront since CHO-1826 U3b).
func TestCreateQuestionJob_PersistError_500(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	jobRepo.createErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs",
		map[string]any{"type": "ai_draft", "question_type": "mcq", "prompt": "p"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

// type=manual against a missing parent atom → 404 CREATION_ATOM_NOT_FOUND.
func TestCreateManualJob_AtomNotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, _ := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/question-jobs",
		map[string]any{"type": "manual"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// type=manual with jobRepo.Create failing → 500 CREATION_JOB_INTERNAL.
func TestCreateManualJob_PersistError_500(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	jobRepo.createErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs",
		map[string]any{"type": "manual"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

// GET a missing job → 404 CREATION_JOB_NOT_FOUND.
func TestGetQuestionJob_NotFound_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// GET a job that belongs to a different atom → 404 CREATION_JOB_NOT_FOUND.
func TestGetQuestionJob_WrongAtom_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	jid := postJSONJob(t, srv, atomID, map[string]any{
		"type": "ai_draft", "question_type": "mcq", "prompt": "p",
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/01970000-0000-7000-8000-00000000dead/question-jobs/"+jid, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}
}

// GET with the job repo erroring → 500 CREATION_JOB_INTERNAL.
func TestGetQuestionJob_RepoError_500(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	jobRepo.getErr = errors.New("db down")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}
