// orphan_frozen_guard_test.go — ADR-229 Amendment A1 (CHO-2132): every
// mutation surface refuses an orphan edition with the loud 409
// CREATION_ATOM_ORPHANED_FROZEN taxonomy. Orphans are immutable: no atom
// edits, no soft-delete, no publish, no audience change, no revision appends.
// Clone (the fork path) stays allowed and is covered in the domain tests.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// seedOrphanServer persists a published source atom + its minted orphan
// edition (both authored by gcidA) and returns the orphan + wired router +
// recording publisher.
func seedOrphanServer(t *testing.T) (*atom.LearningAtom, http.Handler, *recordingPub) {
	t.Helper()
	repo := inmem.NewAtomRepository()
	pub := &recordingPub{}

	src, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "orphan source", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	src.QuestionType = atom.AtomType("mcq")
	src.Stem = "stem"
	if err := src.Publish(); err != nil {
		t.Fatalf("publish source: %v", err)
	}
	if err := repo.Save(context.Background(), src); err != nil {
		t.Fatalf("save source: %v", err)
	}

	orphan, err := atom.CloneOrphan(atom.OrphanCloneParams{
		Source:           src,
		SourceRevisionID: "01970000-0000-7000-8000-0000000000f1",
	})
	if err != nil {
		t.Fatalf("CloneOrphan: %v", err)
	}
	if err := repo.Save(context.Background(), orphan); err != nil {
		t.Fatalf("save orphan: %v", err)
	}

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               repo,
		QuestionRepository: newFakeQRepo(),
		AtomEventPublisher: pub,
	})
	return orphan, srv, pub
}

func assertOrphanFrozen409(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != "CREATION_ATOM_ORPHANED_FROZEN" {
		t.Fatalf("error code = %q, want CREATION_ATOM_ORPHANED_FROZEN; body = %s", body.Code, rec.Body.String())
	}
}

func TestOrphanFrozen_PatchReuseVisibility_409(t *testing.T) {
	orphan, srv, pub := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPatch, "/api/atoms/"+orphan.AtomID+"/reuse-visibility", gcidA,
		map[string]string{"reuse_visibility": "tenant"}))
	assertOrphanFrozen409(t, rec)
	if len(pub.events) != 0 {
		t.Fatalf("refused mutation must emit nothing; got %d events", len(pub.events))
	}
}

func TestOrphanFrozen_PatchAtom_409(t *testing.T) {
	orphan, srv, _ := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPatch, "/api/atoms/"+orphan.AtomID, gcidA,
		map[string]string{"title": "rewritten"}))
	assertOrphanFrozen409(t, rec)
}

func TestOrphanFrozen_DeleteAtom_409(t *testing.T) {
	orphan, srv, pub := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodDelete, "/api/atoms/"+orphan.AtomID, gcidA, nil))
	assertOrphanFrozen409(t, rec)
	if len(pub.events) != 0 {
		t.Fatalf("refused delete must not emit atom.archived; got %d events", len(pub.events))
	}
}

// Publish on an orphan must refuse LOUD — never the idempotent-republish 200
// (the orphan is already status=published; a silent 200 would hide that an
// authoring flow targeted a frozen edition).
func TestOrphanFrozen_PublishAtom_409(t *testing.T) {
	orphan, srv, pub := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPost, "/api/atoms/"+orphan.AtomID+"/publish", gcidA, nil))
	assertOrphanFrozen409(t, rec)
	if len(pub.events) != 0 {
		t.Fatalf("refused publish must not emit atom.published; got %d events", len(pub.events))
	}
}

func TestOrphanFrozen_CreateQuestion_409(t *testing.T) {
	orphan, srv, _ := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPost, "/api/atoms/"+orphan.AtomID+"/questions", gcidA,
		map[string]any{
			"type":   "mcq",
			"prompt": "p?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"label": "a", "is_correct": true, "explainer": "e"},
					{"label": "b", "is_correct": false, "explainer": "e"},
				},
			},
		}))
	assertOrphanFrozen409(t, rec)
}

func TestOrphanFrozen_PatchQuestion_409(t *testing.T) {
	orphan, srv, _ := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	// The guard fires on the PARENT atom before any question lookup, so no
	// seeded question is needed — revision appends are refused wholesale.
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPatch,
		"/api/atoms/"+orphan.AtomID+"/questions/01970000-0000-7000-8000-00000000q001", gcidA,
		map[string]any{"prompt": "edited"}))
	assertOrphanFrozen409(t, rec)
}

// The source atom stays fully mutable — guard scoping check.
func TestOrphanFrozen_SourceAtomUnaffected(t *testing.T) {
	orphan, srv, _ := seedOrphanServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, reqWithGcid(http.MethodPatch, "/api/atoms/"+orphan.OrphanedFromAtomID+"/reuse-visibility", gcidA,
		map[string]string{"reuse_visibility": "tenant"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("source atom mutation status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}
