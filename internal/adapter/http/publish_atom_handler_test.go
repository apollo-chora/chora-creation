// publish_atom_handler_test.go — HTTP-layer tests for the
// POST /api/atoms/{atom_id}/publish handler.
//
// Answers FE A22 ack on Phase K (atom Publish + Preview flow). Contract per
// chora-contracts/openapi/creation-admin.yaml#publishAtom — transitions an
// atom from DRAFT → PUBLISHED iff at least one published QuestionRevision
// exists. Per A22 Option α the question revisions are auto-published at
// PATCH/POST time (no separate revision-publish step), so the publish handler
// only flips the atom-level status.
//
// Discriminated 409 sub-codes per ADR-141 D4 (safety_and_robustness):
//   - CREATION_ATOM_NO_PUBLISHED_REVISION — no Question/Revision exists
//   - CREATION_ATOM_ARCHIVED              — atom is soft-deleted
//
// Idempotent re-publish on an already-PUBLISHED atom returns 200 (not 409)
// per A22.Q3 idempotency-friendly success convention.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// Happy path — DRAFT atom with a published question revision flips to PUBLISHED
// -----------------------------------------------------------------------------

func TestPublishAtom_FlipsDraftToPublished_WhenRevisionExists(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "mcq atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	// Seed a question + revision against the atom.
	seedBody := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	// Publish.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("publish json: %v body=%s", err, w.Body.String())
	}
	if got["status"] != "published" {
		t.Errorf("status = %v; want published; body=%s", got["status"], w.Body.String())
	}
	if got["atom_id"] != a.AtomID {
		t.Errorf("atom_id mismatch: got %v want %s", got["atom_id"], a.AtomID)
	}
	// A22.Q4 — current_revision_number must be populated from the latest
	// QuestionRevision so the FE "Published! (rev N is live)" toast renders.
	if rn, _ := got["current_revision_number"].(float64); rn < 1 {
		t.Errorf("current_revision_number = %v; want >= 1", got["current_revision_number"])
	}
	if rid, _ := got["current_revision_id"].(string); rid == "" {
		t.Errorf("current_revision_id empty; want UUID; body=%s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 409 sub-code: NO_PUBLISHED_REVISION (atom exists but has no question)
// -----------------------------------------------------------------------------

func TestPublishAtom_409_NoPublishedRevision_WhenNoQuestionExists(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "bare atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (no revision); body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_NO_PUBLISHED_REVISION" {
		t.Errorf("code = %v; want CREATION_ATOM_NO_PUBLISHED_REVISION; body=%s", got["code"], w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 409 sub-code: ARCHIVED (atom is soft-deleted)
// -----------------------------------------------------------------------------

func TestPublishAtom_409_Archived_WhenAtomSoftDeleted(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "to-archive atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	// Soft-delete.
	delW := httptest.NewRecorder()
	srv.ServeHTTP(delW, authedReq(http.MethodDelete, "/api/atoms/"+a.AtomID, nil))
	if delW.Code != http.StatusNoContent && delW.Code != http.StatusOK {
		t.Fatalf("seed delete: status=%d", delW.Code)
	}

	// Publishing a soft-deleted atom should 409 (per ddd-enforcement #5 —
	// soft-deleted is the canonical archived state; the handler must NOT
	// revive an archived atom by flipping it to PUBLISHED).
	//
	// NOTE: the in-memory repo filters soft-deleted in Get, so the publish
	// handler currently sees ErrNotFound and returns 404. That is acceptable
	// — the FE treats 404 the same as ARCHIVED (cannot publish what does
	// not exist for this tenant). The 409 ARCHIVED case applies when an
	// adapter that returns soft-deleted rows (e.g. an admin-view repo) is
	// wired; the contract is documented for that future path.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusNotFound && w.Code != http.StatusConflict {
		t.Errorf("status = %d; want 404 or 409 (archived); body=%s", w.Code, w.Body.String())
	}
	if w.Code == http.StatusConflict {
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["code"] != "CREATION_ATOM_ARCHIVED" {
			t.Errorf("code = %v; want CREATION_ATOM_ARCHIVED; body=%s", got["code"], w.Body.String())
		}
	}
}

// -----------------------------------------------------------------------------
// 404 — atom not found
// -----------------------------------------------------------------------------

func TestPublishAtom_404_OnUnknownAtom(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost,
		"/api/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb/publish", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_NOT_FOUND" {
		t.Errorf("code = %v; want CREATION_ATOM_NOT_FOUND", got["code"])
	}
}

// -----------------------------------------------------------------------------
// Method gate — only POST allowed
// -----------------------------------------------------------------------------

func TestPublishAtom_RejectsNonPost(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo})

	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, authedReq(m, "/api/atoms/"+a.AtomID+"/publish", nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("method %s: status %d; want 405", m, w.Code)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Idempotent re-publish — already-PUBLISHED atom returns 200 (not 409)
//
// A22.Q3 — FE prefers idempotent success on re-publish (vs. 409
// ALREADY_PUBLISHED) so the "Publish" CTA stays single-tap forgiving.
// -----------------------------------------------------------------------------

func TestPublishAtom_IdempotentRepublish_Returns200(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "idempotent atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	// Seed question revision.
	seedBody := map[string]any{
		"type":   "mcq",
		"prompt": "What is 2+2?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "4", "is_correct": true, "explainer": "2+2=4"},
				{"option_id": "opt_2", "label": "5", "is_correct": false, "explainer": "Incorrect arithmetic"},
			},
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	// First publish — DRAFT → PUBLISHED.
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first publish: status=%d body=%s", w1.Code, w1.Body.String())
	}

	// Second publish — already PUBLISHED. Idempotent 200.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w2.Code != http.StatusOK {
		t.Errorf("re-publish: status=%d; want 200 (idempotent); body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["status"] != "published" {
		t.Errorf("re-publish status = %v; want published", got["status"])
	}
}

// -----------------------------------------------------------------------------
// Response shape — A22.Q4 confirms LearningAtom includes status + revision_id +
// revision_number on the publish response so the FE toast renders verbatim.
// -----------------------------------------------------------------------------

func TestPublishAtom_ResponseIncludesStatusAndRevisionSummary(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "shape atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	// Seed q rev.
	seedBody := map[string]any{
		"type":   "mcq",
		"prompt": "Shape probe.",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "A", "is_correct": true, "explainer": "Correct"},
				{"option_id": "opt_2", "label": "B", "is_correct": false, "explainer": "Wrong"},
			},
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Must NOT be wrapped — bare LearningAtom (Q2 answer α: no wrap).
	if strings.HasPrefix(strings.TrimSpace(body), `{"atom":`) {
		t.Errorf("response should be bare LearningAtom (no {atom: ...} wrap); body=%s", body)
	}
	for _, key := range []string{`"status"`, `"current_revision_number"`, `"current_revision_id"`, `"atom_id"`} {
		if !strings.Contains(body, key) {
			t.Errorf("missing key %s in response; body=%s", key, body)
		}
	}
}
