// convert_service_test.go — ADR-233 domain-service slice (WS-4), RED-first.
//
// Three behaviours under test:
//
//  1. 🔴 GetVisible — the SECURITY FIX (ADR-233 D8 / Context §6). Today the
//     handler never passes the caller's GCID and nothing checks owner or
//     visibility, so any tenant member can read anyone's PRIVATE collection by
//     ID. A non-entitled read must now return ErrNotFound (404, NOT 403 — a 403
//     confirms the collection exists to someone who may not see it).
//
//  2. AddAtom gate (ADR-229 D4.4 / ADR-233 D10b) — non-entitled atoms cannot
//     enter a collection at all. Mints NO grant and charges nothing: adding to a
//     collection is CURATION, not licensing (FR-030 / T068).
//
//  3. ConvertToStudyList (ADR-233 D9/D10a/D11) — per-atom entitlement, partial
//     success with named exclusions, GRANT_SCOPE_COLLECTION minted on the
//     tenant-visible leg, zero survivors → ErrNoEntitledAtoms.
//
// Every gate dependency FAILS LOUD when absent or erroring: ADR-229's contract
// is that a sharing outage REFUSES the operation. It must never degrade the gate
// open, nor silently narrow it (a 404 on an outage would be a silent narrowing).
package collection_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// -----------------------------------------------------------------------------
// Gate stubs
// -----------------------------------------------------------------------------

// memFacts models the production adapter (pg.CollectionRepository.ReuseFacts):
// a TENANT-SCOPED batch read — `WHERE la.tenant_id = $1`, inside RunInTenantTx.
//
// The tenant scope is not decoration. It is the load-bearing property that let
// the AtomLookup port be deleted (ADR-233 D7): a cross-tenant atom is INVISIBLE
// to this read, so it can never reach the aggregate's cross-tenant invariant —
// it is excluded upstream as ATOM_NOT_FOUND. A stub that ignored tenantID would
// let that claim pass untested, which is how the untenanted AtomLookup read
// survived review in the first place.
type memFacts struct {
	mu    sync.Mutex
	facts map[string]reuseconsent.AtomFact
	// tenantByID pins an atom to a tenant. An atom with no entry belongs to
	// whichever tenant asks for it (the same-tenant default every test but the
	// cross-tenant ones wants).
	tenantByID map[string]string
	err        error
	calls      int
}

// count reports how many times the reuse gate actually read atom facts. A fork
// refused at the D8 read must leave this at ZERO: authorization precedes the
// work, so a caller who may not even see the collection never reaches the gate.
func (m *memFacts) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *memFacts) ReuseFacts(_ context.Context, tenantID string, atomIDs []string) (map[string]reuseconsent.AtomFact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[string]reuseconsent.AtomFact, len(atomIDs))
	for _, id := range atomIDs {
		f, ok := m.facts[id]
		if !ok {
			continue
		}
		// TENANT SCOPE — an atom owned by another tenant is not visible to this
		// read, exactly as RLS + `WHERE tenant_id = $1` make it invisible in
		// chora_creation.
		if owner, pinned := m.tenantByID[id]; pinned && owner != tenantID {
			continue
		}
		out[id] = f
	}
	return out, nil
}

// memConsent models chora-sharing's GetReuseContext (ADR-229 WS-0), which is
// CALLER-SCOPED: it answers "what may THIS actor reuse", never "what may this
// collection's owner reuse".
//
// The flat friends/granted fields answer every caller identically. That is fine
// for the WS-4 tests, where actor and owner are the same person — but on its own
// such a stub CANNOT distinguish an implementation that passes the ACTOR's gcid
// from one that passes the collection OWNER's, because both return the same
// context. That is exactly the defect CHO-2165 must not ship: it would make
// curation a laundering channel for consent. Hence friendsByActor/grantedByActor
// (per-actor overrides, which win when present) and `asked` (whose entitlements
// were actually consulted). A fork test MUST use them.
type memConsent struct {
	mu             sync.Mutex
	friends        []string
	granted        []string
	friendsByActor map[string][]string
	grantedByActor map[string][]string
	err            error
	calls          int
	asked          []string
}

