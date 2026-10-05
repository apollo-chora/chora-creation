// question_bank_list_facets_test.go — RED-first end-to-end coverage of the
// listMyQuestionBanks facets (CHO-1899 BE gap #2): name-search (?q=), tag-filter
// (?tag=), sort (?sort=field:dir), and the fail-loud 400 on a malformed sort.
// Exercises handler → service → in-memory repo so the wiring is real.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createBankWithTags POSTs a bank with name + tags and returns nothing (the
// list assertions read it back through GET /me/question-banks).
func createBankWithTags(t *testing.T, srv http.Handler, gcid, name string, tags []string) {
	t.Helper()
	body := map[string]any{"name": name, "tags": tags}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", body, gcid))
	if w.Code != http.StatusCreated {
		t.Fatalf("createBankWithTags(%s): status=%d body=%s", name, w.Code, w.Body.String())
	}
}

func listBankNames(t *testing.T, srv http.Handler, query string) []string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/me/question-banks"+query, nil, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("list%s: status=%d body=%s", query, w.Code, w.Body.String())
	}
	var got struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	out := make([]string, len(got.Items))
	for i, it := range got.Items {
		out[i] = it.Name
	}
	return out
}

func TestListMyQuestionBanks_QFilterNarrows(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	_ = createIB(t, srv, ibOwner, "Algebra pool")
	_ = createIB(t, srv, ibOwner, "Geometry pool")

	names := listBankNames(t, srv, "?q=alg")
	if len(names) != 1 || names[0] != "Algebra pool" {
		t.Errorf("?q=alg → %v; want [Algebra pool]", names)
	}
}

func TestListMyQuestionBanks_TagFilterNarrows(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	createBankWithTags(t, srv, ibOwner, "Exam bank", []string{"exam-2026"})
	createBankWithTags(t, srv, ibOwner, "Warmup bank", []string{"warmup"})

	names := listBankNames(t, srv, "?tag=exam-2026")
	if len(names) != 1 || names[0] != "Exam bank" {
		t.Errorf("?tag=exam-2026 → %v; want [Exam bank]", names)
	}
}

func TestListMyQuestionBanks_SortNameAsc(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	_ = createIB(t, srv, ibOwner, "Beta")
	_ = createIB(t, srv, ibOwner, "Alpha")

	names := listBankNames(t, srv, "?sort=name:asc")
	if len(names) != 2 || names[0] != "Alpha" || names[1] != "Beta" {
		t.Errorf("?sort=name:asc → %v; want [Alpha Beta]", names)
	}
}

func TestListMyQuestionBanks_RejectsMalformedSort_400(t *testing.T) {
	t.Parallel()
	srv := newQuestionBankServer(t)
	for _, bad := range []string{"bogus:asc", "name:sideways", "owner_gcid:asc"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/me/question-banks?sort="+bad, nil, ibOwner))
		if w.Code != http.StatusBadRequest {
			t.Errorf("?sort=%s: status=%d; want 400", bad, w.Code)
		}
	}
}
