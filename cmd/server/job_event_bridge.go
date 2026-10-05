// job_event_bridge.go — JobEventPublisher implementation that:
//  1. Publishes the event to the NATS JetStream event bus when wired (for
//     cross-service consumers like Observability + Notifications).
//  2. ALSO fans out generation_requested events to an in-process channel
//     so the QuestionSubscriber goroutine can consume them without a
//     full bus Subscribe loop.
//
// This is the pragmatic bridge per the lead plan: subscriber-in-server +
// outbox-driven event flow.
//
// Wire-encoding: the chora.creation.* topics the bridge emits to carry
// binary-encoded payloads. We marshal each payload through the
// protomarshal encoder before publishing. Unsupported topics fall back to
// JSON + log a one-shot WARN.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"

	cgcenv "github.com/apollo-chora/chora-common/env"
)

// errPubSubClientNotWired is returned by PublishJobEventSync when the bridge
// has no event bus (dev / unit-test boot without NATS_URL).
// The qgen-crew ai_draft dispatch treats this as a dispatch failure (fail +
// refund) rather than a silent no-op that would strand the job.
var errPubSubClientNotWired = errors.New("creation: event bus not wired; cannot synchronously publish")

// jobEventPublisherBridge implements httpadapter.JobEventPublisher AND
// pubsubadapter.JobEventPublisher AND pubsubadapter.JobEventSyncPublisher.
type jobEventPublisherBridge struct {
	bus     eventbus.Bus
	project string

	// inProc channel for generation_requested events fed to the in-process
	// subscriber. Cap = 256 — burst tolerance without unbounded memory.
	inProc chan pubsubadapter.QuestionGenerationRequestedEvent

	mu      sync.Mutex
	stopped bool
}

// Compile-time checks — the bridge satisfies both the fire-and-forget and the
// load-bearing synchronous publish ports.
var (
	_ pubsubadapter.JobEventPublisher     = (*jobEventPublisherBridge)(nil)
	_ pubsubadapter.JobEventSyncPublisher = (*jobEventPublisherBridge)(nil)
)

// newJobEventPublisherBridge constructs the bridge.
func newJobEventPublisherBridge(bus eventbus.Bus, project string) *jobEventPublisherBridge {
	return &jobEventPublisherBridge{
		bus:     bus,
		project: project,
		inProc:  make(chan pubsubadapter.QuestionGenerationRequestedEvent, 256),
	}
}

// PublishJobEvent satisfies both JobEventPublisher interfaces.
func (b *jobEventPublisherBridge) PublishJobEvent(ctx context.Context, topic string, payload any) error {
	// 1. Fan-out to the in-process subscriber for generation_requested.
	// ADR-195 WS7 step 5 — match the .v2 topic (v1 retired); the dispatch reads
	// job.Intent (loaded row) || evt.Intent (the job_type shim was retired in WS9).
	if topic == "chora.creation.question.generation_requested.v2" {
		if evt, ok := coerceToEvent(payload); ok {
			b.mu.Lock()
			stopped := b.stopped
			b.mu.Unlock()
			if !stopped {
				select {
				case b.inProc <- evt:
				default:
					log.Printf("creation: in-proc question subscriber channel full; dropping event %s", evt.JobID)
				}
			}
		}
	}

	// 2. Best-effort emit to the event bus for cross-service consumers.
	// When bus is nil (dev / unit tests) this is a no-op.
	if b.bus == nil {
		return nil
	}
	env, bytes, err := encodeBridgePayload(topic, payload)
	if err != nil {
		return err
	}
	// Don't block the HTTP handler on the bus — fire-and-forget.
	go func() {
		publishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.bus.Publish(publishCtx, topic, env, bytes); err != nil {
			log.Printf("creation: event bus publish %s failed: %v", topic, err)
		}
	}()
	return nil
}

// PublishJobEventSync is the LOAD-BEARING publish path (W2 Seam A): it blocks
// until the broker accepts the message and RETURNS the publish error. Used by
// the qgen-crew ai_draft dispatch where a swallowed publish failure would
// strand the question-job in running with no orchestrator pickup. Unlike
// PublishJobEvent this does NOT fan-out to the in-process subscriber channel
// (the started.v1 topic is consumed by the orchestrator, not in-pod)
// and does NOT fire-and-forget.
//
// Satisfies pubsubadapter.JobEventSyncPublisher.
func (b *jobEventPublisherBridge) PublishJobEventSync(ctx context.Context, topic string, payload any) error {
	if b.bus == nil {
		// Fail loud — the caller treats this as a dispatch failure + refunds
		// mana rather than silently no-op'ing (which would strand the job).
		return errPubSubClientNotWired
	}
	env, bytes, err := encodeBridgePayload(topic, payload)
	if err != nil {
		return err
	}
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.bus.Publish(publishCtx, topic, env, bytes); err != nil {
		return err
	}
	return nil
}

