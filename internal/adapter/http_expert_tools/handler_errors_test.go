// Error-branch tests for the Expert Tools HTTP routes (CHO-30).
//
// Complements handler_test.go (black-box happy paths) with the failure
// branches: malformed bodies, repository errors, invalid domain input,
// nil-publisher degradation, and the X-Chora-GCID header alias.
package http_expert_tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	et "github.com/apollo-chora/chora-creation/internal/adapter/http_expert_tools"
	expertinmem "github.com/apollo-chora/chora-creation/internal/adapter/inmem/expert_tools"
	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
)

// -----------------------------------------------------------------------------
// Failing fakes (repo / publisher error injection)
// -----------------------------------------------------------------------------

var errFakeRepo = errors.New("fake repo failure")

type failingPeerReviewRepo struct{ inner pr.Repository }

func (r failingPeerReviewRepo) Save(ctx context.Context, s *pr.ReviewSubmission) error {
	return errFakeRepo
}
func (r failingPeerReviewRepo) Get(ctx context.Context, tenantID, reviewID string) (*pr.ReviewSubmission, error) {
	return nil, errFakeRepo
}
func (r failingPeerReviewRepo) List(ctx context.Context, tenantID string, f pr.ListFilter) ([]*pr.ReviewSubmission, error) {
	return nil, errFakeRepo
}

// failingSavePeerReviewRepo fails only Save; Get/List delegate to the in-mem
// repo so the vote flow can reach its Save branch.
type failingSavePeerReviewRepo struct{ inner pr.Repository }

func (r failingSavePeerReviewRepo) Save(ctx context.Context, s *pr.ReviewSubmission) error {
	return errFakeRepo
}
func (r failingSavePeerReviewRepo) Get(ctx context.Context, tenantID, reviewID string) (*pr.ReviewSubmission, error) {
	return r.inner.Get(ctx, tenantID, reviewID)
}
func (r failingSavePeerReviewRepo) List(ctx context.Context, tenantID string, f pr.ListFilter) ([]*pr.ReviewSubmission, error) {
	return r.inner.List(ctx, tenantID, f)
}

type failingAnchorRepo struct{}

func (r failingAnchorRepo) Save(ctx context.Context, a *gs.Anchor) error { return errFakeRepo }
func (r failingAnchorRepo) Get(ctx context.Context, tenantID, anchorID string) (*gs.Anchor, error) {
	return nil, errFakeRepo
}
func (r failingAnchorRepo) List(ctx context.Context, tenantID string) ([]*gs.Anchor, error) {
	return nil, errFakeRepo
}

type failingQualityScoreRepo struct{}

func (r failingQualityScoreRepo) Append(ctx context.Context, q *qs.QualityScore) error {
	return errFakeRepo
}
func (r failingQualityScoreRepo) Latest(ctx context.Context, tenantID, atomID string) (*qs.QualityScore, error) {
	return nil, errFakeRepo
}
func (r failingQualityScoreRepo) History(ctx context.Context, tenantID, atomID string) ([]*qs.QualityScore, error) {
	return nil, errFakeRepo
}

type failingPublisher struct{}

func (failingPublisher) Publish(ctx context.Context, topic string, env expertinmem.MemEnvelope, payload any) error {
	return errors.New("fake publish failure")
}

func serverWithDeps(deps et.Deps) http.Handler {
	return et.NewRouter(deps)
}

func defaultDeps() et.Deps {
	return et.Deps{
		PeerReviewRepo:   expertinmem.NewPeerReviewRepository(),
		AnchorRepo:       expertinmem.NewAnchorRepository(),
		QualityScoreRepo: expertinmem.NewQualityScoreRepository(),
		Publisher:        expertinmem.NewMemRecorder(),
	}
}

func rawAuthedReq(method, path, rawBody string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(rawBody))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etGcidA)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// Malformed body → 400 on every mutating endpoint
// -----------------------------------------------------------------------------

func TestExpertTools_MalformedBodyReturns400(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/v1/expert-tools/peer-review/queue"},
		{http.MethodPost, "/v1/expert-tools/peer-review/some-id/vote"},
		{http.MethodPost, "/v1/expert-tools/golden-set/anchors"},
		{http.MethodPost, "/v1/expert-tools/golden-set/compare"},
		{http.MethodPost, "/v1/expert-tools/quality-score/compute"},
		{http.MethodPost, "/v1/expert-tools/version-diff/compute"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, rawAuthedReq(c.method, c.path, `{"broken":`))
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %s malformed: status = %d; want 400", c.path, w.Code)
		}
	}
}

// -----------------------------------------------------------------------------
// Repository errors → 500
// -----------------------------------------------------------------------------

