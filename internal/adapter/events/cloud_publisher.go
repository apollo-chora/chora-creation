// cloud_publisher.go — outbox-backed production replacement for InMemoryPublisher.
//
// CloudPublisher satisfies atom.EventPublisher and atomically writes events
// to the chora_creation outbox_events table via chora-go-common/outbox.Recorder.
// A separate Relay process drains pending rows to Cloud Pub/Sub.
//
// Why an outbox + Relay (vs direct publish)?
//
//   - Atomic: domain state-change + event-publish in ONE transaction. A
//     network failure mid-publish never leaves orphan state nor orphan events.
//   - Resilient: dead pods leave outbox rows pending; replacement pods pick
//     up via the SKIP-LOCKED claim contract.
//   - Auditable: every emitted event has a durable row in chora_creation
//     before the wire-publish, with retry telemetry.
//
// Aligned with services/chora-identity/internal/adapter/events/cloud_publisher.go.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"

	"github.com/apollo-chora/chora-common/env"
)

// CloudPublisher writes atom.Event payloads to the outbox via the supplied
// Recorder. Defaults to AggregateType="atom".
type CloudPublisher struct {
	rec cgcoutbox.Recorder
}

// NewCloudPublisher constructs a CloudPublisher.
func NewCloudPublisher(rec cgcoutbox.Recorder) *CloudPublisher {
	return &CloudPublisher{rec: rec}
}

// Publish satisfies atom.EventPublisher. Validates topic + envelope, marshals
// the payload to JSON, and writes a pending outbox row.
func (p *CloudPublisher) Publish(ctx context.Context, e atom.Event) error {
	topic := string(e.Type)
	if err := validateTopic(topic); err != nil {
		return err
	}

	occurred, err := parseRFC3339(e.OccurredAt)
	if err != nil {
		return fmt.Errorf("events.CloudPublisher: occurred_at: %w", err)
	}
	published, err := parseRFC3339(e.PublishedAt)
	if err != nil {
		// Default to now if missing — keeps publish-time the boundary.
		published = time.Now().UTC()
	}

	env := cgcenvelope.Envelope{
		EventID:            ensureUUIDv7(e.EventID),
		IdempotencyKey:     ensureUUIDv7(e.IdempotencyKey),
		TenantID:           e.TenantID,
		GCID:               e.Gcid,
		OccurredAt:         occurred,
		PublishedAt:        published,
		Traceparent:        e.TraceParent,
		Tracestate:         e.TraceState,
		SourceProject:      defaultIfEmpty(e.SourceProject, SourceProject),
		SourceService:      defaultIfEmpty(e.SourceService, SourceService),
		SchemaVersion:      defaultSchemaIfZero(e.SchemaVersion),
		ChoraImdaDimension: e.ChoraImdaDimension,
		ImdaLifecycleStage: e.ImdaLifecycleStage,
	}
	if err := cgcenvelope.Validate(env); err != nil {
		return fmt.Errorf("events.CloudPublisher: envelope: %w", err)
	}

	// Producer-side encoding: emit canonical binary protobuf for topics
	// whose broker schema is BINARY-encoded. JSON
	// payloads on a schema-attached topic dead-letter forever with
	// "Invalid binary proto message". Per task #33 (2026-05-16). Unsupported
	// topics fall back to JSON + log a one-shot WARN — these rows WILL be
	// rejected by Schema Registry at publish, but the fallback preserves
	// behaviour for topics still pending a binary encoder.
	body, err := encodeCloudPublisherPayload(topic, e, env, occurred)
	if err != nil {
		return fmt.Errorf("events.CloudPublisher: marshal payload: %w", err)
	}

	aggregateID := e.AtomID
	if aggregateID == "" {
		aggregateID = e.EventID
	}

	row := &cgcoutbox.Row{
		ID:            newRowID(),
		AggregateType: "atom",
		AggregateID:   aggregateID,
		EventType:     deriveEventType(topic),
		Topic:         topic,
		Payload:       body,
		Envelope:      env,
		OccurredAt:    occurred,
		Status:        cgcoutbox.StatusPending,
	}
	return p.rec.Record(ctx, nil, row)
}

// SourceProject + SourceService for envelope provenance. chora-creation
// runs in chora-local (build-out platform host per ADR-144).
const (
	SourceService = "chora-creation"
)

// SourceProject + SourceService for envelope provenance. chora-creation
// runs in chora-local (build-out platform host per ADR-144).
//
// Was a hardcoded literal until 2026-09-02 (CHO-2419). No manifest could reach
// it, so a second org stamped every event with chora-local and any consumer
// filtering on source_project would have been filtering on a lie.
// CHORA_SOURCE_PROJECT is now set on every event-emitting service; the literal
// stays as the fallback so this estate is provably unchanged.
var SourceProject = env.GetOrDefault("CHORA_SOURCE_PROJECT", "chora-local")

