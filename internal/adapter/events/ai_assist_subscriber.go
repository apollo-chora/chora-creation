// AiAssistSubscriber for the qgen 2-agent crew terminal events
// (Step 4d per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md).
//
// Receives chora.creation.ai_assist.completed.v1 + .refused.v1 emitted
// by the chora-ai-kernel-orchestrator's qgen_crew LangGraph state
// machine after it finishes (or refuses) one AI Assist run. Maps the
// payload to AiAssistJobsRepository.UpdateCompleted /  .UpdateRefused
// so the chora-creation HTTP handler's GET /api/atoms/ai-assist/
// {job_id} can surface terminal state to the FE.
//
// Hexagonal:
//   - INBOUND ADAPTER from Pub/Sub
//   - depends on ports.AiAssistJobsRepository (this service's DB only —
//     cross-DB queries forbidden per .claude/rules/ddd-enforcement.md)
//
// Idempotency: inbox dedupe via chora-go-common/idempotent.Store keyed
// on assist_id (== job_id). At-least-once Pub/Sub delivery → repeat
// events are dropped after the first UPDATE. Mirrors closure_subscriber
// pattern.
//
// D6 resilience:
//   - pod-death survival: subscriber resumes from Pub/Sub un-ACK'd
//     messages on restart; idempotent UPDATE re-applies cleanly
//   - DLQ: configured on the topic side (chora-infra/terraform/modules/
//     m10-data-plane/main.tf §dlq pairing); handler errors → NACK →
//     Pub/Sub retries → DLQ after redelivery exhaustion
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// Topic constants for the qgen 2-agent crew terminal events.
const (
	TopicAiAssistCompleted = "chora.creation.ai_assist.completed.v1"
	TopicAiAssistRefused   = "chora.creation.ai_assist.refused.v1"
)

// AiAssistInboxTTL is the dedupe-key retention window. 7d covers
// Pub/Sub's default max redelivery window; idempotent UPDATEs make
// shorter windows safe too. Production may tune via WithInboxTTL.
const AiAssistInboxTTL = 7 * 24 * time.Hour

// AiAssistCompletedPayload mirrors the chora.creation.ai_assist.
// completed.v1 wire shape (chora-contracts/proto/events/creation/
// ai_assist.proto §AiAssistCompleted + its 2026-05-17 additions).
type AiAssistCompletedPayload struct {
	AssistID             string `json:"assist_id"`
	TenantID             string `json:"tenant_id"`
	CandidatePayloadJSON string `json:"candidate_payload_json"`
	PipelineTraceJSON    string `json:"pipeline_trace_json"`
	QualityWarning       bool   `json:"quality_warning"`
	AttemptCount         int    `json:"attempt_count"`
	CriticNotes          string `json:"critic_notes"`
	ManaCharged          int    `json:"mana_charged"`
}

// AiAssistRefusedPayload mirrors chora.creation.ai_assist.refused.v1.
type AiAssistRefusedPayload struct {
	AssistID                 string `json:"assist_id"`
	TenantID                 string `json:"tenant_id"`
	RefusalReason            string `json:"refusal_reason"`      // GUARDRAIL_PRE | GUARDRAIL_POST | VALIDATION
	ModelArmorVerdict        string `json:"model_armor_verdict"` // e.g. "armor:pii_high_risk_block"
	UserFacingMessage        string `json:"user_facing_message"`
	LastCandidatePayloadJSON string `json:"last_candidate_payload_json"`
	PipelineTraceJSON        string `json:"pipeline_trace_json"`
	AttemptCount             int    `json:"attempt_count"`
	ManaCharged              int    `json:"mana_charged"`
}

// AiAssistSubscriber consumes the qgen 2-agent crew terminal events.
type AiAssistSubscriber struct {
	repo  ports.AiAssistJobsRepository
	inbox idempotent.Store
	ttl   time.Duration
	log   *slog.Logger
}

// NewAiAssistSubscriber wires the subscriber.
func NewAiAssistSubscriber(
	repo ports.AiAssistJobsRepository,
	inbox idempotent.Store,
) *AiAssistSubscriber {
	return &AiAssistSubscriber{
		repo:  repo,
		inbox: inbox,
		ttl:   AiAssistInboxTTL,
		log:   slog.Default(),
	}
}

// WithInboxTTL overrides the dedupe retention window.
func (s *AiAssistSubscriber) WithInboxTTL(ttl time.Duration) *AiAssistSubscriber {
	s.ttl = ttl
	return s
}

// WithLogger overrides the default slog logger.
func (s *AiAssistSubscriber) WithLogger(l *slog.Logger) *AiAssistSubscriber {
	s.log = l
	return s
}

