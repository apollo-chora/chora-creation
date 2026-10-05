// Tests for the federated-closure-saga subscriber.
//
// Per Tier 3 D11 + the federated saga contract:
//
//	chora.closure.requested.v1 (closure orchestrator)
//	  → chora.creation.pii.pseudonymise.requested.v1 (per-domain fan-out)
//	    → chora-creation ClosureSubscriber.Handle
//	      → apply PII_Closure_Map.yaml fields to repo
//	      → emit chora.creation.account.pseudonymised.v1
package events_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-creation/internal/adapter/events"
	"github.com/apollo-chora/chora-creation/internal/config"
)

const (
	testTenantID   = "01970000-0000-7000-8000-0000000000aa"
	testGCID       = "01970000-0000-7000-8000-0000000000bb"
	testSagaID     = "01970000-0000-7000-8000-0000000000cc"
	testTrace      = "00-1234567890abcdef1234567890abcdef-1234567890abcdef-01"
	testTracestate = "vendor=test"
)

func newPIIMap(domainName string) *config.PIIClosureMap {
	pii := &config.PIIClosureMap{
		Domain:  domainName,
		Version: "1.0",
		FieldsToTokenize: []config.TableSpec{
			{
				Table: "t1",
				Columns: []config.ColumnSpec{
					{Column: "c1", Strategy: "tombstone_string", Value: "Former member"},
				},
			},
		},
		OnCreatorClosure: config.CreatorClosureSpec{
			Strategy:         "tokenise_authorship_keep_atom",
			ShowAuthorshipAs: "Former member",
		},
	}
	pii.AGIDApplicable = (domainName == "chora_a2a")
	return pii
}

func TestClosureSubscriber_HappyPath(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)

	sub := events.NewClosureSubscriber(repo, pub, newPIIMap("chora_creation"), nil)

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
		Traceparent: testTrace, Tracestate: testTracestate,
	}
	if err := sub.Handle(context.Background(), payload); err != nil {
		t.Fatalf("handle: %v", err)
	}

	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 1 {
		t.Fatalf("expected 1 completed event; got %d", len(emitted))
	}
	if emitted[0].TenantID != testTenantID {
		t.Fatalf("envelope tenant: %q", emitted[0].TenantID)
	}
	got := emitted[0].Payload
	if got["saga_id"] != testSagaID {
		t.Fatalf("saga_id: %v", got["saga_id"])
	}
	if got["domain"] != "creation" {
		t.Fatalf("domain: %v", got["domain"])
	}
	if got["chora_imda_dimension"] != "accountability" {
		t.Fatalf("imda dimension: %v", got["chora_imda_dimension"])
	}
	if got["imda_lifecycle_stage"] != "runtime" {
		t.Fatalf("imda lifecycle: %v", got["imda_lifecycle_stage"])
	}
	pseudonymised, err := repo.IsPseudonymised(context.Background(), testTenantID, testGCID)
	if err != nil {
		t.Fatalf("IsPseudonymised: %v", err)
	}
	if !pseudonymised {
		t.Fatalf("subject not pseudonymised in repo")
	}
}

func TestClosureSubscriber_IdempotentReplay(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)

	sub := events.NewClosureSubscriber(repo, pub, newPIIMap("chora_creation"), nil)

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}
	if err := sub.Handle(context.Background(), payload); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	if err := sub.Handle(context.Background(), payload); err != nil {
		t.Fatalf("replay should be no-op: %v", err)
	}
	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 1 {
		t.Fatalf("expected 1 completed event after replay; got %d", len(emitted))
	}
}

func TestClosureSubscriber_AGIDHandling(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	pii := newPIIMap("chora_creation")

	sub := events.NewClosureSubscriber(repo, pub, pii, nil)

	agid := "0197a000-0000-0000-0000-000000000001"
	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: agid, TenantID: testTenantID, SubjectKind: "agid",
	}
	err := sub.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("handle should not fail for AGID: %v", err)
	}
	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 1 {
		t.Fatalf("expected 1 ack event for AGID; got %d", len(emitted))
	}
	// Domain-specific: AGID-applicable domains process; non-applicable skip.
	if pii.AGIDApplicable {
		if emitted[0].Payload["status"] != "ok" {
			t.Fatalf("AGID-applicable domain should ack ok; got %v", emitted[0].Payload["status"])
		}
	} else {
		if emitted[0].Payload["status"] != "skipped_agid_closure" {
			t.Fatalf("non-AGID domain should ack skipped; got %v", emitted[0].Payload["status"])
		}
	}
}