func (m *memConsent) ConsentContext(_ context.Context, actorGCID, _ string) (reuseconsent.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.asked = append(m.asked, actorGCID)
	if m.err != nil {
		return reuseconsent.Context{}, m.err
	}
	friends, granted := m.friends, m.granted
	if v, ok := m.friendsByActor[actorGCID]; ok {
		friends = v
	}
	if v, ok := m.grantedByActor[actorGCID]; ok {
		granted = v
	}
	return reuseconsent.Context{
		ActorGCID:      actorGCID,
		FriendGCIDs:    append([]string(nil), friends...),
		GrantedAtomIDs: append([]string(nil), granted...),
	}, nil
}

// askedAbout returns every gcid the service resolved a consent context for.
func (m *memConsent) askedAbout() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.asked...)
}

type memAuthz struct {
	mu      sync.Mutex
	granted []string
	err     error
}

func (m *memAuthz) AuthorizeCollectionUse(_ context.Context, _, granteeGCID, atomID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.granted = append(m.granted, granteeGCID+"|"+atomID)
	return nil
}

func (m *memAuthz) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.granted)
}

// grants returns the "<grantee>|<atom>" audit records minted, so a test can
// assert WHO the D2 grant names. On a fork that is the FORKER — the grant
// records who is now reusing the atom, and that is not the sharer.
func (m *memAuthz) grants() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.granted...)
}

// fixture wires a service with all five ports and seeds one collection.
type fixture struct {
	repo    *memRepo
	pub     *memPub
	facts   *memFacts
	consent *memConsent
	authz   *memAuthz
	svc     *collection.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		repo: newMemRepo(),
		pub:  &memPub{},
		facts: &memFacts{
			facts:      map[string]reuseconsent.AtomFact{},
			tenantByID: map[string]string{},
		},
		consent: &memConsent{
			friendsByActor: map[string][]string{},
			grantedByActor: map[string][]string{},
		},
		authz: &memAuthz{},
	}
	f.svc = collection.NewService(f.repo, f.pub, f.facts, f.consent, f.authz)
	return f
}

// seed creates a collection owned by ownerA with the supplied audience and
// atoms already curated (bypassing the add-time gate, as a legacy row would).
func (f *fixture) seed(t *testing.T, aud audience.Audience, atomIDs ...string) *collection.Collection {
	t.Helper()
	c, err := collection.New(collection.NewParams{
		TenantID:   tenantA,
		OwnerGcid:  ownerA,
		Title:      "Seeded",
		Visibility: aud,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, id := range atomIDs {
		if err := c.AddAtom(id, collection.AtomContext{TenantID: tenantA}); err != nil {
			t.Fatalf("AddAtom: %v", err)
		}
	}
	if err := f.repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return c
}

// ownFact / tenantFact / privateFact build the three interesting atom facts.
func ownFact(id string) reuseconsent.AtomFact {
	return reuseconsent.AtomFact{AtomID: id, AuthorGCID: ownerA, Audience: audience.Private, Published: true}
}

func tenantFact(id string) reuseconsent.AtomFact {
	return reuseconsent.AtomFact{AtomID: id, AuthorGCID: strangerGCD, Audience: audience.Tenant, Published: true}
}

func privateFact(id string) reuseconsent.AtomFact {
	return reuseconsent.AtomFact{AtomID: id, AuthorGCID: strangerGCD, Audience: audience.Private, Published: true}
}

// =============================================================================
// 1. 🔴 GetVisible — the security fix
// =============================================================================

func TestGetVisible_OwnerReadsOwnPrivateCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)

	got, err := f.svc.GetVisible(context.Background(), tenantA, ownerA, c.CollectionID)
	if err != nil {
		t.Fatalf("owner GetVisible own private: %v", err)
	}
	if got.CollectionID != c.CollectionID {
		t.Errorf("CollectionID = %q; want %q", got.CollectionID, c.CollectionID)
	}
}

