package httpadapter_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
)

const variantAtomID = "01970000-0000-7000-bbbb-000000000001"

func newVariantServer(t *testing.T) (http.Handler, *inmem.EventRecorder) {
	t.Helper()
	repo := inmem.NewVariantRepository()
	rec := inmem.NewEventRecorder()
	mux := httpadapter.NewAtomVariantsRouter(repo, rec)
	return mux, rec
}

func authedVariantReq(method, path string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// Flashcard
// -----------------------------------------------------------------------------

func TestFlashcardVariant_Returns201AndPublishesEvent(t *testing.T) {
	t.Parallel()

	srv, rec := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:flashcard",
		map[string]any{
			"front": "What is photosynthesis?",
			"back":  "Plants converting light into chemical energy.",
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["type"] != "flashcard" {
		t.Errorf("type = %v; want flashcard", got["type"])
	}
	if got["atom_id"] != variantAtomID {
		t.Errorf("atom_id roundtrip mismatch: %v", got["atom_id"])
	}
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	if !strings.HasPrefix(events[0].Topic, "chora.creation.atom.variant.published.v1") {
		t.Errorf("topic = %q; want chora.creation.atom.variant.published.v1", events[0].Topic)
	}
	if events[0].Envelope.SchemaVersion != 1 || events[0].Envelope.SourceProject != "chora-content" {
		t.Errorf("envelope mandatory fields incorrect: %+v", events[0].Envelope)
	}
}

func TestFlashcardVariant_RejectsMissingFront(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:flashcard",
		map[string]any{"back": "A"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Code editor
// -----------------------------------------------------------------------------

func TestCodeEditorVariant_HappyPath(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:code-editor",
		map[string]any{
			"language":     "go",
			"starter_code": "package main\n",
			"test_cases": []map[string]any{
				{"name": "smoke", "input": "1", "expected_output": "1"},
			},
			"solution": "package main // sol",
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
}

func TestCodeEditorVariant_RejectsBadLanguage(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:code-editor",
		map[string]any{
			"language":     "perl",
			"starter_code": "x",
			"test_cases":   []map[string]any{{"name": "t", "input": "1", "expected_output": "1"}},
			"solution":     "x",
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Drag-drop matching
// -----------------------------------------------------------------------------

func TestDragDropVariant_HappyPath(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:drag-drop-matching",
		map[string]any{
			"left_items":  []map[string]any{{"id": "L1", "label": "Cat"}, {"id": "L2", "label": "Dog"}},
			"right_items": []map[string]any{{"id": "R1", "label": "Meow"}, {"id": "R2", "label": "Bark"}},
			"mappings":    []map[string]any{{"left_id": "L1", "right_id": "R1"}, {"left_id": "L2", "right_id": "R2"}},
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
}

func TestDragDropVariant_RejectsMissingMappings(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:drag-drop-matching",
		map[string]any{
			"left_items":  []map[string]any{{"id": "L1", "label": "Cat"}},
			"right_items": []map[string]any{{"id": "R1", "label": "Meow"}},
			"mappings":    []map[string]any{},
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Multimedia
// -----------------------------------------------------------------------------

func TestMultimediaVariant_HappyPath(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:multimedia",
		map[string]any{
			"video_url":      "https://cdn.chora.site/v/abc.mp4",
			"question":       "Capital of France?",
			"correct_answer": "Paris",
			"thumbnail_url":  "https://cdn.chora.site/v/abc.jpg",
		}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
}

func TestMultimediaVariant_RejectsHTTPVideo(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:multimedia",
		map[string]any{
			"video_url":      "http://insecure.test/v.mp4",
			"question":       "Q?",
			"correct_answer": "A",
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Header enforcement (shared)
// -----------------------------------------------------------------------------

func TestVariantRoutes_RejectMissingHeaders(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/atoms/"+variantAtomID+"/variant:flashcard",
		bytes.NewBufferString(`{"front":"Q","back":"A"}`))
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 when headers missing", w.Code)
	}
}

func TestVariantRoutes_RejectsUnknownVariant(t *testing.T) {
	t.Parallel()

	srv, _ := newVariantServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedVariantReq(http.MethodPost,
		"/atoms/"+variantAtomID+"/variant:essay-typed",
		map[string]any{}))
	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 404 or 400 for unknown variant", w.Code)
	}
}