func TestClosureSubscriber_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	sub := events.NewClosureSubscriber(repo, pub, newPIIMap("chora_creation"), nil)
	ctx := context.Background()

	tests := map[string]events.PseudonymiseRequestedPayload{
		"empty_saga_id": {Gcid: testGCID, TenantID: testTenantID},
		"empty_gcid":    {SagaID: testSagaID, TenantID: testTenantID},
		"empty_tenant":  {SagaID: testSagaID, Gcid: testGCID},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			err := sub.Handle(ctx, p)
			if err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
			if !strings.Contains(err.Error(), "required") {
				t.Fatalf("expected 'required' error for %s; got %v", name, err)
			}
		})
	}
}

func TestClosureSubscriber_TopicConstants(t *testing.T) {
	t.Parallel()
	if events.TopicPseudonymiseRequested != "chora.creation.pii.pseudonymise.requested.v1" {
		t.Fatalf("requested topic: %q", events.TopicPseudonymiseRequested)
	}
	if events.TopicPseudonymiseCompleted != "chora.creation.account.pseudonymised.v1" {
		t.Fatalf("completed topic: %q", events.TopicPseudonymiseCompleted)
	}
	if events.TopicPseudonymiseFailed != "chora.creation.pii.pseudonymise.failed.v1" {
		t.Fatalf("failed topic: %q", events.TopicPseudonymiseFailed)
	}
}

func TestClosureSubscriber_RepoFailureEmitsCompensation(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SetFailNext(true)

	sub := events.NewClosureSubscriber(repo, pub, newPIIMap("chora_creation"), nil)

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}
	err := sub.Handle(context.Background(), payload)
	if err == nil {
		t.Fatalf("expected error from repo")
	}
	failed := pub.ClosureRecordedByTopic(events.TopicPseudonymiseFailed)
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed event; got %d", len(failed))
	}
}

func TestClosureSubscriber_NilGuards(t *testing.T) {
	t.Parallel()
	if err := (&events.ClosureSubscriber{}).Handle(context.Background(), events.PseudonymiseRequestedPayload{}); err == nil {
		t.Fatalf("nil sub should error")
	}
}

func TestClosureSubscriber_SubscribedTopic(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	sub := events.NewClosureSubscriber(repo, pub, &config.PIIClosureMap{Domain: "x"}, nil)
	if got := sub.SubscribedTopic(); got != events.TopicPseudonymiseRequested {
		t.Fatalf("SubscribedTopic = %q want %q", got, events.TopicPseudonymiseRequested)
	}
}

func TestInMemoryClosureRepo_IsPseudonymised_TenantScoped(t *testing.T) {
	t.Parallel()
	repo := events.NewInMemoryClosureRepo()
	ctx := context.Background()
	const otherTenant = "01970000-0000-7000-8000-0000000000dd"

	// Pseudonymise the GCID under testTenantID only.
	if _, err := repo.Pseudonymise(ctx, testTenantID, testGCID, nil); err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}

	got, err := repo.IsPseudonymised(ctx, testTenantID, testGCID)
	if err != nil {
		t.Fatalf("IsPseudonymised(testTenantID): %v", err)
	}
	if !got {
		t.Fatalf("expected pseudonymised=true for (testTenantID, testGCID)")
	}

	// The SAME gcid under a DIFFERENT tenant must NOT read as pseudonymised
	// — a pre-fix cross-tenant collision (the map keyed on gcid alone).
	got, err = repo.IsPseudonymised(ctx, otherTenant, testGCID)
	if err != nil {
		t.Fatalf("IsPseudonymised(otherTenant): %v", err)
	}
	if got {
		t.Fatalf("cross-tenant collision: gcid pseudonymised in %q leaked into %q", testTenantID, otherTenant)
	}
}

// W1.7 (2026-05-12) — inbox dedup tests proving chaos scenario (h)
// "idempotency under duplicates" cannot fire double side-effects across
// the failure modes the in-process map dedup couldn't survive:
//
//   1. Pod-death: dedup state must persist outside the subscriber
//      instance. Tested by swapping the subscriber instance between the
//      first delivery and the duplicate redelivery, sharing the same
//      Store.
//   2. Multi-replica: replica A and replica B see the same event; only
//      one of them runs the handler body. Tested by running two
//      subscribers backed by the same Store concurrently.
//   3. TTL expiry: a token outside the TTL allows reprocessing (correct
//      semantics — the saga is allowed to re-run if explicitly replayed
//      after grace window).