// THE DEFECT: before ADR-233 D8 this returned the collection.
func TestGetVisible_StrangerCannotReadPrivateCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)

	_, err := f.svc.GetVisible(context.Background(), tenantA, strangerGCD, c.CollectionID)
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound (404, NOT 403 — a 403 confirms existence)", err)
	}
	if errors.Is(err, collection.ErrForbidden) {
		t.Errorf("err = ErrForbidden; want ErrNotFound — 403 leaks the collection's existence")
	}
}

func TestGetVisible_StrangerReadsTenantCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Tenant)

	if _, err := f.svc.GetVisible(context.Background(), tenantA, strangerGCD, c.CollectionID); err != nil {
		t.Fatalf("stranger GetVisible tenant-audience: %v", err)
	}
}

func TestGetVisible_FriendReadsFriendsCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.consent.friends = []string{ownerA} // the actor's friend set contains the owner
	c := f.seed(t, audience.Friends)

	if _, err := f.svc.GetVisible(context.Background(), tenantA, friendGCID, c.CollectionID); err != nil {
		t.Fatalf("friend GetVisible friends-audience: %v", err)
	}
}

func TestGetVisible_NonFriendCannotReadFriendsCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.consent.friends = []string{} // not friends with the owner
	c := f.seed(t, audience.Friends)

	_, err := f.svc.GetVisible(context.Background(), tenantA, strangerGCD, c.CollectionID)
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

// A sharing outage must REFUSE loudly — never silently narrow to a 404, which
// would look identical to "you are not a friend".
func TestGetVisible_ConsentFetchErrorFailsLoud(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	boom := errors.New("sharing unavailable")
	f.consent.err = boom
	c := f.seed(t, audience.Friends)

	_, err := f.svc.GetVisible(context.Background(), tenantA, strangerGCD, c.CollectionID)
	if err == nil {
		t.Fatalf("expected a loud error when the consent fetch fails")
	}
	if errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = ErrNotFound; a sharing outage must NOT be silently narrowed into a 404")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the underlying sharing error wrapped", err)
	}
}

func TestGetVisible_NilConsentPortFailsLoudOnFriendsAudience(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := collection.NewService(repo, &memPub{}, nil, nil, nil)
	c, err := collection.New(collection.NewParams{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "x", Visibility: audience.Friends,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = svc.GetVisible(context.Background(), tenantA, strangerGCD, c.CollectionID)
	if err == nil || errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want a loud not-wired error, never a silent 404", err)
	}
}

func TestGetVisible_MissingCollectionIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	_, err := f.svc.GetVisible(context.Background(), tenantA, ownerA, "01970000-0000-7000-7000-00000000dead")
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

// =============================================================================
// 2. AddAtom gate — curation is not licensing
// =============================================================================

func TestAddAtom_RefusesNonEntitledAtom(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	f.facts.facts[atomID] = privateFact(atomID) // another author's PRIVATE atom

	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if !errors.Is(err, collection.ErrAtomNotReusable) {
		t.Fatalf("err = %v; want ErrAtomNotReusable", err)
	}
	if f.authz.count() != 0 {
		t.Errorf("a grant was minted on a REFUSED add; want none")
	}
}

func TestAddAtom_AllowsOwnAtomAndMintsNoGrant(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	f.facts.facts[atomID] = ownFact(atomID)

	updated, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if err != nil {
		t.Fatalf("AddAtom own atom: %v", err)
	}
	if len(updated.Atoms) != 1 {
		t.Errorf("len(Atoms) = %d; want 1", len(updated.Atoms))
	}
	if f.authz.count() != 0 {
		t.Errorf("a grant was minted for the author's OWN atom; want none")
	}
}

// FR-030 / T068: adding a tenant-visible atom to a collection is CURATION.
// It mints NO grant and charges nothing — the grant is minted at CONVERT.
func TestAddAtom_TenantVisibleAtomMintsNoGrant(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	f.facts.facts[atomID] = tenantFact(atomID)

	if _, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	}); err != nil {
		t.Fatalf("AddAtom tenant-visible atom: %v", err)
	}
	if f.authz.count() != 0 {
		t.Errorf("AddAtom minted %d grant(s); want 0 — adding is curation, not licensing", f.authz.count())
	}
}

func TestAddAtom_FactsErrorFailsLoud(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	boom := errors.New("db down")
	f.facts.err = boom

	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the fact-lookup error wrapped (fail loud)", err)
	}
}

