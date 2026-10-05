// Package inmem provides in-memory implementations of the Content Creation
// service ports — used in tests and the M11 skeleton; replaced by Cloud SQL
// + Pub/Sub publishers in M12.
package inmem

import (
	"context"
	"sync"
	"time"
)

// Envelope mirrors the chora.common.v1.EventEnvelope mandatory fields per
// chora-contracts/proto/common/envelope.proto. The proto message is
// generated under chora-contracts/gen/go (deferred until M11.4); this
// struct is the in-memory stand-in.
type Envelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	Gcid           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
	CorrelationID  string
	CausationID    string
}

// RecordedEvent is one captured publish call.
type RecordedEvent struct {
	Topic    string
	Envelope Envelope
	Payload  any
}

// EventRecorder is the in-memory EventPublisher port. Production code will
// substitute a Cloud Pub/Sub publisher backed by chora-contracts proto
// schemas + Schema Registry validation.
type EventRecorder struct {
	mu     sync.Mutex
	events []RecordedEvent
}

// NewEventRecorder constructs an empty recorder.
func NewEventRecorder() *EventRecorder {
	return &EventRecorder{events: make([]RecordedEvent, 0, 8)}
}

// Publish appends an event. Always succeeds.
func (r *EventRecorder) Publish(_ context.Context, topic string, env Envelope, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, RecordedEvent{Topic: topic, Envelope: env, Payload: payload})
	return nil
}

// Events returns a snapshot copy of all recorded events.
func (r *EventRecorder) Events() []RecordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecordedEvent, len(r.events))
	copy(out, r.events)
	return out
}
