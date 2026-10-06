// ai_assist_terminal_subscriber.go — wire-level Pub/Sub adapter for the
// qgen 2-agent crew terminal events.
//
// Bridges raw Cloud Pub/Sub messages (binary protobuf payload + canonical
// message attributes) to chora-creation's AiAssistJobsRepository.
// Receives:
//
//   - chora.creation.ai_assist.completed.v1
//   - chora.creation.ai_assist.refused.v1
//
// Published by services/chora-ai-kernel-orchestrator's qgen_crew_publisher.py
// after the LangGraph state machine reaches a terminal node. The orchestrator
// uses an outbox dispatcher that sets canonical Pub/Sub attributes
// (idempotency_key, event_id, tenant_id, gcid) AND a binary-protobuf payload
// validated against the Schema Registry schema attached to the topic.
//
// Per [[feedback-no-stubs-real-wiring]]:
//   - This adapter ONLY decodes wire bytes + dispatches to the repo. Higher-
//     level inbox dedupe + log-on-not-found semantics already live in
//     services/chora-creation/internal/adapter/events/ai_assist_subscriber.go
//     (the JSON-payload variant retained for in-process delivery + tests).
//     The orchestrator owns idempotency_key in the envelope; downstream
//     dedupe wraps this adapter externally (M14 chain).
//   - Subscriber attribute extraction prefers Pub/Sub MESSAGE ATTRIBUTES over
//     decoded envelope bytes (the orchestrator outbox sets BOTH). Falling
//     back to the envelope fields covers producers that publish without
//     attributes (none today, but the failsafe is cheap).
//
// Naming convention follows pubsubadapter.QuestionSubscriber: an inbound
// adapter that owns the wire-format ↔ port translation. The cmd/server/
// main.go wiring spawns a Receive goroutine per subscription that calls
// HandleCompleted / HandleRefused.
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

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// AiAssistJobsPort is the minimal subset of ports.AiAssistJobsRepository
// the terminal subscriber needs. Declared here so unit tests can stub it
// without pulling the full repo interface.
type AiAssistJobsPort interface {
	UpdateCompleted(
		ctx context.Context,
		tenantID, jobID string,
		resultPayload, pipelineTrace []byte,
		qualityWarning bool,
		attemptCount, manaCharged int,
	) error
	UpdateRefused(
		ctx context.Context,
		tenantID, jobID string,
		reason aiassist.RefusalReason,
		armorVerdict, userFacingMsg string,
		lastCandidatePayload, pipelineTrace []byte,
		attemptCount, manaCharged int,
	) error
	UpdateFailed(
		ctx context.Context,
		tenantID, jobID string,
		pipelineTrace []byte,
		errMsg string,
		attemptCount, manaCharged int,
	) error
}

// AiAssistTerminalSubscriber dispatches binary-proto-encoded terminal
// events to AiAssistJobsRepository.UpdateCompleted / UpdateRefused.
//
// W2 Seam A — dual-dispatch. Both the legacy /api/atoms/ai-assist surface AND
// the question-jobs ai_draft path (POST /api/atoms/{id}/question-jobs) flow
// through the SAME two terminal subscriptions on the SAME wire bytes; no
// routing attribute distinguishes them. When wired WithQuestionJobs(...), the
// subscriber resolves assist_id against question_generation_jobs FIRST: a hit
// (ai_draft + running) routes onto the question-job mapping; a miss
// (question.ErrNotFound) falls through to the legacy ai_assist_jobs path.
type AiAssistTerminalSubscriber struct {
	jobsRepo AiAssistJobsPort
	log      *slog.Logger

	// Question-jobs path (optional; nil ⇒ legacy-only behaviour). All three
	// are required together when WithQuestionJobs is called.
	questionJobs ports.QuestionJobRepository
	mana         ports.ManaLedger
	jobPub       JobEventPublisher

	// Lane 1c (D15) — source-chunk store backing the citation-verification
	// pass on batch completions. Nil ⇒ citations pass through unstamped
	// (verified stays null / AI-reported); the completion itself is never
	// blocked on verification.
	chunkRepo ports.SourceChunkRepository
}

// NewAiAssistTerminalSubscriber wires the subscriber. Pass the pgx-backed
// repo from main.go; tests inject a stub satisfying AiAssistJobsPort.
func NewAiAssistTerminalSubscriber(jobsRepo AiAssistJobsPort) *AiAssistTerminalSubscriber {
	return &AiAssistTerminalSubscriber{
		jobsRepo: jobsRepo,
		log:      slog.Default(),
	}
}

// WithQuestionJobs enables the dual-dispatch question-jobs path (W2 Seam A).
// questionJobs resolves assist_id → question_generation_jobs; mana refunds on
// refusal; jobPub emits chora.creation.question.generation_completed.v1.
func (s *AiAssistTerminalSubscriber) WithQuestionJobs(
	questionJobs ports.QuestionJobRepository,
	mana ports.ManaLedger,
	jobPub JobEventPublisher,
) *AiAssistTerminalSubscriber {
	s.questionJobs = questionJobs
	s.mana = mana
	s.jobPub = jobPub
	return s
}

// WithChunkStore enables the Lane 1c (D15) citation-verification pass on
// batch completions. Nil-safe — without it citations stay AI-reported.
func (s *AiAssistTerminalSubscriber) WithChunkStore(chunks ports.SourceChunkRepository) *AiAssistTerminalSubscriber {
	s.chunkRepo = chunks
	return s
}

// WithLogger overrides the default slog logger (test seam).
func (s *AiAssistTerminalSubscriber) WithLogger(l *slog.Logger) *AiAssistTerminalSubscriber {
	s.log = l
	return s
}

