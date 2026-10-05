// AI Assist subscriber tests per audit §3.1.
//
// Subscribes to events emitted by chora-ai-kernel-orchestrator (S3.4) when the
// AI Assist crew finalises atoms. Topic name (verified in audit):
//
//	chora.ai_kernel.crew.atoms_ready.v1
//
// Persists atoms with governance_status = approved | remediated (from the
// Reporter's Guardrail Screen verdict). Idempotent on event_id + atom_hash.
//
// Emits chora.creation.atom.created.v1 per atom.
package atom_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

type recPub struct{ events []atom.Event }

func (p *recPub) Publish(_ context.Context, e atom.Event) error {
	p.events = append(p.events, e)
	return nil
}

func TestAtomsReadySubscriber_PersistsApprovedBatch(t *testing.T) {
	t.Parallel()

	repo := inmem.NewAtomRepository()
	pub := &recPub{}
	sub := atom.NewAtomsReadySubscriber(repo, pub, nil)

	ev := atom.AtomsReadyEvent{
		EventID:        "01970000-0000-7000-eeee-000000000001",
		IdempotencyKey: "01970000-0000-7000-eeee-000000000001",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		Gcid:           "01970000-0000-7000-9000-000000000001",
		CourseID:       "01970000-0000-7000-7000-000000000001",
		Traceparent:    "00-aabbccddeeff00112233445566778899-0011223344556677-01",
		ContentType:    "mcq",
		Difficulty:     3,
		Atoms: []atom.AtomsReadyAtom{
			{Title: "What is sprint planning?", Body: "Sprint planning is...", GovernanceStatus: "approved"},
			{Title: "What is a stand-up?", Body: "A stand-up is...", GovernanceStatus: "approved"},
			{Title: "What is a retro?", Body: "A retro is...", GovernanceStatus: "remediated"},
		},
	}

	res, err := sub.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(res.PersistedAtomIDs) != 3 {
		t.Errorf("persisted = %d; want 3", len(res.PersistedAtomIDs))
	}
	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 3 {
		t.Errorf("created events = %d; want 3", created)
	}
}

func TestAtomsReadySubscriber_IsIdempotentOnEventID(t *testing.T) {
	t.Parallel()

	repo := inmem.NewAtomRepository()
	pub := &recPub{}
	sub := atom.NewAtomsReadySubscriber(repo, pub, nil)

	ev := atom.AtomsReadyEvent{
		EventID:        "01970000-0000-7000-eeee-000000000099",
		IdempotencyKey: "01970000-0000-7000-eeee-000000000099",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		Gcid:           "01970000-0000-7000-9000-000000000001",
		CourseID:       "01970000-0000-7000-7000-000000000001",
		ContentType:    "mcq",
		Difficulty:     3,
		Atoms: []atom.AtomsReadyAtom{
			{Title: "Q1", Body: "A1", GovernanceStatus: "approved"},
			{Title: "Q2", Body: "A2", GovernanceStatus: "approved"},
		},
	}

	r1, err := sub.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if len(r1.PersistedAtomIDs) != 2 {
		t.Fatalf("first run persisted = %d", len(r1.PersistedAtomIDs))
	}

	// Re-deliver the SAME event — must not double-persist or double-publish.
	r2, err := sub.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if len(r2.PersistedAtomIDs) != 0 {
		t.Errorf("re-delivery should persist 0 new atoms (was %d)", len(r2.PersistedAtomIDs))
	}
	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 2 {
		t.Errorf("total atom.created.v1 events = %d; want 2 (no double-publish)", created)
	}
}

func TestAtomsReadySubscriber_SkipsBlockedAtoms(t *testing.T) {
	t.Parallel()

	repo := inmem.NewAtomRepository()
	pub := &recPub{}
	sub := atom.NewAtomsReadySubscriber(repo, pub, nil)

	ev := atom.AtomsReadyEvent{
		EventID:        "01970000-0000-7000-eeee-000000000100",
		IdempotencyKey: "01970000-0000-7000-eeee-000000000100",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		Gcid:           "01970000-0000-7000-9000-000000000001",
		CourseID:       "01970000-0000-7000-7000-000000000001",
		ContentType:    "mcq",
		Difficulty:     3,
		Atoms: []atom.AtomsReadyAtom{
			{Title: "OK", Body: "B", GovernanceStatus: "approved"},
			{Title: "Blocked", Body: "X", GovernanceStatus: "blocked"},
		},
	}
	res, err := sub.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(res.PersistedAtomIDs) != 1 {
		t.Errorf("persisted = %d; want 1 (blocked atom skipped)", len(res.PersistedAtomIDs))
	}
}

