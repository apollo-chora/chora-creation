package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

// fakeTopicRepo is a configurable topic.Repository + topic.SeedSink for handler
// tests. Presence in `nodes` drives GetByID/GetTree; the *Err fields force a
// specific domain sentinel so the error→status mapping can be asserted.
type fakeTopicRepo struct {
	nodes      map[string]*topic.TopicNode
	createErr  error
	updateErr  error
	moveErr    error
	deleteErr  error
	attachErr  error
	getTreeErr error
	existing   map[string]struct{}
}

func newFakeTopicRepo() *fakeTopicRepo {
	return &fakeTopicRepo{nodes: map[string]*topic.TopicNode{}, existing: map[string]struct{}{}}
}

func (f *fakeTopicRepo) Create(_ context.Context, n *topic.TopicNode) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.nodes[n.TopicID] = n
	return nil
}
func (f *fakeTopicRepo) Update(_ context.Context, n *topic.TopicNode) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.nodes[n.TopicID] = n
	return nil
}
func (f *fakeTopicRepo) GetByID(_ context.Context, _, id string) (*topic.TopicNode, error) {
	if n, ok := f.nodes[id]; ok {
		return n, nil
	}
	return nil, topic.ErrNotFound
}
func (f *fakeTopicRepo) GetTree(_ context.Context, _ string, _ *string) ([]*topic.TopicNode, error) {
	if f.getTreeErr != nil {
		return nil, f.getTreeErr
	}
	out := []*topic.TopicNode{}
	for _, n := range f.nodes {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeTopicRepo) Move(_ context.Context, _, _ string, _ *string) error { return f.moveErr }
func (f *fakeTopicRepo) SoftDelete(_ context.Context, _, _ string) error      { return f.deleteErr }
func (f *fakeTopicRepo) AttachAtom(_ context.Context, _, _, _ string) error   { return f.attachErr }
func (f *fakeTopicRepo) ExistingRootNames(_ context.Context, _ string) (map[string]struct{}, error) {
	return f.existing, nil
}

type fakeTagSource struct{ tags []string }

func (f fakeTagSource) DistinctAtomTags(_ context.Context, _ string) ([]string, error) {
	return f.tags, nil
}

func topicRouter(repo *fakeTopicRepo, src topic.AtomTagSource) http.Handler {
	return httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		TopicRepository: repo,
		TopicSeedSink:   repo,
		TopicTagSource:  src,
	})
}

func topicReq(method, path string, body any, admin bool) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	if admin {
		r.Header.Set("x-mesh-user-roles", "learner,admin")
	}
	return r
}

func mustTopicNode(t *testing.T, id, name string) *topic.TopicNode {
	t.Helper()
	n, err := topic.NewTopicNode(tenantA, name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n.TopicID = id
	return n
}

// -----------------------------------------------------------------------------

func TestTopicHandler_Create_Admin(t *testing.T) {
	repo := newFakeTopicRepo()
	srv := topicRouter(repo, nil)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics",
		map[string]any{"name": "Fractions", "sort_order": 1}, true))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var dto struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		SortOrder int    `json:"sort_order"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &dto)
	if dto.Name != "Fractions" || dto.ID == "" || dto.SortOrder != 1 {
		t.Errorf("dto = %+v", dto)
	}
	if len(repo.nodes) != 1 {
		t.Errorf("repo should hold 1 node, got %d", len(repo.nodes))
	}
}

func TestTopicHandler_Create_ForbiddenWithoutAdmin(t *testing.T) {
	srv := topicRouter(newFakeTopicRepo(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics", map[string]any{"name": "X"}, false))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestTopicHandler_Create_InvalidName(t *testing.T) {
	srv := topicRouter(newFakeTopicRepo(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics", map[string]any{"name": "  "}, true))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestTopicHandler_List(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "Algebra")
	srv := topicRouter(repo, nil)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodGet, "/api/topics", nil, false))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "Algebra" {
		t.Errorf("data = %+v", resp.Data)
	}
}

func TestTopicHandler_Get_NotFound(t *testing.T) {
	srv := topicRouter(newFakeTopicRepo(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodGet, "/api/topics/missing", nil, false))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestTopicHandler_Update(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "Old")
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPut, "/api/topics/a", map[string]any{"name": "New"}, true))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if repo.nodes["a"].Name != "New" {
		t.Errorf("name = %q, want New", repo.nodes["a"].Name)
	}
}

func TestTopicHandler_Delete_HasChildren409(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "Parent")
	repo.deleteErr = topic.ErrHasChildren
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodDelete, "/api/topics/a", nil, true))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestTopicHandler_Delete_NoContent(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "Leaf")
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodDelete, "/api/topics/a", nil, true))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

func TestTopicHandler_Move_Cycle409(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "A")
	repo.moveErr = topic.ErrCycle
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics/a/move",
		map[string]any{"parent_id": "b"}, true))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cycle); body=%s", w.Code, w.Body.String())
	}
}

func TestTopicHandler_Move_ToRoot(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "A")
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics/a/move",
		map[string]any{"parent_id": nil}, true))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestTopicHandler_AttachAtom(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "A")
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics/a/atoms",
		map[string]any{"atom_id": "00000000-0000-7000-8000-0000000000cc"}, true))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestTopicHandler_Backfill(t *testing.T) {
	repo := newFakeTopicRepo()
	srv := topicRouter(repo, fakeTagSource{tags: []string{"fractions", "algebra"}})
	w := httptest.NewRecorder()
	// Internal route bypasses the tenant header gate; tenant comes in the body.
	r := httptest.NewRequest(http.MethodPost, "/api/internal/topics/backfill", nil)
	body, _ := json.Marshal(map[string]any{"tenant_id": tenantA})
	r = httptest.NewRequest(http.MethodPost, "/api/internal/topics/backfill", bytes.NewReader(body))
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var rep struct {
		Created int `json:"created"`
		Scanned int `json:"scanned"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	if rep.Created != 2 || rep.Scanned != 2 {
		t.Errorf("report = %+v, want Created=2 Scanned=2", rep)
	}
	if len(repo.nodes) != 2 {
		t.Errorf("backfill created %d nodes, want 2", len(repo.nodes))
	}
}

func TestTopicHandler_AttachAtom_Invalid400(t *testing.T) {
	repo := newFakeTopicRepo()
	repo.nodes["a"] = mustTopicNode(t, "a", "A")
	repo.attachErr = topic.ErrInvalidAtomID
	srv := topicRouter(repo, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPost, "/api/topics/a/atoms",
		map[string]any{"atom_id": "  "}, true))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestTopicHandler_MethodNotAllowed(t *testing.T) {
	srv := topicRouter(newFakeTopicRepo(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodPatch, "/api/topics", nil, true))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestTopicHandler_Backfill_NotWired503(t *testing.T) {
	// TopicRepository wired but no seed sink/source ⇒ backfill 503s (fail-loud),
	// while the CRUD routes still serve.
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{TopicRepository: newFakeTopicRepo()})
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"tenant_id": tenantA})
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/internal/topics/backfill", bytes.NewReader(body)))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestTopicHandler_NotWired_404(t *testing.T) {
	// TopicRepository nil ⇒ /api/topics 404s (fail-loud).
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, topicReq(http.MethodGet, "/api/topics", nil, false))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when unwired", w.Code)
	}
}