// HandleCompleted is the wire handler for chora.creation.ai_assist.completed.v1.
//
// Returns:
//   - nil on success: caller (CloudSubscriber receive loop) ACKs the message.
//   - error on decode failure OR repo write failure: caller NACKs; the
//     broker retries per the subscription's retry_policy and DLQs after
//     max_delivery_attempts.
//
// On aiassist.ErrNotFound (the assist_id never existed in chora_creation —
// could happen if started.v1 publish was lost) we ACK with a WARN log;
// the orchestrator is the source of truth for the run, but the FE's GET
// will surface a 404 which is the right fail-loud behaviour.
func (s *AiAssistTerminalSubscriber) HandleCompleted(
	ctx context.Context,
	msg eventbus.Message,
) error {
	if s.jobsRepo == nil {
		return errors.New("ai_assist_terminal_subscriber: jobsRepo is nil")
	}
	payload, err := protomarshal.DecodeAiAssistCompleted(msg.Payload)
	if err != nil {
		return fmt.Errorf("ai_assist completed: decode: %w", err)
	}
	tenantID := preferEnvelopeField(msg.Envelope.TenantID, payload.TenantID)
	jobID := payload.AssistID
	if tenantID == "" || jobID == "" {
		return fmt.Errorf("ai_assist completed: missing tenant_id or assist_id (tenant=%q assist=%q)",
			tenantID, jobID)
	}

	// W2 Seam A — try the question-jobs path first. routed=true means it was
	// claimed (or fully handled); only fall through to legacy when this is
	// NOT a question-job (question.ErrNotFound / wrong type / not running).
	if routed, err := s.tryQuestionJobCompleted(ctx, tenantID, jobID, payload); routed || err != nil {
		return err
	}

	if err := s.jobsRepo.UpdateCompleted(
		ctx,
		tenantID,
		jobID,
		[]byte(payload.CandidatePayloadJSON),
		[]byte(payload.PipelineTraceJSON),
		payload.QualityWarning,
		int(payload.AttemptCount),
		int(payload.ManaCharged),
	); err != nil {
		if errors.Is(err, aiassist.ErrNotFound) {
			s.log.LogAttrs(ctx, slog.LevelWarn,
				"ai_assist_completed_unknown_job",
				slog.String("assist_id", jobID),
				slog.String("tenant_id", tenantID),
			)
			return nil // ACK — orchestrator is source of truth
		}
		return fmt.Errorf("ai_assist completed: UpdateCompleted: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"ai_assist_completed",
		slog.String("assist_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.Bool("quality_warning", payload.QualityWarning),
		slog.Int("attempt_count", int(payload.AttemptCount)),
		slog.Int("mana_charged", int(payload.ManaCharged)),
	)
	return nil
}

// HandleProgress is the wire handler for chora.creation.ai_assist.progress.v1 —
// the mid-run live-trace event (CR Phase 1 streaming). It updates ONLY a running
// question-job's pipeline_trace (no status transition; no legacy ai_assist_jobs
// fall-through — that path has no incremental-trace writer). Ephemeral semantics:
// a not-claimable (terminal / legacy / unknown) assist_id ACKs since the terminal
// completed.v1 is authoritative; only a transient repo error NACKs. The repo's
// monotonic + status='running' SQL guard absorbs out-of-order / duplicate
// (dev+prod double-dispatch) deliveries.
func (s *AiAssistTerminalSubscriber) HandleProgress(
	ctx context.Context,
	msg eventbus.Message,
) error {
	payload, err := protomarshal.DecodeAiAssistProgress(msg.Payload)
	if err != nil {
		return fmt.Errorf("ai_assist progress: decode: %w", err)
	}
	tenantID := preferEnvelopeField(msg.Envelope.TenantID, payload.TenantID)
	jobID := payload.AssistID
	if tenantID == "" || jobID == "" {
		return fmt.Errorf("ai_assist progress: missing tenant_id or assist_id (tenant=%q assist=%q)",
			tenantID, jobID)
	}

	job, claimed, err := s.resolveQuestionJob(ctx, tenantID, jobID)
	if err != nil {
		return fmt.Errorf("ai_assist progress: %w", err) // transient → NACK
	}
	if !claimed {
		// Not a running question-job (already terminal / legacy drawer / unknown
		// assist_id) — ACK. The terminal completed.v1 carries the final trace.
		return nil
	}

	applied, uerr := s.questionJobs.UpdatePipelineTraceOnly(
		ctx, job.TenantID, job.JobID, []byte(payload.PipelineTraceJSON),
	)
	if uerr != nil {
		return fmt.Errorf("ai_assist progress: UpdatePipelineTraceOnly: %w", uerr) // NACK
	}
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"ai_assist_progress",
		slog.String("assist_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.Int("step_index", int(payload.StepIndex)),
		slog.String("step_name", payload.StepName),
		slog.Bool("applied", applied),
	)
	return nil
}

// HandleChunkCompleted is the wire handler for
// chora.creation.ai_assist.chunk_completed.v1 (ADR-251 D5, CHO-2398): one
// finished chunk's published-shape candidates while the job is still RUNNING.
// The repo's slot guard (chunk_index key, status='running') absorbs duplicate
// and out-of-order deliveries in SQL. A not-claimable (terminal / legacy /
// unknown) assist_id ACKs since the terminal completed.v1 is authoritative;
// a candidate_count mismatch NACKs fail-loud (never a partial apply); a
// transient repo error NACKs.
func (s *AiAssistTerminalSubscriber) HandleChunkCompleted(
	ctx context.Context,
	msg eventbus.Message,
) error {
	payload, err := protomarshal.DecodeAiAssistChunkCompleted(msg.Payload)
	if err != nil {
		return fmt.Errorf("ai_assist chunk_completed: decode: %w", err)
	}
	tenantID := preferEnvelopeField(msg.Envelope.TenantID, payload.TenantID)
	jobID := payload.AssistID
	if tenantID == "" || jobID == "" {
		return fmt.Errorf("ai_assist chunk_completed: missing tenant_id or assist_id (tenant=%q assist=%q)",
			tenantID, jobID)
	}

	// Fail-loud sanity: the producer stamps candidate_count from the same
	// array it serialises; a mismatch means a corrupted or hand-rolled
	// payload and must never partially apply.
	var candidates []json.RawMessage
	if err := json.Unmarshal([]byte(payload.CandidatesPayloadJSON), &candidates); err != nil {
		return fmt.Errorf("ai_assist chunk_completed: candidates_payload_json parse: %w", err)
	}
	if len(candidates) != int(payload.CandidateCount) {
		return fmt.Errorf(
			"ai_assist chunk_completed: candidate_count mismatch (declared %d, carried %d)",
			payload.CandidateCount, len(candidates),
		)
	}

	job, claimed, err := s.resolveQuestionJob(ctx, tenantID, jobID)
	if err != nil {
		return fmt.Errorf("ai_assist chunk_completed: %w", err) // transient -> NACK
	}
	if !claimed {
		// Not a running question-job (already terminal / legacy drawer /
		// unknown assist_id) - ACK. The terminal carries the full set.
		return nil
	}

	applied, aerr := s.questionJobs.ApplyChunkCandidates(
		ctx, job.TenantID, job.JobID,
		int(payload.ChunkIndex), int(payload.ChunkCount),
		[]byte(payload.CandidatesPayloadJSON),
	)
	if aerr != nil {
		return fmt.Errorf("ai_assist chunk_completed: ApplyChunkCandidates: %w", aerr) // NACK
	}
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"ai_assist_chunk_completed",
		slog.String("assist_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.Int("chunk_index", int(payload.ChunkIndex)),
		slog.Int("chunk_count", int(payload.ChunkCount)),
		slog.Int("candidate_count", int(payload.CandidateCount)),
		slog.Int("warned_count", int(payload.WarnedCount)),
		slog.Bool("applied", applied),
	)
	return nil
}

// HandleRefused is the wire handler for chora.creation.ai_assist.refused.v1.
// Same ACK/NACK semantics as HandleCompleted.
func (s *AiAssistTerminalSubscriber) HandleRefused(
	ctx context.Context,
	msg eventbus.Message,
) error {
	if s.jobsRepo == nil {
		return errors.New("ai_assist_terminal_subscriber: jobsRepo is nil")
	}
	payload, err := protomarshal.DecodeAiAssistRefused(msg.Payload)
	if err != nil {
		return fmt.Errorf("ai_assist refused: decode: %w", err)
	}
	tenantID := preferEnvelopeField(msg.Envelope.TenantID, payload.TenantID)
	jobID := payload.AssistID
	if tenantID == "" || jobID == "" {
		return fmt.Errorf("ai_assist refused: missing tenant_id or assist_id (tenant=%q assist=%q)",
			tenantID, jobID)
	}

	// W2 Seam A — try the question-jobs path first.
	if routed, err := s.tryQuestionJobRefused(ctx, tenantID, jobID, payload); routed || err != nil {
		return err
	}

	if payload.RefusalReason == "" {
		return fmt.Errorf("ai_assist refused: empty refusal_reason (assist=%q)", jobID)
	}
	reason := aiassist.RefusalReason(payload.RefusalReason)
	if !reason.Valid() || reason == "" {
		return fmt.Errorf("ai_assist refused: invalid refusal_reason %q (assist=%q)",
			payload.RefusalReason, jobID)
	}
	if err := s.jobsRepo.UpdateRefused(
		ctx,
		tenantID,
		jobID,
		reason,
		payload.ModelArmorVerdict,
		payload.UserFacingMessage,
		[]byte(payload.LastCandidatePayloadJSON),
		nil, // refused.v1 schema has no pipeline_trace_json field
		int(payload.AttemptCount),
		int(payload.ManaCharged),
	); err != nil {
		if errors.Is(err, aiassist.ErrNotFound) {
			s.log.LogAttrs(ctx, slog.LevelWarn,
				"ai_assist_refused_unknown_job",
				slog.String("assist_id", jobID),
				slog.String("tenant_id", tenantID),
			)
			return nil // ACK
		}
		return fmt.Errorf("ai_assist refused: UpdateRefused: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"ai_assist_refused",
		slog.String("assist_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.String("refusal_reason", payload.RefusalReason),
		slog.String("armor_verdict", payload.ModelArmorVerdict),
		slog.Int("attempt_count", int(payload.AttemptCount)),
	)
	return nil
}

// -----------------------------------------------------------------------------
// W2 Seam A — question-jobs dual-dispatch
// -----------------------------------------------------------------------------

// resolveQuestionJob looks up assist_id (= question_generation_jobs.job_id)
// and reports whether THIS terminal event belongs to the question-jobs
// ai_draft path. Returns (job, claimed, err):
//   - claimed=true  → this is a live ai_draft question-job in running; the
//     caller owns the terminal mapping (do NOT fall through to legacy).
//   - claimed=false, err=nil → NOT a question-job (ErrNotFound), wrong
//     job_type, or no longer running (idempotent replay / dual-dispatch
//     guard) → fall through to the legacy ai_assist_jobs path.
//   - err!=nil → transient repo error → caller NACKs.
func (s *AiAssistTerminalSubscriber) resolveQuestionJob(
	ctx context.Context, tenantID, jobID string,
) (*question.ComposeJob, bool, error) {
	if s.questionJobs == nil {
		return nil, false, nil // question-jobs path not wired → legacy-only
	}
	job, err := s.questionJobs.Get(ctx, tenantID, jobID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			// NotFound is the legacy fall-through signal, NOT a NACK.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("question-job lookup: %w", err)
	}
	// ai_draft (single candidate) + batch_source_material (N-candidate array) +
	// image_regen (CHO-1819 P3 — a 1-candidate image patch onto a parent batch
	// job) + model_answer_fill (CHO-1658 — a 1-candidate fill of the author's
	// existing question) are dispatched via ai_assist.started.v2 → routed through
	// the crew terminal path.
	// ADR-195 WS9 step 2 — route on the reconstructed compose VOs (Intent/Input),
	// not job_type. Crew-dispatched = new_question-with-LLM (the retired ai_draft /
	// batch_source_material) + image_regen + model_answer_fill. A by-hand
	// new_question carries inline candidates (no crew terminal). model_answer_fill
	// always uses the LLM crew (CHO-1658 repoint off the dead us-central1 Agent
	// Engine), so it is unconditionally claimable.
	claimable := (job.Intent == question.IntentNewQuestion && job.Input.RequiresLLM()) ||
		job.Intent == question.IntentImageRegen ||
		job.Intent == question.IntentModelAnswerFill
	if !claimable || job.Status != question.JobStatusRunning {
		return nil, false, nil
	}
	return job, true, nil
}

// tryQuestionJobCompleted maps a completed.v1 onto a question-job. Returns
// (handled, err): handled=true means it was a question-job (success OR a
// fail-on-empty-candidate) and the caller must NOT fall through.
func (s *AiAssistTerminalSubscriber) tryQuestionJobCompleted(
	ctx context.Context, tenantID, jobID string, payload protomarshal.AiAssistCompletedPayload,
) (bool, error) {
	job, claimed, err := s.resolveQuestionJob(ctx, tenantID, jobID)
	if err != nil || !claimed {
		return false, err
	}

	// CHO-1819 P3 — an image_regen completion is an image PATCH onto a parent
	// candidate, NOT a candidates array. Handle it before the normalizer (which
	// would reject the [{draft_id, placement, image_url}] shape as un-usable).
	if job.Intent == question.IntentImageRegen {
		return s.completeImageRegen(ctx, job, payload)
	}

	// Normalize the verbatim crew candidate(s) → canonical candidateDraft(s).
	// The question_generation_jobs row carries no question_type column, so we
	// pass "" and let the normalizer discriminate from the payload (options ⇒
	// mcq, model_answer ⇒ oe / explicit candidate question_type field).
	//
	//   - ai_draft (count=1): candidate_payload_json is ONE candidate → 1 draft.
	//   - ai_draft (count>1, ADR-195 WS8 BE-1): dispatched through the set lane
	//     (job_kind=batch), so the orchestrator returns a {candidates:[...]} /
	//     array batch shape → N drafts. The job stays type=ai_draft, so the route
	//     is taken on the PAYLOAD shape (looksLikeBatch), not JobType.
	//   - batch_source_material: a JSON ARRAY / {candidates:...} of N → N drafts.
	//
	// quality_warning=true is a best-effort last candidate — it still SUCCEEDS
	// (don't drop it). An un-normalizable candidate (no options / no
	// model_answer) cannot succeed → fail + refund (no broken success; for batch
	// any bad element fails the whole job per the no-partial-success rule).
	var (
		drafts   []candidateDraft
		proposal json.RawMessage
	)
	if job.Input.HasFiles() || looksLikeBatch(payload.CandidatePayloadJSON) {
		batch, prop, berr := normalizeBatchCandidates([]byte(payload.CandidatePayloadJSON))
		if berr != nil {
			s.log.LogAttrs(ctx, slog.LevelWarn,
				"question_job_batch_unusable",
				slog.String("job_id", jobID),
				slog.String("tenant_id", tenantID),
				slog.String("err", berr.Error()),
			)
			s.failQuestionJob(ctx, job, "qgen_batch_unusable", "AI returned an unusable batch: "+berr.Error())
			return true, nil
		}
		for i := range batch {
			batch[i].ModelUsed = payload.ModelUsed
		}
		drafts = batch
		proposal = prop

		// Lane 1c D15 — citation verification BEFORE the FE poll surfaces
		// candidates. Best-effort: a verification failure never blocks the
		// completion (citations just stay AI-reported).
		s.verifyDraftCitations(ctx, job, drafts)
	} else {
		draft, nerr := normalizeCandidatePayload([]byte(payload.CandidatePayloadJSON), "")
		if nerr != nil {
			s.log.LogAttrs(ctx, slog.LevelWarn,
				"question_job_candidate_unusable",
				slog.String("job_id", jobID),
				slog.String("tenant_id", tenantID),
				slog.Bool("quality_warning", payload.QualityWarning),
				slog.String("err", nerr.Error()),
			)
			s.failQuestionJob(ctx, job, "qgen_candidate_unusable", "AI returned an unusable candidate: "+nerr.Error())
			return true, nil
		}
		draft.ModelUsed = payload.ModelUsed
		drafts = []candidateDraft{draft}
	}

	candJSON, merr := json.Marshal(drafts)
	if merr != nil {
		// Marshalling freshly-normalized drafts should never fail; treat as
		// transient (NACK) so we don't silently lose the candidates.
		return true, fmt.Errorf("question-job completed: marshal candidates: %w", merr)
	}

	// CHO-1819 P2 — the mixed-batch outcome projection. nil when the
	// orchestrator emits no generation_summary (single-type / legacy / pre-P2)
	// so the column stays untouched (COALESCE-preserving SQL).
	summaryJSON := buildGenerationSummaryJSON(payload)

	// One UPDATE stamps candidates + the composer proposal + the generation
	// summary + the per-step pipeline trace together (each nil arg leaves ITS
	// column untouched per the COALESCE-preserving SQL). The trace (CHO-1826
	// Gap #4) is already decoded on this completed.v1 (field 12) — the SAME
	// trace the single-mode AI-Assist drawer renders — and was previously
	// dropped here; now it persists so the canvas can show the transparency card.
	if err := s.questionJobs.UpdateStatusWithProposal(ctx, job.TenantID, job.JobID, question.JobStatusSucceeded, candJSON, proposal, summaryJSON, []byte(payload.PipelineTraceJSON), ""); err != nil {
		// Repo-write failure → NACK so the broker retries / DLQs.
		return true, fmt.Errorf("question-job completed: UpdateStatus succeeded: %w", err)
	}
	s.emitQuestionCompletion(ctx, job, question.JobStatusSucceeded, len(drafts), "", "", 0)
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"question_job_completed",
		slog.String("job_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.Int("candidate_count", len(drafts)),
		slog.Bool("quality_warning", payload.QualityWarning),
		slog.Bool("proposed_test_set", len(proposal) > 0),
	)
	return true, nil
}

// -----------------------------------------------------------------------------
// CHO-1819 P3 — review image regenerate (terminal patch)
// -----------------------------------------------------------------------------

// imageRegenSpec is the image_regen job's settings_json (written by
// createImageRegenJob): the parent batch job + the originally-requested target.
type imageRegenSpec struct {
	ParentJobID string `json:"parent_job_id"`
	DraftID     string `json:"draft_id"`
	Placement   string `json:"placement"`
}

// imagePatch is one entry of the ImageRegenRunner completed.v1
// candidate_payload_json (a 1-element array [{draft_id, placement, image_url}]).
type imagePatch struct {
	DraftID   string `json:"draft_id"`
	Placement string `json:"placement"`
	ImageURL  string `json:"image_url"`
	// ImageGcsURI — ADR-210: the NEW durable gs:// object path of the
	// regenerated image, so a subsequent regen edits THIS result (iterative
	// image-to-image) rather than the stale original. Optional (an older
	// orchestrator omits it → the prior ref is left in place).
	ImageGcsURI string `json:"image_gcs_uri"`
}

// errImageRegenFailSoft marks an image_regen patch outcome that is terminal but
// unrecoverable (parent left review, draft vanished, candidates unusable) — the
// completeImageRegen apply closure returns it so the handler ACKs + fails the
// image_regen job rather than NACKing (which a transient repo error does).
var errImageRegenFailSoft = errors.New("image_regen_fail_soft")

// completeImageRegen applies an image_regen completed.v1 to the PARENT batch
// job's targeted candidate, in place, and marks the image_regen job succeeded.
// The parent's status is preserved (stays succeeded / in review) — a candidates-
// only PatchCandidates, never a transition.
//
// Returns (handled, err); handled is always true (an image_regen job is fully
// owned here, never falls through to legacy). err != nil ⇒ a transient repo
// failure ⇒ NACK (the image_regen job is left running so a retry re-claims it).
// Terminal-but-unrecoverable cases (no parent, unusable payload, vanished
// parent/draft) FAIL the image_regen job and ACK.
//
// Ordering is load-bearing: patch the parent FIRST, mark the image_regen job
// succeeded LAST. If the parent patch NACKs, the image_regen job is still
// running so the retry re-claims it; were it marked succeeded first, a retry
// would see a non-running job and fall through to legacy — stranding the parent.
func (s *AiAssistTerminalSubscriber) completeImageRegen(
	ctx context.Context, job *question.ComposeJob, payload protomarshal.AiAssistCompletedPayload,
) (bool, error) {
	var spec imageRegenSpec
	if len(job.SettingsJSON) > 0 {
		_ = json.Unmarshal(job.SettingsJSON, &spec)
	}
	if strings.TrimSpace(spec.ParentJobID) == "" {
		s.failQuestionJob(ctx, job, "image_regen_no_parent",
			"image_regen job settings missing parent_job_id")
		return true, nil
	}

	patch, perr := parseImagePatch([]byte(payload.CandidatePayloadJSON), spec)
	if perr != nil {
		s.failQuestionJob(ctx, job, "image_regen_unusable_payload",
			"image_regen completed payload unusable: "+perr.Error())
		return true, nil
	}

	// Atomic read-modify-write of the PARENT candidates under a row lock: load
	// status + candidates FOR UPDATE, patch the targeted draft, write back — in
	// ONE tx. This serialises concurrent image_regen completions for DIFFERENT
	// drafts of the same parent; a blind full-column overwrite from a stale read
	// would clobber the other draft's new image (silent loss at accept time).
	// The parent's status is preserved (only candidate_questions_jsonb changes).
	failCategory := "image_regen_patch_failed"
	patchErr := s.questionJobs.PatchCandidatesUnderLock(ctx, job.TenantID, spec.ParentJobID,
		func(status string, current []byte) ([]byte, error) {
			// A regen landing after the parent left review (accepted/cancelled)
			// is moot — its patch would touch a historical row, not the already-
			// minted atoms. Fail soft rather than report a misleading success.
			if status != string(question.JobStatusSucceeded) {
				failCategory = "image_regen_parent_not_reviewable"
				return nil, fmt.Errorf("%w: parent status %q", errImageRegenFailSoft, status)
			}
			patched, applied, aerr := applyImagePatch(current, patch)
			if aerr != nil {
				failCategory = "image_regen_patch_failed"
				return nil, fmt.Errorf("%w: %v", errImageRegenFailSoft, aerr)
			}
			if !applied {
				failCategory = "image_regen_draft_gone"
				return nil, fmt.Errorf("%w: draft_id no longer present in parent candidates", errImageRegenFailSoft)
			}
			return patched, nil
		})
	if patchErr != nil {
		if errors.Is(patchErr, question.ErrNotFound) {
			s.failQuestionJob(ctx, job, "image_regen_parent_gone",
				"parent job not found for image_regen patch")
			return true, nil
		}
		if errors.Is(patchErr, errImageRegenFailSoft) {
			s.failQuestionJob(ctx, job, failCategory, patchErr.Error())
			return true, nil
		}
		// Transient repo failure → NACK; the image_regen job stays running so a
		// redelivery re-claims + re-patches idempotently (same url under lock).
		return true, fmt.Errorf("image_regen completed: patch parent: %w", patchErr)
	}

	// Mark the image_regen job succeeded LAST, stamping the patch on its OWN
	// candidates so the FE poll of THIS job surfaces the new url directly (no
	// parent re-fetch needed).
	patchJSON, _ := json.Marshal([]imagePatch{patch})
	if err := s.questionJobs.UpdateStatus(ctx, job.TenantID, job.JobID, question.JobStatusSucceeded, patchJSON, ""); err != nil {
		return true, fmt.Errorf("image_regen completed: UpdateStatus succeeded: %w", err)
	}

	s.emitQuestionCompletion(ctx, job, question.JobStatusSucceeded, 1, "", "", 0)
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"image_regen_completed",
		slog.String("job_id", job.JobID),
		slog.String("parent_job_id", spec.ParentJobID),
		slog.String("draft_id", patch.DraftID),
		slog.String("placement", patch.Placement),
		slog.String("tenant_id", job.TenantID),
	)
	return true, nil
}

// parseImagePatch decodes the ImageRegenRunner completed.v1 image-patch payload
// (a 1-element array; a bare object is tolerated) and fills any missing draft_id
// / placement from the image_regen job's own settings (the originally-requested
// target). Fails on an empty payload, a bad placement, or a missing image_url.
func parseImagePatch(payloadJSON []byte, spec imageRegenSpec) (imagePatch, error) {
	trimmed := strings.TrimSpace(string(payloadJSON))
	if trimmed == "" {
		return imagePatch{}, errors.New("empty payload")
	}
	var entries []imagePatch
	if trimmed[0] == '[' {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return imagePatch{}, fmt.Errorf("decode image-patch array: %w", err)
		}
	} else {
		var one imagePatch
		if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
			return imagePatch{}, fmt.Errorf("decode image-patch object: %w", err)
		}
		entries = []imagePatch{one}
	}
	if len(entries) == 0 {
		return imagePatch{}, errors.New("no image-patch entries")
	}
	p := imagePatch{
		DraftID:     strings.TrimSpace(entries[0].DraftID),
		Placement:   strings.TrimSpace(entries[0].Placement),
		ImageURL:    strings.TrimSpace(entries[0].ImageURL),
		ImageGcsURI: strings.TrimSpace(entries[0].ImageGcsURI),
	}
	if p.DraftID == "" {
		p.DraftID = strings.TrimSpace(spec.DraftID)
	}
	if p.Placement == "" {
		p.Placement = strings.TrimSpace(spec.Placement)
	}
	if p.DraftID == "" {
		return imagePatch{}, errors.New("missing draft_id")
	}
	if p.Placement != "stem" && p.Placement != "answer" {
		return imagePatch{}, fmt.Errorf("placement %q must be stem|answer", p.Placement)
	}
	if p.ImageURL == "" {
		return imagePatch{}, errors.New("missing image_url")
	}
	return p, nil
}

// applyImagePatch rewrites the targeted candidate's image url inside the parent's
// persisted candidates ([]candidateDraft — the authoritative stored shape, so a
// decode→patch→marshal round-trip is lossless, preserving image_specs etc.).
// stem→ImageURL, answer→AnswerImageURL on whichever payload (mcq|oe) is present.
// Returns (patchedJSON, applied, err): applied=false when draft_id is absent.
func applyImagePatch(parentCandJSON []byte, patch imagePatch) ([]byte, bool, error) {
	if len(parentCandJSON) == 0 {
		return nil, false, errors.New("parent has no candidates to patch")
	}
	var drafts []candidateDraft
	if err := json.Unmarshal(parentCandJSON, &drafts); err != nil {
		return nil, false, fmt.Errorf("decode parent candidates: %w", err)
	}
	url := patch.ImageURL
	applied := false
	for i := range drafts {
		if drafts[i].DraftID != patch.DraftID {
			continue
		}
		// Fail loud on a malformed draft — a regen target must be a real
		// candidate (MCQ or OE), not an empty placeholder.
		if drafts[i].MCQPayload == nil && drafts[i].OEPayload == nil {
			return nil, false, fmt.Errorf("draft %q has neither mcq nor oe payload", patch.DraftID)
		}
		// CHO-1825 — rewrite the regenerated image at the CANONICAL top-level
		// draft location (image_url / answer_image_url), consistent with the
		// initial render_image_set emit + the FE's top-level read. The published
		// question's nested mcq_payload/oe_payload image fields are populated from
		// these at accept time — the draft never nests the image itself.
		if patch.Placement == "answer" {
			drafts[i].AnswerImageURL = &url
		} else {
			drafts[i].ImageURL = &url
		}
		// ADR-210 — persist the NEW durable gs:// object path alongside the
		// signed URL so a subsequent regen edits THIS result (iterative
		// image-to-image), not the stale original. Omitted by an older
		// orchestrator ⇒ the prior gs:// ref is left untouched.
		if gcs := strings.TrimSpace(patch.ImageGcsURI); gcs != "" {
			if patch.Placement == "answer" {
				drafts[i].AnswerImageGcsURI = &gcs
			} else {
				drafts[i].ImageGcsURI = &gcs
			}
		}
		applied = true
		break
	}
	if !applied {
		return nil, false, nil
	}
	out, err := json.Marshal(drafts)
	if err != nil {
		return nil, false, fmt.Errorf("marshal patched candidates: %w", err)
	}
	return out, true, nil
}

// -----------------------------------------------------------------------------
// Lane 1c D15 — citation verification
// -----------------------------------------------------------------------------

// verifyDraftCitations matches every model-reported citation against the
// job's source_material_chunks and stamps the tri-state outcome in place:
//
//	verified=true + chunk_id — excerpt matched (normalised substring, then
//	                           fuzzy ≥0.8 token overlap; tiers cited-file+page
//	                           → cited-file → all chunks)
//	verified=false           — no match (surfaced, NEVER dropped)
//	verified=nil              — no verification possible (image source per the
//	                           job's settings.source_files, empty corpus, or
//	                           no chunk store wired)
//
// Best-effort by design: any failure logs + leaves citations unstamped.
func (s *AiAssistTerminalSubscriber) verifyDraftCitations(
	ctx context.Context, job *question.ComposeJob, drafts []candidateDraft,
) {
	if s.chunkRepo == nil {
		return
	}
	hasCitations := false
	for i := range drafts {
		if len(drafts[i].Citations) > 0 {
			hasCitations = true
			break
		}
	}
	if !hasCitations {
		return
	}

	corpus, err := s.chunkRepo.ListByJob(ctx, job.TenantID, job.JobID)
	if err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn,
			"citation_verification_chunks_unavailable",
			slog.String("job_id", job.JobID),
			slog.String("err", err.Error()),
		)
		return
	}

	imageFiles := imageSourceFileRefs(job.SettingsJSON)
	verified, unverified, aiReported := 0, 0, 0
	for di := range drafts {
		for ci := range drafts[di].Citations {
			c := &drafts[di].Citations[ci]
			isImage := false
			for _, ref := range imageFiles {
				if sourcechunk.FileRefMatches(c.SourceFile, ref) {
					isImage = true
					break
				}
			}
			v := sourcechunk.Verify(sourcechunk.Citation{
				SourceFile: c.SourceFile,
				Page:       c.Page,
				Excerpt:    c.Excerpt,
			}, corpus, isImage)
			c.Verified = v.Verified
			c.ChunkID = v.ChunkID
			switch {
			case v.Verified == nil:
				aiReported++
			case *v.Verified:
				verified++
			default:
				unverified++
			}
		}
	}
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"citation_verification_done",
		slog.String("job_id", job.JobID),
		slog.Int("verified", verified),
		slog.Int("unverified", unverified),
		slog.Int("ai_reported", aiReported),
		slog.Int("chunks", len(corpus)),
	)
}

