// Package pubsub — model_answer_amended_subscriber consumes ADR-172 §D8.
//
// When an instructor corrects a question's canonical model_answer during OE
// grading, chora-delivery emits chora.delivery.grading.model_answer_amended.v1.
// This subscriber consumes it and APPENDS a new QuestionRevision to the
// question's oe_payload.model_answer — append-only, idempotent on event_id, the
// ONLY sanctioned creation↔delivery coupling (cross-DB stays forbidden; it rides
// an event).
//
// Decode is JSON: chora-delivery emits this via the outbox PublishCustom(payload
// map[string]any) path (assessment_handler.publishCustomEvent), so the body is a
// JSON object and the envelope (event_id / tenant_id / traceparent) rides the
// event envelope — NOT a binary-proto payload.
//
// Provenance (ADR-172 §D7): the revision is authored by the instructor
// (AuthoredByGCID = actor_gcid → HUMAN provenance) with SourceMetadata.source =
// "grading_amendment" + the submission/assessment/question refs. SourceType stays
// `manual` (a human edit) — the grading-origin tag lives in SourceMetadata, so no
// new enum value / migration is required.
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// QuestionAmendPort is the minimal subset of ports.QuestionRepository this
// subscriber needs — declared here so unit tests stub it without the full repo.
type QuestionAmendPort interface {
	GetByID(ctx context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error)
	AppendRevision(ctx context.Context, rev *question.QuestionRevision) error
}

// DefaultAmendInboxTTL bounds the idempotency window (Pub/Sub redelivery window).
const DefaultAmendInboxTTL = 24 * time.Hour

// RevisionSourceGradingAmendment is the SourceMetadata.source tag (ADR-172 §D8)
// marking a revision that originated from an instructor's grading-time model-
// answer correction (vs an authoring-UI edit).
const RevisionSourceGradingAmendment = "grading_amendment"

// modelAnswerAmendedEvent is the JSON body of model_answer_amended.v1.
type modelAnswerAmendedEvent struct {
	SubmissionID      string `json:"submission_id"`
	AssessmentID      string `json:"assessment_id"`
	TestSetQuestionID string `json:"test_set_question_id"`
	QuestionID        string `json:"question_id"`
	AtomID            string `json:"atom_id"`
	NewModelAnswer    string `json:"new_model_answer"`
	ActorGCID         string `json:"actor_gcid"`
	AmendedAt         string `json:"amended_at"`
}

// ModelAnswerAmendedSubscriber appends a grading-amendment QuestionRevision.
type ModelAnswerAmendedSubscriber struct {
	repo  QuestionAmendPort
	inbox idempotent.Store
	ttl   time.Duration
	log   *slog.Logger
}

// NewModelAnswerAmendedSubscriber wires the subscriber. A nil inbox defaults to
// the in-memory store (dev/test); production should inject an idempotent.Postgres
// Store against chora_creation idempotency_keys (same pattern as the closure
// subscriber).
func NewModelAnswerAmendedSubscriber(repo QuestionAmendPort) *ModelAnswerAmendedSubscriber {
	return &ModelAnswerAmendedSubscriber{
		repo:  repo,
		inbox: idempotent.NewMemoryStore(),
		ttl:   DefaultAmendInboxTTL,
		log:   slog.Default(),
	}
}

// WithInbox injects a durable idempotency store (prod: PostgresStore).
func (s *ModelAnswerAmendedSubscriber) WithInbox(inbox idempotent.Store) *ModelAnswerAmendedSubscriber {
	if inbox != nil {
		s.inbox = inbox
	}
	return s
}

// WithLogger overrides the default slog logger (test seam).
func (s *ModelAnswerAmendedSubscriber) WithLogger(l *slog.Logger) *ModelAnswerAmendedSubscriber {
	if l != nil {
		s.log = l
	}
	return s
}

