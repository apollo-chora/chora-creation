// publisher_orphan_created_test.go — ADR-229 Amendment A1 (CHO-2132): the
// outbox publisher must map atom.Event → the orphan_created payload keys the
// binary encoder pins (orphan_atom_id / orphaned_from_atom_id /
// source_revision_id / author_gcid / trigger / orphaned_at), and the
// DETERMINISTIC idempotency key must dedupe re-publishes at the store
// (the DB-level "no duplicate orphan_created event" enforcement).
package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func orphanForPublisherTest(t *testing.T) *atom.LearningAtom {
	t.Helper()
	src := &atom.LearningAtom{
		AtomID:    "01971a90-aaaa-7000-8000-00000000a001",
		TenantID:  "01971a90-aaaa-7111-8111-000000000001",
		Gcid:      "01971a90-aaaa-7000-8000-000000000aa1",
		Title:     "Fractions",
		Body:      "b",
		Mode:      atom.ModeStraightUp,
		Status:    atom.StatusPublished,
		Stem:      "1/2 + 1/4?",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	o, err := atom.CloneOrphan(atom.OrphanCloneParams{
		Source:           src,
		SourceRevisionID: "01971a90-aaaa-7000-8000-00000000f001",
	})
	if err != nil {
		t.Fatalf("CloneOrphan: %v", err)
	}
	return o
}

func TestOutboxPublisher_Publish_OrphanCreated_BinaryRoundTrip(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
	})

	o := orphanForPublisherTest(t)
	ev := atom.NewAtomOrphanCreatedEvent(o, atom.OrphanTriggerNarrowed, "00-tp-01", "")
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != "chora.creation.atom.orphan_created.v1" {
		t.Fatalf("row.Topic = %q", row.Topic)
	}
	if row.AggregateID != o.AtomID {
		t.Errorf("aggregate id = %q, want the orphan %q", row.AggregateID, o.AtomID)
	}
	wantKey := o.OrphanedFromAtomID + ":orphan_created:" + o.OrphanedSourceRevisionID
	if row.IdempotencyKey != wantKey {
		t.Errorf("idempotency key = %q, want %q", row.IdempotencyKey, wantKey)
	}

	var m creationv1.AtomOrphanCreated
	if err := proto.Unmarshal(row.Payload, &m); err != nil {
		t.Fatalf("payload is not binary AtomOrphanCreated: %v", err)
	}
	if m.GetOrphanAtomId() != o.AtomID {
		t.Errorf("orphan_atom_id = %q, want %q", m.GetOrphanAtomId(), o.AtomID)
	}
	if m.GetOrphanedFromAtomId() != o.OrphanedFromAtomID {
		t.Errorf("orphaned_from_atom_id = %q", m.GetOrphanedFromAtomId())
	}
	if m.GetSourceRevisionId() != o.OrphanedSourceRevisionID {
		t.Errorf("source_revision_id = %q", m.GetSourceRevisionId())
	}
	if m.GetAuthorGcid() != o.Gcid {
		t.Errorf("author_gcid = %q, want the original author %q", m.GetAuthorGcid(), o.Gcid)
	}
	if m.GetTrigger() != "narrowed" {
		t.Errorf("trigger = %q", m.GetTrigger())
	}
	if m.GetOrphanedAt() == nil {
		t.Errorf("orphaned_at missing")
	}
	if m.GetEnvelope().GetTenantId() != o.TenantID {
		t.Errorf("envelope tenant = %q", m.GetEnvelope().GetTenantId())
	}
}

// A crash-redelivery re-publish (fresh EventID, same singleton) must dedupe
// on the deterministic key — never a second orphan_created row.
func TestOutboxPublisher_Publish_OrphanCreated_DedupesOnSingletonKey(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, SourceProject: "chora-local"})

	o := orphanForPublisherTest(t)
	if err := pub.Publish(context.Background(), atom.NewAtomOrphanCreatedEvent(o, atom.OrphanTriggerNarrowed, "", "")); err != nil {
		t.Fatalf("Publish 1: %v", err)
	}
	err := pub.Publish(context.Background(), atom.NewAtomOrphanCreatedEvent(o, atom.OrphanTriggerNarrowed, "", ""))
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Fatalf("second Publish err = %v; want ErrDuplicateIdempotencyKey (the singleton-event backstop)", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d after dedupe; want 1", len(rows))
	}
}