// validateTopic enforces chora.{domain}.{aggregate}.{event_type}.v{N}
// shape and locks domain to "creation" for this publisher.
func validateTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("events: topic required")
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return errors.New("events: topic must follow chora.{domain}.{aggregate}.{event_type}.v{N}")
	}
	if parts[0] != "chora" {
		return errors.New("events: topic must start with 'chora.'")
	}
	if parts[1] != "creation" && parts[1] != "governance" {
		return errors.New("events: topic domain must be 'creation' or 'governance'")
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return errors.New("events: topic must end with v{N} version suffix")
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return errors.New("events: topic version suffix must be numeric")
		}
	}
	return nil
}

func deriveEventType(topic string) string {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) < 3 {
		return topic
	}
	return parts[2]
}

func parseRFC3339(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// Be permissive — RFC3339 (no fractional) also accepted.
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.UTC(), nil
}

func ensureUUIDv7(s string) string {
	if s != "" {
		return s
	}
	return uuid.Must(uuid.NewV7()).String()
}

func defaultIfEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func defaultSchemaIfZero(v int32) int32 {
	if v <= 0 {
		return 1
	}
	return v
}

func newRowID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Compile-time check.
var _ atom.EventPublisher = (*CloudPublisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder. Mirror of the
// outbox.Publisher encoder per task #33 (2026-05-16). New topics MUST add
// a case in internal/adapter/events/protomarshal/MarshalPayload.
// -----------------------------------------------------------------------------

var (
	cloudWarnedUnknownTopicsMu sync.Mutex
	cloudWarnedUnknownTopics   = map[string]bool{}
)

func encodeCloudPublisherPayload(topic string, e atom.Event, env cgcenvelope.Envelope, createdAt time.Time) ([]byte, error) {
	// Difficulty is a bounded domain value, 0 to 5, enforced on the way in at
	// internal/domain/atom/subscriber.go. The wire field is 32-bit, so the
	// bound is CHECKED here rather than assumed: the unchecked int to int32
	// conversion this replaces was only safe while that invariant held, and
	// nothing at this layer would have noticed if it stopped. An out-of-range
	// value now fails the encode loudly instead of being silently truncated
	// into a different difficulty on the wire.
	if e.Difficulty < 0 || e.Difficulty > 5 {
		return nil, fmt.Errorf("difficulty out of range [0..5]: %d", e.Difficulty)
	}
	payload := map[string]any{
		"atom_id":     e.AtomID,
		"author_gcid": e.Gcid,
		"title":       e.Title,
		"difficulty":  int32(e.Difficulty),
	}
	if e.AtomType != "" {
		payload["atom_type"] = string(e.AtomType)
	}
	if e.SourceType != "" {
		payload["source_type"] = string(e.SourceType)
	}
	if e.RevisionID != "" {
		payload["revision_id"] = e.RevisionID
	}
	if e.RevisionNumber > 0 {
		payload["revision_number"] = int32(e.RevisionNumber)
	}
	if e.CourseID != "" {
		payload["course_id"] = e.CourseID
	}
	if e.Stem != "" {
		payload["stem"] = e.Stem
	}
	if e.CognitiveLevel != "" {
		payload["cognitive_level"] = e.CognitiveLevel
	}
	if e.ReuseVisibility != "" {
		payload["reuse_visibility"] = e.ReuseVisibility
	}
	if e.AuthorDisplayName != "" {
		payload["author_display_name"] = e.AuthorDisplayName
	}
	if e.CorrectOptionID != "" {
		payload["correct_option_id"] = e.CorrectOptionID
	}
	if e.AnswerCount > 0 {
		payload["answer_count"] = int32(e.AnswerCount)
	}
	if e.HasOpenEndedQuestion {
		payload["has_open_ended_question"] = true
	}
	if !createdAt.IsZero() {
		payload["created_at"] = createdAt.UTC()
	}

	protoEnv := protomarshal.Envelope{
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

	bz, err := protomarshal.MarshalPayload(topic, protoEnv, payload)
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		return nil, err
	}

	// Topic not yet wired for binary protobuf. Log a one-shot WARN and fall
	// back to JSON so the pre-existing path doesn't regress. These rows
	// WILL be rejected by Schema Registry at publish — track + add encoders.
	cloudWarnedUnknownTopicsMu.Lock()
	if !cloudWarnedUnknownTopics[topic] {
		cloudWarnedUnknownTopics[topic] = true
		log.Printf("WARN events.CloudPublisher: topic %q has no binary protobuf encoder — payload will JSON-marshal and Schema Registry will REJECT at publish; expect outbox_deadletter. Add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	cloudWarnedUnknownTopicsMu.Unlock()

	fallback := map[string]any{
		"event_id":        e.EventID,
		"type":            string(e.Type),
		"tenant_id":       e.TenantID,
		"gcid":            e.Gcid,
		"atom_id":         e.AtomID,
		"course_id":       e.CourseID,
		"revision_id":     e.RevisionID,
		"revision_number": e.RevisionNumber,
		"title":           e.Title,
		"atom_type":       string(e.AtomType),
		"difficulty":      e.Difficulty,
		"source_type":     string(e.SourceType),
	}
	bz, jErr := json.Marshal(fallback)
	if jErr != nil {
		return nil, fmt.Errorf("events.CloudPublisher: json fallback marshal: %w", jErr)
	}
	return bz, nil
}