func TestAtomsReadySubscriber_RejectsInvalidEvent(t *testing.T) {
	t.Parallel()

	repo := inmem.NewAtomRepository()
	pub := &recPub{}
	sub := atom.NewAtomsReadySubscriber(repo, pub, nil)

	cases := []struct {
		name string
		ev   atom.AtomsReadyEvent
	}{
		{"missing event_id", atom.AtomsReadyEvent{TenantID: "t", CourseID: "c", Gcid: "g", ContentType: "mcq", Difficulty: 1}},
		{"missing tenant", atom.AtomsReadyEvent{EventID: "e", IdempotencyKey: "e", CourseID: "c", Gcid: "g", ContentType: "mcq", Difficulty: 1}},
		{"missing course", atom.AtomsReadyEvent{EventID: "e", IdempotencyKey: "e", TenantID: "t", Gcid: "g", ContentType: "mcq", Difficulty: 1}},
		{"invalid content_type", atom.AtomsReadyEvent{EventID: "e", IdempotencyKey: "e", TenantID: "t", CourseID: "c", Gcid: "g", ContentType: "freeform", Difficulty: 1}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := sub.Handle(context.Background(), tc.ev); err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

// AtomsReadyEvent emits IMDA D1 (accountability) evidence per ADR-141 — the
// resulting atom.created.v1 events should carry the canonical label so the
// O+ governance dashboard can audit the AI-assisted authoring trail.
func TestAtomsReadySubscriber_EmitsIMDAEvidence(t *testing.T) {
	t.Parallel()

	repo := inmem.NewAtomRepository()
	pub := &recPub{}
	sub := atom.NewAtomsReadySubscriber(repo, pub, nil)

	ev := atom.AtomsReadyEvent{
		EventID:        "01970000-0000-7000-eeee-000000000200",
		IdempotencyKey: "01970000-0000-7000-eeee-000000000200",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		Gcid:           "01970000-0000-7000-9000-000000000001",
		CourseID:       "01970000-0000-7000-7000-000000000001",
		ContentType:    "mcq",
		Difficulty:     3,
		Atoms: []atom.AtomsReadyAtom{
			{Title: "Q", Body: "B", GovernanceStatus: "approved"},
		},
	}
	if _, err := sub.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(pub.events) == 0 {
		t.Fatalf("no events published")
	}
	got := pub.events[0]
	if got.ChoraImdaDimension != "accountability" {
		t.Errorf("chora_imda_dimension = %q; want accountability (per ADR-141)", got.ChoraImdaDimension)
	}
	if got.ImdaLifecycleStage != "runtime" {
		t.Errorf("imda_lifecycle_stage = %q; want runtime", got.ImdaLifecycleStage)
	}
}

// -----------------------------------------------------------------------------
// W1.8 chaos-readiness tests — mirror the W1.7 closure_subscriber pattern.
// Verify that the inbox idempotent.Store correctly dedupes across:
//
//  1. Subscriber recreation (simulates pod-restart with same Store backing)
//  2. Fresh-store negative control (proves shared Store is required for dedup)
//  3. Concurrent replicas (multi-pod race against the same Store)
//  4. TTL expiry (re-processing after dedup-key retention window)
//
// Production wires idempotent.PostgresStore so these properties hold across
// real pod-death + multi-replica deployment; in-memory tests substitute the
// MemoryStore which exhibits the same semantics within a single process.
// -----------------------------------------------------------------------------

func makeAtomsReadyEvent(id string) atom.AtomsReadyEvent {
	return atom.AtomsReadyEvent{
		EventID:        id,
		IdempotencyKey: id,
		TenantID:       "01970000-0000-7000-8000-000000000001",
		Gcid:           "01970000-0000-7000-9000-000000000001",
		CourseID:       "01970000-0000-7000-7000-000000000001",
		ContentType:    "mcq",
		Difficulty:     3,
		Atoms: []atom.AtomsReadyAtom{
			{Title: "Q", Body: "A", GovernanceStatus: "approved"},
		},
	}
}

func TestAtomsReadySubscriber_InboxSurvivesSubscriberRecreation(t *testing.T) {
	t.Parallel()
	pub := &recPub{}
	repo := inmem.NewAtomRepository()
	// Shared inbox — crosses the pod-restart boundary.
	inbox := idempotent.NewMemoryStore()

	sub1 := atom.NewAtomsReadySubscriber(repo, pub, inbox)
	ev := makeAtomsReadyEvent("01970000-0000-7000-eeee-000000000301")
	if _, err := sub1.Handle(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	// Simulate pod restart: drop sub1, recreate sub2 with the SAME inbox.
	sub2 := atom.NewAtomsReadySubscriber(repo, pub, inbox)
	res, err := sub2.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("redelivery on sub2 (post-restart): %v", err)
	}
	if !res.AlreadyProcessed {
		t.Errorf("post-restart redelivery: AlreadyProcessed = false; want true")
	}
	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 1 {
		t.Errorf("expected exactly 1 atom.created across pod-restart redelivery; got %d", created)
	}
}

func TestAtomsReadySubscriber_InboxFreshStoreReprocesses(t *testing.T) {
	t.Parallel()
	// Negative control: fresh per-subscriber stores fail to dedup across
	// restart — exactly the failure mode the PostgresStore (or a shared
	// MemoryStore) is designed to fix.
	pub := &recPub{}
	repo := inmem.NewAtomRepository()

	sub1 := atom.NewAtomsReadySubscriber(repo, pub, idempotent.NewMemoryStore())
	sub2 := atom.NewAtomsReadySubscriber(repo, pub, idempotent.NewMemoryStore())

	ev := makeAtomsReadyEvent("01970000-0000-7000-eeee-000000000302")
	if _, err := sub1.Handle(context.Background(), ev); err != nil {
		t.Fatalf("sub1 handle: %v", err)
	}
	res, err := sub2.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("sub2 handle: %v", err)
	}
	if res.AlreadyProcessed {
		t.Errorf("FRESH-store negative control: AlreadyProcessed = true; want false (no shared dedup)")
	}
	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 2 {
		t.Errorf("expected 2 atom.created across non-shared stores; got %d", created)
	}
}

func TestAtomsReadySubscriber_InboxConcurrentReplicas(t *testing.T) {
	t.Parallel()
	pub := &recPub{}
	repo := inmem.NewAtomRepository()
	inbox := idempotent.NewMemoryStore()

	// Two subscribers (two replicas) sharing one inbox.
	subA := atom.NewAtomsReadySubscriber(repo, pub, inbox)
	subB := atom.NewAtomsReadySubscriber(repo, pub, inbox)

	ev := makeAtomsReadyEvent("01970000-0000-7000-eeee-000000000303")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = subA.Handle(context.Background(), ev) }()
	go func() { defer wg.Done(); _, _ = subB.Handle(context.Background(), ev) }()
	wg.Wait()

	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("multi-replica: expected exactly 1 atom.created; got %d", created)
	}
}

func TestAtomsReadySubscriber_InboxTTLExpiryReprocesses(t *testing.T) {
	t.Parallel()
	pub := &recPub{}
	repo := inmem.NewAtomRepository()
	inbox := idempotent.NewMemoryStore()

	sub := atom.NewAtomsReadySubscriber(repo, pub, inbox).WithInboxTTL(50 * time.Millisecond)
	ev := makeAtomsReadyEvent("01970000-0000-7000-eeee-000000000304")

	if _, err := sub.Handle(context.Background(), ev); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	// Advance the inbox clock past the TTL.
	inbox.Advance(100 * time.Millisecond)
	res, err := sub.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("second handle (post-TTL): %v", err)
	}
	if res.AlreadyProcessed {
		t.Errorf("post-TTL: AlreadyProcessed = true; want false (dedup-key expired)")
	}
	created := 0
	for _, e := range pub.events {
		if e.Type == atom.EventTypeAtomCreated {
			created++
		}
	}
	if created != 2 {
		t.Fatalf("TTL-expiry: expected 2 atom.created; got %d", created)
	}
}