// -----------------------------------------------------------------------------
// Payload encoding for the JobEventPublisher direct-publish path.
//
// Per task #33 (2026-05-16): the chora.creation.* topics emitted by the
// JobEventPublisher carry BINARY-encoded Schema Registry schemas. JSON
// payloads on those topics dead-letter at publish with "Invalid binary
// proto message". We marshal each payload through the protomarshal encoder
// before publishing. Unsupported topics fall back to JSON + log a one-shot
// WARN so production surfaces the gap.
// -----------------------------------------------------------------------------

var (
	bridgeWarnedUnknownTopicsMu sync.Mutex
	bridgeWarnedUnknownTopics   = map[string]bool{}
)

// encodeBridgePayload marshals the loose-typed handler payload onto the
// canonical schema for the supplied topic and returns the wire envelope.
// The handler payload is always either map[string]any (most call sites) or
// a typed completion-event struct that JSON-encodes to a comparable shape;
// we project both onto the encoder's map[string]any contract.
func encodeBridgePayload(topic string, payload any) (envelope.Envelope, []byte, error) {
	asMap, mapOK := payload.(map[string]any)
	if !mapOK {
		// Typed struct (e.g. completionEvent) — round-trip via JSON to
		// produce the map[string]any shape protomarshal expects. This
		// step is a small allocation but keeps the bridge encoder-agnostic
		// to handler payload shapes.
		jBz, jErr := json.Marshal(payload)
		if jErr != nil {
			return envelope.Envelope{}, nil, jErr
		}
		var coerced map[string]any
		if err := json.Unmarshal(jBz, &coerced); err != nil {
			// Non-map JSON shape — fall back loud (the encoder needs a
			// map[string]any to project onto schema field slots).
			return envelope.Envelope{}, nil, err
		}
		asMap = coerced
	}

	env := envelopeFromPayloadMap(asMap, topic)
	wireEnv := envelope.Envelope{
		EventID:        env.EventID,
		IdempotencyKey: env.IdempotencyKey,
		TenantID:       env.TenantID,
		GCID:           env.GCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    env.PublishedAt,
		Traceparent:    env.Traceparent,
		Tracestate:     env.Tracestate,
		SourceProject:  env.SourceProject,
		SourceService:  env.SourceService,
		SchemaVersion:  env.SchemaVersion,
	}
	bz, err := protomarshal.MarshalPayload(topic, env, asMap)
	if err == nil {
		return wireEnv, bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		return envelope.Envelope{}, nil, err
	}

	bridgeWarnedUnknownTopicsMu.Lock()
	if !bridgeWarnedUnknownTopics[topic] {
		bridgeWarnedUnknownTopics[topic] = true
		log.Printf("WARN job_event_bridge: topic %q has no binary protobuf encoder — payload will JSON-marshal. Add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	bridgeWarnedUnknownTopicsMu.Unlock()

	bz, jErr := json.Marshal(payload)
	return wireEnv, bz, jErr
}

// envelopeFromPayloadMap synthesises the per-event envelope from fields the
// handler attaches to the loose payload map (tenant_id / author_gcid /
// requested_at / completed_at / authored_at). The chora.creation.* schemas
// require event_id + idempotency_key + envelope.{schema_version, source}*
// so we mint UUIDv7 for missing keys and apply chora-local defaults.
func envelopeFromPayloadMap(payload map[string]any, topic string) protomarshal.Envelope {
	env := protomarshal.Envelope{
		SchemaVersion: 1,
		SourceProject: cgcenv.GetOrDefault("CHORA_SOURCE_PROJECT", "chora-local"),
		SourceService: "chora-creation",
	}

	if v, ok := payload["tenant_id"].(string); ok {
		env.TenantID = v
	}
	if v, ok := payload["author_gcid"].(string); ok {
		env.GCID = v
	}
	// W3C trace context — handler stamps traceparent into the payload
	// from r.Context() so the orchestrator's qgen_crew_runner can
	// continue the FE-originated trace tree via Pub/Sub envelope.
	if v, ok := payload["traceparent"].(string); ok && v != "" {
		env.Traceparent = v
	}
	if v, ok := payload["tracestate"].(string); ok && v != "" {
		env.Tracestate = v
	}

	// Pick an event-time field that exists on the topic's payload. Falls
	// back to time.Now() if none surface (rare — every emit site sets at
	// least one timestamp).
	now := time.Now().UTC()
	for _, k := range []string{"requested_at", "authored_at", "completed_at", "created_at"} {
		if v, ok := payload[k].(string); ok && v != "" {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				env.OccurredAt = t
				break
			}
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				env.OccurredAt = t
				break
			}
		}
		if t, ok := payload[k].(time.Time); ok && !t.IsZero() {
			env.OccurredAt = t
			break
		}
	}
	if env.OccurredAt.IsZero() {
		env.OccurredAt = now
	}
	env.PublishedAt = now

	// Mint event_id + idempotency_key. The JobEventPublisher contract does
	// not surface these from the handler; the broker still needs them on
	// the envelope so we synthesise per-emit and rely on the outbox path's
	// dedupe (the M14 wire-up will replace the bridge with the outbox).
	if u, err := uuid.NewV7(); err == nil {
		env.EventID = u.String()
		// The chora envelope contract makes traceparent MANDATORY (the
		// consumption dispatcher rejects without it). API-originated emits
		// without an inbound W3C header synthesise a valid root traceparent
		// from the event uuid (32-hex trace-id + first-16 span-id).
		if env.Traceparent == "" {
			hexID := strings.ReplaceAll(env.EventID, "-", "")
			env.Traceparent = "00-" + hexID + "-" + hexID[:16] + "-01"
		}
		// Derive idempotency_key from a stable per-EMIT field so broker retries
		// collapse WITHOUT collapsing distinct emits. assist_id (the AI-assist
		// job id) is FIRST so ai_assist.started.v1 dedups per-assist-run: that
		// payload carries no job_id, and falling through to atom_id made every
		// assist on the same atom share one key — a review image-regen (same
		// atom as its parent batch) then collided with the already-processed
		// batch event and the orchestrator InboxIdempotencyStore silently
		// skipped it, stranding the job (stuck-regen bug, CHO-1822). job_id /
		// question_id / atom_id remain for the other chora.creation.* payloads.
		for _, k := range []string{"assist_id", "job_id", "question_id", "atom_id"} {
			if v, ok := payload[k].(string); ok && v != "" {
				env.IdempotencyKey = topic + "|" + v
				break
			}
		}
		if env.IdempotencyKey == "" {
			env.IdempotencyKey = env.EventID
		}
	}

	return env
}