func TestAddAtom_ConsentErrorFailsLoud(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	f.facts.facts[atomID] = tenantFact(atomID)
	boom := errors.New("sharing unavailable")
	f.consent.err = boom

	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the consent error wrapped (fail loud)", err)
	}
}

func TestAddAtom_NilGatePortsFailLoud(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	atomID := makeAtomID(t, 7)
	svc := collection.NewService(repo, &memPub{}, nil, nil, nil)

	c, err := collection.New(collection.NewParams{TenantID: tenantA, OwnerGcid: ownerA, Title: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if err == nil {
		t.Fatalf("expected a loud not-wired error when the reuse gate is unwired")
	}
	if errors.Is(err, collection.ErrAtomNotReusable) {
		t.Errorf("an unwired gate must not masquerade as a clean refusal (403)")
	}
	// New failure mode now that the gate ALSO answers existence: an unwired gate
	// must not masquerade as a clean "that atom does not exist" either. A 404 is a
	// statement of fact about the data; we have not read the data.
	if errors.Is(err, collection.ErrAtomDoesNotExist) {
		t.Errorf("an unwired gate must not masquerade as a clean not-found (404)")
	}
}

// Cross-tenant is STILL always refused — the invariant is untouched. What changed
// is the MECHANISM, and with it the sentinel.
//
// Was TestAddAtom_CrossTenantAlwaysRefused, which asserted
// ErrCrossTenantAtomNotPermitted. That sentinel could only fire because AddAtom
// asked an UNTENANTED lookup "who owns this atom?" — the very read that made
// POST /collections/{id}/atoms 500 in production (`SELECT tenant_id FROM
// learning_atoms WHERE atom_id = $1` with no tenant filter, against a table whose
// RLS is `tenant_id = current_setting('chora.tenant_id')::uuid`; on a pooled
// connection whose GUC has reset to ”, `”::uuid` throws 22P02).
//
// ADR-233 D7 retired the BP-01 rule that read needed. The refusal now falls out of
// the gate BY CONSTRUCTION: ReuseFacts is tenant-scoped, so another tenant's atom
// is never in the facts map, and the atom is excluded as ATOM_NOT_FOUND before the
// aggregate's cross-tenant check could run. Same refusal, no untenanted read.
func TestAddAtom_CrossTenantAtomIsInvisibleToTheTenantScopedGate(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Tenant) // the widest audience there is
	atomID := makeAtomID(t, 7)
	f.facts.tenantByID[atomID] = tenantB    // owned by ANOTHER tenant
	f.facts.facts[atomID] = ownFact(atomID) // and otherwise perfectly entitled

	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if err == nil {
		t.Fatalf("a cross-tenant atom was ACCEPTED; want refusal")
	}
	if !errors.Is(err, collection.ErrAtomDoesNotExist) {
		t.Errorf("err = %v; want ErrAtomDoesNotExist — the tenant-scoped gate cannot see another tenant's atom", err)
	}

	// The invariant that actually matters: it never entered the collection.
	after, gerr := f.repo.Get(context.Background(), tenantA, c.CollectionID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if len(after.Atoms) != 0 {
		t.Errorf("collection holds %d atom(s); want 0 — a cross-tenant atom must never be curated", len(after.Atoms))
	}
}

// An atom that does not RESOLVE is a 404; an atom the actor may not REUSE is a
// 403. The AtomLookup port used to carry that distinction. The gate carries it
// now, via ReasonAtomNotFound — and it must not collapse the two, or a learner
// gets told "you may not reuse this" about an atom that does not exist.
func TestAddAtom_UnresolvableAtomIsNotFoundNotForbidden(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private)
	atomID := makeAtomID(t, 7)
	// NO fact is seeded: the atom does not resolve in the tenant-scoped read that
	// is now the single source of truth for atom existence.

	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID: tenantA, CollectionID: c.CollectionID, OwnerGcid: ownerA, AtomID: atomID,
	})
	if !errors.Is(err, collection.ErrAtomDoesNotExist) {
		t.Errorf("err = %v; want ErrAtomDoesNotExist (404)", err)
	}
	if errors.Is(err, collection.ErrAtomNotReusable) {
		t.Errorf("err = ErrAtomNotReusable (403); a non-existent atom is a 404, not a refusal to license")
	}
}

