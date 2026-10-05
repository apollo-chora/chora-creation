// atom_lifecycle_events_test.go — ADR-217 Debt 3. Verifies the bare atom CRUD
// handlers emit the atom-count projection's feed events: POST /api/atoms →
// chora.creation.atom.created.v1, DELETE /api/atoms/{id} →
// chora.creation.atom.archived.v1. The tenancy atom-count subscriber folds
// these (by envelope tenant_id) into per-tenant ChildTenantSummary.atom_count.
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

func TestCreateAtom_EmitsAtomCreatedOutboxEvent(t *testing.T) {
	t.Parallel()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               inmem.NewAtomRepository(),
		AtomEventPublisher: pub,
	})

	body := map[string]any{
		"stem":    "What gas do plants release during photosynthesis?",
		"subject": "biology",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	atomID, _ := resp["atom_id"].(string)

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1 atom.created emit", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomCreated {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomCreated)
	}
	if atomID != "" && ev.AtomID != atomID {
		t.Errorf("event atom_id = %q; want %q (response atom_id)", ev.AtomID, atomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("event tenant_id = %q; want %q", ev.TenantID, tenantA)
	}
}

func TestDeleteAtom_EmitsAtomArchivedOutboxEvent(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	pub := &recordingPub{}
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "atom to archive", Body: "body", Mode: atom.ModeStraightUp,
	})
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		AtomEventPublisher: pub,
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodDelete, "/api/atoms/"+a.AtomID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s", w.Code, w.Body.String())
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1 atom.archived emit", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomArchived {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomArchived)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("event atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("event tenant_id = %q; want %q", ev.TenantID, tenantA)
	}
	// The archiving actor rides the envelope gcid (== archived_by_gcid).
	if ev.Gcid != gcidA {
		t.Errorf("event gcid (archived_by) = %q; want %q", ev.Gcid, gcidA)
	}
}