// Drain returns the channel of inbound generation_requested events.
func (b *jobEventPublisherBridge) Drain() <-chan pubsubadapter.QuestionGenerationRequestedEvent {
	return b.inProc
}

// Stop closes the in-process channel so the subscriber goroutine exits.
func (b *jobEventPublisherBridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.stopped = true
	close(b.inProc)
}

// coerceToEvent converts the handler's map[string]any payload shape into
// the typed QuestionGenerationRequestedEvent.
func coerceToEvent(payload any) (pubsubadapter.QuestionGenerationRequestedEvent, bool) {
	m, ok := payload.(map[string]any)
	if !ok {
		return pubsubadapter.QuestionGenerationRequestedEvent{}, false
	}
	evt := pubsubadapter.QuestionGenerationRequestedEvent{}
	if v, ok := m["job_id"].(string); ok {
		evt.JobID = v
	}
	if v, ok := m["atom_id"].(string); ok {
		evt.AtomID = v
	}
	if v, ok := m["author_gcid"].(string); ok {
		evt.AuthorGCID = v
	}
	if v, ok := m["tenant_id"].(string); ok {
		evt.TenantID = v
	}
	if v, ok := m["question_type"].(string); ok {
		evt.QuestionType = v
	}
	if v, ok := m["target_question_id"].(string); ok {
		evt.TargetQuestionID = v
	}
	if v, ok := m["mana_action_code"].(string); ok {
		evt.ManaActionCode = v
	}
	if v, ok := m["mana_charged"].(int); ok {
		evt.ManaCharged = v
	}
	if v, ok := m["settings_json"].(string); ok {
		evt.SettingsJSON = v
	}
	if v, ok := m["requested_at"].(string); ok {
		evt.RequestedAt = v
	}
	if v, ok := m["traceparent"].(string); ok {
		evt.Traceparent = v
	}
	// Batch source-material path (P6): without these two the in-process
	// fan-out drops the upload location + MIME, and the worker fails the job
	// with "missing source_blob_uri on event" (question_subscriber.go).
	if v, ok := m["source_blob_uri"].(string); ok {
		evt.SourceBlobURI = v
	}
	if v, ok := m["source_mime_type"].(string); ok {
		evt.SourceMimeType = v
	}
	// ADR-195 WS7 (D7) compose model — carried on the .v2 event (additive; empty
	// on a .v1 payload, where runCompose falls back to the persisted job.Intent).
	if v, ok := m["intent"].(string); ok {
		evt.Intent = v
	}
	if v, ok := m["input_kind"].(string); ok {
		evt.InputKind = v
	}
	return evt, true
}

// startQuestionSubscriberLoop spawns a goroutine that drains
// bridge.inProc + dispatches to the subscriber.Handle method. Returns
// immediately; the goroutine exits when ctx is canceled OR the channel
// is closed.
func startQuestionSubscriberLoop(ctx context.Context, sub *pubsubadapter.QuestionSubscriber, bridge *jobEventPublisherBridge) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-bridge.Drain():
				if !ok {
					return
				}
				log.Printf("creation: question subscriber drained job=%s intent=%s", evt.JobID, evt.Intent)
				// Async — use a child context with our own timeout so a
				// slow QGen call does not block the loop.
				hCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				// Log Handle errors — the in-proc loop has no NACK/retry, so a
				// swallowed error silently strands the job (e.g. a failed
				// transition-to-running). Surface it (no-debts; fail-loud).
				if err := sub.Handle(hCtx, evt); err != nil {
					log.Printf("creation: question subscriber Handle error job=%s: %v", evt.JobID, err)
				}
				cancel()
			}
		}
	}()
}
