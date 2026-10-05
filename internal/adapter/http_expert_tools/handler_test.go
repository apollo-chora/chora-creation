// Tests for the Expert Tools HTTP routes (CHO-30).
//
// Black-box: end-to-end against in-memory repositories + an in-memory
// event recorder. Each mutating endpoint asserts the resulting status,
// payload shape, and Pub/Sub event.
package http_expert_tools_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	et "github.com/apollo-chora/chora-creation/internal/adapter/http_expert_tools"
	expertinmem "github.com/apollo-chora/chora-creation/internal/adapter/inmem/expert_tools"
)

const (
	etTenantA   = "01970000-0000-7000-8000-000000000001"
	etGcidA     = "01970000-0000-7000-9000-000000000001"
	etReviewer  = "01970000-0000-7000-9000-000000000002"
	etReviewerB = "01970000-0000-7000-9000-000000000003"
	etAtomA     = "01970000-0000-7000-aaaa-000000000001"
	etAtomB     = "01970000-0000-7000-aaaa-000000000002"
	etRevA      = "01970000-0000-7000-bbbb-000000000001"
	etRevB      = "01970000-0000-7000-bbbb-000000000002"
)

func newServer(t *testing.T) (http.Handler, *expertinmem.MemRecorder) {
	t.Helper()
	rec := expertinmem.NewMemRecorder()
	deps := et.Deps{
		PeerReviewRepo:   expertinmem.NewPeerReviewRepository(),
		AnchorRepo:       expertinmem.NewAnchorRepository(),
		QualityScoreRepo: expertinmem.NewQualityScoreRepository(),
		Publisher:        rec,
	}
	mux := et.NewRouter(deps)
	return mux, rec
}

func authedReq(method, path string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etGcidA)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------------

