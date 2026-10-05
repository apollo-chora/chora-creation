// atom_owner_gate_test.go — CHO-2264 (SECURITY): the atom MUTATION doors are
// author-owned.
//
// Sibling of CHO-2254 (which role-gated the /questions authoring subtree). This
// is the other half of the same class: patchAtom / deleteAtom / publishAtom were
// gated by TENANT ALONE, so any learner-role caller sharing a tenant could edit,
// archive (cascading to the attached question) or force-publish ANY atom.
//
// deleteAtom is the sharpest case: it DID read the caller gcid, but only to
// stamp the archived event — the value was never compared to a.Gcid. Attribution
// masquerading as authorization.
//
// The gate is OWNERSHIP, not role, and follows two ratified precedents in this
// same service rather than inventing a posture:
//
//   - atom_media_handler.go:154 — `if a.Gcid != gcid` -> 403
//     CREATION_ATOM_NOT_AUTHOR ("Phase 1 strict author check")
//   - reuse_visibility_handler.go — 403 CREATION_NOT_AUTHOR, whose doc states it
//     outright: "Any other caller (including tenant admins) receives 403".
//
// A role gate alone would NOT fix this: it would still let author A destroy
// author B's work. Role elevation for instructor/admin is an explicit deferred
// TODO (atom_media_handler.go:29-32) — when it lands it must land on all these
// doors at once.
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

// assertNotAuthor403 checks the canonical refusal envelope and that nothing was
// emitted — a refused mutation must leave no trace downstream.
func assertNotAuthor403(t *testing.T, w *httptest.ResponseRecorder, pub *recordingPub) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("a non-author mutation must be 403; got %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if code, _ := resp["code"].(string); code != "CREATION_ATOM_NOT_AUTHOR" {
		t.Errorf("error code = %q; want CREATION_ATOM_NOT_AUTHOR (the atom_media precedent)", code)
	}
	if len(pub.events) != 0 {
		t.Fatalf("a refused mutation emitted %d event(s); want 0", len(pub.events))
	}
}

// A non-author must not be able to ARCHIVE someone else's atom. This is the
// worst of the three: SoftDelete cascades to the attached question, so a single
// call destroys the atom AND its assessment.
func TestDeleteAtom_NonAuthor_Forbidden_NoArchive_NoEvent(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, reqWithGcid(http.MethodDelete, "/api/atoms/"+a.AtomID, gcidIntruder, nil))
	assertNotAuthor403(t, w, pub)

	// The atom must still be there for its author — a refused delete that
	// "succeeded" quietly would be undetectable from the response alone.
	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if wGet.Code != http.StatusOK {
		t.Fatalf("the atom must survive a refused delete; author GET got %d", wGet.Code)
	}
}

func TestPatchAtom_NonAuthor_Forbidden_NoMutation_NoEvent(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, reqWithGcid(http.MethodPatch, "/api/atoms/"+a.AtomID, gcidIntruder,
		map[string]any{"title": "rewritten by someone else", "stem": "s"}))
	assertNotAuthor403(t, w, pub)

	// Title untouched.
	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	var got map[string]any
	_ = json.Unmarshal(wGet.Body.Bytes(), &got)
	if title, _ := got["title"].(string); title == "rewritten by someone else" {
		t.Fatalf("a refused patch mutated the atom: title=%q", title)
	}
}

func TestPublishAtom_NonAuthor_Forbidden_StaysDraft_NoEvent(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, reqWithGcid(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", gcidIntruder, nil))
	assertNotAuthor403(t, w, pub)

	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	var got map[string]any
	_ = json.Unmarshal(wGet.Body.Bytes(), &got)
	if st, _ := got["status"].(string); st == "published" {
		t.Fatalf("a refused publish flipped the atom to published")
	}
}

// The author is unaffected — the gate must not cost an authoring regression.
func TestAtomMutation_Author_StillAllowed(t *testing.T) {
	t.Parallel()
	a, srv, _ := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID,
		map[string]any{"title": "renamed by its author", "stem": "s"}))
	if w.Code != http.StatusOK {
		t.Fatalf("the author must still patch their own atom; got %d %s", w.Code, w.Body.String())
	}

	wDel := httptest.NewRecorder()
	srv.ServeHTTP(wDel, authedReq(http.MethodDelete, "/api/atoms/"+a.AtomID, nil))
	if wDel.Code != http.StatusOK && wDel.Code != http.StatusNoContent {
		t.Fatalf("the author must still delete their own atom; got %d %s", wDel.Code, wDel.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Phyllis /v1/ router parity (CHO-2264).
//
// The same capabilities live on BOTH routers. Before this story the /v1/ twins
// were open while their legacy counterparts were gated — POST /api/atoms/{id}/media
// enforced `a.Gcid != gcid` (atom_media_handler.go:154) while POST
// /v1/atoms/{id}/media did not even bind the atom it loaded (`if _, err := ...`).
// One locked door is worth nothing when the second is open, so the gate must be
// asserted on both.
// -----------------------------------------------------------------------------

// phyllisReqAs builds a /v1/ request as an arbitrary caller gcid.
func phyllisReqAs(method, path, gcid, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcid)
	return r
}

func TestPhyllisPatchAtom_NonAuthor_Forbidden(t *testing.T) {
	t.Parallel()
	pub := &recordingPub{}
	srv, repo := newPhyllisServer(t, nil, pub)

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "phyllis atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := len(pub.events)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, phyllisReqAs(http.MethodPatch, "/v1/atoms/"+a.AtomID, gcidIntruder,
		`{"body":"revision appended by someone else"}`))

	if w.Code != http.StatusForbidden {
		t.Fatalf("a non-author appending a revision on /v1/ must be 403; got %d %s", w.Code, w.Body.String())
	}
	if len(pub.events) != before {
		t.Fatalf("a refused /v1/ patch emitted an event")
	}

	// POSITIVE CONTROL — the author on the SAME route must NOT be 403. Without
	// this, a 403 from any unrelated cause (bad route, bad fixture) would make
	// the assertion above pass vacuously and "prove" a gate that isn't there.
	wOK := httptest.NewRecorder()
	srv.ServeHTTP(wOK, phyllisReqAs(http.MethodPatch, "/v1/atoms/"+a.AtomID, gcidA,
		`{"body":"revision appended by its author"}`))
	if wOK.Code == http.StatusForbidden {
		t.Fatalf("the AUTHOR was also 403 on /v1/ — this test is not exercising the gate: %s", wOK.Body.String())
	}
}
