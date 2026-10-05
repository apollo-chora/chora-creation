// Package events is the EventPublisher port adapter.
//
// MVP shipping a goroutine-safe in-memory publisher per the Phyllis MVP brief
// — production will swap this for a Cloud Pub/Sub adapter that wraps the
// domain Event in a Protobuf chora.creation.atom.{created,revised}.v1
// message with chora.common.v1.EventEnvelope per
// chora-contracts/proto/events/creation/atom.proto.
//
// The in-memory publisher is also the test double — handler tests assert
// the event count + envelope mandatory fields.
package events

import (
	"context"
	"sync"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// InMemoryPublisher is a goroutine-safe domain.EventPublisher.
type InMemoryPublisher struct {
	mu     sync.Mutex
	events []atom.Event
}

// NewInMemoryPublisher constructs a fresh publisher.
func NewInMemoryPublisher() *InMemoryPublisher {
	return &InMemoryPublisher{events: nil}
}

// Publish records the event in memory. Always succeeds.
func (p *InMemoryPublisher) Publish(_ context.Context, e atom.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.PublishedAt == "" {
		e.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	p.events = append(p.events, e)
	return nil
}

// Snapshot returns a copy of the published events.
func (p *InMemoryPublisher) Snapshot() []atom.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]atom.Event, len(p.events))
	copy(out, p.events)
	return out
}

// Count returns how many events have been published.
func (p *InMemoryPublisher) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}
