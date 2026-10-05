// Package collection_test — Service tests exercising the Repository +
// EventPublisher + reuse-consent-gate composition (WS-6a TDD layer; ADR-233 WS-4).
//
// Tests use lightweight in-memory stubs so the suite stays pure-domain
// (no infrastructure imports). The pg adapter has its own test file in
// internal/adapter/pg/collection_repository_test.go.
//
// The AtomLookup stub that used to live here is gone with the port (ADR-233 D7).
// Atom existence is now a property of the TENANT-SCOPED gate read — see memFacts
// in convert_service_test.go, which models that scope deliberately.
package collection_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// -----------------------------------------------------------------------------
// In-memory stubs
// -----------------------------------------------------------------------------

type memRepo struct {
	mu     sync.Mutex
	byID   map[string]*collection.Collection
	saved  int
	getErr error
}

func newMemRepo() *memRepo {
	return &memRepo{byID: map[string]*collection.Collection{}}
}

func (m *memRepo) Save(_ context.Context, c *collection.Collection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *c
	clone.Atoms = append([]*collection.CollectionAtom(nil), c.Atoms...)
	m.byID[c.CollectionID] = &clone
	m.saved++
	return nil
}

// saveCount reports how many times the aggregate was persisted. A fork must
// leave this untouched: ADR-233 D9 forbids a non-owner convert from writing to
// someone else's Collection.
func (m *memRepo) saveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved
}

func (m *memRepo) Get(_ context.Context, tenantID, id string) (*collection.Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	c, ok := m.byID[id]
	if !ok || c.TenantID != tenantID || c.DeletedAt != nil {
		return nil, collection.ErrNotFound
	}
	clone := *c
	clone.Atoms = append([]*collection.CollectionAtom(nil), c.Atoms...)
	return &clone, nil
}

