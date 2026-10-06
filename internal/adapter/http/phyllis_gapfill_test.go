// Phyllis MVP gap-fill HTTP tests per docs/m13/audit-content-fillgaps.md §3.1.
//
// Covers:
//   - PATCH /v1/atoms/{id} → AppendRevision (gap: route was missing)
//   - POST /v1/atoms/batch → alias of /v1/atoms:generate (gap: contract said
//     /batch, code shipped :generate)
//   - POST /v1/atoms/{id}/media → MediaAsset upload (gap: aggregate absent)
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// PATCH /v1/atoms/{id} → AppendRevision
// -----------------------------------------------------------------------------

func TestPhyllis_PatchAtom_AppendsRevisionWithoutPublishingEvent(t *testing.T) {
	t.Parallel()

	pub := &recordingPub{}
	srv, repo := newPhyllisServer(t, nil, pub)

	// Seed: create an atom.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id":  courseA,
		"title":      "What is Agile?",
		"body":       "v1 body",
		"type":       "mcq",
		"difficulty": 3,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create failed: %d", w.Code)
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// PATCH appends a new revision.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPatch, "/v1/atoms/"+atomID, map[string]any{
		"body":        "v2 body — author edit",
		"source_type": "manual",
	}))
	if w2.Code != http.StatusOK {
		t.Fatalf("PATCH /v1/atoms/{id} status = %d body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	cur, ok := got["current_revision"].(map[string]any)
	if !ok {
		t.Fatalf("current_revision missing")
	}
	if cur["body"] != "v2 body — author edit" {
		t.Errorf("current_revision.body = %v; want v2 body", cur["body"])
	}
	if int(cur["revision_number"].(float64)) != 2 {
		t.Errorf("revision_number = %v; want 2", cur["revision_number"])
	}

	// Repo state: still 2 revisions.
	a, err := repo.Get(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if len(a.RevisionHistory()) != 2 {
		t.Errorf("revision history len = %d; want 2", len(a.RevisionHistory()))
	}

	// Event: the append emits NOTHING. chora.creation.atom.revised.v1 has no
	// contract and no binary encoder, so publishing it would JSON-fall-back
	// and be rejected at the schema registry — and no domain consumes it. The
	// only event in the recorder is the initial atom.created.v1.
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomRevised {
			t.Errorf("PATCH emitted atom.revised.v1 (%+v); it has no contract/encoder/consumer and must not be published", e)
		}
	}
}

func TestPhyllis_PatchAtom_404OnUnknown(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch,
		"/v1/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb",
		map[string]any{"body": "x", "source_type": "manual"}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

func TestPhyllis_PatchAtom_400OnEmptyBody(t *testing.T) {
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
	srv.ServeHTTP(w2, authedReq(http.MethodPatch, "/v1/atoms/"+id, map[string]any{
		"body":        "",
		"source_type": "manual",
	}))
	if w2.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (empty body rejected)", w2.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /v1/atoms/batch — alias of /v1/atoms:generate per spec §5.3
// -----------------------------------------------------------------------------

func TestPhyllis_PostBatchAlias_BehavesIdenticallyToGenerate(t *testing.T) {
	t.Parallel()

	items := make([]atom.GeneratedItem, 3)
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

	// Hit /batch — must succeed exactly like :generate.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms/batch", map[string]any{
		"course_id":    courseA,
		"prompt":       "Generate 3 MCQ on Agile",
		"content_type": "mcq",
		"difficulty":   3,
		"count":        3,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/atoms/batch status = %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		GeneratedAtomIDs  []string `json:"generated_atom_ids"`
		ScreeningDecision string   `json:"screening_decision"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ScreeningDecision != "allow" {
		t.Errorf("decision = %q; want allow", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 3 {
		t.Errorf("generated len = %d; want 3", len(resp.GeneratedAtomIDs))
	}
}

func TestPhyllis_PostBatchAlias_405OnGetMethod(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/v1/atoms/batch", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405 on GET /v1/atoms/batch", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /v1/atoms/{id}/media — MediaAsset upload (signed URL stub)
// -----------------------------------------------------------------------------

func TestPhyllis_PostAtomMedia_ReturnsSignedURL(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	// Seed: create the parent atom.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id":  courseA,
		"title":      "Video atom",
		"body":       "intro video",
		"type":       "video",
		"difficulty": 1,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create failed: %d", w.Code)
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPost, "/v1/atoms/"+atomID+"/media",
		map[string]any{
			"filename":     "intro.mp4",
			"content_type": "video/mp4",
			"size_bytes":   1024 * 1024,
		}))
	if w2.Code != http.StatusCreated {
		t.Fatalf("POST /v1/atoms/{id}/media status = %d body=%s",
			w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["asset_id"] == nil || got["asset_id"] == "" {
		t.Errorf("asset_id missing")
	}
	if got["upload_url"] == nil {
		t.Errorf("upload_url missing")
	}
	uploadURL, _ := got["upload_url"].(string)
	if !strings.HasPrefix(uploadURL, "https://") &&
		!strings.HasPrefix(uploadURL, "stub://") {
		t.Errorf("upload_url = %q; want https or stub URL", uploadURL)
	}
	if got["status"] != "pending_upload" {
		t.Errorf("status = %v; want pending_upload", got["status"])
	}
}

func TestPhyllis_PostAtomMedia_404OnUnknownAtom(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost,
		"/v1/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb/media",
		map[string]any{
			"filename":     "x.mp4",
			"content_type": "video/mp4",
			"size_bytes":   1,
		}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

func TestPhyllis_PostAtomMedia_400OnInvalidContentType(t *testing.T) {
	t.Parallel()

	srv, _ := newPhyllisServer(t, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", map[string]any{
		"course_id":  courseA,
		"title":      "x",
		"body":       "y",
		"type":       "video",
		"difficulty": 1,
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created["atom_id"].(string)

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPost, "/v1/atoms/"+id+"/media",
		map[string]any{
			"filename":     "evil.exe",
			"content_type": "application/x-msdownload",
			"size_bytes":   1,
		}))
	if w2.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (disallowed content type)", w2.Code)
	}
}
