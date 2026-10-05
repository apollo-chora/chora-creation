// question_batch_publisher.go — transactional-outbox publisher for
// chora.creation.question_batch.accepted.v1 (Lane 1c, CHO-1703 / ADR-180
// D10).
//
// Mirrors CollectionOutboxPublisher (the shared outbox_events table + the
// existing outbox.Dispatcher drain) with ONE load-bearing difference: the
// row payload is BINARY protobuf encoded AT INSERT via protomarshal —
// per the in_app.created dead-letter lesson, a Schema-Registry-attached
// topic with a JSON payload dead-letters silently at publish. The encoder
// is round-trip-proven against the generated pb in
// protomarshal_question_batch_test.go.
//
// Exactly-once intent: the CALLER (acceptQuestionJob) publishes only after
// winning QuestionJobRepository.TransitionFromSucceeded; the row's
// idempotency_key ("{topic}|{job_id}") additionally collapses broker-side
// duplicates. Subscribers still treat job_id as the idempotency key
// (Pub/Sub is at-least-once; delivery keys test_sets.source_job_id UNIQUE).
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// topicQuestionBatchAccepted is the canonical Lane 1c topic.
const topicQuestionBatchAccepted = "chora.creation.question_batch.accepted.v1"

// QuestionBatchOutboxConfig wires the publisher.
type QuestionBatchOutboxConfig struct {
	// Store is the outbox-table backend. Required.
	Store creationoutbox.Store

	// SourceProject defaults to "chora-local".
	SourceProject string

	// SourceService defaults to "chora-creation".
	SourceService string

	// Now is injectable for tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// QuestionBatchOutboxPublisher satisfies ports.QuestionBatchAcceptedPublisher.
type QuestionBatchOutboxPublisher struct {
	cfg QuestionBatchOutboxConfig
}

// NewQuestionBatchOutboxPublisher constructs the publisher.
func NewQuestionBatchOutboxPublisher(cfg QuestionBatchOutboxConfig) *QuestionBatchOutboxPublisher {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.SourceProject == "" {
		cfg.SourceProject = "chora-local"
	}
	if cfg.SourceService == "" {
		cfg.SourceService = "chora-creation"
	}
	return &QuestionBatchOutboxPublisher{cfg: cfg}
}

// Compile-time port assertion.
var _ ports.QuestionBatchAcceptedPublisher = (*QuestionBatchOutboxPublisher)(nil)

// PublishQuestionBatchAccepted writes the BINARY-encoded event as a pending
// outbox row. The existing Dispatcher drains it to Pub/Sub.
func (p *QuestionBatchOutboxPublisher) PublishQuestionBatchAccepted(ctx context.Context, evt ports.QuestionBatchAcceptedEvent) error {
	if p.cfg.Store == nil {
		return errors.New("question_batch_outbox: store not wired")
	}
	if strings.TrimSpace(evt.JobID) == "" {
		return errors.New("question_batch_outbox: JobID required")
	}
	if strings.TrimSpace(evt.TenantID) == "" {
		return errors.New("question_batch_outbox: TenantID required")
	}
	if strings.TrimSpace(evt.AuthorGCID) == "" {
		return errors.New("question_batch_outbox: AuthorGCID required")
	}
	if strings.TrimSpace(evt.TestSetTitle) == "" {
		return errors.New("question_batch_outbox: TestSetTitle required (D1 — the curated header)")
	}
	if len(evt.Items) == 0 {
		return errors.New("question_batch_outbox: at least one item required (no empty test sets)")
	}

	now := p.cfg.Now()
	acceptedAt := evt.AcceptedAt
	if acceptedAt.IsZero() {
		acceptedAt = now
	}

	eventID := newUUIDv7()
	// Stable per-job idempotency key — the broker collapses redelivered
	// duplicates; delivery's source_job_id UNIQUE is the final guard.
	idemKey := topicQuestionBatchAccepted + "|" + evt.JobID

	envelope := map[string]string{
		"event_id":        eventID,
		"idempotency_key": idemKey,
		"tenant_id":       evt.TenantID,
		"gcid":            evt.AuthorGCID,
		"occurred_at":     acceptedAt.Format(time.RFC3339Nano),
		"published_at":    now.Format(time.RFC3339Nano),
		"traceparent":     evt.Traceparent,
		"tracestate":      "",
		"source_project":  p.cfg.SourceProject,
		"source_service":  p.cfg.SourceService,
		"schema_version":  strconv.Itoa(1),
		// Per the proto doc comment (ADR-141): D1 accountability evidence.
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "",
	}

	items := make([]map[string]any, 0, len(evt.Items))
	for _, it := range evt.Items {
		items = append(items, map[string]any{
			"question_atom_id": it.QuestionAtomID,
			"question_id":      it.QuestionID,
			"question_type":    it.QuestionType,
			"points":           it.Points,
			"display_order":    it.DisplayOrder,
		})
	}
	sourceFiles := make([]map[string]any, 0, len(evt.SourceFiles))
	for _, sf := range evt.SourceFiles {
		sourceFiles = append(sourceFiles, map[string]any{
			"blob_uri":  sf.BlobURI,
			"mime_type": sf.MimeType,
			"role":      sf.Role,
		})
	}

	payload := map[string]any{
		"job_id":       evt.JobID,
		"host_atom_id": evt.HostAtomID,
		"tenant_id":    evt.TenantID,
		"author_gcid":  evt.AuthorGCID,
		"test_set": map[string]any{
			"title":       evt.TestSetTitle,
			"description": evt.TestSetDescription,
		},
		"items":        items,
		"source_files": sourceFiles,
		"accepted_at":  acceptedAt,
		// Envelope IMDA fields read by encodeEnvelope from the payload map.
		"chora_imda_dimension": "accountability",
	}

	env := protomarshal.Envelope{
		EventID:        eventID,
		IdempotencyKey: idemKey,
		TenantID:       evt.TenantID,
		GCID:           evt.AuthorGCID,
		OccurredAt:     acceptedAt,
		PublishedAt:    now,
		Traceparent:    evt.Traceparent,
		SourceProject:  p.cfg.SourceProject,
		SourceService:  p.cfg.SourceService,
		SchemaVersion:  1,
	}

	// BINARY encode at insert — protomarshal fails loud on a shape error;
	// there is deliberately NO JSON fallback for this topic.
	payloadBytes, err := protomarshal.MarshalPayload(topicQuestionBatchAccepted, env, payload)
	if err != nil {
		return fmt.Errorf("question_batch_outbox: binary encode: %w", err)
	}

	row := creationoutbox.Row{
		ID:             eventID,
		TenantID:       evt.TenantID,
		GCID:           evt.AuthorGCID,
		AggregateType:  "question_generation_job",
		AggregateID:    evt.JobID,
		EventType:      "creation.question_batch.accepted",
		Topic:          topicQuestionBatchAccepted,
		Payload:        payloadBytes,
		Envelope:       envelope,
		IdempotencyKey: idemKey,
		OccurredAt:     acceptedAt.UTC(),
	}
	return p.cfg.Store.Insert(ctx, row)
}
