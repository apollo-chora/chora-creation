// Phyllis MVP HTTP handler tests (docs/m13/phyllis-mvp-2026-05-08.md §5.3).
//
// New endpoints:
//
//	POST   /v1/atoms                              create
//	POST   /v1/atoms:generate                     AI Assist
//	GET    /v1/atoms/{id}                         single atom
//	GET    /v1/courses/{course_id}/atoms          list by course
//	GET    /v1/atoms/{id}/revisions               revision history
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const (
	courseA = "01970000-0000-7000-7000-000000000001"
	courseB = "01970000-0000-7000-7000-000000000002"
)

type stubBroker struct {
	resp atom.GenerateResponse
	err  error
}

func (s *stubBroker) Generate(_ context.Context, _ atom.GenerateRequest) (atom.GenerateResponse, error) {
	if s.err != nil {
		return atom.GenerateResponse{}, s.err
	}
	return s.resp, nil
}

type recordingPub struct {
	events []atom.Event
}

func (r *recordingPub) Publish(_ context.Context, e atom.Event) error {
	r.events = append(r.events, e)
	return nil
}

func newPhyllisServer(t *testing.T, broker atom.ModelBrokerClient, pub atom.EventPublisher) (http.Handler, *inmem.AtomRepository) {
	t.Helper()
	repo := inmem.NewAtomRepository()
	if pub == nil {
		pub = &recordingPub{}
	}
	if broker == nil {
		broker = &stubBroker{
			resp: atom.GenerateResponse{
				ScreeningDecision: atom.ScreeningAllow,
				ModelUsed:         "stub",
				Items:             nil,
			},
		}
	}
	h := httpadapter.NewPhyllisRouter(httpadapter.PhyllisDeps{
		Repo:        repo,
		Broker:      broker,
		Publisher:   pub,
		MediaRepo:   inmem.NewMediaRepository(),
		MediaSigner: storage.NewInMemorySigner(""),
	})
	return h, repo
}

// -----------------------------------------------------------------------------
// POST /v1/atoms — create
// -----------------------------------------------------------------------------