// imageSourceFileRefs extracts the blob_uris of image-MIME source files from
// the job's settings JSONB (written by the Lane 1c multipart handler).
// Citations against these stay AI-reported — images have no text layer in v1.
func imageSourceFileRefs(settingsJSON []byte) []string {
	if len(settingsJSON) == 0 {
		return nil
	}
	var settings struct {
		SourceFiles []struct {
			BlobURI  string `json:"blob_uri"`
			MimeType string `json:"mime_type"`
		} `json:"source_files"`
	}
	if err := json.Unmarshal(settingsJSON, &settings); err != nil {
		return nil
	}
	var out []string
	for _, f := range settings.SourceFiles {
		if strings.HasPrefix(f.MimeType, "image/") && f.BlobURI != "" {
			out = append(out, f.BlobURI)
		}
	}
	return out
}

// tryQuestionJobRefused maps a refused.v1 onto a question-job: status→failed +
// mana refund + emit generation_completed (failed).
func (s *AiAssistTerminalSubscriber) tryQuestionJobRefused(
	ctx context.Context, tenantID, jobID string, payload protomarshal.AiAssistRefusedPayload,
) (bool, error) {
	job, claimed, err := s.resolveQuestionJob(ctx, tenantID, jobID)
	if err != nil || !claimed {
		return false, err
	}

	reason := strings.TrimSpace(payload.RefusalReason)
	if reason == "" {
		reason = "REFUSED"
	}
	msg := reason
	if uf := strings.TrimSpace(payload.UserFacingMessage); uf != "" {
		msg = reason + ": " + uf
	}

	if err := s.questionJobs.UpdateStatus(ctx, job.TenantID, job.JobID, question.JobStatusFailed, nil, msg); err != nil {
		return true, fmt.Errorf("question-job refused: UpdateStatus failed: %w", err)
	}
	refunded := s.refundQuestionJobMana(ctx, job, msg)
	s.emitQuestionCompletion(ctx, job, question.JobStatusFailed, 0, reason, msg, refunded)
	s.log.LogAttrs(ctx, slog.LevelInfo,
		"question_job_refused",
		slog.String("job_id", jobID),
		slog.String("tenant_id", tenantID),
		slog.String("refusal_reason", reason),
	)
	return true, nil
}

