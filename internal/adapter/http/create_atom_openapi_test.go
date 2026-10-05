// create_atom_openapi_test.go — A20 fix tests for the
// POST /api/atoms handler accepting the OpenAPI-canonical body shape.
//
// Per FE A20 ask (2026-05-16): FE sends
//
//	{"atom_type":"MULTIPLE_CHOICE","title":"Untitled Atom","locale":"en"}
//
// per chora-contracts/openapi/creation-admin.yaml#createAtom. The handler
// must accept this shape (in addition to the legacy {title,body,tags,mode}
// skeleton shape used by handler_test.go::TestCreateAtom_Returns201AndAtom)
// and round-trip atom_type onto the LearningAtom envelope so the FE can
// reuse the returned id for the Phase I AI-assist mint flow.
//
// ADR-156 Phase 1 (2026-05-17): the LearningAtom envelope's `atom_type`
// JSON tag is renamed to `question_type` (Decision #2). Tests assert the
// rename; the legacy `atom_type` REQUEST field stays accepted for one
// release cycle per plan §8 backwards-compat shim. `stem` is now REQUIRED
// on POST per ADR-156 Decision #1 structural change — legacy tests
// extended to send a stem so they continue to pass.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FE-canonical body shape per A20. atom_type is the OpenAPI enum
// "MULTIPLE_CHOICE"; chora-creation maps it to its internal "mcq" type.
func TestCreateAtom_Accepts_OpenAPICanonicalShape(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	body := map[string]any{
		"atom_type": "MULTIPLE_CHOICE",
		"title":     "Untitled Atom",
		"locale":    "en",
		// ADR-156 Decision #1 — stem now required.
		"stem": "Placeholder stem",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got["atom_id"] == nil || got["atom_id"] == "" {
		t.Errorf("atom_id missing; body=%s", w.Body.String())
	}
	if got["status"] != "draft" {
		t.Errorf("status = %v; want draft", got["status"])
	}
	if got["title"] != "Untitled Atom" {
		t.Errorf("title = %v; want Untitled Atom", got["title"])
	}
	// question_type round-trip — FE sends atom_type=MULTIPLE_CHOICE
	// (deprecated synonym); BE accepts it + persists as question_type=mcq
	// per ADR-156 Decision #2.
	if got["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq (mapped from MULTIPLE_CHOICE)", got["question_type"])
	}
}

// Empty title is now ACCEPTABLE per ADR-156 Decision #1 (title relaxed to
// optional). FE auto-derives a display_label from stem when title is empty.
// Stem-required is now the canonical gate; see TestCreateAtom_StemRequired
// in handler_test.go for the new invariant.
func TestCreateAtom_RejectsEmptyTitle_WithOpenAPIShape(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"atom_type": "MULTIPLE_CHOICE",
		"title":     "",
		"locale":    "en",
		"stem":      "Stem stands in for title",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d; want 201 (title is now optional per ADR-156 Decision #1)", w.Code)
	}
}

// Legacy {title,body,tags,mode} shape must continue to work for the existing
// skeleton tests + Phyllis path. Phase 1 requires stem — added here so the
// legacy shape can coexist with the new invariant.
func TestCreateAtom_BackwardCompat_LegacyShape(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"title": "Legacy",
		"body":  "Legacy body",
		"tags":  []string{"a"},
		"mode":  "straight-up",
		"stem":  "Legacy stem",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["title"] != "Legacy" {
		t.Errorf("title = %v; want Legacy", got["title"])
	}
	// Legacy shape doesn't supply question_type/atom_type — should remain
	// empty in the response envelope.
	if qt, present := got["question_type"]; present && qt != "" && qt != nil {
		t.Errorf("question_type = %v; want empty (legacy shape)", got["question_type"])
	}
}

// Mixed legacy + OpenAPI fields — both should be honoured.
func TestCreateAtom_AcceptsMixedFields(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"atom_type":  "ESSAY",
		"title":      "Mixed atom",
		"body":       "Explanatory body",
		"locale":     "en-SG",
		"difficulty": 3,
		"stem":       "Explain story points.",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	// question_type round-trip — FE sends atom_type=ESSAY; BE persists as
	// question_type=essay per ADR-156 Decision #2.
	if got["question_type"] != "essay" {
		t.Errorf("question_type = %v; want essay (mapped from ESSAY)", got["question_type"])
	}
	if difficulty, _ := got["difficulty"].(float64); difficulty != 3 {
		t.Errorf("difficulty = %v; want 3", got["difficulty"])
	}
}
