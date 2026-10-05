// question_bank_handler_test.go — HTTP adapter coverage for the QuestionBank CRUD
// endpoints (W3.B.1, 2026-06-28).
//
// Tests run end-to-end against the in-memory QuestionBankRepository + a stub
// QuestionLookup so the handler's auth/tenant/JSON-envelope wiring is exercised
// without a live DB. The EventPublisher is left UNWIRED (nil) per B.1 scope —
// no event assertions here. Mirrors collection_handler_test.go.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

const (
	ibOwner  = "01970000-0000-7000-9000-000000000001"
	ibOther  = "01970000-0000-7000-9000-000000000099"
	qExists  = "01970000-0000-7000-b000-000000000001"
	qExists2 = "01970000-0000-7000-b000-000000000002"
	qMissing = "01970000-0000-7000-b000-0000000000ff"
)

// memQuestionBankQuestions is a goroutine-safe stub of questionbank.QuestionLookup
// keyed by question_id → exists (within the asked tenant).
type memQuestionBankQuestions struct {
	mu      sync.Mutex
	exists  map[string]bool
	tenants []string // records each tenantID asked (asserts tenant-scoping)
}

func (l *memQuestionBankQuestions) Resolve(_ context.Context, tenantID, questionID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tenants = append(l.tenants, tenantID)
	return l.exists[questionID], nil
}

// Details resolves a synthetic atom_id + type + prompt for an existing question.
func (l *memQuestionBankQuestions) Details(_ context.Context, tenantID, questionID string) (string, string, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tenants = append(l.tenants, tenantID)
	if !l.exists[questionID] {
		return "", "", "", errors.New("memQuestionBankQuestions: unknown question " + questionID)
	}
	return "atom-" + questionID, "mcq", "prompt-" + questionID, nil
}

// fakeQBTestSetAssembler captures the AssembledTestSet published by the handler
// so the assemble-test-set HTTP tests can assert the event payload. Satisfies
// questionbank.TestSetAssemblyPublisher.
type fakeQBTestSetAssembler struct {
	mu    sync.Mutex
	last  *questionbank.AssembledTestSet
	calls int
	err   error
}

func (f *fakeQBTestSetAssembler) PublishAssembledTestSet(_ context.Context, evt questionbank.AssembledTestSet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	cp := evt
	cp.Items = append([]questionbank.AssembledTestSetItem(nil), evt.Items...)
	f.last = &cp
	return nil
}

func newQuestionBankServer(t *testing.T) http.Handler {
	t.Helper()
	repo := inmem.NewQuestionBankRepository()
	lookup := &memQuestionBankQuestions{exists: map[string]bool{qExists: true, qExists2: true}}
	deps := httpadapter.RouterDeps{
		Repo:                       inmem.NewAtomRepository(),
		QuestionBankRepository:     repo,
		QuestionBankQuestionLookup: lookup,
		// QuestionBankPublisher intentionally nil (B.1 — events deferred).
		// QuestionBankTestSetPublisher intentionally nil — assemble-test-set 503s.
	}
	return httpadapter.NewRouterWithDeps(deps)
}

// newQuestionBankServerWithAssembler wires a fake test-set assembler so the
// happy-path assemble-test-set route returns 202.
func newQuestionBankServerWithAssembler(t *testing.T, asm questionbank.TestSetAssemblyPublisher) http.Handler {
	t.Helper()
	repo := inmem.NewQuestionBankRepository()
	lookup := &memQuestionBankQuestions{exists: map[string]bool{qExists: true, qExists2: true}}
	deps := httpadapter.RouterDeps{
		Repo:                         inmem.NewAtomRepository(),
		QuestionBankRepository:       repo,
		QuestionBankQuestionLookup:   lookup,
		QuestionBankTestSetPublisher: asm,
	}
	return httpadapter.NewRouterWithDeps(deps)
}

func ibReq(method, path string, body any, gcid string) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcid)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// POST /api/v1/question-banks — create
// -----------------------------------------------------------------------------