// failQuestionJob transitions a question-job to failed + refunds mana + emits
// the failed completion event. Mirrors question_subscriber.fail.
func (s *AiAssistTerminalSubscriber) failQuestionJob(
	ctx context.Context, job *question.ComposeJob, category, message string,
) {
	if err := s.questionJobs.UpdateStatus(ctx, job.TenantID, job.JobID, question.JobStatusFailed, nil, message); err != nil {
		s.log.LogAttrs(ctx, slog.LevelError,
			"question_job_fail_update_failed",
			slog.String("job_id", job.JobID),
			slog.String("err", err.Error()),
		)
	}
	refunded := s.refundQuestionJobMana(ctx, job, message)
	s.emitQuestionCompletion(ctx, job, question.JobStatusFailed, 0, category, message, refunded)
}

// refundQuestionJobMana credits the original debit back idempotently
// ({jobID}-refund). Best-effort: a refund-transport failure is logged but does
// NOT NACK — the job is already failed + the refund is idempotent on retry.
// Returns the units refunded (0 when nothing was charged).
func (s *AiAssistTerminalSubscriber) refundQuestionJobMana(
	ctx context.Context, job *question.ComposeJob, reason string,
) int {
	if s.mana == nil || job.ManaCharged <= 0 {
		return 0
	}
	if _, err := s.mana.Refund(ctx, ports.RefundManaReq{
		GCID:           job.AuthorGCID,
		TenantID:       job.TenantID,
		ActionCode:     job.ManaActionCode,
		Units:          job.ManaCharged,
		IdempotencyKey: job.JobID + "-refund",
		Reason:         reason,
	}); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn,
			"question_job_mana_refund_failed",
			slog.String("job_id", job.JobID),
			slog.String("err", err.Error()),
		)
		return 0
	}
	return job.ManaCharged
}