func TestHealthz_ReturnsOK(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 1. Peer review submit
// -----------------------------------------------------------------------------

func TestPeerReview_SubmitReturns201AndPublishesEvent(t *testing.T) {
	t.Parallel()
	srv, rec := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{
			"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2,
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["review_id"] == nil || got["review_id"] == "" {
		t.Errorf("review_id missing")
	}
	if got["status"] != "pending" {
		t.Errorf("status = %v; want pending", got["status"])
	}
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	if !strings.HasPrefix(events[0].Topic, "chora.creation.peer_review.submitted.v1") {
		t.Errorf("topic = %q; want chora.creation.peer_review.submitted.v1", events[0].Topic)
	}
	env := events[0].Envelope
	if env.SchemaVersion != 1 || env.SourceProject != "chora-content" || env.SourceService != "chora-creation" {
		t.Errorf("envelope mandatory fields incorrect: %+v", env)
	}
	if env.EventID == "" || env.IdempotencyKey == "" || env.TenantID != etTenantA {
		t.Errorf("envelope ids/tenant missing: %+v", env)
	}
}

func TestPeerReview_SubmitRejectsMissingAtomID(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"revision_id": etRevA, "quorum_size": 2}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

func TestPeerReview_SubmitRejectsMissingHeaders(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		bytes.NewBufferString(`{"atom_id":"x","revision_id":"y","quorum_size":2}`))
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 2. Peer review vote + quorum
// -----------------------------------------------------------------------------

func TestPeerReview_VoteRecordsAndPublishes(t *testing.T) {
	t.Parallel()
	srv, rec := newServer(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	var sub map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	reviewID := sub["review_id"].(string)

	w2 := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/expert-tools/peer-review/"+reviewID+"/vote",
		bytes.NewBufferString(`{"decision":"approve","comment":"lgtm"}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etReviewer)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w2, r)

	if w2.Code != http.StatusAccepted {
		t.Fatalf("vote status = %d body=%s; want 202", w2.Code, w2.Body.String())
	}
	if len(rec.Events()) != 2 {
		t.Fatalf("events = %d; want 2", len(rec.Events()))
	}
	if !strings.HasPrefix(rec.Events()[1].Topic, "chora.creation.peer_review.vote_recorded.v1") {
		t.Errorf("2nd topic = %q; want vote_recorded", rec.Events()[1].Topic)
	}
}

func TestPeerReview_VoteToQuorumPublishesQuorumReached(t *testing.T) {
	t.Parallel()
	srv, rec := newServer(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	var sub map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	reviewID := sub["review_id"].(string)

	for _, gcid := range []string{etReviewer, etReviewerB} {
		w2 := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost,
			"/v1/expert-tools/peer-review/"+reviewID+"/vote",
			bytes.NewBufferString(`{"decision":"approve"}`))
		r.Header.Set("X-Tenant-Id", etTenantA)
		r.Header.Set("gcid", gcid)
		r.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(w2, r)
		if w2.Code != http.StatusAccepted {
			t.Fatalf("vote status = %d body=%s", w2.Code, w2.Body.String())
		}
	}

	topics := make([]string, 0, len(rec.Events()))
	for _, e := range rec.Events() {
		topics = append(topics, e.Topic)
	}
	wantQuorum := false
	for _, tp := range topics {
		if strings.HasPrefix(tp, "chora.creation.peer_review.quorum_reached.v1") {
			wantQuorum = true
		}
	}
	if !wantQuorum {
		t.Errorf("missing quorum_reached event; topics=%v", topics)
	}
}

func TestPeerReview_VoteRejectsAuthorSelfVote(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	var sub map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	reviewID := sub["review_id"].(string)

	w2 := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/expert-tools/peer-review/"+reviewID+"/vote",
		bytes.NewBufferString(`{"decision":"approve"}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etGcidA) // same as submitter
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w2, r)
	if w2.Code != http.StatusBadRequest && w2.Code != http.StatusConflict {
		t.Errorf("status = %d; want 400/409", w2.Code)
	}
}

func TestPeerReview_VoteRejectsUnknownReview(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/expert-tools/peer-review/01970000-0000-7000-9999-000000000099/vote",
		bytes.NewBufferString(`{"decision":"approve"}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etReviewer)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 3. Peer review queue list
// -----------------------------------------------------------------------------

func TestPeerReview_QueueListReturnsItems(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
			map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
		if w.Code != http.StatusCreated {
			t.Fatalf("seed %d failed: %d", i, w.Code)
		}
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/expert-tools/peer-review/queue", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 3 {
		t.Errorf("items = %d; want 3", len(items))
	}
}

func TestPeerReview_QueueListSupportsStatusFilter(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet,
		"/v1/expert-tools/peer-review/queue?status=approved", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
}

func TestPeerReview_QueueListRejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet,
		"/v1/expert-tools/peer-review/queue?status=BAD", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 4. Golden set anchor create
// -----------------------------------------------------------------------------

func TestGoldenSet_CreateAnchorReturns201AndPublishes(t *testing.T) {
	t.Parallel()
	srv, rec := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{
			"atom_id":       etAtomA,
			"title":         "Photosynthesis ref",
			"body":          "plants convert light to chemical energy",
			"tags":          []string{"biology"},
			"quality_score": 0.9,
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(rec.Events()[0].Topic, "chora.creation.golden_set.anchor_created.v1") {
		t.Errorf("topic = %q; want anchor_created", rec.Events()[0].Topic)
	}
}

func TestGoldenSet_CreateAnchorRejectsBadQualityScore(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{"atom_id": etAtomA, "title": "t", "body": "b", "quality_score": 1.5}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 5. Golden set compare
// -----------------------------------------------------------------------------

func TestGoldenSet_CompareReturnsScore(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)

	w0 := httptest.NewRecorder()
	srv.ServeHTTP(w0, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{
			"atom_id":       etAtomA,
			"title":         "Photosynthesis",
			"body":          "plants convert light to chemical energy",
			"tags":          []string{"biology"},
			"quality_score": 0.9,
		}))
	var anchor map[string]any
	_ = json.Unmarshal(w0.Body.Bytes(), &anchor)
	anchorID := anchor["anchor_id"].(string)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/compare",
		map[string]any{
			"anchor_id":            anchorID,
			"compared_atom_id":     etAtomB,
			"compared_revision_id": etRevA,
			"compared_body":        "plants convert light to chemical energy",
			"compared_tags":        []string{"biology"},
		}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["verdict"] == nil {
		t.Errorf("verdict missing")
	}
	sim, _ := got["similarity_score"].(float64)
	if sim <= 0 {
		t.Errorf("similarity = %f; want > 0", sim)
	}
}

func TestGoldenSet_CompareRejectsUnknownAnchor(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/compare",
		map[string]any{
			"anchor_id":            "01970000-0000-7000-9999-000000000099",
			"compared_atom_id":     etAtomB,
			"compared_revision_id": etRevA,
			"compared_body":        "x",
		}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 6. Quality score compute
// -----------------------------------------------------------------------------

func TestQualityScore_ComputeReturns201AndPublishes(t *testing.T) {
	t.Parallel()
	srv, rec := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/quality-score/compute",
		map[string]any{
			"atom_id":               etAtomA,
			"revision_id":           etRevA,
			"clarity":               0.8,
			"pedagogical_soundness": 0.9,
			"fairness":              0.7,
			"accessibility":         0.7,
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["aggregate_score"] == nil {
		t.Errorf("aggregate_score missing")
	}
	if !strings.HasPrefix(rec.Events()[0].Topic, "chora.creation.quality_score.computed.v1") {
		t.Errorf("topic = %q; want quality_score.computed", rec.Events()[0].Topic)
	}
}

func TestQualityScore_ComputeRejectsBadInputs(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/quality-score/compute",
		map[string]any{
			"atom_id":               etAtomA,
			"revision_id":           etRevA,
			"clarity":               1.5,
			"pedagogical_soundness": 0.5,
			"fairness":              0.5,
			"accessibility":         0.5,
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 7. Quality score latest
// -----------------------------------------------------------------------------

func TestQualityScore_LatestReturnsScore(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w0 := httptest.NewRecorder()
	srv.ServeHTTP(w0, authedReq(http.MethodPost, "/v1/expert-tools/quality-score/compute",
		map[string]any{
			"atom_id":               etAtomA,
			"revision_id":           etRevA,
			"clarity":               0.8,
			"pedagogical_soundness": 0.8,
			"fairness":              0.8,
			"accessibility":         0.8,
		}))
	if w0.Code != http.StatusCreated {
		t.Fatalf("seed: %d", w0.Code)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet,
		"/v1/expert-tools/quality-score/"+etAtomA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["aggregate_score"] == nil {
		t.Errorf("score missing")
	}
}

func TestQualityScore_LatestReturns404WhenAbsent(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet,
		"/v1/expert-tools/quality-score/"+etAtomB, nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 8. Version diff compute
// -----------------------------------------------------------------------------

func TestVersionDiff_ComputeReturnsHunks(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/version-diff/compute",
		map[string]any{
			"atom_id":          etAtomA,
			"base_revision_id": etRevA,
			"base_body":        "alpha\nbeta\ngamma",
			"head_revision_id": etRevB,
			"head_body":        "alpha\ndelta\ngamma",
		}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["additions_count"] == nil || got["deletions_count"] == nil {
		t.Errorf("counts missing: %+v", got)
	}
	hunks, _ := got["hunks"].([]any)
	if len(hunks) == 0 {
		t.Errorf("hunks empty")
	}
}

func TestVersionDiff_RejectsSameRevision(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/version-diff/compute",
		map[string]any{
			"atom_id":          etAtomA,
			"base_revision_id": etRevA, "base_body": "x",
			"head_revision_id": etRevA, "head_body": "y",
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Method-not-allowed + path NOT_FOUND coverage
// -----------------------------------------------------------------------------

func TestExpertTools_RejectsWrongMethod(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodDelete, "/v1/expert-tools/peer-review/queue"},
		{http.MethodPut, "/v1/expert-tools/golden-set/anchors"},
		{http.MethodGet, "/v1/expert-tools/golden-set/compare"},
		{http.MethodGet, "/v1/expert-tools/quality-score/compute"},
		{http.MethodGet, "/v1/expert-tools/version-diff/compute"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(`{}`))
		r.Header.Set("X-Tenant-Id", etTenantA)
		r.Header.Set("gcid", etGcidA)
		r.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d; want 405", c.method, c.path, w.Code)
		}
	}
}

func TestPeerReview_UnknownSubresourceReturns404(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/expert-tools/peer-review/abc/somethingelse",
		bytes.NewBufferString(`{}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etGcidA)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}
