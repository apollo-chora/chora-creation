// collection_convert_handler_test.go — ADR-233 HTTP slice (WS-4), RED-first.
//
//   - POST /api/v1/collections/{id}/convert-to-study-list — the new route.
//   - GET  /api/v1/collections/{id} — 🔴 the security fix at the HTTP boundary.
//     The handler previously never passed the caller's GCID at all, so the
//     service could not have enforced visibility even if it had wanted to.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// -----------------------------------------------------------------------------
// Gate stubs
// -----------------------------------------------------------------------------

// httpFacts models the production adapter (pg.CollectionRepository.ReuseFacts): a
// TENANT-SCOPED batch read (`WHERE la.tenant_id = $1`, inside RunInTenantTx).
//
// Since ADR-233 D7 it is also the ATOM-EXISTENCE oracle — the AtomLookup port it
// replaced is deleted. The tenant scope is what made that possible, so the stub
// models it: an atom pinned to another tenant is invisible here, exactly as RLS
// makes it invisible in chora_creation.
type httpFacts struct {
	mu    sync.Mutex
	facts map[string]reuseconsent.AtomFact
	// tenantByID pins an atom to a tenant; unpinned atoms belong to whichever
	// tenant asks.
	tenantByID map[string]string
	err        error
}

func (f *httpFacts) ReuseFacts(_ context.Context, tenantID string, atomIDs []string) (map[string]reuseconsent.AtomFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]reuseconsent.AtomFact{}
	for _, id := range atomIDs {
		fact, ok := f.facts[id]
		if !ok {
			continue
		}
		if owner, pinned := f.tenantByID[id]; pinned && owner != tenantID {
			continue // another tenant's atom — not visible to this read
		}
		out[id] = fact
	}
	return out, nil
}

type httpConsent struct {
	friends []string
	granted []string
	err     error
}

func (c *httpConsent) ConsentContext(_ context.Context, actorGCID, _ string) (reuseconsent.Context, error) {
	if c.err != nil {
		return reuseconsent.Context{}, c.err
	}
	return reuseconsent.Context{
		ActorGCID:      actorGCID,
		FriendGCIDs:    append([]string(nil), c.friends...),
		GrantedAtomIDs: append([]string(nil), c.granted...),
	}, nil
}

type httpAuthz struct {
	mu    sync.Mutex
	count int
	err   error
}

func (a *httpAuthz) AuthorizeCollectionUse(_ context.Context, _, _, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.count++
	return nil
}

type convertRig struct {
	srv     http.Handler
	pub     *recPub
	facts   *httpFacts
	consent *httpConsent
	authz   *httpAuthz
}

func newConvertRig(t *testing.T) *convertRig {
	t.Helper()
	rig := &convertRig{
		pub: &recPub{},
		facts: &httpFacts{
			facts:      map[string]reuseconsent.AtomFact{},
			tenantByID: map[string]string{},
		},
		consent: &httpConsent{},
		authz:   &httpAuthz{},
	}
	rig.srv = httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                 inmem.NewAtomRepository(),
		CollectionRepository: inmem.NewCollectionRepository(),
		CollectionPublisher:  rig.pub,
		CollectionFacts:      rig.facts,
		CollectionConsent:    rig.consent,
		CollectionAuthz:      rig.authz,
	})
	return rig
}

// seedCollection creates a collection via the API, then curates atoms into it.
func (r *convertRig) seedCollection(t *testing.T, owner, visibility string, atomIDs ...string) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections",
		map[string]any{"title": "Drill", "visibility": visibility}, owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id, _ := created["collection_id"].(string)
	if id == "" {
		t.Fatalf("no collection_id in %s", w.Body.String())
	}

	for _, atomID := range atomIDs {
		// Every seeded atom is the owner's own → entitled at add-time. No tenant
		// pin ⇒ it belongs to the calling tenant, so the tenant-scoped gate read
		// resolves it.
		r.facts.mu.Lock()
		if _, ok := r.facts.facts[atomID]; !ok {
			r.facts.facts[atomID] = reuseconsent.AtomFact{
				AtomID: atomID, AuthorGCID: owner, Audience: audience.Private, Published: true,
			}
		}
		r.facts.mu.Unlock()

		aw := httptest.NewRecorder()
		r.srv.ServeHTTP(aw, collReq(http.MethodPost, "/api/v1/collections/"+id+"/atoms",
			map[string]any{"atom_id": atomID}, owner))
		if aw.Code != http.StatusCreated {
			t.Fatalf("add atom %s: status = %d body=%s", atomID, aw.Code, aw.Body.String())
		}
	}
	return id
}

