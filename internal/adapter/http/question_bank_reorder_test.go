// question_bank_reorder_test.go — RED-first end-to-end coverage of
// POST /api/v1/question-banks/{id}/reorder (CHO-1899 Wave 2). Exercises
// handler → service → in-memory repo so the wiring + ordering is real.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func addQToBank(t *testing.T, srv http.Handler, gcid, bankID, questionID string) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+bankID+"/questions",
		map[string]any{"question_id": questionID}, gcid))
	if w.Code != http.StatusCreated {
		t.Fatalf("addQToBank(%s): status=%d body=%s", questionID, w.Code, w.Body.String())
	}
}

type reorderItemsView struct {
	Items []struct {
		QuestionID string `json:"question_id"`
		Position   int    `json:"position"`
	} `json:"items"`
}

func TestReorderQuestionBank_ReordersAndReturns200(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Reorder bank")
	addQToBank(t, srv, ibOwner, id, qExists)  // position 0
	addQToBank(t, srv, ibOwner, id, qExists2) // position 1

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/reorder",
		map[string]any{"question_ids": []string{qExists2, qExists}}, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", w.Code, w.Body.String())
	}
	var got reorderItemsView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(got.Items) != 2 ||
		got.Items[0].QuestionID != qExists2 || got.Items[0].Position != 0 ||
		got.Items[1].QuestionID != qExists || got.Items[1].Position != 1 {
		t.Errorf("reordered items = %+v; want [qExists2@0, qExists@1]", got.Items)
	}
}

func TestReorderQuestionBank_NonPermutation_400(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Reorder bank")
	addQToBank(t, srv, ibOwner, id, qExists)
	addQToBank(t, srv, ibOwner, id, qExists2)

	w := httptest.NewRecorder()
	// Wrong count (missing one) → not a permutation.
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/reorder",
		map[string]any{"question_ids": []string{qExists}}, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400 for non-permutation; body=%s", w.Code, w.Body.String())
	}
}

func TestReorderQuestionBank_EmptyBody_400(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Reorder bank")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/reorder",
		map[string]any{}, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400 for missing question_ids", w.Code)
	}
}

func TestReorderQuestionBank_NonOwner_403(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Reorder bank")
	addQToBank(t, srv, ibOwner, id, qExists)
	addQToBank(t, srv, ibOwner, id, qExists2)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/reorder",
		map[string]any{"question_ids": []string{qExists2, qExists}}, ibOther))
	if w.Code != http.StatusForbidden {
		t.Errorf("status=%d; want 403 for non-owner", w.Code)
	}
}

func TestReorderQuestionBank_MissingBank_404(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	missing := "01970000-0000-7000-7000-0000000000ff"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+missing+"/reorder",
		map[string]any{"question_ids": []string{qExists}}, ibOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d; want 404 for missing bank", w.Code)
	}
}

func TestReorderQuestionBank_OnlyPOST_405(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Reorder bank")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/question-banks/"+id+"/reorder", nil, ibOwner))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d; want 405 on GET /reorder", w.Code)
	}
}