func TestClosureSubscriber_InboxSurvivesSubscriberRecreation(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)
	piiMap := newPIIMap("chora_creation")

	// Shared inbox — same Store crosses the pod-restart boundary.
	inbox := idempotent.NewMemoryStore()

	sub1 := events.NewClosureSubscriber(repo, pub, piiMap, inbox)
	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}
	if err := sub1.Handle(context.Background(), payload); err != nil {
		t.Fatalf("first delivery on sub1: %v", err)
	}

	// Simulate pod restart: drop sub1, recreate sub2 with the SAME inbox.
	sub2 := events.NewClosureSubscriber(repo, pub, piiMap, inbox)
	if err := sub2.Handle(context.Background(), payload); err != nil {
		t.Fatalf("redelivery on sub2 (post-restart): %v", err)
	}

	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 1 {
		t.Fatalf("expected exactly 1 completed event across pod-restart redelivery; got %d", len(emitted))
	}
}

func TestClosureSubscriber_InboxFreshStoreReprocessesEvent(t *testing.T) {
	t.Parallel()
	// Negative-control of the above: WITHOUT a shared store (each subscriber
	// has its own MemoryStore) the inbox CANNOT dedupe across restarts —
	// which is exactly the failure mode the production PostgresStore wiring
	// is designed to fix. This test documents the contract explicitly:
	// chaos test must use a shared store (Postgres in prod, shared
	// MemoryStore in test) to prove dedup.
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)
	piiMap := newPIIMap("chora_creation")

	sub1 := events.NewClosureSubscriber(repo, pub, piiMap, idempotent.NewMemoryStore())
	sub2 := events.NewClosureSubscriber(repo, pub, piiMap, idempotent.NewMemoryStore())

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}
	if err := sub1.Handle(context.Background(), payload); err != nil {
		t.Fatalf("sub1 handle: %v", err)
	}
	if err := sub2.Handle(context.Background(), payload); err != nil {
		t.Fatalf("sub2 handle: %v", err)
	}
	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 2 {
		t.Errorf("FRESH-store negative control: expected 2 completed events (no shared dedup); got %d", len(emitted))
	}
}

func TestClosureSubscriber_InboxConcurrentReplicas(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)
	piiMap := newPIIMap("chora_creation")

	// Two subscribers (two replicas) sharing one inbox.
	inbox := idempotent.NewMemoryStore()
	subA := events.NewClosureSubscriber(repo, pub, piiMap, inbox)
	subB := events.NewClosureSubscriber(repo, pub, piiMap, inbox)

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = subA.Handle(context.Background(), payload) }()
	go func() { defer wg.Done(); _ = subB.Handle(context.Background(), payload) }()
	wg.Wait()

	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 1 {
		t.Fatalf("multi-replica: expected exactly 1 completed event; got %d", len(emitted))
	}
}

func TestClosureSubscriber_InboxTTLExpiryReprocesses(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	repo := events.NewInMemoryClosureRepo()
	repo.SeedSubject(testTenantID, testGCID)
	piiMap := newPIIMap("chora_creation")

	inbox := idempotent.NewMemoryStore()
	sub := events.NewClosureSubscriber(repo, pub, piiMap, inbox).WithInboxTTL(50 * time.Millisecond)

	payload := events.PseudonymiseRequestedPayload{
		SagaID: testSagaID, Gcid: testGCID, TenantID: testTenantID,
	}
	if err := sub.Handle(context.Background(), payload); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	// Advance the inbox clock past the TTL.
	inbox.Advance(100 * time.Millisecond)
	if err := sub.Handle(context.Background(), payload); err != nil {
		t.Fatalf("second handle (post-TTL): %v", err)
	}
	emitted := pub.ClosureRecordedByTopic(events.TopicPseudonymiseCompleted)
	if len(emitted) != 2 {
		t.Fatalf("TTL-expiry: expected 2 completed events; got %d", len(emitted))
	}
}

func TestInMemoryClosurePublisher_RecordedAll(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryClosurePublisher()
	if err := pub.Publish("chora.test.x.v1", "t", "g", "tp", map[string]interface{}{"k": "v"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := pub.Publish("", "t", "g", "tp", map[string]interface{}{}); err == nil {
		t.Fatalf("expected error for empty topic")
	}
	all := pub.ClosureRecorded()
	if len(all) != 1 {
		t.Fatalf("expected 1 recorded; got %d", len(all))
	}
}
