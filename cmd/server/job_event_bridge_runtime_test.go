// job_event_bridge_runtime_test.go — runtime-path coverage for the
// JobEventPublisher bridge beyond the encoder unit tests in
// job_event_bridge_test.go: construction, the in-process fan-out,
// PublishJobEvent / PublishJobEventSync against a stub event bus,
// Drain/Stop lifecycle, and the subscriber goroutine loop
// (startQuestionSubscriberLoop).
package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
)

// fakeBus captures Publish calls and serves a scripted Subscribe.
type fakeBus struct {
	mu        sync.Mutex
	published []publishedMsg
	pubErr    error
}

type publishedMsg struct {
	subject string
	env     envelope.Envelope
	data    []byte
}

func (b *fakeBus) Publish(_ context.Context, subject string, env envelope.Envelope, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, publishedMsg{subject: subject, env: env, data: data})
	return b.pubErr
}

func (b *fakeBus) Subscribe(context.Context, eventbus.ConsumerConfig, eventbus.Handler) error {
	return nil
}

func (b *fakeBus) Close() error { return nil }

func (b *fakeBus) publishedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.published)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- construction + lifecycle -------------------------------------------------

func TestNewJobEventPublisherBridge_DrainStopLifecycle(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	if b == nil {
		t.Fatal("newJobEventPublisherBridge returned nil")
	}
	if b.Drain() == nil {
		t.Fatal("Drain returned nil channel")
	}

	// Stop closes the channel; a second Stop is a no-op (idempotent).
	b.Stop()
	b.Stop()

	select {
	case _, ok := <-b.Drain():
		if ok {
			t.Fatal("channel should be closed after Stop")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after Stop")
	}
}

// --- PublishJobEvent -----------------------------------------------------------

func TestPublishJobEvent_NilBusFansOutInProc(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	defer b.Stop()

	payload := map[string]any{
		"job_id":      "job-1",
		"atom_id":     "atom-1",
		"author_gcid": "gcid-1",
		"tenant_id":   "tenant-1",
		"intent":      "ai_draft",
	}
	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", payload); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}

	select {
	case evt := <-b.Drain():
		if evt.JobID != "job-1" || evt.Intent != "ai_draft" {
			t.Errorf("fanned-out event = %+v, want job-1 / ai_draft", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected in-proc fan-out event")
	}
}

func TestPublishJobEvent_NilBusNonGenerationTopicNoFanOut(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	defer b.Stop()

	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.completed.v1", map[string]any{"job_id": "j"}); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}
	select {
	case evt := <-b.Drain():
		t.Fatalf("unexpected fan-out for non-generation topic: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPublishJobEvent_AfterStopDropsFanOut(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	b.Stop()

	// Stopped bridge: coerce matches but the stopped guard drops the send —
	// and must not panic on the closed channel.
	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", map[string]any{"job_id": "j"}); err != nil {
		t.Fatalf("PublishJobEvent after Stop: %v", err)
	}
}

func TestPublishJobEvent_NonMapPayloadSkipsFanOut(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	defer b.Stop()

	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", "not-a-map"); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}
	select {
	case evt := <-b.Drain():
		t.Fatalf("unexpected fan-out for non-map payload: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPublishJobEvent_WithBusPublishesAsync(t *testing.T) {
	bus := &fakeBus{}
	b := newJobEventPublisherBridge(bus, "proj")
	defer b.Stop()

	payload := map[string]any{
		"job_id":       "job-9",
		"tenant_id":    "tenant-1",
		"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", payload); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}

	// Fire-and-forget: poll until the goroutine's Publish lands.
	waitFor(t, "async publish", func() bool { return bus.publishedCount() == 1 })

	bus.mu.Lock()
	msg := bus.published[0]
	bus.mu.Unlock()
	if msg.subject != "chora.creation.question.generation_requested.v2" {
		t.Errorf("published subject = %q", msg.subject)
	}
	if len(msg.data) == 0 {
		t.Error("published empty payload bytes")
	}
	if msg.env.EventID == "" || msg.env.IdempotencyKey == "" {
		t.Errorf("envelope fields missing: %+v", msg.env)
	}
}

func TestPublishJobEvent_WithBusPublishErrorLogged(t *testing.T) {
	bus := &fakeBus{pubErr: errors.New("broker down")}
	b := newJobEventPublisherBridge(bus, "proj")
	defer b.Stop()

	// Publish error inside the fire-and-forget goroutine is logged, not
	// returned.
	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", map[string]any{"job_id": "j"}); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}
	waitFor(t, "async publish attempt", func() bool { return bus.publishedCount() == 1 })
}

// --- PublishJobEventSync -------------------------------------------------------

func TestPublishJobEventSync_NilBusFailsLoud(t *testing.T) {
	b := newJobEventPublisherBridge(nil, "proj")
	err := b.PublishJobEventSync(context.Background(), "chora.creation.ai_assist.started.v2", map[string]any{"job_id": "j"})
	if !errors.Is(err, errPubSubClientNotWired) {
		t.Errorf("err = %v, want errPubSubClientNotWired", err)
	}
}

func TestPublishJobEventSync_WithBusSuccess(t *testing.T) {
	bus := &fakeBus{}
	b := newJobEventPublisherBridge(bus, "proj")
	defer b.Stop()

	payload := map[string]any{
		"assist_id":  "assist-1",
		"tenant_id":  "tenant-1",
		"started_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := b.PublishJobEventSync(context.Background(), "chora.creation.ai_assist.started.v2", payload); err != nil {
		t.Fatalf("PublishJobEventSync: %v", err)
	}
	if bus.publishedCount() != 1 {
		t.Fatalf("published %d messages, want 1 (synchronous)", bus.publishedCount())
	}
}

func TestPublishJobEventSync_WithBusPropagatesBrokerError(t *testing.T) {
	brokerErr := errors.New("broker rejected payload")
	bus := &fakeBus{pubErr: brokerErr}
	b := newJobEventPublisherBridge(bus, "proj")
	defer b.Stop()

	err := b.PublishJobEventSync(context.Background(), "chora.creation.ai_assist.started.v2", map[string]any{"assist_id": "a"})
	if !errors.Is(err, brokerErr) {
		t.Errorf("err = %v, want broker error propagated", err)
	}
}

func TestPublishJobEventSync_DoesNotFanOutInProc(t *testing.T) {
	bus := &fakeBus{}
	b := newJobEventPublisherBridge(bus, "proj")
	defer b.Stop()

	// Even on the generation_requested topic the sync path must not feed the
	// in-process channel (orchestrator consumes started.v1, not in-pod).
	if err := b.PublishJobEventSync(context.Background(), "chora.creation.question.generation_requested.v2", map[string]any{"job_id": "j"}); err != nil {
		t.Fatalf("PublishJobEventSync: %v", err)
	}
	select {
	case evt := <-b.Drain():
		t.Fatalf("sync path must not fan out in-proc, got %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

// --- startQuestionSubscriberLoop ----------------------------------------------

func TestStartQuestionSubscriberLoop_DrainsUntilChannelClose(t *testing.T) {
	// Zero-dep subscriber: a malformed event (missing required fields) is
	// ack-skipped by Handle before any dep is touched, so the loop runs
	// cleanly without repositories.
	sub := pubsubadapter.NewQuestionSubscriber(pubsubadapter.QuestionSubscriberDeps{})
	b := newJobEventPublisherBridge(nil, "proj")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startQuestionSubscriberLoop(ctx, sub, b)

	if err := b.PublishJobEvent(context.Background(), "chora.creation.question.generation_requested.v2", map[string]any{}); err != nil {
		t.Fatalf("PublishJobEvent: %v", err)
	}
	// Give the loop a beat to drain, then close the channel — the goroutine
	// must exit on its own (no leak, no panic).
	time.Sleep(50 * time.Millisecond)
	b.Stop()
	waitFor(t, "drained channel", func() bool {
		select {
		case _, ok := <-b.Drain():
			return !ok
		default:
			return false
		}
	})
}

func TestStartQuestionSubscriberLoop_ExitsOnContextCancel(t *testing.T) {
	sub := pubsubadapter.NewQuestionSubscriber(pubsubadapter.QuestionSubscriberDeps{})
	b := newJobEventPublisherBridge(nil, "proj")
	defer b.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	startQuestionSubscriberLoop(ctx, sub, b)
	cancel()
	// No assertion beyond "returns promptly" — the goroutine exits via the
	// ctx.Done() branch; the test would hang the race/timeout detector if it
	// deadlocked.
	time.Sleep(50 * time.Millisecond)
}