func TestCreateQuestionBank_Returns201WithDefaultsAndTags(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	body := map[string]any{
		"name":        "Algebra pool",
		"description": "mid-term question pool",
		"tags":        []string{"algebra", "algebra", " geometry "},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", body, ibOwner))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got["name"] != "Algebra pool" {
		t.Errorf("name = %v; want match", got["name"])
	}
	if got["visibility"] != "PRIVATE" {
		t.Errorf("visibility = %v; want PRIVATE default", got["visibility"])
	}
	if got["question_bank_id"] == nil || got["question_bank_id"] == "" {
		t.Errorf("question_bank_id missing: %v", got)
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 2 {
		t.Errorf("tags = %v; want 2 (deduped+trimmed)", got["tags"])
	}
}

func TestCreateQuestionBank_RejectsMissingName(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", map[string]any{"name": ""}, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateQuestionBank_RejectsPublicVisibility(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	body := map[string]any{"name": "x", "visibility": "PUBLIC"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", body, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (PUBLIC forbidden); body=%s", w.Code, w.Body.String())
	}
}

func TestCreateQuestionBank_RejectsMissingAuthHeaders(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/question-banks",
		bytes.NewBuffer(jsonOrPanic(map[string]any{"name": "x"})))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for missing auth headers", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/me/question-banks — list mine
// -----------------------------------------------------------------------------

func TestListMyQuestionBanks_ReturnsOnlyMine(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	_ = createIB(t, srv, ibOwner, "Mine")
	_ = createIB(t, srv, ibOther, "Theirs")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/me/question-banks", nil, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []struct {
			Name      string `json:"name"`
			OwnerGCID string `json:"owner_gcid"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0].Name != "Mine" {
		t.Errorf("items = %+v; want 1 'Mine'", got.Items)
	}
	if got.Items[0].OwnerGCID != ibOwner {
		t.Errorf("owner_gcid = %s; want %s", got.Items[0].OwnerGCID, ibOwner)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/question-banks/{id}
// -----------------------------------------------------------------------------

func TestGetQuestionBank_ReturnsOwnerBank(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "X")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/question-banks/"+id, nil, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetQuestionBank_404ForUnknownID(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet,
		"/api/v1/question-banks/01970000-0000-7000-7000-deadbeef0000", nil, ibOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/question-banks/{id}
// -----------------------------------------------------------------------------

func TestPatchQuestionBank_UpdatesName(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Old")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPatch, "/api/v1/question-banks/"+id, map[string]any{"name": "New"}, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["name"] != "New" {
		t.Errorf("name = %v; want New", got["name"])
	}
}

func TestPatchQuestionBank_403ForNonOwner(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "x")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPatch, "/api/v1/question-banks/"+id, map[string]any{"name": "trying"}, ibOther))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/question-banks/{id}/questions
// -----------------------------------------------------------------------------

func TestAddQuestion_201(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Set")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{"question_id": qExists}, ibOwner))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %v; want 1", got["items"])
	}
}

func TestAddQuestion_404ForMissingQuestion(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Set")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{"question_id": qMissing}, ibOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestAddQuestion_400ForMissingQuestionID(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Set")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{}, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestAddQuestion_403ForNonOwner(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Set")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{"question_id": qExists}, ibOther))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/question-banks/{id}/questions — list questions
// -----------------------------------------------------------------------------

// fakeItemPager stubs questionbank.ItemPager — records the bank/filter it was
// asked for and returns a canned page, so the handler's query-param parsing +
// paginated-response shaping are exercised without SQL (the JOIN itself is
// verified live).
type fakeItemPager struct {
	mu        sync.Mutex
	items     []questionbank.EnrichedQuestionBankItem
	total     int
	gotBankID string
	gotFilter questionbank.ItemPageFilter
}

func (p *fakeItemPager) ListItemsPage(_ context.Context, _, bankID string, f questionbank.ItemPageFilter) ([]questionbank.EnrichedQuestionBankItem, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gotBankID = bankID
	p.gotFilter = f
	return p.items, p.total, nil
}

func newQuestionBankServerWithPager(t *testing.T, pager questionbank.ItemPager) http.Handler {
	t.Helper()
	return httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                       inmem.NewAtomRepository(),
		QuestionBankRepository:     inmem.NewQuestionBankRepository(),
		QuestionBankQuestionLookup: &memQuestionBankQuestions{exists: map[string]bool{qExists: true, qExists2: true}},
		QuestionBankItemPager:      pager,
	})
}

func TestListQuestions_ReturnsPagedItemsFromPager(t *testing.T) {
	t.Parallel()
	pager := &fakeItemPager{
		items: []questionbank.EnrichedQuestionBankItem{
			{QuestionID: qExists, AtomID: "atom-" + qExists, QuestionType: "mcq", Prompt: "What is 2+2?", Position: 0},
			{QuestionID: qExists2, AtomID: "atom-" + qExists2, QuestionType: "oe", Prompt: "Explain.", Position: 1},
		},
		total: 7, // exceeds the page — proves total is the MATCH count, not len(items)
	}
	srv := newQuestionBankServerWithPager(t, pager)
	id := createIB(t, srv, ibOwner, "Set")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet,
		"/api/v1/question-banks/"+id+"/questions?q=scrum&question_type=mcq&sort=prompt:desc&page=2&page_size=5",
		nil, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []struct {
			AtomID       string `json:"atom_id"`
			QuestionType string `json:"question_type"`
			Prompt       string `json:"prompt"`
		} `json:"items"`
		Total    int `json:"total"`
		Page     int `json:"page"`
		PageSize int `json:"page_size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got.Total != 7 || got.Page != 2 || got.PageSize != 5 || len(got.Items) != 2 {
		t.Fatalf("got total=%d page=%d page_size=%d items=%d; want 7/2/5/2",
			got.Total, got.Page, got.PageSize, len(got.Items))
	}
	if got.Items[0].AtomID != "atom-"+qExists || got.Items[0].Prompt != "What is 2+2?" ||
		got.Items[0].QuestionType != "mcq" {
		t.Errorf("item0 = %+v; want the pager's canned enriched row", got.Items[0])
	}
	// The handler parsed the query params into the filter the pager received.
	gf := pager.gotFilter
	if pager.gotBankID != id {
		t.Errorf("pager bank = %q; want %q", pager.gotBankID, id)
	}
	if gf.Query != "scrum" || gf.SortKey != "prompt" || !gf.SortDesc || gf.Limit != 5 || gf.Offset != 5 {
		t.Errorf("filter = %+v; want q=scrum sort=prompt desc limit=5 offset=5", gf)
	}
	if len(gf.Types) != 1 || gf.Types[0] != "mcq" {
		t.Errorf("filter types = %v; want [mcq]", gf.Types)
	}
}

