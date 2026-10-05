package events_test

import (
	"context"
	"testing"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-creation/internal/adapter/events"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// stubRecorder captures the rows passed to Record so the test can assert
// the outbox row shape without spinning up Postgres.
type stubRecorder struct {
	rows []*cgcoutbox.Row
}

func (s *stubRecorder) Record(_ context.Context, _ cgcoutbox.Tx, row *cgcoutbox.Row) error {
	s.rows = append(s.rows, row)
	return nil
}

func (s *stubRecorder) Claim(_ context.Context, _ int) ([]*cgcoutbox.Row, error) {
	return nil, nil
}
func (s *stubRecorder) MarkPublished(_ context.Context, _ []string) error              { return nil }
func (s *stubRecorder) MarkFailed(_ context.Context, _ string, _ string, _ bool) error { return nil }

func TestCloudPublisher_Publish_WritesOutboxRow(t *testing.T) {
	rec := &stubRecorder{}
	p := events.NewCloudPublisher(rec)

	now := time.Now().UTC()
	ev := atom.Event{
		EventID:        "01970000-0000-7000-8000-000000000001",
		IdempotencyKey: "01970000-0000-7000-8000-000000000001",
		Type:           atom.EventTypeAtomCreated,
		TenantID:       "22222222-2222-7222-8222-222222222222",
		Gcid:           "00000000-0000-7000-8000-000000001002",
		OccurredAt:     now.Format(time.RFC3339Nano),
		PublishedAt:    now.Format(time.RFC3339Nano),
		TraceParent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-local",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
		AtomID:         "00000000-0000-7000-8000-000000005e10",
		CourseID:       "33333333-3333-7333-8333-333333333333",
		Title:          "What is a Product Owner?",
	}

	if err := p.Publish(context.Background(), ev); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	row := rec.rows[0]
	if row.Topic != string(atom.EventTypeAtomCreated) {
		t.Errorf("topic: got %q want %q", row.Topic, atom.EventTypeAtomCreated)
	}
	if row.AggregateType != "atom" {
		t.Errorf("aggregate_type: got %q want %q", row.AggregateType, "atom")
	}
	if row.AggregateID != ev.AtomID {
		t.Errorf("aggregate_id: got %q want %q", row.AggregateID, ev.AtomID)
	}
	if row.Status != cgcoutbox.StatusPending {
		t.Errorf("status: got %q want pending", row.Status)
	}
	if row.Envelope.TenantID != ev.TenantID {
		t.Errorf("envelope tenant: got %q want %q", row.Envelope.TenantID, ev.TenantID)
	}
	if row.Envelope.Traceparent != ev.TraceParent {
		t.Errorf("envelope traceparent: got %q want %q", row.Envelope.Traceparent, ev.TraceParent)
	}
	// envelope round-trips successfully through the outbox JSON encoder
	if _, err := cgcoutbox.MarshalEnvelopeJSON(row.Envelope); err != nil {
		t.Errorf("envelope marshal: %v", err)
	}
	// envelope satisfies the chora-go-common validator
	if err := cgcenvelope.Validate(row.Envelope); err != nil {
		t.Errorf("envelope validate: %v", err)
	}
}
