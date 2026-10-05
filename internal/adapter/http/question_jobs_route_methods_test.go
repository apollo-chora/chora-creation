// question_jobs_route_methods_test.go — dispatch routing: the 405
// MethodNotAllowed branches (each sub-resource enforces its verb) and the
// model-answer-job not-found paths. Exercises the routing arm of dispatch.
package httpadapter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET on the collection (POST-only) → 405.
func TestCreateQuestionJob_GETCollection_405(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

// POST on a bare jobID route (GET-only) → 405.
func TestGetQuestionJob_POSTJobItem_405(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

// GET on the /accept sub-resource (POST-only) → 405.
func TestAcceptQuestionJob_GET_405(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0/accept", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

// GET on the /regenerate-image sub-resource (POST-only) → 405.
func TestRegenerateImage_GET_405(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0/regenerate-image", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

// Unknown job sub-resource → 404.
func TestQuestionJob_UnknownSubresource_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost,
		"/api/atoms/"+atomID+"/question-jobs/01970000-0000-7000-8000-0000000beef0/unknown", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

// GET on model-answer-jobs (POST-only) → 405.
func TestCreateModelAnswerJob_GET_405(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-00000000q001/ai-model-answer-jobs", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

// POST model-answer-job against a question that does not exist → 404.
func TestCreateModelAnswerJob_UnknownQuestion_404(t *testing.T) {
	t.Parallel()
	srv, _, _, _, atomID := newJobsServer(t, &fakeManaLedger{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost,
		"/api/atoms/"+atomID+"/questions/01970000-0000-7000-8000-00000000q999/ai-model-answer-jobs", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}