// emitQuestionCompletion publishes chora.creation.question.generation_completed.v2
// matching the completionEvent shape (question_subscriber.go). Best-effort —
// a publish failure is logged but does NOT NACK (the job state is already
// persisted; the FE polls the job row, not this event).
func (s *AiAssistTerminalSubscriber) emitQuestionCompletion(
	ctx context.Context,
	job *question.ComposeJob,
	status question.JobStatus,
	candidateCount int,
	failureCategory, failureMessage string,
	manaRefunded int,
) {
	if s.jobPub == nil {
		return
	}
	intent, inputKind := composeModel(job)
	if err := s.jobPub.PublishJobEvent(ctx, "chora.creation.question.generation_completed.v2", completionEvent{
		JobID:           job.JobID,
		AtomID:          job.AtomID,
		AuthorGCID:      job.AuthorGCID,
		TenantID:        job.TenantID,
		Status:          string(status),
		CandidateCount:  candidateCount,
		FailureCategory: failureCategory,
		FailureMessage:  failureMessage,
		ManaRefunded:    manaRefunded,
		CompletedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Operation:       question.OperationCompose,
		Intent:          string(intent),
		InputKind:       string(inputKind),
	}); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn,
			"question_job_completion_publish_failed",
			slog.String("job_id", job.JobID),
			slog.String("err", err.Error()),
		)
	}
}

