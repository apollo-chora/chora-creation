// collection_publisher.go — Collection outbox publisher (WS-6a Phase 4, 2026-05-26).
//
// CollectionOutboxPublisher satisfies collection.EventPublisher by writing
// chora.creation.collection.* events to the shared outbox_events table
// (see internal/adapter/outbox/store.go). The existing
// outbox.Dispatcher drains the table to Cloud Pub/Sub on a background
// goroutine — no separate dispatcher is required for collection events.
//
// Wire shape:
//   - topic = chora.creation.collection.{event_type}.v1 (verified at
//     publish-time; non-canonical topics are rejected)
//   - aggregate_type = "collection"
//   - aggregate_id = collection_id (UUIDv7)
//   - envelope = JSONB string-map carrying the 11 CLAUDE.md §6 mandatory
//     envelope fields + ChoraImdaDimension + ImdaLifecycleStage (empty
//     for now — Collection events are not IMDA evidence in WS-6a)
//   - payload = JSON-marshalled event body (collection_id, owner_gcid,
//     title, description, visibility, changed_fields, atom_id, position,
//     deleted_by_gcid as applicable)
//
// Payload encoding is BIMODAL since ADR-233 (see encodeCollectionPayload):
// converted_to_study_list.v1 is BINARY protobuf (Schema-Registry bound — it has
// a real cross-domain consumer), while the other five topics stay JSON-wire
// (declared schemaless in m10-data-plane; no consumer, no encoder).
//
// Per `feedback_no_inline_config`: no inline URLs/secrets — every
// dependency is injected via CollectionOutboxConfig.
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// CollectionOutboxConfig wires the publisher.
type CollectionOutboxConfig struct {
	// Store is the outbox-table backend. Required.
	Store creationoutbox.Store

	// SourceProject is the GCP project the service runs in (e.g.
	// chora-local). Defaults to "chora-local".
	SourceProject string

	// SourceService is the publisher's service name. Defaults to
	// "chora-creation".
	SourceService string

	// Now is injectable for tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// CollectionOutboxPublisher satisfies collection.EventPublisher.
type CollectionOutboxPublisher struct {
	cfg CollectionOutboxConfig
}

// NewCollectionOutboxPublisher constructs a publisher.
func NewCollectionOutboxPublisher(cfg CollectionOutboxConfig) *CollectionOutboxPublisher {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.SourceProject == "" {
		cfg.SourceProject = "chora-local"
	}
	if cfg.SourceService == "" {
		cfg.SourceService = "chora-creation"
	}
	return &CollectionOutboxPublisher{cfg: cfg}
}

// Publish satisfies collection.EventPublisher. Writes the event as a
// pending row in outbox_events. The Dispatcher publishes to Pub/Sub
// asynchronously.
func (p *CollectionOutboxPublisher) Publish(ctx collection.Context, e collection.Event) error {
	if p.cfg.Store == nil {
		return errors.New("collection_outbox: store not wired")
	}
	topic := string(e.Type)
	if err := validateCanonicalCollectionTopic(topic); err != nil {
		return err
	}

	// Mint event_id (UUIDv7) when the caller didn't.
	eventID := e.EventID
	if eventID == "" {
		eventID = newUUIDv7()
	}
	idemKey := e.IdempotencyKey
	if idemKey == "" {
		idemKey = eventID
	}
	occurred := parseRFC3339Or(e.OccurredAt, p.cfg.Now())
	publishedAt := parseRFC3339Or(e.PublishedAt, p.cfg.Now())

	sourceProject := e.SourceProject
	if sourceProject == "" {
		sourceProject = p.cfg.SourceProject
	}
	sourceService := e.SourceService
	if sourceService == "" {
		sourceService = p.cfg.SourceService
	}
	schemaVersion := e.SchemaVersion
	if schemaVersion < 1 {
		schemaVersion = 1
	}

	envelope := map[string]string{
		"event_id":             eventID,
		"idempotency_key":      idemKey,
		"tenant_id":            e.TenantID,
		"gcid":                 e.Gcid,
		"occurred_at":          occurred.Format(time.RFC3339Nano),
		"published_at":         publishedAt.Format(time.RFC3339Nano),
		"traceparent":          e.TraceParent,
		"tracestate":           e.TraceState,
		"source_project":       sourceProject,
		"source_service":       sourceService,
		"schema_version":       strconv.Itoa(int(schemaVersion)),
		"chora_imda_dimension": "",
		"imda_lifecycle_stage": "",
	}

	payload := buildCollectionPayload(e)
	payloadBytes, err := encodeCollectionPayload(topic, payload, protomarshal.Envelope{
		EventID:        eventID,
		IdempotencyKey: idemKey,
		TenantID:       e.TenantID,
		GCID:           e.Gcid,
		OccurredAt:     occurred,
		PublishedAt:    publishedAt,
		Traceparent:    e.TraceParent,
		Tracestate:     e.TraceState,
		SourceProject:  sourceProject,
		SourceService:  sourceService,
		SchemaVersion:  schemaVersion,
	})
	if err != nil {
		return fmt.Errorf("collection_outbox: marshal payload: %w", err)
	}

	aggregateID := e.CollectionID
	if aggregateID == "" {
		aggregateID = eventID
	}

	row := creationoutbox.Row{
		ID:             eventID,
		TenantID:       e.TenantID,
		GCID:           e.Gcid,
		AggregateType:  "collection",
		AggregateID:    aggregateID,
		EventType:      deriveCollectionEventType(topic),
		Topic:          topic,
		Payload:        payloadBytes,
		Envelope:       envelope,
		IdempotencyKey: idemKey,
		OccurredAt:     occurred.UTC(),
	}
	// Convert collection.Context → context.Context: collection.Context
	// embeds the same surface so the assertion is direct.
	stdCtx, ok := ctx.(context.Context)
	if !ok {
		stdCtx = context.Background()
	}
	return p.cfg.Store.Insert(stdCtx, row)
}

// encodeCollectionPayload picks the wire encoding for a collection topic.
//
// The collection topics are BIMODAL, and deliberately so:
//
//	converted_to_study_list.v1  → BINARY protobuf. It is the ONE collection topic
//	                              with a real cross-domain consumer
//	                              (chora-consumption builds the LearningPath from
//	                              it), so it is Schema-Registry bound. A JSON
//	                              payload on this topic is rejected at publish
//	                              with "Invalid binary proto message" and
//	                              dead-letters — silently, from the FE's point of
//	                              view, since the HTTP 201 already returned.
//
//	the other five              → JSON. They are declared schemaless in
//	                              m10-data-plane (no consumer, no encoder). Their
//	                              existing payload path is preserved byte-for-byte.
//
// An encoding failure on the binary topic FAILS LOUD rather than falling back to
// JSON: a JSON fallback here would produce a row that is guaranteed to
// dead-letter, which is strictly worse than refusing the write, because the
// conversion would look like it succeeded.
func encodeCollectionPayload(topic string, payload map[string]any, env protomarshal.Envelope) ([]byte, error) {
	if topic == string(collection.EventTypeCollectionConvertedToStudyList) {
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		if err != nil {
			return nil, fmt.Errorf("binary encode %s: %w", topic, err)
		}
		return bz, nil
	}
	return json.Marshal(payload)
}

// buildCollectionPayload projects collection.Event onto the wire payload
// map. Fields that don't apply to the current event type are omitted.
func buildCollectionPayload(e collection.Event) map[string]any {
	p := map[string]any{
		"event_id":      e.EventID,
		"type":          string(e.Type),
		"tenant_id":     e.TenantID,
		"collection_id": e.CollectionID,
		"owner_gcid":    e.OwnerGcid,
	}
	if e.Title != "" {
		p["title"] = e.Title
	}
	if e.Description != "" {
		p["description"] = e.Description
	}
	if e.Visibility != "" {
		p["visibility"] = string(e.Visibility)
	}
	if e.AtomID != "" {
		p["atom_id"] = e.AtomID
	}
	switch e.Type {
	case collection.EventTypeCollectionAtomAdded:
		p["position"] = int32(e.Position)
	}
	if len(e.ChangedFields) > 0 {
		p["changed_fields"] = append([]string(nil), e.ChangedFields...)
	}
	if e.DeletedByGcid != "" {
		p["deleted_by_gcid"] = e.DeletedByGcid
	}
	// converted_to_study_list.v1 (ADR-233) — proto fields 4 + 5. atom_ids order
	// is the collection's curated order and is load-bearing: the learner's
	// sequence rides it into chora_consumption.
	if len(e.AtomIDs) > 0 {
		p["atom_ids"] = append([]string(nil), e.AtomIDs...)
	}
	if e.StudyListEventID != "" {
		p["study_list_event_id"] = e.StudyListEventID
	}
	return p
}

// validateCanonicalCollectionTopic enforces chora.creation.collection.{event_type}.v{N}.
func validateCanonicalCollectionTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("collection_outbox: topic required")
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return fmt.Errorf("collection_outbox: topic %q must follow chora.creation.collection.{event_type}.v{N}", topic)
	}
	if parts[0] != "chora" {
		return fmt.Errorf("collection_outbox: topic %q must start with 'chora.'", topic)
	}
	if parts[1] != "creation" {
		return fmt.Errorf("collection_outbox: topic domain segment = %q; want creation", parts[1])
	}
	if parts[2] != "collection" {
		return fmt.Errorf("collection_outbox: topic aggregate segment = %q; want collection", parts[2])
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return fmt.Errorf("collection_outbox: topic %q must end with v{N}", topic)
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("collection_outbox: topic %q version suffix must be numeric", topic)
		}
	}
	return nil
}

func deriveCollectionEventType(topic string) string {
	parts := strings.Split(topic, ".")
	if len(parts) < 5 {
		return topic
	}
	// chora.creation.collection.{event_type}.v{N} → creation.collection.{event_type}
	return strings.Join(parts[1:len(parts)-1], ".")
}

func parseRFC3339Or(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback.UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return fallback.UTC()
}

func newUUIDv7() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Compile-time port assertion.
var _ collection.EventPublisher = (*CollectionOutboxPublisher)(nil)
