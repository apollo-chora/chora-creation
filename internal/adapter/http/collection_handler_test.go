// collection_handler_test.go — HTTP adapter coverage for the Collection
// CRUD endpoints (WS-6a, 2026-05-26; ADR-233 WS-4).
//
// Tests run end-to-end against the in-memory CollectionRepository + the
// tenant-scoped reuse-fact gate (httpFacts), so the handler's
// auth/tenant/visibility/JSON envelope wiring is exercised without a live DB.
// There is no AtomLookup stub any more — ADR-233 D7 deleted the port.
package httpadapter_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

const (
	collOwner    = "01970000-0000-7000-9000-000000000001"
	collOther    = "01970000-0000-7000-9000-000000000099"
	atomOwnedID  = "01970000-0000-7000-a000-000000000001"
	atomMissID   = "01970000-0000-7000-a000-0000000000ff"
	atomOtherTen = "01970000-0000-7000-a000-000000000ff0"
)

// recPub records every event published.
type recPub struct {
	mu     sync.Mutex
	events []collection.Event
}

func (p *recPub) Publish(_ collection.Context, e collection.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	return nil
}

// newCollServer wires the in-memory repo + the (tenant-scoped) reuse-fact gate +
// event publisher into the httpadapter under test.
//
// The atom-existence oracle is now the FACTS stub, not a separate AtomLookup —
// ADR-233 D7 deleted that port (its pg adapter read learning_atoms untenanted and
// 500'd with 22P02 under RLS). httpFacts is tenant-scoped, so it answers both
// "does this atom exist here?" and "may this actor reuse it?" — exactly as the
// production adapter does.
func newCollServer(t *testing.T) (http.Handler, *httpFacts, *recPub) {
	t.Helper()
	repo := inmem.NewCollectionRepository()
	pub := &recPub{}
	// Both known atoms are seeded as collOwner's OWN work, so they are entitled by
	// the `own` leg of the disjunct. atomOtherTen is additionally pinned to ANOTHER
	// tenant, so the tenant-scoped read cannot see it at all — that is what makes
	// the cross-tenant assertion below honest (the atom is otherwise entitled;
	// only the tenant scope refuses it).
	//
	// atomMissID is deliberately absent from `facts` entirely: it does not resolve.
	facts := &httpFacts{
		facts: map[string]reuseconsent.AtomFact{
			atomOwnedID: {
				AtomID: atomOwnedID, AuthorGCID: collOwner,
				Audience: audience.Private, Published: true,
			},
			atomOtherTen: {
				AtomID: atomOtherTen, AuthorGCID: collOwner,
				Audience: audience.Private, Published: true,
			},
		},
		tenantByID: map[string]string{
			atomOtherTen: "01970000-0000-7000-8000-000000000099",
		},
	}
	atomRepo := inmem.NewAtomRepository()
	deps := httpadapter.RouterDeps{
		Repo:                 atomRepo,
		CollectionRepository: repo,
		CollectionPublisher:  pub,
		CollectionFacts:      facts,
		CollectionConsent:    &httpConsent{},
		CollectionAuthz:      &httpAuthz{},
	}
	return httpadapter.NewRouterWithDeps(deps), facts, pub
}

func collReq(method, path string, body any, gcid string) *http.Request {
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
	return r
}

// -----------------------------------------------------------------------------
// POST /api/v1/collections — create
// -----------------------------------------------------------------------------

func TestCreateCollection_Returns201AndAtomCount0(t *testing.T) {
	t.Parallel()

	srv, _, pub := newCollServer(t)

	body := map[string]any{
		"title":       "Photosynthesis essentials",
		"description": "atoms about the Calvin cycle",
		"visibility":  "private",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections", body, collOwner))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got["title"] != "Photosynthesis essentials" {
		t.Errorf("title = %v; want match", got["title"])
	}
	if got["visibility"] != "private" {
		t.Errorf("visibility = %v; want private", got["visibility"])
	}
	if got["collection_id"] == nil || got["collection_id"] == "" {
		t.Errorf("collection_id missing in response: %v", got)
	}
	// One created.v1 event published.
	if len(pub.events) != 1 || pub.events[0].Type != collection.EventTypeCollectionCreated {
		t.Errorf("events = %+v; want 1 created.v1", pub.events)
	}
}

