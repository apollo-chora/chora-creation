// reuse_visibility_handler_test.go — ADR-229 WS-1 (CHO-2127):
// PATCH /api/atoms/{atom_id}/reuse-visibility.
//
// RED before patchAtomReuseVisibility lands. Contract under test:
//   - author changes the audience -> 200 + persisted + exactly ONE
//     chora.creation.atom.reuse_visibility_changed.v1 outbox emit carrying
//     new + previous audience.
//   - same-value change -> 200 idempotent no-op, NO event (the domain
//     reports changed=false).
//   - non-author -> 403 CREATION_NOT_AUTHOR (the first author-authz in
//     creation), NO event, flag untouched.
//   - invalid audience -> 400; unknown atom -> 404; non-PATCH -> 405.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// gcidIntruder is a distinct caller for the non-author 403 case.
const gcidIntruder = "01970000-0000-7000-9000-00000000beef"

// reqWithGcid mirrors authedReq but stamps an arbitrary caller gcid.
func reqWithGcid(method, path, gcid string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcid)
	r.Header.Set("Content-Type", "application/json")
	// CHO-2254: every caller of this builder is an AUTHORING request, so it
	// carries the author role on the canonical mesh header the gateway
	// populates. The /questions subtree now fails closed without it.
	r.Header.Set(servicemesh.HeaderUserRoles, "author,learner")
	return r
}

// seedReuseAtom persists a draft atom authored by gcidA and returns it + a
// router wired with a recording publisher.
func seedReuseAtom(t *testing.T) (*atom.LearningAtom, http.Handler, *recordingPub) {
	t.Helper()
	repo := inmem.NewAtomRepository()
	pub := &recordingPub{}
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "reuse endpoint atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               repo,
		AtomEventPublisher: pub,
	})
	return a, srv, pub
}

// TestPatchReuseVisibility_AuthorChange_EmitsChangedEvent — the happy path.
func TestPatchReuseVisibility_AuthorChange_EmitsChangedEvent(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID+"/reuse-visibility",
		map[string]any{"reuse_visibility": "tenant"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if got, _ := resp["reuse_visibility"].(string); got != "tenant" {
		t.Errorf("response reuse_visibility = %q; want tenant", got)
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1 reuse_visibility_changed emit", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomReuseVisibilityChanged {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomReuseVisibilityChanged)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("event atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("event tenant_id = %q; want %q", ev.TenantID, tenantA)
	}
	if ev.Gcid != gcidA {
		t.Errorf("event gcid = %q; want author %q", ev.Gcid, gcidA)
	}
	if ev.ReuseVisibility != "tenant" {
		t.Errorf("event reuse_visibility = %q; want tenant", ev.ReuseVisibility)
	}
	if ev.PreviousReuseVisibility != "private" {
		t.Errorf("event previous_visibility = %q; want private (the default)", ev.PreviousReuseVisibility)
	}
}

// TestPatchReuseVisibility_SameValue_NoEvent — an idempotent no-op emits
// nothing (the projection never sees a phantom change).
func TestPatchReuseVisibility_SameValue_NoEvent(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID+"/reuse-visibility",
		map[string]any{"reuse_visibility": "private"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200 (idempotent no-op)", w.Code, w.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0 for a same-value change", len(pub.events))
	}
}

// TestPatchReuseVisibility_NonAuthor_403 — the first author-authz in creation:
// a different caller gcid is refused and nothing mutates or emits.
func TestPatchReuseVisibility_NonAuthor_403(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, reqWithGcid(http.MethodPatch, "/api/atoms/"+a.AtomID+"/reuse-visibility",
		gcidIntruder, map[string]any{"reuse_visibility": "tenant"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s; want 403 for non-author", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if code, _ := resp["code"].(string); code != "CREATION_NOT_AUTHOR" {
		t.Errorf("error code = %q; want CREATION_NOT_AUTHOR", code)
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0 on refusal", len(pub.events))
	}

	// Flag untouched — the author still sees private.
	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if wGet.Code != http.StatusOK {
		t.Fatalf("get after refusal: %d %s", wGet.Code, wGet.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(wGet.Body.Bytes(), &got)
	if v, _ := got["reuse_visibility"].(string); v != "private" {
		t.Errorf("reuse_visibility after refused change = %q; want private", v)
	}
}

// TestPatchReuseVisibility_InvalidValue_400 — an unknown audience label is a
// 400 (the domain validates before any author check mutates state).
func TestPatchReuseVisibility_InvalidValue_400(t *testing.T) {
	t.Parallel()
	a, srv, pub := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID+"/reuse-visibility",
		map[string]any{"reuse_visibility": "everyone"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400 for invalid audience", w.Code, w.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0", len(pub.events))
	}
}

// TestPatchReuseVisibility_UnknownAtom_404.
func TestPatchReuseVisibility_UnknownAtom_404(t *testing.T) {
	t.Parallel()
	_, srv, _ := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch,
		"/api/atoms/01970000-dead-7000-8000-000000000404/reuse-visibility",
		map[string]any{"reuse_visibility": "tenant"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s; want 404", w.Code, w.Body.String())
	}
}

// TestPatchReuseVisibility_MethodNotAllowed — PATCH is the only verb.
func TestPatchReuseVisibility_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	a, srv, _ := seedReuseAtom(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID+"/reuse-visibility", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s; want 405", w.Code, w.Body.String())
	}
}