// =============================================================================
// 3. ConvertToStudyList
// =============================================================================

func TestConvert_MixedEntitlement_PartialSuccessWithNamedExclusions(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	own := makeAtomID(t, 1)        // own            → include, no grant
	tenantAtom := makeAtomID(t, 2) // tenant-visible → include + GRANT_SCOPE_COLLECTION
	narrowed := makeAtomID(t, 3)   // private        → EXCLUDE (REUSE_VISIBILITY_NARROWED)
	gone := makeAtomID(t, 4)       // soft-deleted   → EXCLUDE (ATOM_NOT_FOUND)

	c := f.seed(t, audience.Private, own, tenantAtom, narrowed, gone)
	f.facts.facts[own] = ownFact(own)
	f.facts.facts[tenantAtom] = tenantFact(tenantAtom)
	f.facts.facts[narrowed] = privateFact(narrowed)
	// `gone` is deliberately absent from the facts map.

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
		TraceParent: "00-trace-span-01",
	})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if res.AtomCount != 2 {
		t.Errorf("AtomCount = %d; want 2 (own + tenant-visible)", res.AtomCount)
	}
	if len(res.Excluded) != 2 {
		t.Fatalf("len(Excluded) = %d; want 2", len(res.Excluded))
	}
	byID := map[string]reuseconsent.Reason{}
	for _, d := range res.Excluded {
		byID[d.AtomID] = d.Reason
	}
	if byID[narrowed] != reuseconsent.ReasonNarrowed {
		t.Errorf("excluded[%s] = %q; want REUSE_VISIBILITY_NARROWED", narrowed, byID[narrowed])
	}
	if byID[gone] != reuseconsent.ReasonAtomNotFound {
		t.Errorf("excluded[%s] = %q; want ATOM_NOT_FOUND", gone, byID[gone])
	}

	// Exactly ONE grant — for the tenant-visible atom only.
	if f.authz.count() != 1 {
		t.Errorf("grants minted = %d; want 1 (tenant-visible leg only)", f.authz.count())
	}

	// The event carries the entitled atoms in CURATED order.
	if len(f.pub.events) != 1 {
		t.Fatalf("events = %d; want 1", len(f.pub.events))
	}
	ev := f.pub.events[0]
	if ev.Type != collection.EventTypeCollectionConvertedToStudyList {
		t.Errorf("event type = %q; want converted_to_study_list.v1", ev.Type)
	}
	if len(ev.AtomIDs) != 2 || ev.AtomIDs[0] != own || ev.AtomIDs[1] != tenantAtom {
		t.Errorf("event AtomIDs = %v; want [%s %s] in curated order", ev.AtomIDs, own, tenantAtom)
	}
	if res.StudyListEventID != ev.StudyListEventID || res.StudyListEventID == "" {
		t.Errorf("ConvertResult.StudyListEventID = %q; want the event's own id (%q)", res.StudyListEventID, ev.StudyListEventID)
	}

	// D9: the Collection is NOT mutated — the excluded atoms stay curated so a
	// later convert picks them up if the author re-widens.
	after, err := f.repo.Get(context.Background(), tenantA, c.CollectionID)
	if err != nil {
		t.Fatalf("Get after convert: %v", err)
	}
	if len(after.Atoms) != 4 {
		t.Errorf("collection now holds %d atoms; want 4 — conversion must not mutate the Collection", len(after.Atoms))
	}
}

// An already-granted atom (incl. a grant repointed to an orphan edition) is
// entitled and needs NO new grant.
func TestConvert_AlreadyGrantedAtomNeedsNoNewGrant(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	orphan := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, orphan)
	// Orphan editions are minted audience=private and are never published —
	// reachable ONLY through the repointed grant.
	f.facts.facts[orphan] = reuseconsent.AtomFact{
		AtomID: orphan, AuthorGCID: strangerGCD, Audience: audience.Private, Published: false,
	}
	f.consent.granted = []string{orphan}

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}
	if res.AtomCount != 1 {
		t.Errorf("AtomCount = %d; want 1 (the granted orphan survives)", res.AtomCount)
	}
	if f.authz.count() != 0 {
		t.Errorf("grants minted = %d; want 0 — re-minting an existing grant is noise", f.authz.count())
	}
}