func TestPhyllis_PostAtoms_Returns201AndPublishes(t *testing.T) {
	t.Parallel()

	pub := &recordingPub{}
	srv, repo := newPhyllisServer(t, nil, pub)

	body := map[string]any{
		"course_id":  courseA,
		"title":      "What is Agile?",
		"body":       "Agile is...",
		"type":       "mcq",
		"difficulty": 3,
		"tags":       []string{"agile", "scrum"},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", body))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["course_id"] != courseA {
		t.Errorf("course_id = %v; want %s", got["course_id"], courseA)
	}
	if got["atom_type"] != "mcq" {
		t.Errorf("atom_type = %v; want mcq", got["atom_type"])
	}
	atomID, _ := got["atom_id"].(string)
	if atomID == "" {
		t.Errorf("atom_id missing")
	}
	if len(pub.events) != 1 {
		t.Errorf("published events = %d; want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomCreated {
		t.Errorf("event type = %q; want chora.creation.atom.created.v1", ev.Type)
	}
	if ev.AtomID != atomID {
		t.Errorf("event atom_id = %q; want %q", ev.AtomID, atomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("event tenant_id = %q; want %s", ev.TenantID, tenantA)
	}
	if ev.SourceProject != "chora-content" {
		t.Errorf("event source_project = %q; want chora-content", ev.SourceProject)
	}
	if ev.SourceService != "chora-creation" {
		t.Errorf("event source_service = %q; want chora-creation", ev.SourceService)
	}
	if ev.SchemaVersion != 1 {
		t.Errorf("event schema_version = %d; want 1", ev.SchemaVersion)
	}
	if ev.EventID == "" {
		t.Errorf("event event_id is empty")
	}
	if ev.IdempotencyKey == "" {
		t.Errorf("event idempotency_key is empty")
	}
	if ev.OccurredAt == "" {
		t.Errorf("event occurred_at is empty")
	}
	if ev.PublishedAt == "" {
		t.Errorf("event published_at is empty")
	}
	// Verify atom is queryable from repo
	if _, err := repo.Get(context.Background(), tenantA, atomID); err != nil {
		t.Errorf("repo.Get unexpected error: %v", err)
	}
}

func TestPhyllis_PostAtoms_PropagatesTraceparentToEvent(t *testing.T) {
	t.Parallel()

	pub := &recordingPub{}
	srv, _ := newPhyllisServer(t, nil, pub)

	r := httptest.NewRequest(http.MethodPost, "/v1/atoms", strings.NewReader(`{
		"course_id":"`+courseA+`","title":"x","body":"y","type":"mcq","difficulty":1
	}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	// A VALID inbound W3C traceparent must propagate verbatim to the event
	// (trace correlation). (A malformed or absent one is replaced with a
	// freshly-minted valid root — see TestPhyllis_PostAtoms_MintsTraceparentWhenAbsent.)
	const validTP = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	r.Header.Set("traceparent", validTP)
	r.Header.Set("tracestate", "rojo=00f067aa")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d", len(pub.events))
	}
	if pub.events[0].TraceParent != validTP {
		t.Errorf("event traceparent = %q; want %q (valid inbound must propagate)", pub.events[0].TraceParent, validTP)
	}
	if pub.events[0].TraceState != "rojo=00f067aa" {
		t.Errorf("event tracestate = %q", pub.events[0].TraceState)
	}
}

func TestPhyllis_PostAtoms_400OnMissingFields(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"title": "x",
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

func TestPhyllis_PostAtoms_401OnMissingHeaders(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/atoms",
		bytes.NewBufferString(`{"course_id":"`+courseA+`","title":"x","body":"y","type":"mcq","difficulty":1}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 400 or 401 when headers missing", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /v1/atoms/{id}
// -----------------------------------------------------------------------------

func TestPhyllis_GetAtom_ReturnsAtomWithCurrentRevision(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id":  courseA,
		"title":      "x",
		"body":       "y",
		"type":       "mcq",
		"difficulty": 1,
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created["atom_id"].(string)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/v1/atoms/"+id, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d", w2.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["atom_id"] != id {
		t.Errorf("atom_id = %v; want %s", got["atom_id"], id)
	}
	cur, ok := got["current_revision"].(map[string]any)
	if !ok {
		t.Fatalf("current_revision missing or wrong type: %v", got["current_revision"])
	}
	if cur["body"] != "y" {
		t.Errorf("current_revision.body = %v; want y", cur["body"])
	}
	if cur["source_type"] != "manual" {
		t.Errorf("current_revision.source_type = %v; want manual", cur["source_type"])
	}
}

func TestPhyllis_GetAtom_404OnUnknown(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /v1/courses/{course_id}/atoms
// -----------------------------------------------------------------------------

func TestPhyllis_ListCourseAtoms_FiltersByCourse(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	for _, c := range []string{courseA, courseA, courseB} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
			"course_id":  c,
			"title":      "t",
			"body":       "b",
			"type":       "mcq",
			"difficulty": 1,
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("create failed: %d", w.Code)
		}
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/courses/"+courseA+"/atoms", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("total = %d; want 2", resp.Total)
	}
	for _, it := range resp.Items {
		if it["course_id"] != courseA {
			t.Errorf("returned atom for wrong course: %v", it["course_id"])
		}
	}
}

func TestPhyllis_ListCourseAtoms_HidesSoftDeleted(t *testing.T) {
	t.Parallel()

	srv, repo := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id": courseA, "title": "to-delete", "body": "y", "type": "mcq", "difficulty": 1,
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// Soft-delete via repo direct (no DELETE endpoint in MVP).
	a, _ := repo.Get(context.Background(), tenantA, atomID)
	_ = a.SoftDelete()
	_ = repo.Save(context.Background(), a)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/v1/courses/"+courseA+"/atoms", nil))
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("total = %d; want 0 (soft-deleted hidden)", resp.Total)
	}
}

// -----------------------------------------------------------------------------
// GET /v1/atoms/{id}/revisions
// -----------------------------------------------------------------------------

func TestPhyllis_GetAtomRevisions_ReturnsAppendOnlyHistory(t *testing.T) {
	t.Parallel()

	srv, repo := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id": courseA, "title": "x", "body": "v1", "type": "mcq", "difficulty": 1,
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created["atom_id"].(string)

	// Append two more revisions.
	a, _ := repo.Get(context.Background(), tenantA, id)
	_, err := a.AppendRevision(atom.AppendRevisionParams{
		Body: "v2", AuthoredBy: gcidA, SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}
	_, _ = a.AppendRevision(atom.AppendRevisionParams{
		Body: "v3", AuthoredBy: gcidA, SourceType: atom.SourceAIAssist,
	})
	_ = repo.Save(context.Background(), a)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/v1/atoms/"+id+"/revisions", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w2.Code, w2.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Errorf("total = %d; want 3", resp.Total)
	}
	for i, item := range resp.Items {
		want := i + 1
		if int(item["revision_number"].(float64)) != want {
			t.Errorf("revisions[%d].revision_number = %v; want %d",
				i, item["revision_number"], want)
		}
	}
	// First revision body must remain v1 (append-only invariant)
	if resp.Items[0]["body"] != "v1" {
		t.Errorf("revisions[0].body = %v; want v1", resp.Items[0]["body"])
	}
	if resp.Items[2]["source_type"] != "ai_assist" {
		t.Errorf("revisions[2].source_type = %v; want ai_assist", resp.Items[2]["source_type"])
	}
}

func TestPhyllis_GetAtomRevisions_404OnUnknown(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/atoms/01970000-0000-7000-cccc-dddddddddddd/revisions", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /v1/atoms:generate — AI Assist
// -----------------------------------------------------------------------------

func TestPhyllis_AIAssistGenerate_AllowDecision_Creates10Atoms(t *testing.T) {
	t.Parallel()

	items := make([]atom.GeneratedItem, 10)
	for i := range items {
		items[i] = atom.GeneratedItem{Title: "t", Body: "b"}
	}
	broker := &stubBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision: atom.ScreeningAllow,
			Items:             items,
			ModelUsed:         "vertex/gemini-1.5-pro",
		},
	}
	pub := &recordingPub{}
	srv, _ := newPhyllisServer(t, broker, pub)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms:generate", map[string]any{
		"course_id":    courseA,
		"prompt":       "Generate 10 MCQ on Agile Estimation",
		"content_type": "mcq",
		"difficulty":   3,
		"count":        10,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		GeneratedAtomIDs     []string `json:"generated_atom_ids"`
		ScreeningDecision    string   `json:"screening_decision"`
		ScreeningExplanation string   `json:"screening_explanation"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ScreeningDecision != "allow" {
		t.Errorf("ScreeningDecision = %q", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 10 {
		t.Errorf("GeneratedAtomIDs len = %d; want 10", len(resp.GeneratedAtomIDs))
	}
	if len(pub.events) != 10 {
		t.Errorf("published events = %d; want 10", len(pub.events))
	}
}

func TestPhyllis_AIAssistGenerate_RefuseDecision_CreatesZeroAtoms(t *testing.T) {
	t.Parallel()

	broker := &stubBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision:    atom.ScreeningRefuse,
			ScreeningExplanation: "policy",
			Items:                nil,
		},
	}
	pub := &recordingPub{}
	srv, _ := newPhyllisServer(t, broker, pub)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms:generate", map[string]any{
		"course_id":    courseA,
		"prompt":       "biased prompt",
		"content_type": "mcq",
		"difficulty":   3,
		"count":        5,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (refuse is a successful response)",
			w.Code, w.Body.String())
	}
	var resp struct {
		GeneratedAtomIDs     []string `json:"generated_atom_ids"`
		ScreeningDecision    string   `json:"screening_decision"`
		ScreeningExplanation string   `json:"screening_explanation"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ScreeningDecision != "refuse" {
		t.Errorf("ScreeningDecision = %q; want refuse", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 0 {
		t.Errorf("GeneratedAtomIDs len = %d; want 0 on refuse", len(resp.GeneratedAtomIDs))
	}
	if len(pub.events) != 0 {
		t.Errorf("published events = %d; want 0 on refuse", len(pub.events))
	}
}

func TestPhyllis_AIAssistGenerate_BrokerError_Returns502(t *testing.T) {
	t.Parallel()

	broker := &stubBroker{err: errors.New("upstream broken")}
	srv, _ := newPhyllisServer(t, broker, nil)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms:generate", map[string]any{
		"course_id":    courseA,
		"prompt":       "x",
		"content_type": "mcq",
		"difficulty":   3,
		"count":        1,
	}))
	if w.Code != http.StatusBadGateway && w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 502 or 500 on broker error", w.Code)
	}
}

func TestPhyllis_AIAssistGenerate_400OnInvalidPayload(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms:generate", map[string]any{
		"course_id":    courseA,
		"prompt":       "x",
		"content_type": "freeform",
		"difficulty":   3,
		"count":        1,
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}
