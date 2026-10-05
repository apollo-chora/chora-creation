// Package inmem_test exercises the in-memory EventRecorder implementation.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
)

func TestEventRecorder_PublishAndEvents(t *testing.T) {
	t.Parallel()

	r := inmem.NewEventRecorder()
	if got := r.Events(); len(got) != 0 {
		t.Errorf("Events on fresh recorder = %d; want 0", len(got))
	}

	env := inmem.Envelope{EventID: "evt-1", TenantID: "t1", Gcid: "g1"}
	if err := r.Publish(context.Background(), "topic.a", env, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := r.Publish(context.Background(), "topic.b", env, nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := r.Events()
	if len(got) != 2 {
		t.Fatalf("Events len = %d; want 2", len(got))
	}
	if got[0].Topic != "topic.a" || got[1].Topic != "topic.b" {
		t.Errorf("topics out of order: %+v", got)
	}
	if got[0].Envelope.EventID != "evt-1" {
		t.Errorf("Envelope.EventID = %q; want evt-1", got[0].Envelope.EventID)
	}

	// Snapshot copy: mutating the returned slice must not affect the recorder.
	got[0].Topic = "MUTATED"
	if again := r.Events(); again[0].Topic != "topic.a" {
		t.Errorf("recorder leaked internal events slice; topic = %q", again[0].Topic)
	}
}