func (r *convertRig) convert(t *testing.T, id, actor string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+id+"/convert-to-study-list", nil, actor))
	return w
}

// =============================================================================
// POST /convert-to-study-list
// =============================================================================

func TestConvertToStudyList_201WithEventIDAndCount(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	a1, a2 := "01970000-0000-7000-a000-00000000c001", "01970000-0000-7000-a000-00000000c002"
	id := rig.seedCollection(t, collOwner, "private", a1, a2)

	w := rig.convert(t, id, collOwner)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}

	var got struct {
		StudyListEventID string `json:"study_list_event_id"`
		AtomCount        int    `json:"atom_count"`
		Excluded         []struct {
			AtomID string `json:"atom_id"`
			Reason string `json:"reason"`
		} `json:"excluded"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got.StudyListEventID == "" {
		t.Errorf("study_list_event_id empty; consumption dedups on it")
	}
	if got.AtomCount != 2 {
		t.Errorf("atom_count = %d; want 2", got.AtomCount)
	}
	if len(got.Excluded) != 0 {
		t.Errorf("excluded = %v; want none", got.Excluded)
	}

	// The event must be published, carry the curated order, and carry a real
	// traceparent — effectiveTraceparent(r), NOT the raw header (a raw read
	// yields "" and the outbox publisher would strand the row as `failed`).
	if len(rig.pub.events) == 0 {
		t.Fatal("no event published")
	}
	ev := rig.pub.events[len(rig.pub.events)-1]
	if ev.Type != collection.EventTypeCollectionConvertedToStudyList {
		t.Fatalf("event type = %q", ev.Type)
	}
	if ev.StudyListEventID != got.StudyListEventID {
		t.Errorf("response id %q != event id %q", got.StudyListEventID, ev.StudyListEventID)
	}
	if len(ev.AtomIDs) != 2 || ev.AtomIDs[0] != a1 || ev.AtomIDs[1] != a2 {
		t.Errorf("event AtomIDs = %v; want curated order [%s %s]", ev.AtomIDs, a1, a2)
	}
	if ev.TraceParent == "" {
		t.Errorf("traceparent empty — the outbox publisher rejects it and the event strands as `failed`")
	}
}

func TestConvertToStudyList_MixedEntitlementNamesExclusions(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	mine := "01970000-0000-7000-a000-00000000c001"
	narrowed := "01970000-0000-7000-a000-00000000c002"

	// Curate `narrowed` while it is still tenant-visible (as the add-time gate
	// would have allowed), THEN have its author withdraw it.
	rig.facts.facts[narrowed] = reuseconsent.AtomFact{
		AtomID: narrowed, AuthorGCID: collOther, Audience: audience.Tenant, Published: true,
	}
	id := rig.seedCollection(t, collOwner, "private", mine, narrowed)

	// The author narrows to private AFTER curation.
	rig.facts.mu.Lock()
	rig.facts.facts[narrowed] = reuseconsent.AtomFact{
		AtomID: narrowed, AuthorGCID: collOther, Audience: audience.Private, Published: true,
	}
	rig.facts.mu.Unlock()

	w := rig.convert(t, id, collOwner)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201 (partial success)", w.Code, w.Body.String())
	}
	var got struct {
		AtomCount int `json:"atom_count"`
		Excluded  []struct {
			AtomID string `json:"atom_id"`
			Reason string `json:"reason"`
		} `json:"excluded"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AtomCount != 1 {
		t.Errorf("atom_count = %d; want 1", got.AtomCount)
	}
	if len(got.Excluded) != 1 {
		t.Fatalf("excluded = %v; want 1", got.Excluded)
	}
	if got.Excluded[0].AtomID != narrowed {
		t.Errorf("excluded atom = %q; want %q", got.Excluded[0].AtomID, narrowed)
	}
	if got.Excluded[0].Reason != string(reuseconsent.ReasonNarrowed) {
		t.Errorf("reason = %q; want REUSE_VISIBILITY_NARROWED", got.Excluded[0].Reason)
	}
}