func TestListQuestions_DefaultPaging(t *testing.T) {
	t.Parallel()
	pager := &fakeItemPager{total: 0}
	srv := newQuestionBankServerWithPager(t, pager)
	id := createIB(t, srv, ibOwner, "Set")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/question-banks/"+id+"/questions", nil, ibOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Page     int `json:"page"`
		PageSize int `json:"page_size"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Page != 1 || got.PageSize != 20 {
		t.Errorf("default page/page_size = %d/%d; want 1/20", got.Page, got.PageSize)
	}
	gf := pager.gotFilter
	if gf.Query != "" || len(gf.Types) != 0 || gf.SortKey != "" || gf.Limit != 20 || gf.Offset != 0 {
		t.Errorf("default filter = %+v; want empty / limit20 / offset0", gf)
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/question-banks/{id}/questions/{qid}
// -----------------------------------------------------------------------------

func TestRemoveQuestion_204(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "Set")
	doAddQuestion(t, srv, id, qExists, ibOwner)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodDelete, "/api/v1/question-banks/"+id+"/questions/"+qExists, nil, ibOwner))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d; want 204; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/question-banks/{id}
// -----------------------------------------------------------------------------

func TestDeleteQuestionBank_204(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t)
	id := createIB(t, srv, ibOwner, "x")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodDelete, "/api/v1/question-banks/"+id, nil, ibOwner))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d; want 204; body=%s", w.Code, w.Body.String())
	}
	// Now GET → 404.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodGet, "/api/v1/question-banks/"+id, nil, ibOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("GET after delete status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func createIB(t *testing.T, srv http.Handler, gcid, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", map[string]any{"name": name}, gcid))
	if w.Code != http.StatusCreated {
		t.Fatalf("createIB: status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("createIB unmarshal: %v body=%s", err, w.Body.String())
	}
	id, _ := got["question_bank_id"].(string)
	if id == "" {
		t.Fatalf("createIB no question_bank_id in response: %v", got)
	}
	return id
}

func doAddQuestion(t *testing.T, srv http.Handler, id, qid, gcid string) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{"question_id": qid}, gcid))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("doAddQuestion: status=%d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/question-banks/{id}/assemble-test-set (W3.B.2)
// -----------------------------------------------------------------------------

func TestAssembleTestSet_202(t *testing.T) {
	t.Parallel()

	asm := &fakeQBTestSetAssembler{}
	srv := newQuestionBankServerWithAssembler(t, asm)
	id := createIB(t, srv, ibOwner, "Pool")
	doAddQuestion(t, srv, id, qExists, ibOwner)
	doAddQuestion(t, srv, id, qExists2, ibOwner)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/assemble-test-set",
		map[string]any{"title": "Midterm", "description": "ch1-3"}, ibOwner))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want 202; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		JobID         string `json:"job_id"`
		TestSetStatus string `json:"test_set_status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got.JobID == "" {
		t.Errorf("job_id missing: %s", w.Body.String())
	}
	if got.TestSetStatus != "assembling" {
		t.Errorf("test_set_status = %q; want assembling", got.TestSetStatus)
	}
	if asm.calls != 1 {
		t.Fatalf("assembler calls = %d; want 1", asm.calls)
	}
	if asm.last.JobID != got.JobID {
		t.Errorf("event JobID = %q; want %q (matches response)", asm.last.JobID, got.JobID)
	}
	if asm.last.Title != "Midterm" {
		t.Errorf("event Title = %q; want Midterm", asm.last.Title)
	}
	if asm.last.HostAtomID != id {
		t.Errorf("event HostAtomID = %q; want question_bank_id %q (provenance)", asm.last.HostAtomID, id)
	}
	if len(asm.last.Items) != 2 {
		t.Fatalf("event items = %d; want 2", len(asm.last.Items))
	}
	if asm.last.Items[0].QuestionID != qExists || asm.last.Items[0].Points != 10 || asm.last.Items[0].DisplayOrder != 1 {
		t.Errorf("item0 = %+v; want qExists/points10/order1", asm.last.Items[0])
	}
	if asm.last.Items[1].QuestionID != qExists2 || asm.last.Items[1].DisplayOrder != 2 {
		t.Errorf("item1 = %+v; want qExists2/order2", asm.last.Items[1])
	}
}