// Handle is the wire handler for chora.delivery.grading.model_answer_amended.v1
// (matches the startAiAssistTerminalReceiveLoop handler shape).
//
// Returns:
//   - nil → ACK: success, idempotent dedupe-hit, unknown/soft-deleted question,
//     non-OE question, or an empty new_model_answer (nothing to amend). These are
//     business no-ops, not retryable failures.
//   - error → NACK: decode failure, missing tenant_id, or a repo write failure;
//     the broker retries per the subscription retry_policy and DLQs after
//     max_delivery_attempts.
func (s *ModelAnswerAmendedSubscriber) Handle(
	ctx context.Context,
	msg eventbus.Message,
) error {
	if s.repo == nil {
		return errors.New("model_answer_amended: repo is nil")
	}

	var ev modelAnswerAmendedEvent
	if err := json.Unmarshal(msg.Payload, &ev); err != nil {
		return fmt.Errorf("model_answer_amended: decode: %w", err)
	}

	tenantID := preferEnvelopeField(msg.Envelope.TenantID, "")
	if tenantID == "" {
		return fmt.Errorf("model_answer_amended: missing tenant_id (envelope=%+v)", msg.Envelope)
	}
	if strings.TrimSpace(ev.QuestionID) == "" {
		return fmt.Errorf("model_answer_amended: missing question_id (submission=%q)", ev.SubmissionID)
	}

	// Idempotency on event_id (envelope rides the event envelope). A redelivered
	// event must NOT append a duplicate revision.
	eventID := preferEnvelopeField(msg.Envelope.EventID, msg.Envelope.IdempotencyKey)
	if eventID == "" {
		// No envelope id — fall back to a deterministic key from the amendment
		// content so at-least-once redelivery still dedupes.
		eventID = ev.SubmissionID + ":" + ev.TestSetQuestionID + ":" + ev.AmendedAt
	}
	dedupKey := "grading_amendment:" + eventID

	return s.inbox.Process(ctx, dedupKey, s.ttl, func() error {
		return s.applyAmendment(ctx, tenantID, ev)
	})
}

// applyAmendment loads the question + appends a grading-amendment revision.
func (s *ModelAnswerAmendedSubscriber) applyAmendment(
	ctx context.Context, tenantID string, ev modelAnswerAmendedEvent,
) error {
	newModelAnswer := strings.TrimSpace(ev.NewModelAnswer)
	if newModelAnswer == "" {
		s.log.LogAttrs(ctx, slog.LevelWarn, "model_answer_amended_empty",
			slog.String("question_id", ev.QuestionID), slog.String("tenant_id", tenantID))
		return nil // nothing to amend — ACK
	}

	q, _, err := s.repo.GetByID(ctx, tenantID, ev.QuestionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			s.log.LogAttrs(ctx, slog.LevelWarn, "model_answer_amended_unknown_question",
				slog.String("question_id", ev.QuestionID), slog.String("tenant_id", tenantID))
			return nil // unknown / soft-deleted — ACK (don't poison the subscription)
		}
		return fmt.Errorf("model_answer_amended: GetByID: %w", err)
	}

	if q.Type != question.TypeOpenEnded || q.OE == nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "model_answer_amended_non_oe",
			slog.String("question_id", ev.QuestionID), slog.String("type", string(q.Type)))
		return nil // not an OE question — no model_answer to amend — ACK
	}

	// Preserve the existing rubric + image URLs; only the model_answer changes.
	newOE := &question.OEPayload{
		ModelAnswer:    newModelAnswer,
		WeightedRubric: q.OE.WeightedRubric,
		ImageURL:       q.OE.ImageURL,
		AnswerImageURL: q.OE.AnswerImageURL,
	}

	authorGCID := ev.ActorGCID
	if strings.TrimSpace(authorGCID) == "" {
		authorGCID = q.AuthorGcid // defensive — ApplyUpdate requires a non-empty author
	}

	_, rev, err := q.ApplyUpdate(question.UpdateParams{
		OE:         newOE,
		AuthorGcid: authorGCID,
		SourceType: atom.SourceManual, // human edit; grading-origin tag in SourceMetadata
	})
	if err != nil {
		return fmt.Errorf("model_answer_amended: ApplyUpdate: %w", err)
	}

	// ADR-172 §D8 provenance — tag the grading origin + correlation refs.
	rev.SourceMetadata = map[string]string{
		"source":               RevisionSourceGradingAmendment,
		"submission_id":        ev.SubmissionID,
		"assessment_id":        ev.AssessmentID,
		"test_set_question_id": ev.TestSetQuestionID,
		"amended_by_gcid":      ev.ActorGCID,
		"amended_at":           ev.AmendedAt,
	}

	if err := s.repo.AppendRevision(ctx, rev); err != nil {
		return fmt.Errorf("model_answer_amended: AppendRevision: %w", err)
	}

	s.log.LogAttrs(ctx, slog.LevelInfo, "model_answer_amended",
		slog.String("question_id", ev.QuestionID),
		slog.String("tenant_id", tenantID),
		slog.String("submission_id", ev.SubmissionID),
		slog.String("revision_id", rev.RevisionID),
	)
	return nil
}