func (m *memRepo) List(_ context.Context, tenantID string, f collection.ListFilter) ([]*collection.Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*collection.Collection{}
	for _, c := range m.byID {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		if f.OwnerGcid != "" && c.OwnerGcid != f.OwnerGcid {
			continue
		}
		if f.Visibility != "" && c.Visibility != f.Visibility {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type memPub struct {
	mu     sync.Mutex
	events []collection.Event
	err    error
}

func (m *memPub) Publish(_ collection.Context, e collection.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, e)
	return nil
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestService_Create_PersistsAndPublishes(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	pub := &memPub{}
	svc := collection.NewService(repo, pub, nil, nil, nil)

	c, err := svc.Create(context.Background(), collection.CreateInput{
		TenantID:    tenantA,
		OwnerGcid:   ownerA,
		Title:       "My set",
		TraceParent: "00-trace-span-01",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.CollectionID == "" {
		t.Errorf("CollectionID empty")
	}
	if repo.saved != 1 {
		t.Errorf("saved = %d; want 1", repo.saved)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events len = %d; want 1", len(pub.events))
	}
	got := pub.events[0]
	if got.Type != collection.EventTypeCollectionCreated {
		t.Errorf("event type = %q; want created.v1", got.Type)
	}
	if got.EventID == "" || got.IdempotencyKey == "" {
		t.Errorf("event envelope missing required IDs: %+v", got)
	}
	if got.TenantID != tenantA || got.Gcid != ownerA {
		t.Errorf("event tenant/gcid mismatch: %+v", got)
	}
	if got.TraceParent != "00-trace-span-01" {
		t.Errorf("event TraceParent = %q; want propagated", got.TraceParent)
	}
	if got.SourceProject == "" || got.SourceService == "" || got.SchemaVersion == 0 {
		t.Errorf("envelope mandatory fields missing: %+v", got)
	}
}

func TestService_AddAtom_LookupReturns404OnMissingAtom(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c, _ := f.svc.Create(context.Background(), collection.CreateInput{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "x",
	})
	_, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		AtomID:       "01970000-0000-7000-a000-000000000001",
	})
	if !errors.Is(err, collection.ErrAtomDoesNotExist) {
		t.Errorf("err = %v; want ErrAtomDoesNotExist", err)
	}
}

// Was TestService_AddAtom_CrossTenantPublicOK — the cross-tenant-on-PUBLIC
// premise is retired with PUBLIC itself (ADR-233 D7; the inverted rule is
// asserted by TestAddAtom_CrossTenantAtomIsInvisibleToTheTenantScopedGate). What
// was UNIQUE to this test — that a successful add emits atom_added.v1 after
// created.v1 — survives here, on the same-tenant entitled path that actually
// exists.
func TestService_AddAtom_PublishesAtomAddedEvent(t *testing.T) {
	t.Parallel()

	const atomID = "01970000-0000-7000-a000-000000000001"
	f := newFixture(t)
	f.facts.facts[atomID] = ownFact(atomID)

	c, _ := f.svc.Create(context.Background(), collection.CreateInput{
		TenantID:  tenantA,
		OwnerGcid: ownerA,
		Title:     "My mix",
	})
	updated, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		AtomID:       atomID,
	})
	if err != nil {
		t.Fatalf("AddAtom: %v", err)
	}
	if len(updated.Atoms) != 1 {
		t.Errorf("len(Atoms) = %d; want 1", len(updated.Atoms))
	}
	// 2 events: created + atom_added
	if len(f.pub.events) != 2 {
		t.Fatalf("events len = %d; want 2", len(f.pub.events))
	}
	if f.pub.events[1].Type != collection.EventTypeCollectionAtomAdded {
		t.Errorf("events[1].Type = %q; want atom_added.v1", f.pub.events[1].Type)
	}
	if f.pub.events[1].AtomID != atomID {
		t.Errorf("events[1].AtomID = %q; want %q", f.pub.events[1].AtomID, atomID)
	}
}

// Cross-tenant refusal does not depend on the COLLECTION's audience: a private
// collection refuses another tenant's atom for exactly the same reason the
// widest one does (TestAddAtom_CrossTenantAtomIsInvisibleToTheTenantScopedGate) —
// the tenant-scoped gate read cannot see it.
//
// Was TestService_AddAtom_CrossTenantPrivateRejected, asserting
// ErrCrossTenantAtomNotPermitted. That sentinel required AddAtom to first ask an
// UNTENANTED lookup who owned the atom — the read that RLS answered with 22P02 in
// production. The refusal survives; the untenanted read does not (ADR-233 D7).
func TestService_AddAtom_CrossTenantAtomRefusedOnPrivateCollection(t *testing.T) {
	t.Parallel()

	const otherTenant = "01970000-0000-7000-8000-000000000099"
	const atomID = "01970000-0000-7000-a000-000000000001"
	f := newFixture(t)
	f.facts.tenantByID[atomID] = otherTenant // owned by another tenant
	f.facts.facts[atomID] = ownFact(atomID)  // and otherwise perfectly entitled

	c, _ := f.svc.Create(context.Background(), collection.CreateInput{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "p",
	})
	updated, err := f.svc.AddAtom(context.Background(), collection.AddAtomInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
		AtomID:       atomID,
	})
	if err == nil {
		t.Fatalf("a cross-tenant atom was ACCEPTED into a private collection (got %+v); want refusal", updated)
	}
	if !errors.Is(err, collection.ErrAtomDoesNotExist) {
		t.Errorf("err = %v; want ErrAtomDoesNotExist — the tenant-scoped gate cannot see another tenant's atom", err)
	}
}

func TestService_Update_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	pub := &memPub{}
	svc := collection.NewService(repo, pub, nil, nil, nil)

	c, _ := svc.Create(context.Background(), collection.CreateInput{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "x",
	})
	newTitle := "by other"
	_, err := svc.Update(context.Background(), collection.UpdateInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    "01970000-0000-7000-9000-000000000999", // not the owner
		Params:       collection.UpdateParams{Title: &newTitle},
	})
	if !errors.Is(err, collection.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
}

func TestService_Delete_PublishesDeletedEvent(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	pub := &memPub{}
	svc := collection.NewService(repo, pub, nil, nil, nil)

	c, _ := svc.Create(context.Background(), collection.CreateInput{
		TenantID: tenantA, OwnerGcid: ownerA, Title: "x",
	})
	err := svc.Delete(context.Background(), collection.DeleteInput{
		TenantID:     tenantA,
		CollectionID: c.CollectionID,
		OwnerGcid:    ownerA,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(pub.events) != 2 {
		t.Fatalf("events len = %d; want 2 (created + deleted)", len(pub.events))
	}
	if pub.events[1].Type != collection.EventTypeCollectionDeleted {
		t.Errorf("events[1].Type = %q; want deleted.v1", pub.events[1].Type)
	}
	if pub.events[1].DeletedByGcid != ownerA {
		t.Errorf("DeletedByGcid = %q; want %q", pub.events[1].DeletedByGcid, ownerA)
	}
}