// Zero survivors → 409, never an empty study list.
func TestConvertToStudyList_ZeroEntitledIs409(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	borrowed := "01970000-0000-7000-a000-00000000c001"
	rig.facts.facts[borrowed] = reuseconsent.AtomFact{
		AtomID: borrowed, AuthorGCID: collOther, Audience: audience.Tenant, Published: true,
	}
	id := rig.seedCollection(t, collOwner, "private", borrowed)

	rig.facts.mu.Lock()
	rig.facts.facts[borrowed] = reuseconsent.AtomFact{
		AtomID: borrowed, AuthorGCID: collOther, Audience: audience.Private, Published: true,
	}
	rig.facts.mu.Unlock()

	w := rig.convert(t, id, collOwner)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !hasErrorCode(env, "CREATION_COLLECTION_NO_ENTITLED_ATOMS") {
		t.Errorf("error envelope = %v; want code CREATION_COLLECTION_NO_ENTITLED_ATOMS", env)
	}
}

// CHO-2165 / ADR-233 D9 — this replaces TestConvertToStudyList_NonOwnerIs403,
// which seeded a TENANT-visible collection and asserted 403. That encoded the
// WS-4 policy (`actor == owner`) faithfully; D9 always named the other half as
// reserved, and it is now live. A non-owner who may VIEW a collection may fork
// it.
func TestConvertToStudyList_NonOwnerForksTenantVisibleCollection(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	shared := "01970000-0000-7000-a000-00000000c001"
	// The owner's own atom, shared tenant-wide. The owner curates it on the `own`
	// leg; the forker earns it on the `tenant` leg — each on their OWN
	// entitlement. Pre-seeding the fact stops seedCollection defaulting it to
	// private.
	rig.facts.facts[shared] = reuseconsent.AtomFact{
		AtomID: shared, AuthorGCID: collOwner, Audience: audience.Tenant, Published: true,
	}
	id := rig.seedCollection(t, collOwner, "tenant", shared)

	w := rig.convert(t, id, collOther)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201 — a non-owner who may view a collection may fork it",
			w.Code, w.Body.String())
	}

	var got struct {
		StudyListEventID string `json:"study_list_event_id"`
		AtomCount        int    `json:"atom_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if got.AtomCount != 1 {
		t.Errorf("atom_count = %d; want 1", got.AtomCount)
	}
	if got.StudyListEventID == "" {
		t.Error("study_list_event_id is empty; the fork published no study list")
	}
}

// You cannot fork what you cannot see — and the refusal must not TELL you it
// exists. 404, never 403.
func TestConvertToStudyList_NonOwnerCannotForkPrivateCollectionIs404(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private", "01970000-0000-7000-a000-00000000c001")

	w := rig.convert(t, id, collOther)
	if w.Code == http.StatusForbidden {
		t.Fatalf("status = 403; a 403 CONFIRMS the collection exists to a caller who may not " +
			"see it — want 404")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404", w.Code, w.Body.String())
	}

	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if fmt.Sprint(env["code"]) == "CREATION_COLLECTION_FORBIDDEN" {
		t.Errorf("error code = CREATION_COLLECTION_FORBIDDEN; a non-visible collection must be "+
			"indistinguishable from a missing one. envelope = %v", env)
	}
}

func TestConvertToStudyList_UnknownCollectionIs404(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	w := rig.convert(t, "01970000-0000-7000-7000-00000000dead", collOwner)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

func TestConvertToStudyList_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id+"/convert-to-study-list", nil, collOwner))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

// =============================================================================
// Add-time gate at the HTTP boundary
// =============================================================================

func TestAddAtom_NonEntitledIs403(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private")

	forbidden := "01970000-0000-7000-a000-00000000c0ff"
	// Resolves in this tenant (no tenant pin) but is ANOTHER author's private atom
	// — so the atom exists and the refusal is a true 403, not a 404.
	rig.facts.facts[forbidden] = reuseconsent.AtomFact{
		AtomID: forbidden, AuthorGCID: collOther, Audience: audience.Private, Published: true,
	}

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+id+"/atoms",
		map[string]any{"atom_id": forbidden}, collOwner))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s; want 403", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !hasErrorCode(env, "CREATION_ATOM_NOT_REUSABLE") {
		t.Errorf("error envelope = %v; want code CREATION_ATOM_NOT_REUSABLE", env)
	}
	// Curation is not licensing: no grant, no charge.
	if rig.authz.count != 0 {
		t.Errorf("AddAtom minted %d grant(s); want 0", rig.authz.count)
	}
}

// =============================================================================
// 🔴 GET /api/v1/collections/{id} — the horizontal-authz fix
// =============================================================================

func TestGetCollection_StrangerCannotReadPrivate(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id, nil, collOther))

	// 404, NOT 403 — a 403 confirms the collection exists to someone who may
	// not see it.
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404 (this is the ADR-233 Context §6 defect)", w.Code, w.Body.String())
	}
	if w.Code == http.StatusForbidden {
		t.Errorf("403 leaks the collection's existence; want 404")
	}
	// And absolutely no content must leak.
	if body := w.Body.String(); jsonContains(body, "Drill") {
		t.Errorf("response leaked the collection title: %s", body)
	}
}

func TestGetCollection_OwnerReadsOwnPrivate(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id, nil, collOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
}

func TestGetCollection_StrangerReadsTenantAudience(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "tenant", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id, nil, collOther))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
}

func TestGetCollection_FriendReadsFriendsAudience(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	rig.consent.friends = []string{collOwner}
	id := rig.seedCollection(t, collOwner, "friends", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id, nil, collOther))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (the actor is a friend of the owner)", w.Code, w.Body.String())
	}
}

func TestGetCollection_NonFriendCannotReadFriendsAudience(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	rig.consent.friends = nil
	id := rig.seedCollection(t, collOwner, "friends", "01970000-0000-7000-a000-00000000c001")

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodGet, "/api/v1/collections/"+id, nil, collOther))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

// =============================================================================
// CHO-2174 — a consent refusal is a VERDICT, not a malformed request.
//
// Live 2026-07-13, POST /convert-to-study-list answered a refusal with:
//
//	400 {"code":"CREATION_COLLECTION_INVALID",
//	     "message":"collection.Service.ConvertToStudyList: authorize collection
//	      use of atom 00000000-…-a0a4: reuse client: AuthorizeAtomUse
//	      (collection): rpc error: code = FailedPrecondition desc = atom not
//	      published or withdrawn (412)"}
//
// Three defects at once: wrong status, leaked internals, and an error code the
// convert dialog could not turn into a human sentence.
//
// The gate ports fail with the DOMAIN SENTINELS the client adapter now
// translates gRPC into (see clients/reuse_context_client.go); the domain wraps
// them with %w; these tests pin what the caller finally sees.
// =============================================================================

// convertedEvents counts the study-list events actually published. Seeding a
// collection publishes created/atom_added events too, so "nothing was converted"
// means zero of THIS type — not an empty publisher.
func (r *convertRig) convertedEvents() int {
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	n := 0
	for _, e := range r.pub.events {
		if e.Type == collection.EventTypeCollectionConvertedToStudyList {
			n++
		}
	}
	return n
}

// assertNoInternalsLeaked fails if the response body carries transport
// vocabulary, the internal call chain, or a raw atom UUID. The learner is owed a
// sentence; per-atom detail belongs in `excluded[]`, and the gRPC status belongs
// in the server log.
func assertNoInternalsLeaked(t *testing.T, body, atomID string) {
	t.Helper()
	for _, leak := range []string{
		"rpc error",
		"code =",
		"FailedPrecondition",
		"PermissionDenied",
		"Unavailable",
		"collection.Service",
		"reuse client",
		atomID,
	} {
		if jsonContains(body, leak) {
			t.Errorf("response leaks internals (%q) to the caller: %s", leak, body)
		}
	}
}

// decodeEnvelope parses the {code,message} error envelope.
func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v body=%s", err, w.Body.String())
	}
	return env
}

// THE PRODUCTION BUG. chora-sharing holds no atom_projections row for the atom,
// so AuthorizeAtomUse refuses with FailedPrecondition; the adapter translates
// that to ErrAtomNotShareable. It is a 409 with an ACTIONABLE code — never a 400
// CREATION_COLLECTION_INVALID with the rpc chain pasted into the message.
func TestConvertToStudyList_AtomNotShareableIs409WithActionableCode(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	atomID := "01970000-0000-7000-a000-00000000a0a4"
	id := rig.seedCollection(t, collOwner, "private", atomID)
	// Non-owner atom → the tenant-visible leg needs a grant, so the convert hits
	// AuthorizeCollectionUse — which is where the refusal lands.
	rig.facts.mu.Lock()
	rig.facts.facts[atomID] = reuseconsent.AtomFact{
		AtomID: atomID, AuthorGCID: "01970000-0000-7000-9000-00000000000b",
		Audience: audience.Tenant, Published: true,
	}
	rig.facts.mu.Unlock()
	rig.authz.err = fmt.Errorf("reuse client: AuthorizeAtomUse (collection): %w",
		collection.ErrAtomNotShareable)

	w := rig.convert(t, id, collOwner)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s; want 409 (a shareability verdict is not a malformed request)",
			w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); !hasErrorCode(env, "CREATION_ATOM_NOT_SHAREABLE") {
		t.Errorf("code = %v; want CREATION_ATOM_NOT_SHAREABLE (the dialog maps it to a human sentence)", env)
	}
	assertNoInternalsLeaked(t, w.Body.String(), atomID)
	if rig.convertedEvents() != 0 {
		t.Errorf("published a study-list event for a refused conversion")
	}
}

// PermissionDenied upstream → 403, not 400 and not 502.
func TestConvertToStudyList_ReuseDeniedIs403(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	atomID := "01970000-0000-7000-a000-00000000a0a5"
	id := rig.seedCollection(t, collOwner, "private", atomID)
	rig.facts.mu.Lock()
	rig.facts.facts[atomID] = reuseconsent.AtomFact{
		AtomID: atomID, AuthorGCID: "01970000-0000-7000-9000-00000000000b",
		Audience: audience.Tenant, Published: true,
	}
	rig.facts.mu.Unlock()
	rig.authz.err = fmt.Errorf("reuse client: AuthorizeAtomUse (collection): %w",
		collection.ErrAtomReuseDenied)

	w := rig.convert(t, id, collOwner)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s; want 403", w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); !hasErrorCode(env, "CREATION_ATOM_REUSE_DENIED") {
		t.Errorf("code = %v; want CREATION_ATOM_REUSE_DENIED", env)
	}
	assertNoInternalsLeaked(t, w.Body.String(), atomID)
	if rig.convertedEvents() != 0 {
		t.Errorf("published a study-list event for a refused conversion")
	}
}

// 🔴 THE PROPERTY THAT MATTERS MOST (CHO-2174 acceptance).
//
// chora-sharing is DOWN. The D2 audit grant therefore cannot be minted. The
// conversion MUST be refused with a 502 — an upstream fault, ours not the
// learner's — and MUST NOT emit a study-list event. A gate that converts anyway
// (or that reports the outage as a terminal 4xx verdict about the atom) has
// degraded open, which is worse than having no gate at all.
func TestConvertToStudyList_SharingOutageRefusesWith502AndMintsNoStudyList(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	atomID := "01970000-0000-7000-a000-00000000a0a6"
	id := rig.seedCollection(t, collOwner, "private", atomID)
	rig.facts.mu.Lock()
	rig.facts.facts[atomID] = reuseconsent.AtomFact{
		AtomID: atomID, AuthorGCID: "01970000-0000-7000-9000-00000000000b",
		Audience: audience.Tenant, Published: true,
	}
	rig.facts.mu.Unlock()
	// Exactly what the client adapter returns when AuthorizeAtomUse dies on
	// codes.Unavailable.
	rig.authz.err = fmt.Errorf("reuse client: AuthorizeAtomUse (collection): %w",
		collection.ErrSharingUnavailable)

	w := rig.convert(t, id, collOwner)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502 — an outage is OUR fault, and the conversion must be refused, not blamed on the learner",
			w.Code, w.Body.String())
	}
	// The refusal is the point: no grant was minted, so no study list may exist.
	if n := rig.convertedEvents(); n != 0 {
		t.Fatalf("published %d study-list event(s) while chora-sharing was down — the conversion proceeded WITHOUT minting the reuse grant; the consent gate degraded OPEN", n)
	}
	assertNoInternalsLeaked(t, w.Body.String(), atomID)
}

// sharing rejected the request WE constructed (InvalidArgument). Our bug → 502
// with a distinct operator-facing code; never a 4xx blaming the learner.
func TestConvertToStudyList_SharingRejectedOurRequestIs502NotClientFault(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	atomID := "01970000-0000-7000-a000-00000000a0a7"
	id := rig.seedCollection(t, collOwner, "private", atomID)
	rig.facts.mu.Lock()
	rig.facts.facts[atomID] = reuseconsent.AtomFact{
		AtomID: atomID, AuthorGCID: "01970000-0000-7000-9000-00000000000b",
		Audience: audience.Tenant, Published: true,
	}
	rig.facts.mu.Unlock()
	rig.authz.err = fmt.Errorf("reuse client: AuthorizeAtomUse (collection): %w",
		collection.ErrSharingRejectedRequest)

	w := rig.convert(t, id, collOwner)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502 (a creation-side defect is never the learner's 4xx)",
			w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); !hasErrorCode(env, "CREATION_SHARING_GATE_ERROR") {
		t.Errorf("code = %v; want CREATION_SHARING_GATE_ERROR", env)
	}
	if rig.convertedEvents() != 0 {
		t.Errorf("published a study-list event for a refused conversion")
	}
}

// The gate's OTHER gRPC leg: GetReuseContext (the consent context) dies. The
// disjunct cannot be EVALUATED at all — refuse loudly with 502, convert nothing.
func TestConvertToStudyList_ConsentContextOutageRefusesWith502(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	atomID := "01970000-0000-7000-a000-00000000a0a8"
	id := rig.seedCollection(t, collOwner, "private", atomID)
	rig.consent.err = fmt.Errorf("reuse client: GetReuseContext: %w", collection.ErrSharingUnavailable)

	w := rig.convert(t, id, collOwner)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502 — an unevaluable gate must refuse, not 400", w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); !hasErrorCode(env, "CREATION_SHARING_UNAVAILABLE") {
		t.Errorf("code = %v; want CREATION_SHARING_UNAVAILABLE", env)
	}
	if rig.convertedEvents() != 0 {
		t.Errorf("converted while the consent context was unavailable")
	}
	assertNoInternalsLeaked(t, w.Body.String(), atomID)
}

// The SAME mapping on AddAtom's gate path. AddAtom mints no grant (curation, not
// licensing — ADR-233), so it cannot see AuthorizeAtomUse's FailedPrecondition;
// but it DOES call GetReuseContext, so a sharing outage reaches it — and must be
// a 502 refusal, not the 400 it used to be, and never a silent add.
func TestAddAtom_SharingOutageIs502AndTheAtomIsNotAdded(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private")
	atomID := "01970000-0000-7000-a000-00000000a0a9"
	// The atom is perfectly addable — the ONLY thing wrong is that sharing is down.
	rig.facts.mu.Lock()
	rig.facts.facts[atomID] = reuseconsent.AtomFact{
		AtomID: atomID, AuthorGCID: collOwner, Audience: audience.Private, Published: true,
	}
	rig.facts.mu.Unlock()
	rig.consent.err = fmt.Errorf("reuse client: GetReuseContext: %w", collection.ErrSharingUnavailable)

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+id+"/atoms",
		map[string]any{"atom_id": atomID}, collOwner))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502 (the add-time gate could not be evaluated)", w.Code, w.Body.String())
	}
	if env := decodeEnvelope(t, w); !hasErrorCode(env, "CREATION_SHARING_UNAVAILABLE") {
		t.Errorf("code = %v; want CREATION_SHARING_UNAVAILABLE", env)
	}
	// And the atom must NOT have entered the collection behind the refusal.
	rig.pub.mu.Lock()
	defer rig.pub.mu.Unlock()
	for _, e := range rig.pub.events {
		if e.Type == collection.EventTypeCollectionAtomAdded {
			t.Fatalf("atom_added event published while the consent gate was unevaluable — the gate degraded OPEN")
		}
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func hasErrorCode(env map[string]any, code string) bool {
	if c, ok := env["code"].(string); ok && c == code {
		return true
	}
	if inner, ok := env["error"].(map[string]any); ok {
		if c, ok := inner["code"].(string); ok && c == code {
			return true
		}
	}
	return false
}

func jsonContains(body, needle string) bool {
	return len(body) > 0 && len(needle) > 0 && contains(body, needle)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