// NB: TestConvert_NonOwnerIsForbidden lived here and asserted that a stranger
// converting a TENANT-visible collection got ErrForbidden. It encoded the WS-4
// policy (`actor == owner`) faithfully — and ADR-233 D9 always named the other
// half as RESERVED. CHO-2165 lands it, so a stranger who may VIEW a collection
// may now fork it, and the refusal for one they may NOT see is ErrNotFound, not
// ErrForbidden (a 403 confirms existence). The replacement lives in
// fork_service_test.go, which owns the whole policy.

func TestConvert_MissingCollectionIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: "01970000-0000-7000-7000-00000000dead", ActorGCID: ownerA,
	})
	if !errors.Is(err, collection.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestConvert_EmptyCollectionRefuses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private) // no atoms

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, collection.ErrNoEntitledAtoms) {
		t.Errorf("err = %v; want ErrNoEntitledAtoms", err)
	}
	if len(f.pub.events) != 0 {
		t.Errorf("published %d event(s) for an empty conversion; want 0", len(f.pub.events))
	}
}

// Zero survivors → refuse. Emitting an empty study list would be fabricating a
// success (ADR-233 D11 → 409).
func TestConvert_ZeroEntitledRefusesAndPublishesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	a1, a2 := makeAtomID(t, 1), makeAtomID(t, 2)
	c := f.seed(t, audience.Private, a1, a2)
	f.facts.facts[a1] = privateFact(a1)
	f.facts.facts[a2] = privateFact(a2)

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, collection.ErrNoEntitledAtoms) {
		t.Fatalf("err = %v; want ErrNoEntitledAtoms", err)
	}
	if len(f.pub.events) != 0 {
		t.Errorf("published %d event(s); want 0 — never emit an empty study list", len(f.pub.events))
	}
	if f.authz.count() != 0 {
		t.Errorf("minted %d grant(s) on a refused conversion; want 0", f.authz.count())
	}
}

// The D2 audit record is NOT optional: a grant-write failure refuses the convert.
func TestConvert_GrantWriteFailureRefusesAndPublishesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	tenantAtom := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, tenantAtom)
	f.facts.facts[tenantAtom] = tenantFact(tenantAtom)
	boom := errors.New("sharing unavailable")
	f.authz.err = boom

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the grant-write error wrapped (the audit record is not optional)", err)
	}
	if len(f.pub.events) != 0 {
		t.Errorf("published %d event(s) after a failed grant write; want 0", len(f.pub.events))
	}
}

func TestConvert_FactsErrorFailsLoud(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private, makeAtomID(t, 1))
	boom := errors.New("db down")
	f.facts.err = boom

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the fact-lookup error wrapped", err)
	}
}

func TestConvert_ConsentErrorFailsLoud(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.seed(t, audience.Private, makeAtomID(t, 1))
	boom := errors.New("sharing unavailable")
	f.consent.err = boom

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the consent error wrapped", err)
	}
}

func TestConvert_NilGatePortsFailLoud(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := collection.NewService(repo, &memPub{}, nil, nil, nil)
	c, err := collection.New(collection.NewParams{TenantID: tenantA, OwnerGcid: ownerA, Title: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.AddAtom(makeAtomID(t, 1), collection.AtomContext{TenantID: tenantA}); err != nil {
		t.Fatalf("AddAtom: %v", err)
	}
	if err := repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if err == nil {
		t.Fatalf("expected a loud not-wired error")
	}
	if errors.Is(err, collection.ErrNoEntitledAtoms) {
		t.Errorf("an unwired gate must not masquerade as a clean zero-survivor refusal")
	}
}

func TestConvert_PublishFailurePropagates(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	own := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, own)
	f.facts.facts[own] = ownFact(own)
	boom := errors.New("outbox down")
	f.pub.err = boom

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the publish error wrapped", err)
	}
}