func TestCreateCollection_RejectsMissingTitle(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	body := map[string]any{"title": ""}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections", body, collOwner))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateCollection_RejectsMissingAuthHeaders(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	body := map[string]any{"title": "x"}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/collections",
		bytes.NewBuffer(jsonOrPanic(body)))
	r.Header.Set("Content-Type", "application/json")
	// no X-Tenant-Id, no gcid
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for missing auth headers", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/me/collections — list mine
// -----------------------------------------------------------------------------

func TestListMyCollections_ReturnsOnlyMine(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)

	// Create one for collOwner.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections",
		map[string]any{"title": "Mine"}, collOwner))
	if w.Code != http.StatusCreated {
		t.Fatalf("setup create mine: status=%d", w.Code)
	}
	// And one for collOther.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections",
		map[string]any{"title": "Theirs"}, collOther))
	if w.Code != http.StatusCreated {
		t.Fatalf("setup create theirs: status=%d", w.Code)
	}

	// List as collOwner.
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/me/collections", nil, collOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []struct {
			Title     string `json:"title"`
			OwnerGcid string `json:"owner_gcid"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0].Title != "Mine" {
		t.Errorf("items = %+v; want 1 'Mine'", got.Items)
	}
	if got.Items[0].OwnerGcid != collOwner {
		t.Errorf("owner_gcid = %s; want %s", got.Items[0].OwnerGcid, collOwner)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/collections/{id} — get detail (visibility-gated)
// -----------------------------------------------------------------------------

func TestGetCollection_ReturnsOwnerCollection(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	cid := createColl(t, srv, collOwner, "X", "private")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+cid, nil, collOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetCollection_404ForUnknownID(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodGet,
		"/api/v1/collections/01970000-0000-7000-7000-deadbeef0000", nil, collOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/collections/{id}
// -----------------------------------------------------------------------------

func TestPatchCollection_UpdatesTitleAndEmitsEvent(t *testing.T) {
	t.Parallel()

	srv, _, pub := newCollServer(t)
	cid := createColl(t, srv, collOwner, "Old", "private")

	body := map[string]any{"title": "New"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPatch, "/api/v1/collections/"+cid, body, collOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["title"] != "New" {
		t.Errorf("title = %v; want New", got["title"])
	}
	// 2 events expected: created + updated.
	if len(pub.events) != 2 {
		t.Fatalf("events len = %d; want 2", len(pub.events))
	}
	if pub.events[1].Type != collection.EventTypeCollectionUpdated {
		t.Errorf("events[1].Type = %q; want updated.v1", pub.events[1].Type)
	}
}

func TestPatchCollection_403ForNonOwner(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	cid := createColl(t, srv, collOwner, "x", "private")

	body := map[string]any{"title": "trying"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPatch, "/api/v1/collections/"+cid, body, collOther))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/collections/{id}/atoms
// -----------------------------------------------------------------------------

func TestAddAtom_201AndEmitsAtomAddedEvent(t *testing.T) {
	t.Parallel()

	srv, _, pub := newCollServer(t)
	cid := createColl(t, srv, collOwner, "Set", "private")

	body := map[string]any{"atom_id": atomOwnedID}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+cid+"/atoms", body, collOwner))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// created + atom_added
	if len(pub.events) != 2 {
		t.Fatalf("events len = %d; want 2", len(pub.events))
	}
	if pub.events[1].Type != collection.EventTypeCollectionAtomAdded {
		t.Errorf("events[1].Type = %q; want atom_added.v1", pub.events[1].Type)
	}
	if pub.events[1].AtomID != atomOwnedID {
		t.Errorf("AtomID = %q; want %q", pub.events[1].AtomID, atomOwnedID)
	}
}

func TestAddAtom_404ForMissingAtom(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	cid := createColl(t, srv, collOwner, "Set", "private")

	body := map[string]any{"atom_id": atomMissID}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+cid+"/atoms", body, collOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404; body=%s", w.Code, w.Body.String())
	}
}

// Cross-tenant atoms are STILL refused at the HTTP boundary — as a 404.
//
// Was TestAddAtom_403ForCrossTenantOnPrivateCollection. The 403 came from
// ErrCrossTenantAtomNotPermitted, which required AddAtom to first ask an
// UNTENANTED lookup which tenant owned the atom — the read that RLS answered with
// 22P02, 500ing every add in production. ADR-233 D7 retired that rule; the
// tenant-scoped gate simply cannot SEE another tenant's atom, so the honest answer
// is now "no such atom here" (404).
//
// 404 is also the better answer on its own terms: a 403 would confirm to the
// caller that an atom with this id exists in some OTHER tenant. It should not.
func TestAddAtom_404ForCrossTenantAtom(t *testing.T) {
	t.Parallel()

	srv, _, _ := newCollServer(t)
	cid := createColl(t, srv, collOwner, "Private", "private")

	body := map[string]any{"atom_id": atomOtherTen}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+cid+"/atoms", body, collOwner))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (the tenant-scoped gate cannot see another tenant's atom); body=%s",
			w.Code, w.Body.String())
	}

	// And it must not have been curated.
	gw := httptest.NewRecorder()
	srv.ServeHTTP(gw, collReq(http.MethodGet, "/api/v1/collections/"+cid, nil, collOwner))
	var got map[string]any
	if err := json.Unmarshal(gw.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if atoms, _ := got["atoms"].([]any); len(atoms) != 0 {
		t.Errorf("collection holds %d atom(s); want 0 — a cross-tenant atom must never be curated", len(atoms))
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/collections/{id}/atoms/{atomId}
// -----------------------------------------------------------------------------

func TestRemoveAtom_204AndEmitsEvent(t *testing.T) {
	t.Parallel()

	srv, _, pub := newCollServer(t)
	cid := createColl(t, srv, collOwner, "Set", "private")

	// Add an atom first.
	_ = doAddAtom(t, srv, cid, atomOwnedID, collOwner)

	// Now remove it.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodDelete,
		"/api/v1/collections/"+cid+"/atoms/"+atomOwnedID, nil, collOwner))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d; want 204; body=%s", w.Code, w.Body.String())
	}
	// created + atom_added + atom_removed = 3 events
	if len(pub.events) != 3 {
		t.Fatalf("events len = %d; want 3", len(pub.events))
	}
	if pub.events[2].Type != collection.EventTypeCollectionAtomRemoved {
		t.Errorf("events[2].Type = %q; want atom_removed.v1", pub.events[2].Type)
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/collections/{id}
// -----------------------------------------------------------------------------

func TestDeleteCollection_204AndEmitsEvent(t *testing.T) {
	t.Parallel()

	srv, _, pub := newCollServer(t)
	cid := createColl(t, srv, collOwner, "x", "private")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodDelete, "/api/v1/collections/"+cid, nil, collOwner))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d; want 204; body=%s", w.Code, w.Body.String())
	}
	if len(pub.events) != 2 {
		t.Fatalf("events len = %d; want 2", len(pub.events))
	}
	if pub.events[1].Type != collection.EventTypeCollectionDeleted {
		t.Errorf("events[1].Type = %q; want deleted.v1", pub.events[1].Type)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func createColl(t *testing.T, srv http.Handler, gcid, title, visibility string) string {
	t.Helper()
	body := map[string]any{"title": title, "visibility": visibility}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections", body, gcid))
	if w.Code != http.StatusCreated {
		t.Fatalf("createColl: status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("createColl unmarshal: %v body=%s", err, w.Body.String())
	}
	id, _ := got["collection_id"].(string)
	if id == "" {
		t.Fatalf("createColl no collection_id in response: %v", got)
	}
	return id
}

func doAddAtom(t *testing.T, srv http.Handler, cid, atomID, gcid string) int {
	t.Helper()
	body := map[string]any{"atom_id": atomID}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+cid+"/atoms", body, gcid))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("doAddAtom: status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Code
}

func jsonOrPanic(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// keep strings import used
var _ = strings.TrimSpace