// -----------------------------------------------------------------------------
// HandleCompleted — chora.creation.ai_assist.completed.v1
// -----------------------------------------------------------------------------

// HandleCompleted is the Pub/Sub message handler for the completed.v1
// topic. Returns:
//   - nil on success (ACK)
//   - nil on dedupe-hit (also ACK — already processed)
//   - error on transient failure (NACK; Pub/Sub will retry; DLQ catches
//     poison after redelivery exhaustion)
func (s *AiAssistSubscriber) HandleCompleted(ctx context.Context, payload AiAssistCompletedPayload) error {
	if payload.AssistID == "" || payload.TenantID == "" {
		return fmt.Errorf("ai_assist completed: missing assist_id or tenant_id")
	}
	key := dedupeKey(TopicAiAssistCompleted, payload.AssistID)
	// Process is atomic: it claims the key, runs fn, and on fn success
	// records the key for the TTL. On fn error the key is NOT claimed —
	// retry will run again. On dedupe hit fn is skipped and nil returned.
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		if err := s.repo.UpdateCompleted(
			ctx,
			payload.TenantID,
			payload.AssistID,
			[]byte(payload.CandidatePayloadJSON),
			[]byte(payload.PipelineTraceJSON),
			payload.QualityWarning,
			payload.AttemptCount,
			payload.ManaCharged,
		); err != nil {
			// On ErrNotFound the job_id is unknown — likely a Pub/Sub
			// fan-out from a sibling instance OR a started.v1 publish
			// that we never received (very rare). ACK rather than
			// retry forever.
			if errors.Is(err, aiassist.ErrNotFound) {
				s.log.LogAttrs(ctx, slog.LevelWarn,
					"ai_assist_completed_unknown_job",
					slog.String("assist_id", payload.AssistID),
					slog.String("tenant_id", payload.TenantID),
				)
				return nil // ACK; idempotent.Store will record dedupe key
			}
			// Transient — NACK + retry. fn-error means key NOT claimed.
			return fmt.Errorf("ai_assist completed: UpdateCompleted: %w", err)
		}
		s.log.LogAttrs(ctx, slog.LevelInfo,
			"ai_assist_completed",
			slog.String("assist_id", payload.AssistID),
			slog.String("tenant_id", payload.TenantID),
			slog.Bool("quality_warning", payload.QualityWarning),
			slog.Int("attempt_count", payload.AttemptCount),
		)
		return nil
	})
}

// -----------------------------------------------------------------------------
// HandleRefused — chora.creation.ai_assist.refused.v1
// -----------------------------------------------------------------------------

// HandleRefused is the Pub/Sub message handler for the refused.v1 topic.
// Same ACK/NACK semantics as HandleCompleted.
func (s *AiAssistSubscriber) HandleRefused(ctx context.Context, payload AiAssistRefusedPayload) error {
	if payload.AssistID == "" || payload.TenantID == "" {
		return fmt.Errorf("ai_assist refused: missing assist_id or tenant_id")
	}
	if payload.RefusalReason == "" {
		return fmt.Errorf("ai_assist refused: empty refusal_reason (orchestrator bug)")
	}
	reason := aiassist.RefusalReason(payload.RefusalReason)
	if !reason.Valid() || reason == "" {
		return fmt.Errorf("ai_assist refused: invalid refusal_reason %q", payload.RefusalReason)
	}
	key := dedupeKey(TopicAiAssistRefused, payload.AssistID)
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		if err := s.repo.UpdateRefused(
			ctx,
			payload.TenantID,
			payload.AssistID,
			reason,
			payload.ModelArmorVerdict,
			payload.UserFacingMessage,
			[]byte(payload.LastCandidatePayloadJSON),
			[]byte(payload.PipelineTraceJSON),
			payload.AttemptCount,
			payload.ManaCharged,
		); err != nil {
			if errors.Is(err, aiassist.ErrNotFound) {
				s.log.LogAttrs(ctx, slog.LevelWarn,
					"ai_assist_refused_unknown_job",
					slog.String("assist_id", payload.AssistID),
					slog.String("tenant_id", payload.TenantID),
				)
				return nil // ACK
			}
			return fmt.Errorf("ai_assist refused: UpdateRefused: %w", err)
		}
		s.log.LogAttrs(ctx, slog.LevelInfo,
			"ai_assist_refused",
			slog.String("assist_id", payload.AssistID),
			slog.String("tenant_id", payload.TenantID),
			slog.String("refusal_reason", payload.RefusalReason),
			slog.String("armor_verdict", payload.ModelArmorVerdict),
		)
		return nil
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func dedupeKey(topic, assistID string) string {
	return topic + "::" + assistID
}