// storedGenerationSummary is the generation_summary_jsonb projection shape: the
// proto GenerationSummary's four fields (the FE's shortfall-banner contract)
// plus the orchestrator's top-level generated_count scalar (field 3) for
// completeness. omitempty keeps the persisted blob tight.
type storedGenerationSummary struct {
	RequestedTotal   int32            `json:"requested_total"`
	GeneratedTotal   int32            `json:"generated_total"`
	GeneratedCount   int32            `json:"generated_count,omitempty"`
	GeneratedPerType map[string]int32 `json:"generated_per_type,omitempty"`
	ShortfallReason  string           `json:"shortfall_reason,omitempty"`
}

// buildGenerationSummaryJSON marshals the decoded GenerationSummary (field 16)
// into the generation_summary_jsonb projection. Returns nil when the
// orchestrator emitted NO summary (single-type / legacy / pre-P2) so the
// COALESCE-preserving UPDATE leaves the column untouched. generated_count
// (field 3) rides along for completeness — it equals generated_total when the
// orchestrator populates both.
func buildGenerationSummaryJSON(payload protomarshal.AiAssistCompletedPayload) []byte {
	gs := payload.GenerationSummary
	if gs == nil {
		return nil
	}
	stored := storedGenerationSummary{
		RequestedTotal:   gs.RequestedTotal,
		GeneratedTotal:   gs.GeneratedTotal,
		GeneratedCount:   payload.GeneratedCount,
		GeneratedPerType: gs.GeneratedPerType,
		ShortfallReason:  gs.ShortfallReason,
	}
	b, err := json.Marshal(stored)
	if err != nil {
		// Marshalling a flat struct of scalars + a string-keyed map cannot fail;
		// guard defensively + skip the projection rather than NACK a successful
		// completion (the summary is a display field, not an invariant).
		return nil
	}
	return b
}

// preferEnvelopeField returns the envelope value when present + non-empty;
// otherwise falls back to the supplied default. The event envelope is the
// producer's authoritative source for routing metadata (idempotency_key /
// tenant_id / gcid) — the payload is the fallback for producers that don't
// project the envelope.
func preferEnvelopeField(envelopeValue, fallback string) string {
	if envelopeValue != "" {
		return envelopeValue
	}
	return fallback
}