func TestPeerReview_SubmitRepoSaveErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.PeerReviewRepo = failingPeerReviewRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestPeerReview_QueueListRepoErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.PeerReviewRepo = failingPeerReviewRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/expert-tools/peer-review/queue", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestPeerReview_VoteRepoGetErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.PeerReviewRepo = failingPeerReviewRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, rawAuthedReq(http.MethodPost,
		"/v1/expert-tools/peer-review/some-id/vote", `{"decision":"approve"}`))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestPeerReview_VoteRepoSaveErrorReturns500(t *testing.T) {
	t.Parallel()
	inner := expertinmem.NewPeerReviewRepository()
	deps := defaultDeps()
	deps.PeerReviewRepo = failingSavePeerReviewRepo{inner: inner}
	srv := serverWithDeps(deps)

	// Seed a review directly through the inner repo.
	sub, err := pr.Submit(pr.SubmitParams{
		TenantID: etTenantA, AtomID: etAtomA, RevisionID: etRevA,
		AuthoredBy: etGcidA, QuorumSize: 2,
	})
	if err != nil {
		t.Fatalf("seed submit: %v", err)
	}
	if err := inner.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/expert-tools/peer-review/"+sub.ReviewID+"/vote",
		bytes.NewBufferString(`{"decision":"approve"}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etReviewer)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d body=%s; want 500", w.Code, w.Body.String())
	}
}

func TestPeerReview_VoteRejectsInvalidDecision(t *testing.T) {
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
		bytes.NewBufferString(`{"decision":"maybe"}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("gcid", etReviewer)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w2, r)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w2.Code)
	}
}

func TestPeerReview_VoteWrongMethodReturns405(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/expert-tools/peer-review/some-id/vote", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

func TestGoldenSet_CreateAnchorRepoSaveErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.AnchorRepo = failingAnchorRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{
			"atom_id": etAtomA, "title": "t", "body": "b", "quality_score": 0.9,
		}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestGoldenSet_CompareRepoGetErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.AnchorRepo = failingAnchorRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/compare",
		map[string]any{
			"anchor_id":            "01970000-0000-7000-9999-000000000099",
			"compared_atom_id":     etAtomB,
			"compared_revision_id": etRevA,
			"compared_body":        "x",
		}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestGoldenSet_CompareRejectsMissingComparedFields(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)

	w0 := httptest.NewRecorder()
	srv.ServeHTTP(w0, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{
			"atom_id": etAtomA, "title": "t", "body": "b", "quality_score": 0.9,
		}))
	var anchor map[string]any
	_ = json.Unmarshal(w0.Body.Bytes(), &anchor)
	anchorID := anchor["anchor_id"].(string)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/compare",
		map[string]any{"anchor_id": anchorID}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

func TestQualityScore_ComputeRepoAppendErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.QualityScoreRepo = failingQualityScoreRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/quality-score/compute",
		map[string]any{
			"atom_id": etAtomA, "revision_id": etRevA,
			"clarity": 0.8, "pedagogical_soundness": 0.8, "fairness": 0.8, "accessibility": 0.8,
		}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestQualityScore_LatestRepoErrorReturns500(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.QualityScoreRepo = failingQualityScoreRepo{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/expert-tools/quality-score/"+etAtomA, nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", w.Code)
	}
}

func TestQualityScore_ItemUnknownSubresourceReturns404(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/expert-tools/quality-score/"+etAtomA+"/extra", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

func TestQualityScore_ItemWrongMethodReturns405(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodDelete, "/v1/expert-tools/quality-score/"+etAtomA, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Nil publisher — mutating endpoints still succeed, publish helpers no-op
// -----------------------------------------------------------------------------

func TestExpertTools_NilPublisherStillSucceeds(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.Publisher = nil
	srv := serverWithDeps(deps)

	// Submit + two votes (quorum) — exercises submitted/vote/quorum helpers.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d", w.Code)
	}
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
			t.Fatalf("vote: %d body=%s", w2.Code, w2.Body.String())
		}
	}

	// Anchor + compare — exercises anchor_created + compared helpers.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/anchors",
		map[string]any{
			"atom_id": etAtomA, "title": "t", "body": "b",
			"tags": []string{"x"}, "quality_score": 0.9,
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("anchor: %d", w.Code)
	}
	var anchor map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &anchor)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/golden-set/compare",
		map[string]any{
			"anchor_id":            anchor["anchor_id"].(string),
			"compared_atom_id":     etAtomB,
			"compared_revision_id": etRevA,
			"compared_body":        "b",
			"compared_tags":        []string{"x"},
		}))
	if w.Code != http.StatusOK {
		t.Fatalf("compare: %d", w.Code)
	}

	// Quality score — exercises computed helper.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/quality-score/compute",
		map[string]any{
			"atom_id": etAtomA, "revision_id": etRevA,
			"clarity": 0.8, "pedagogical_soundness": 0.8, "fairness": 0.8, "accessibility": 0.8,
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("quality: %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Publish failure is non-fatal (logged, request still succeeds)
// -----------------------------------------------------------------------------

func TestExpertTools_PublishFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	deps := defaultDeps()
	deps.Publisher = failingPublisher{}
	srv := serverWithDeps(deps)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		map[string]any{"atom_id": etAtomA, "revision_id": etRevA, "quorum_size": 2}))
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d; want 201 despite publish failure", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Header alias: X-Chora-GCID is accepted in place of gcid
// -----------------------------------------------------------------------------

func TestExpertTools_GCIDHeaderAliasAccepted(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		bytes.NewBufferString(`{"atom_id":"`+etAtomA+`","revision_id":"`+etRevA+`","quorum_size":2}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("X-Chora-GCID", etGcidA)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
}

func TestExpertTools_MissingGCIDReturns400(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/expert-tools/peer-review/queue",
		bytes.NewBufferString(`{"atom_id":"`+etAtomA+`","revision_id":"`+etRevA+`","quorum_size":2}`))
	r.Header.Set("X-Tenant-Id", etTenantA)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}
