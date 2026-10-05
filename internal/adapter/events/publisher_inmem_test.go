package events_test

import (
	"context"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/events"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestInMemoryPublisher_RecordsEvents(t *testing.T) {
	t.Parallel()

	p := events.NewInMemoryPublisher()
	if p.Count() != 0 {
		t.Errorf("Count = %d; want 0", p.Count())
	}

	e := atom.Event{
		EventID:        "ev-1",
		IdempotencyKey: "ev-1",
		Type:           atom.EventTypeAtomCreated,
		TenantID:       "t1",
		Gcid:           "g1",
		AtomID:         "a1",
	}
	if err := p.Publish(context.Background(), e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if p.Count() != 1 {
		t.Errorf("Count = %d; want 1", p.Count())
	}
	snap := p.Snapshot()
	if len(snap) != 1 || snap[0].EventID != "ev-1" {
		t.Errorf("Snapshot = %+v", snap)
	}
	// PublishedAt should be set if not provided.
	if snap[0].PublishedAt == "" {
		t.Errorf("PublishedAt was not auto-populated")
	}
}

func TestInMemoryPublisher_PreservesProvidedPublishedAt(t *testing.T) {
	t.Parallel()

	p := events.NewInMemoryPublisher()
	e := atom.Event{
		EventID:     "ev-2",
		Type:        atom.EventTypeAtomRevised,
		PublishedAt: "2026-05-08T00:00:00Z",
	}
	_ = p.Publish(context.Background(), e)
	if p.Snapshot()[0].PublishedAt != "2026-05-08T00:00:00Z" {
		t.Errorf("PublishedAt was overwritten: %q", p.Snapshot()[0].PublishedAt)
	}
}

func TestInMemoryPublisher_ConcurrentPublish(t *testing.T) {
	t.Parallel()

	p := events.NewInMemoryPublisher()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Publish(context.Background(), atom.Event{
				EventID: "concurrent",
				Type:    atom.EventTypeAtomCreated,
			})
		}()
	}
	wg.Wait()
	if p.Count() != 100 {
		t.Errorf("Count = %d; want 100", p.Count())
	}
}