func TestAssembleTestSet_400MissingTitle(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServerWithAssembler(t, &fakeQBTestSetAssembler{})
	id := createIB(t, srv, ibOwner, "Pool")
	doAddQuestion(t, srv, id, qExists, ibOwner)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/assemble-test-set",
		map[string]any{"description": "no title"}, ibOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestAssembleTestSet_404MissingBank(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServerWithAssembler(t, &fakeQBTestSetAssembler{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost,
		"/api/v1/question-banks/01970000-0000-7000-7000-deadbeef0000/assemble-test-set",
		map[string]any{"title": "X"}, ibOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestAssembleTestSet_403NonOwner(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServerWithAssembler(t, &fakeQBTestSetAssembler{})
	id := createIB(t, srv, ibOwner, "Pool")
	doAddQuestion(t, srv, id, qExists, ibOwner)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/assemble-test-set",
		map[string]any{"title": "X"}, ibOther))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestAssembleTestSet_422EmptyBank(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServerWithAssembler(t, &fakeQBTestSetAssembler{})
	id := createIB(t, srv, ibOwner, "Pool") // no questions added

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/assemble-test-set",
		map[string]any{"title": "X"}, ibOwner))
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422 (cannot assemble empty bank); body=%s", w.Code, w.Body.String())
	}
}

func TestAssembleTestSet_503WhenPublisherNotWired(t *testing.T) {
	t.Parallel()

	srv := newQuestionBankServer(t) // no assembler wired
	id := createIB(t, srv, ibOwner, "Pool")
	doAddQuestion(t, srv, id, qExists, ibOwner)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/assemble-test-set",
		map[string]any{"title": "X"}, ibOwner))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (publisher not wired); body=%s", w.Code, w.Body.String())
	}
}
