// Package pubsub — Question generation Pub/Sub subscriber (P5-E).
//
// QuestionSubscriber processes chora.creation.question.generation_requested.v1
// messages. The flow (per `agentic-resilience-d6` Pillar 2: ack only after
// the DB write succeeds):
//
//  1. Parse the event payload.
//  2. Load the job; if missing OR already terminal, ack + skip (idempotent).
//  3. Transition to running.
//  4. Dispatch to the appropriate QGen method based on the compose intent.
//  5. On success: persist candidates JSONB → succeeded.
//  6. On failure: refund mana → failed (with error message).
//  7. Emit chora.creation.question.generation_completed.v1.
//
// The subscriber is goroutine-runnable from cmd/server/main.go alongside
// the HTTP listener (decision per the lead plan: subscriber-in-server
// until volume justifies extraction).
//
// Tests inject fake deps; production wires the pgx-backed repos + the
// outbox-backed publisher (compose dispatches to the GKE qgen crew).
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// JobEventPublisher mirrors the httpadapter.JobEventPublisher port to
// keep the pubsub package independent of http.
type JobEventPublisher interface {
	PublishJobEvent(ctx context.Context, topic string, payload any) error
}

// JobEventSyncPublisher is the LOAD-BEARING publish path for the qgen-crew
// ai_draft dispatch (W2 Seam A). Unlike JobEventPublisher.PublishJobEvent —
// whose bridge implementation fires Cloud Pub/Sub in a goroutine and swallows
// errors — PublishJobEventSync blocks until the broker accepts the message
// and RETURNS the publish error. A failed started.v1 publish MUST surface so
// runAIDraft can fail the job + refund mana rather than stranding it in
// running with no orchestrator pickup.
type JobEventSyncPublisher interface {
	PublishJobEventSync(ctx context.Context, topic string, payload any) error
}

// QuestionReader is the minimal read port runModelAnswer needs to seed the
// model_answer_fill crew dispatch with the author's EXISTING question content
// (stem + options/rubric + model answer). The qgen crew fills a model answer
// against this rather than re-drafting the question. Satisfied by the pgx
// ports.QuestionRepository (GetByID); the subscriber asks for only the one
// method it uses (interface segregation — the dead Agent Engine path fetched the
// stem itself, so this dep is new on the model-answer path). Required when
// QGenCrewEnabled=true and a model_answer_fill job is processed.
type QuestionReader interface {
	GetByID(ctx context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error)
}

// QuestionSubscriberDeps bundles the subscriber's outbound deps.
type QuestionSubscriberDeps struct {
	JobRepo   ports.QuestionJobRepository
	Mana      ports.ManaLedger
	Publisher JobEventPublisher

	// W2 Seam A — qgen-crew ai_draft dispatch deps.
	//
	// SyncPublisher is the load-bearing started.v1 publish path (see
	// JobEventSyncPublisher). Required when QGenCrewEnabled=true.
	//
	// QGenCrewEnabled gates compose dispatch: when true, runAIDraft /
	// runModelAnswer publish chora.creation.ai_assist.started.v2 onto the GKE
	// qgen crew (via the orchestrator) + leave the job running for the terminal
	// subscriber to finalise. When false they fail loud — the legacy
	// direct-engine QGen path (Vertex AI Agent Engine) is RETIRED (CHO-1658).
	// Sourced from env QUESTION_JOBS_QGEN_CREW_ENABLED in main.go.
	SyncPublisher   JobEventSyncPublisher
	QGenCrewEnabled bool

	// QuestionReader seeds the model_answer_fill crew dispatch with the author's
	// existing question (stem / options / rubric / model_answer). Required when
	// QGenCrewEnabled=true and a model_answer_fill job is processed; nil otherwise.
	QuestionReader QuestionReader
}

// QuestionSubscriber processes question.generation_requested.v1 messages.
type QuestionSubscriber struct {
	deps QuestionSubscriberDeps
}

// NewQuestionSubscriber constructs the subscriber.
func NewQuestionSubscriber(deps QuestionSubscriberDeps) *QuestionSubscriber {
	return &QuestionSubscriber{deps: deps}
}

// QuestionGenerationRequestedEvent is the canonical wire shape for the
// `chora.creation.question.generation_requested.v1` topic. Mirrors the
// proto QuestionGenerationRequested message from
// chora-contracts/proto/events/creation/question.proto via JSON.
type QuestionGenerationRequestedEvent struct {
	JobID      string `json:"job_id"`
	AtomID     string `json:"atom_id"`
	AuthorGCID string `json:"author_gcid"`
	TenantID   string `json:"tenant_id"`
	// Intent / InputKind — ADR-195 compose model, carried explicitly on the .v2
	// event. runCompose resolves the intent from the persisted job.Intent (the
	// reconstructed loaded row, WS9 step 1), then the event's explicit intent.
	Intent           string `json:"intent,omitempty"`
	InputKind        string `json:"input_kind,omitempty"`
	QuestionType     string `json:"question_type"`
	TargetQuestionID string `json:"target_question_id,omitempty"`
	SourceBlobURI    string `json:"source_blob_uri,omitempty"`
	SourceMimeType   string `json:"source_mime_type,omitempty"`
	ManaActionCode   string `json:"mana_action_code"`
	ManaCharged      int    `json:"mana_charged"`
	SettingsJSON     string `json:"settings_json,omitempty"`
	RequestedAt      string `json:"requested_at"`
	// Traceparent carries the FE-originated W3C trace context so the
	// started.v1 event continues the request's trace tree into the
	// orchestrator's qgen_crew_runner (OTLP-everywhere per
	// ai-observability-cloud-trace). Stamped by question_jobs_handler.
	Traceparent string `json:"traceparent,omitempty"`
}

// completionEvent is emitted to the generation_completed.v2 topic. It carries the
// ADR-195 compose model operation/intent/input_kind (read by the v2 encoder; the
// retired job_type discriminant is gone — the v2 message reserves field 5).
type completionEvent struct {
	JobID           string `json:"job_id"`
	AtomID          string `json:"atom_id"`
	AuthorGCID      string `json:"author_gcid"`
	TenantID        string `json:"tenant_id"`
	Status          string `json:"status"`
	CandidateCount  int    `json:"candidate_count"`
	FailureCategory string `json:"failure_category,omitempty"`
	FailureMessage  string `json:"failure_message,omitempty"`
	ManaRefunded    int    `json:"mana_refunded,omitempty"`
	CompletedAt     string `json:"completed_at"`
	// ADR-195 WS7 (D7) compose model — carried for the .v2 event. operation is
	// always "compose"; intent/input_kind come from the job's compose VOs.
	Operation string `json:"operation,omitempty"`
	Intent    string `json:"intent,omitempty"`
	InputKind string `json:"input_kind,omitempty"`
}

// candidateDraft is the JSONB shape persisted in
// question_generation_jobs.candidate_questions_jsonb. The HTTP /accept
// handler decodes the same shape to merge with user overrides.
type candidateDraft struct {
	DraftID    string               `json:"draft_id"`
	Type       string               `json:"type"`
	Prompt     string               `json:"prompt"`
	MCQPayload *question.MCQPayload `json:"mcq_payload,omitempty"`
	OEPayload  *question.OEPayload  `json:"oe_payload,omitempty"`
	QGenScore  float64              `json:"qgen_score,omitempty"`
	ModelUsed  string               `json:"model_used,omitempty"`
	// ImageURL / AnswerImageURL — CHO-1825: the CANONICAL draft-candidate image
	// location (top-level), matching the orchestrator emit, the single-candidate
	// AiAssistCandidate, and the FE's top-level read. The render_image_set node
	// sets these; the review-image-regenerate patch (ai_assist_terminal_
	// subscriber) rewrites them in place; accept maps them onto the published
	// question's nested mcq_payload/oe_payload image fields. Absent ⇒ no image.
	ImageURL       *string `json:"image_url,omitempty"`
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
	// ImageGcsURI / AnswerImageGcsURI — ADR-210 B2/B3: the canonical durable
	// gs:// OBJECT path per placement, stamped by the orchestrator alongside the
	// signed image_url and preserved verbatim into candidate_questions_jsonb so
	// the regenerate producer sources the image-to-image original directly (never
	// parsing the signed URL). Absent ⇒ pre-B2 producer (regen → text-to-image).
	ImageGcsURI       *string `json:"image_gcs_uri,omitempty"`
	AnswerImageGcsURI *string `json:"answer_image_gcs_uri,omitempty"`
	// Citations — Lane 1c (D9/D15): verification-stamped source citations
	// (OpenAPI QuestionCitation). Absent for ungrounded jobs.
	Citations []questionCitation `json:"citations,omitempty"`
	// ImageSpecs — CHO-1819 P2/P3: the per-candidate image generation specs the
	// crew emitted (slot/prompt/status/url). Carried VERBATIM as json.RawMessage
	// so the review-stage regenerate path (P3) can read them back. The normalizer
	// must not strip them; absent ⇒ omitted (omitempty).
	ImageSpecs json.RawMessage `json:"image_specs,omitempty"`
}

// Handle processes a single message. Returns nil when the message MAY be
// ack'd (success or terminal-skip). Returns a non-nil error for transient
// failures (e.g. job repo not reachable) so the Pub/Sub receiver loop
// NACKs and re-delivers.
func (s *QuestionSubscriber) Handle(ctx context.Context, evt QuestionGenerationRequestedEvent) error {
	if evt.JobID == "" || evt.AtomID == "" || evt.AuthorGCID == "" || evt.TenantID == "" {
		// Malformed envelope — ack to avoid poisoning the queue.
		log.Printf("question_subscriber: malformed event missing required fields, ack-skipping: %+v", evt)
		return nil
	}

	job, err := s.deps.JobRepo.Get(ctx, evt.TenantID, evt.JobID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			log.Printf("question_subscriber: job %s not found, ack-skipping", evt.JobID)
			return nil
		}
		return fmt.Errorf("question_subscriber: job lookup: %w", err)
	}
	// Idempotency: if already terminal, no-op.
	if job.Status.IsTerminal() || job.Status == question.JobStatusSucceeded {
		log.Printf("question_subscriber: job %s already in status %q, ack-skipping (idempotent)", evt.JobID, job.Status)
		return nil
	}

	// Transition to running.
	if err := s.deps.JobRepo.UpdateStatus(ctx, evt.TenantID, evt.JobID, question.JobStatusRunning, nil, ""); err != nil {
		return fmt.Errorf("question_subscriber: transition to running: %w", err)
	}

	// ADR-195 WS4 — ONE dispatch keyed on the compose intent (the universal
	// discriminant) instead of the legacy 5-way job_type switch.
	qType := question.QuestionType(evt.QuestionType)
	s.runCompose(ctx, job, evt, qType)
	return nil
}

// runCompose is the single ADR-195 dispatch. It branches on the compose intent
// — the universal discriminant — rather than the retired 5-way job_type switch,
// folding the legacy synchronous model-answer path under
// intent=model_answer_fill. The intent comes from composeIntent — the persisted
// job.Intent (WS3 + migration 0022) first, then an explicit .v2 event intent. The
// routing below never reads job_type (the enum was retired in WS9 step 3).
func (s *QuestionSubscriber) runCompose(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, qType question.QuestionType) {
	switch composeIntent(job, evt) {
	case question.IntentModelAnswerFill:
		s.runModelAnswer(ctx, job, evt, qType)
	case question.IntentImageRegen:
		s.runImageRegen(ctx, job, evt, qType)
	case question.IntentNewQuestion:
		// new_question is input-conditioned: source material → RAG (batch); a
		// pure prompt → single-Q LLM (ai_draft). By-hand authoring carries inline
		// candidates and never publishes a generation event (manual jobs are
		// created 'succeeded'), so it is not reached here.
		if hasSourceMaterial(evt) {
			s.runBatchSourceMaterial(ctx, job, evt, qType)
		} else {
			s.runAIDraft(ctx, job, evt, qType)
		}
	default:
		s.fail(ctx, job, "unknown_intent", "unrecognised compose intent: "+evt.Intent)
	}
}

// composeIntent resolves the compose discriminant for runCompose, in precedence:
//  1. the persisted job.Intent (WS3 + migration 0022 backfill, reconstructed on the
//     repo read in WS9 step 1) — the canonical source, present on the loaded row;
//  2. an explicit intent on the event (the ADR-195 WS7 .v2 contract).
//
// An invalid intent at the higher tier is skipped so a malformed value never
// shadows a valid lower tier; an empty result fails loud in runCompose (the
// job_type shim that previously backed this was retired in WS9 step 3).
func composeIntent(job *question.ComposeJob, evt QuestionGenerationRequestedEvent) question.Intent {
	if job != nil && job.Intent.Valid() {
		return job.Intent
	}
	return question.Intent(evt.Intent)
}

// composeModel resolves a job's {intent, input_kind} for the ADR-195 .v2 compose
// events. Thin adapter wrapper over the domain derivation off the explicit compose
// VOs (always present: create populates them, the repo read reconstructs them).
func composeModel(job *question.ComposeJob) (question.Intent, question.InputKind) {
	return question.ComposeModelForJob(job)
}

// stampComposeV2 adds the compose model {operation, intent, input_kind} to an
// ai_assist.started payload so the .v2 parallel-publish carries it. The v1
// AiAssistStarted encoder ignores these keys (v1 wire byte-identical); the .v2
// encoder reads them + drops job_kind.
func stampComposeV2(payload map[string]any, job *question.ComposeJob) {
	intent, inputKind := composeModel(job)
	payload["operation"] = question.OperationCompose
	payload["intent"] = string(intent)
	payload["input_kind"] = string(inputKind)
}

// hasSourceMaterial reports whether a new_question compose carries source files
// (→ RAG) vs a pure prompt (→ single-Q LLM), keyed on the event's source_blob_uri
// (the .v2 contract; the legacy batch job_type fallback was retired in WS9 step 3).
func hasSourceMaterial(evt QuestionGenerationRequestedEvent) bool {
	return evt.SourceBlobURI != ""
}

// topicAiAssistStarted is the GKE qgen crew kickoff topic. The orchestrator's
// qgen-crew subscriber consumes it keyed on assist_id (= question_generation_
// jobs.job_id) + publishes the terminal completed/refused event the ai_assist
// terminal subscriber maps back. ADR-195 WS7 step 5 — .v2 (operation/intent/
// input_kind; job_kind dropped); the v2 publish is LOAD-BEARING (the orchestrator
// consumes only v2), so a publish failure fails the job + refunds. v1 retired.
const topicAiAssistStarted = "chora.creation.ai_assist.started.v2"

// runAIDraft handles the ai_draft path.
//
// When QGenCrewEnabled (W2 Seam A): publish ai_assist.started.v2 onto the GKE
// qgen 2-agent crew + leave the job RUNNING (the terminal subscriber finalises
// it). The started-publish is LOAD-BEARING — a publish failure fails the job +
// refunds mana so it never strands in running with no orchestrator pickup.
//
// When the flag is off: fail loud — the legacy direct-engine QGen path (Vertex
// AI Agent Engine) is RETIRED (CHO-1658).
func (s *QuestionSubscriber) runAIDraft(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, qType question.QuestionType) {
	if !s.deps.QGenCrewEnabled {
		// CHO-1658 — the legacy direct-engine QGen path (Vertex AI Agent Engine,
		// decommissioned) is RETIRED. The GKE crew is the only dispatch now; the
		// gate must be on (QUESTION_JOBS_QGEN_CREW_ENABLED=true). Fail loud rather
		// than silently dropping the job or calling a dead engine.
		s.fail(ctx, job, "qgen_crew_disabled", "ai_draft requires QGenCrewEnabled (legacy Agent Engine path retired)")
		return
	}
	settings := decodeSettings(evt.SettingsJSON)
	prompt, _ := settings["prompt"].(string)
	difficulty := 0
	if v, ok := settings["difficulty"].(float64); ok {
		difficulty = int(v)
	}
	// CHO-1826 Gap #4 — author forced-image opt-in (set by the canvas composer).
	// Forwarded to the single qgen runner so its render_image node fires and the
	// trace widget surfaces the Illustration card.
	imageForStem, _ := settings["image_for_stem"].(bool)
	imageForAnswer, _ := settings["image_for_answer"].(bool)
	// ADR-195 WS8 BE-1 — count drives the plan: count>1 routes the topic job
	// through the set lane (job_kind=batch + type_plan) so the orchestrator
	// generates N candidates (fixes "count=5 → 1"). count<=1 stays single-Q.
	count := 1
	if v, ok := settings["count"].(float64); ok && int(v) > 1 {
		count = int(v)
	}

	s.dispatchAIDraftToCrew(ctx, job, evt, qType, prompt, difficulty, count, imageForStem, imageForAnswer, metadataHints(settings))
}

// dispatchAIDraftToCrew publishes the ai_assist.started.v1 kickoff for the
// GKE qgen 2-agent crew. The job stays RUNNING (already transitioned at
// Handle) — the terminal subscriber maps the orchestrator's completed/refused
// event back. Fails loud (job→failed + mana refund) on a missing prompt OR a
// publish error.
func (s *QuestionSubscriber) dispatchAIDraftToCrew(
	ctx context.Context,
	job *question.ComposeJob,
	evt QuestionGenerationRequestedEvent,
	qType question.QuestionType,
	prompt string,
	difficulty int,
	count int,
	imageForStem bool,
	imageForAnswer bool,
	metadata map[string]string,
) {
	if s.deps.SyncPublisher == nil {
		// Misconfiguration — fail loud rather than silently dropping the job.
		s.fail(ctx, job, "qgen_crew_misconfigured", "QGenCrewEnabled but no SyncPublisher wired")
		return
	}
	if strings.TrimSpace(prompt) == "" {
		s.fail(ctx, job, "qgen_bad_request", "ai_draft requires a non-empty prompt in settings_json")
		return
	}

	// Mirror ai_assist_async_handler.go:178-189. content_type carries the
	// question_type (the orchestrator reads question_type out of proto field
	// 6 content_type — there is no dedicated question_type proto field);
	// question_type is mirrored for the new field. max_retries=0 lets the
	// orchestrator apply its default (3).
	startedPayload := map[string]any{
		"assist_id":     job.JobID,
		"tenant_id":     job.TenantID,
		"author_gcid":   job.AuthorGCID,
		"atom_id":       job.AtomID,
		"content_type":  string(qType),
		"question_type": string(qType),
		"prompt":        prompt,
		"difficulty":    difficulty,
		"max_retries":   0,
		"started_at":    time.Now().UTC().Format(time.RFC3339),
	}
	// CHO-1657 — lift the author's Subject / Cognitive Level / Difficulty hints onto
	// the started payload's metadata map (proto field 12). Restores the wire-through
	// the unified canvas (CHO-1826) dropped; omitted when empty (byte-stable legacy).
	if len(metadata) > 0 {
		startedPayload["metadata"] = metadata
	}
	if evt.Traceparent != "" {
		startedPayload["traceparent"] = evt.Traceparent
	}
	// ADR-195 WS8 BE-1 — count>1 routes the topic job through the set lane
	// (mirrors runBatchSourceMaterial's payload): the orchestrator reads N from
	// requested_count + the per-type type_plan and runs the set-native single-pass
	// generation. A single-entry plan carries the author's per-type image opt-in
	// (same wire shape as settingsTypePlan). count<=1 stays single-Q with the
	// forced-image opt-in top-level (proto f13/14).
	if count > 1 {
		startedPayload["content_type"] = contentTypeMixed
		startedPayload["job_kind"] = "batch"
		startedPayload["requested_count"] = count
		startedPayload["type_plan"] = []map[string]any{{
			"question_type":    string(qType),
			"count":            count,
			"max_images":       0,
			"image_for_stem":   imageForStem,
			"image_for_answer": imageForAnswer,
		}}
	} else {
		// CHO-1826 Gap #4 — emit the forced-image opt-in ONLY when set (proto3-false
		// elision = byte-stable legacy events). protomarshal serialises these as
		// AiAssistStarted fields 13/14; the single qgen runner reads them into
		// QGenCrewState so the generation agent emits image_specs → render_image.
		if imageForStem {
			startedPayload["image_for_stem"] = true
		}
		if imageForAnswer {
			startedPayload["image_for_answer"] = true
		}
	}

	stampComposeV2(startedPayload, job)

	if err := s.deps.SyncPublisher.PublishJobEventSync(ctx, topicAiAssistStarted, startedPayload); err != nil {
		// Load-bearing: a swallowed publish error would strand the job in
		// running with no orchestrator pickup. Fail + refund.
		s.fail(ctx, job, "qgen_crew_dispatch_failed", "publish ai_assist.started.v1: "+err.Error())
		return
	}

	log.Printf("question_subscriber: ai_draft job %s dispatched to qgen crew (assist_id=%s, type=%s); job left running",
		job.JobID, job.JobID, qType)
	// Intentionally leave the job in running — the terminal subscriber
	// transitions it to succeeded/failed when the crew finishes.
}

// runModelAnswer handles the model_answer_fill ("generate model answer with AI")
// path.
//
// When QGenCrewEnabled (CHO-1658): publish ai_assist.started.v2 onto the GKE qgen
// crew (the ADR-169 move already done for ai_draft), seeded with the author's
// EXISTING question content, and leave the job RUNNING for the terminal
// subscriber to finalise. When the flag is off: fail loud — the legacy
// direct-engine QGen path (us-central1 Agent Engine, decommissioned) is RETIRED.
func (s *QuestionSubscriber) runModelAnswer(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, qType question.QuestionType) {
	if !s.deps.QGenCrewEnabled {
		// CHO-1658 — legacy direct-engine QGen path retired (see runAIDraft).
		s.fail(ctx, job, "qgen_crew_disabled", "model_answer_fill requires QGenCrewEnabled (legacy Agent Engine path retired)")
		return
	}
	settings := decodeSettings(evt.SettingsJSON)
	toneHint, _ := settings["tone_hint"].(string)
	s.dispatchModelAnswerToCrew(ctx, job, evt, toneHint)
}

// existingQuestionWire is the author's CURRENT question content JSON-marshalled
// onto ai_assist.started.v2.existing_question_json (proto field 26). The
// orchestrator json-decodes it into input_obj["existing_question"], which the
// reasoning_engine_executor reads into the qgen agent's author_stem /
// author_options / author_rubric / model_answer session keys. Field set + the
// nested shapes match the qgen agent's documented contract (composer_question.go
// ExistingQuestion: mcq_options=[{option_id,label,is_correct}],
// oe_rubric=[{criterion,weight}]).
type existingQuestionWire struct {
	Stem        string                   `json:"stem"`
	MCQOptions  []existingMCQOptionWire  `json:"mcq_options,omitempty"`
	OERubric    []existingRubricCritWire `json:"oe_rubric,omitempty"`
	ModelAnswer string                   `json:"model_answer,omitempty"`
}

type existingMCQOptionWire struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
}

type existingRubricCritWire struct {
	Criterion string  `json:"criterion"`
	Weight    float64 `json:"weight"`
}

// buildExistingQuestion projects the persisted Question aggregate into the
// existing_question wire shape the qgen crew fills against. The domain stores OE
// rubric weights as integer percents (0..100); the agent reads a float weight, so
// WeightPercent is normalised to a 0..1 fraction.
func buildExistingQuestion(q *question.Question) existingQuestionWire {
	out := existingQuestionWire{Stem: q.Prompt}
	switch q.Type {
	case question.TypeMCQ:
		if q.MCQ != nil {
			for _, o := range q.MCQ.Options {
				out.MCQOptions = append(out.MCQOptions, existingMCQOptionWire{
					OptionID: o.OptionID, Label: o.Label, IsCorrect: o.IsCorrect,
				})
			}
		}
	case question.TypeOpenEnded:
		if q.OE != nil {
			// Thread the author's current model answer so the agent REFINES rather
			// than re-drafts (reasoning_engine_executor surfaces it as the
			// model_answer session key only when non-empty).
			out.ModelAnswer = q.OE.ModelAnswer
			if q.OE.WeightedRubric != nil {
				for _, c := range q.OE.WeightedRubric.Criteria {
					out.OERubric = append(out.OERubric, existingRubricCritWire{
						Criterion: c.Description,
						Weight:    float64(c.WeightPercent) / 100.0,
					})
				}
			}
		}
	}
	return out
}

// dispatchModelAnswerToCrew publishes the ai_assist.started.v2 kickoff for the
// GKE qgen crew on the model_answer_fill path. The job stays RUNNING (already
// transitioned at Handle) — the terminal subscriber maps the orchestrator's
// completed/refused event back (resolveQuestionJob claims model_answer_fill).
// Fails loud (job→failed + mana refund) on a misconfig, a missing target
// question, an encode failure, OR a publish error.
func (s *QuestionSubscriber) dispatchModelAnswerToCrew(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, toneHint string) {
	if s.deps.SyncPublisher == nil {
		s.fail(ctx, job, "model_answer_misconfigured", "QGenCrewEnabled but no SyncPublisher wired for model_answer dispatch")
		return
	}
	if s.deps.QuestionReader == nil {
		s.fail(ctx, job, "model_answer_misconfigured", "QGenCrewEnabled but no QuestionReader wired for model_answer dispatch")
		return
	}
	qid := strings.TrimSpace(evt.TargetQuestionID)
	if qid == "" {
		s.fail(ctx, job, "model_answer_bad_request", "model_answer_fill requires target_question_id")
		return
	}

	// Fetch the author's existing question to seed the fill. The dead Agent Engine
	// path let the engine resolve the stem itself; the crew gets the content ONLY
	// through this event, so a missing question is a hard fail (no fabricated
	// success).
	q, _, err := s.deps.QuestionReader.GetByID(ctx, job.TenantID, qid)
	if err != nil {
		s.fail(ctx, job, "model_answer_question_not_found", "load target question "+qid+": "+err.Error())
		return
	}

	existingJSON, err := json.Marshal(buildExistingQuestion(q))
	if err != nil {
		s.fail(ctx, job, "model_answer_encode_failed", "marshal existing_question: "+err.Error())
		return
	}

	// content_type carries the question_type (the orchestrator reads question_type
	// out of proto field 6 content_type). prompt = the author's stem so the crew's
	// generate node has a non-empty prompt; max_retries=0 lets the orchestrator
	// apply its default.
	qt := string(q.Type)
	startedPayload := map[string]any{
		"assist_id":              job.JobID,
		"tenant_id":              job.TenantID,
		"author_gcid":            job.AuthorGCID,
		"atom_id":                job.AtomID,
		"content_type":           qt,
		"question_type":          qt,
		"prompt":                 q.Prompt,
		"existing_question_json": string(existingJSON),
		"max_retries":            0,
		"started_at":             time.Now().UTC().Format(time.RFC3339),
	}
	if toneHint != "" {
		// Carry the optional tone hint on the free-form metadata map (proto field
		// 12). The qgen agent does not yet specialise on it; forwarded so it is not
		// lost + available for observability.
		startedPayload["metadata"] = map[string]string{"tone_hint": toneHint}
	}
	if evt.Traceparent != "" {
		startedPayload["traceparent"] = evt.Traceparent
	}

	// stampComposeV2 reads job.Intent (== model_answer_fill) → operation/intent/
	// input_kind on the .v2 payload; the orchestrator routes the single crew run
	// on intent and the executor surfaces the author_* fill keys.
	stampComposeV2(startedPayload, job)

	if err := s.deps.SyncPublisher.PublishJobEventSync(ctx, topicAiAssistStarted, startedPayload); err != nil {
		// Load-bearing: a swallowed publish error would strand the job in running
		// with no orchestrator pickup. Fail + refund.
		s.fail(ctx, job, "model_answer_dispatch_failed", "publish ai_assist.started.v2 (model_answer): "+err.Error())
		return
	}

	log.Printf("question_subscriber: model_answer_fill job %s dispatched to qgen crew (assist_id=%s, type=%s, qid=%s); job left running",
		job.JobID, job.JobID, qt, qid)
	// Intentionally leave the job running — the terminal subscriber transitions it
	// to succeeded/failed when the crew finishes.
}

// Canonical batch grounding modes (EPIC-1a). Mirrors chora-contracts
// openapi/creation-questions.yaml QuestionJobSettings.grounding_mode +
// proto AiAssistStarted.grounding_mode.
const (
	groundingModeStrict        = "strict"
	groundingModeStartingPoint = "starting_point"
)

// contentTypeMixed is the AiAssistStarted.content_type discriminator for a
// mixed-type batch (CHO-1819 P2). When the started event carries a non-empty
// type_plan (field 21) the orchestrator reads the per-type quotas instead of
// collapsing content_type to one question_type.
const contentTypeMixed = "mixed"

// runBatchSourceMaterial handles the batch_source_material path (EPIC-1a).
//
// Batch reuses the qgen 2-agent crew pipe: it publishes ONE
// chora.creation.ai_assist.started.v1 with job_kind=batch + the uploaded
// grounding material (source_blob_uri) + requested_count (1..50). The
// orchestrator's QGenBatchRunner loops the qgen graph N times and publishes
// ONE completed.v1 whose candidate_payload_json is a JSON ARRAY; the
// ai_assist terminal subscriber parses the array → N candidate drafts. The job
// stays RUNNING here (already transitioned in Handle) — the terminal subscriber
// finalises it.
//
// The legacy synchronous BlobStore-download + DocumentExtractor (chora-doc-
// parser) per-chunk path is REMOVED per ADR-169 / B1: grounding rides the model
// gateway as Gemini multimodal inlineData inside the crew, so chora-creation
// neither downloads the blob nor issues an LLM call here.
//
// Gated on QGenCrewEnabled (same flag as ai_draft). Flag off OR a misconfigured
// / failed dispatch fails the job loud + refunds the parse-step mana — never a
// silent success.
func (s *QuestionSubscriber) runBatchSourceMaterial(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, qType question.QuestionType) {
	if !s.deps.QGenCrewEnabled {
		s.fail(ctx, job, "batch_not_wired", "batch requires the qgen crew (set QUESTION_JOBS_QGEN_CREW_ENABLED=true); the legacy doc-parser path is retired per ADR-169")
		return
	}
	if s.deps.SyncPublisher == nil {
		s.fail(ctx, job, "qgen_crew_misconfigured", "QGenCrewEnabled but no SyncPublisher wired for batch dispatch")
		return
	}
	if strings.TrimSpace(evt.SourceBlobURI) == "" {
		s.fail(ctx, job, "batch_bad_request", "missing source_blob_uri on batch event")
		return
	}

	settings := decodeSettings(evt.SettingsJSON)
	count := clampBatchCount(settingsInt(settings, "count"))
	groundingMode := normalizeGroundingMode(settingsString(settings, "grounding_mode"))
	difficulty := settingsInt(settings, "difficulty")
	targetGrowthEdges := settingsStringSlice(settings, "target_growth_edges")
	// CHO-1819 P2 — lift the validated mixed-type breakdown out of settings
	// (written by the HTTP handler after NewTypePlan). Non-empty ⇒ content_type
	// flips to "mixed" + the quotas ride field 21; requested_count stays the sum
	// (the handler already set count == sum(quota.count)).
	typePlan := settingsTypePlan(settings)
	contentType := string(qType)
	if len(typePlan) > 0 {
		contentType = contentTypeMixed
	}
	// The author's free-text context/instructions seed the generator; the
	// uploaded material carries the substance. A non-empty prompt keeps the
	// orchestrator's validate guard happy + gives the generator an instruction.
	prompt := strings.TrimSpace(settingsString(settings, "context"))
	if prompt == "" {
		if len(typePlan) > 0 {
			prompt = defaultMixedBatchPrompt(groundingMode)
		} else {
			prompt = defaultBatchPrompt(qType, groundingMode)
		}
	}

	startedPayload := map[string]any{
		"assist_id":           job.JobID,
		"tenant_id":           job.TenantID,
		"author_gcid":         job.AuthorGCID,
		"atom_id":             job.AtomID,
		"content_type":        contentType,
		"question_type":       string(qType),
		"prompt":              prompt,
		"job_kind":            "batch",
		"requested_count":     count,
		"grounding_mode":      groundingMode,
		"source_blob_uri":     evt.SourceBlobURI,
		"source_mime_type":    evt.SourceMimeType,
		"target_growth_edges": targetGrowthEdges,
		"difficulty":          difficulty,
		"max_retries":         0,
		"started_at":          time.Now().UTC().Format(time.RFC3339),
	}
	// CHO-1657 — lift the author's Subject / Cognitive Level / Difficulty hints onto
	// the started payload's metadata map (proto field 12), same as the ai_draft path.
	if md := metadataHints(settings); len(md) > 0 {
		startedPayload["metadata"] = md
	}
	// Field 21: emit the validated per-type quotas ONLY when present (proto3
	// repeated default-elision keeps legacy single-type events byte-stable).
	if len(typePlan) > 0 {
		startedPayload["type_plan"] = typePlan
	}
	// Lane 1c (D7) — lift the role-tagged source_files out of settings onto
	// the started payload (proto f20; encoder serialises `source_files`).
	// Wire shape is exactly {blob_uri, mime_type, role} — display filenames
	// stay in settings only. Absent/empty emits nothing (byte-stable for
	// pre-1c single-file jobs); f17/18 above keep mirroring source_files[0].
	if files := settingsSourceFiles(settings); len(files) > 0 {
		startedPayload["source_files"] = files
	}
	if evt.Traceparent != "" {
		startedPayload["traceparent"] = evt.Traceparent
	}

	stampComposeV2(startedPayload, job)

	if err := s.deps.SyncPublisher.PublishJobEventSync(ctx, topicAiAssistStarted, startedPayload); err != nil {
		s.fail(ctx, job, "qgen_crew_dispatch_failed", "publish ai_assist.started.v1 (batch): "+err.Error())
		return
	}

	log.Printf("question_subscriber: batch job %s dispatched to qgen crew (assist_id=%s, count=%d, grounding=%s, edges=%d); job left running",
		job.JobID, job.JobID, count, groundingMode, len(targetGrowthEdges))
	// Intentionally leave the job running — the ai_assist terminal subscriber
	// parses the N-candidate array + transitions it to succeeded/failed.
}

// jobKindImageRegen is the AiAssistStarted.job_kind discriminator for a P3
// single-candidate image regenerate (CHO-1819 P3). The orchestrator routes on it
// to an ImageRegenRunner that re-renders ONE image from regen.prompt for the
// targeted draft + placement instead of running the full qgen graph.
const jobKindImageRegen = "image_regen"

// runImageRegen handles the image_regen path (CHO-1819 P3 review image
// regenerate). It lifts the regen spec (draft_id / placement / prompt / mode) out
// of settings (written by the HTTP handler), publishes ONE
// chora.creation.ai_assist.started.v1 with job_kind=image_regen + the regen
// nested message (proto f22), and leaves the job RUNNING — the ai_assist terminal
// subscriber patches the parent job's candidate image when the orchestrator's
// render completes. assist_id is THIS image_regen job's id; the parent job + draft
// to patch live in the job's own settings (parent_job_id + draft_id).
//
// Fails loud (job→failed + mana refund) on a missing SyncPublisher, a malformed
// regen spec, or a publish error — never a silent no-op render.
func (s *QuestionSubscriber) runImageRegen(ctx context.Context, job *question.ComposeJob, evt QuestionGenerationRequestedEvent, qType question.QuestionType) {
	if s.deps.SyncPublisher == nil {
		s.fail(ctx, job, "image_regen_misconfigured", "QGenCrewEnabled but no SyncPublisher wired for image_regen dispatch")
		return
	}
	settings := decodeSettings(evt.SettingsJSON)
	draftID := strings.TrimSpace(settingsString(settings, "draft_id"))
	placement := strings.TrimSpace(settingsString(settings, "placement"))
	prompt := strings.TrimSpace(settingsString(settings, "prompt"))
	mode := strings.TrimSpace(settingsString(settings, "mode"))
	if draftID == "" {
		s.fail(ctx, job, "image_regen_bad_request", "image_regen requires draft_id in settings_json")
		return
	}
	if placement != "stem" && placement != "answer" {
		s.fail(ctx, job, "image_regen_bad_request", "image_regen placement must be 'stem' or 'answer'")
		return
	}
	if prompt == "" {
		s.fail(ctx, job, "image_regen_bad_request", "image_regen requires a non-empty prompt")
		return
	}

	regen := map[string]any{
		"draft_id":  draftID,
		"placement": placement,
		"prompt":    prompt,
	}
	if mode != "" {
		regen["mode"] = mode
	}
	// Bug 1 — lift the CURRENT question context (stem + model answer) + the
	// original authoring grounding out of settings_json so protomarshal encodes
	// ImageRegenSpec f5/f6/f7 and the orchestrator regenerates an image of the
	// ACTUAL question (not the empty-context "red apple"). Omit empties to keep
	// the spec byte-identical to a pre-fix producer (proto3 default-elision).
	if v := strings.TrimSpace(settingsString(settings, "current_stem")); v != "" {
		regen["current_stem"] = v
	}
	if v := strings.TrimSpace(settingsString(settings, "current_model_answer")); v != "" {
		regen["current_model_answer"] = v
	}
	if v := strings.TrimSpace(settingsString(settings, "original_source")); v != "" {
		regen["original_source"] = v
	}
	// ADR-210 B3 — lift the persisted durable gs:// object path so protomarshal
	// encodes ImageRegenSpec f8; the orchestrator fetches those bytes for true
	// image-to-image editing (fail-loud if set but unfetchable). Empty omitted
	// (proto3 default-elision) so a non-image-to-image regen stays byte-stable.
	if v := strings.TrimSpace(settingsString(settings, "original_image_gcs_uri")); v != "" {
		regen["original_image_gcs_uri"] = v
	}

	startedPayload := map[string]any{
		"assist_id":     job.JobID,
		"tenant_id":     job.TenantID,
		"author_gcid":   job.AuthorGCID,
		"atom_id":       job.AtomID,
		"content_type":  string(qType),
		"question_type": string(qType),
		"prompt":        prompt,
		"job_kind":      jobKindImageRegen,
		"regen":         regen,
		"max_retries":   0,
		"started_at":    time.Now().UTC().Format(time.RFC3339),
	}
	if evt.Traceparent != "" {
		startedPayload["traceparent"] = evt.Traceparent
	}

	stampComposeV2(startedPayload, job)

	if err := s.deps.SyncPublisher.PublishJobEventSync(ctx, topicAiAssistStarted, startedPayload); err != nil {
		s.fail(ctx, job, "image_regen_dispatch_failed", "publish ai_assist.started.v1 (image_regen): "+err.Error())
		return
	}

	log.Printf("question_subscriber: image_regen job %s dispatched (assist_id=%s, draft=%s, placement=%s); job left running",
		job.JobID, job.JobID, draftID, placement)
	// Intentionally leave the job running — the ai_assist terminal subscriber
	// patches the parent candidate's image + finalises this job.
}

// clampBatchCount bounds the requested candidate count to [1, MaxBatchCount].
// The ceiling has ONE home (aiassist.MaxBatchCount) shared with the TypePlan VO
// + the HTTP batch handler.
func clampBatchCount(n int) int {
	if n < 1 {
		return 1
	}
	if n > aiassist.MaxBatchCount {
		return aiassist.MaxBatchCount
	}
	return n
}

// normalizeGroundingMode defaults empty / unknown values to "starting_point"
// (the contract default). Only "strict" is honoured as the closed-book mode.
func normalizeGroundingMode(v string) string {
	if strings.TrimSpace(v) == groundingModeStrict {
		return groundingModeStrict
	}
	return groundingModeStartingPoint
}

// defaultBatchPrompt is the generator instruction used when the author supplied
// no free-text context. Grounding mode shapes the closed/open framing.
func defaultBatchPrompt(qType question.QuestionType, groundingMode string) string {
	kind := "multiple-choice questions"
	if qType == question.TypeOpenEnded {
		kind = "open-ended questions"
	}
	if groundingMode == groundingModeStrict {
		return "Generate " + kind + " answerable strictly from the provided source material."
	}
	return "Generate " + kind + " using the provided source material as a starting point."
}

// defaultMixedBatchPrompt is the generator instruction for a mixed-type batch
// (type_plan present) when the author supplied no free-text context. The per-
// type counts ride field 21; this prompt only frames the closed/open grounding.
func defaultMixedBatchPrompt(groundingMode string) string {
	if groundingMode == groundingModeStrict {
		return "Generate a mixed set of questions per the requested type plan, answerable strictly from the provided source material."
	}
	return "Generate a mixed set of questions per the requested type plan, using the provided source material as a starting point."
}

func settingsString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func settingsInt(m map[string]any, key string) int {
	// decodeSettings unmarshals via encoding/json, so numbers arrive as float64.
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func settingsStringSlice(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// settingsSourceFiles decodes settings.source_files (written by the Lane 1c
// multipart handler) into the proto-f20 wire shape: ordered entries of
// {blob_uri, mime_type, role} only. Entries without a blob_uri are dropped
// (nothing for the crew to download). Returns nil for pre-1c settings.
func settingsSourceFiles(m map[string]any) []map[string]any {
	raw, ok := m["source_files"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		blobURI, _ := entry["blob_uri"].(string)
		if strings.TrimSpace(blobURI) == "" {
			continue
		}
		mime, _ := entry["mime_type"].(string)
		role, _ := entry["role"].(string)
		if role == "" {
			role = "source"
		}
		out = append(out, map[string]any{
			"blob_uri":  blobURI,
			"mime_type": mime,
			"role":      role,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// settingsTypePlan decodes settings.type_plan (written by the HTTP batch handler
// after NewTypePlan validation) into the proto-f21 wire shape: ordered entries of
// {question_type, count, max_images}. Counts arrive as JSON float64 and are
// narrowed to int so the protomarshal encoder + logs see whole numbers. Entries
// without a question_type are dropped (the handler already rejected those — this
// is a defensive re-lift, mirroring settingsSourceFiles). Returns nil for a
// legacy single-type batch (no type_plan key).
func settingsTypePlan(m map[string]any) []map[string]any {
	raw, ok := m["type_plan"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		qt, _ := entry["question_type"].(string)
		if strings.TrimSpace(qt) == "" {
			continue
		}
		// CHO-1825 2b: per-type deterministic author image opt-in. JSON-decoded
		// bools arrive as bool; absent ⇒ false (legacy plans byte-unchanged).
		ifs, _ := entry["image_for_stem"].(bool)
		ifa, _ := entry["image_for_answer"].(bool)
		out = append(out, map[string]any{
			"question_type":    qt,
			"count":            settingsEntryInt(entry, "count"),
			"max_images":       settingsEntryInt(entry, "max_images"),
			"image_for_stem":   ifs,
			"image_for_answer": ifa,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// settingsEntryInt reads an int from a settings sub-map, tolerating the JSON
// float64 shape (decodeSettings unmarshals numbers as float64) as well as a
// native int. Absent / wrong-typed → 0.
func settingsEntryInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

// succeed persists candidates JSONB and transitions to succeeded.
func (s *QuestionSubscriber) succeed(ctx context.Context, job *question.ComposeJob, drafts []candidateDraft) {
	candJSON, err := json.Marshal(drafts)
	if err != nil {
		s.fail(ctx, job, "internal_error", "marshal candidates: "+err.Error())
		return
	}
	if err := s.deps.JobRepo.UpdateStatus(ctx, job.TenantID, job.JobID, question.JobStatusSucceeded, candJSON, ""); err != nil {
		log.Printf("question_subscriber: succeed UpdateStatus: %v", err)
		// The job is still in running state — surface a fail event so the
		// orchestrator can investigate.
		s.fail(ctx, job, "internal_error", "persist candidates: "+err.Error())
		return
	}
	intent, inputKind := composeModel(job)
	_ = s.deps.Publisher.PublishJobEvent(ctx, "chora.creation.question.generation_completed.v2", completionEvent{
		JobID:          job.JobID,
		AtomID:         job.AtomID,
		AuthorGCID:     job.AuthorGCID,
		TenantID:       job.TenantID,
		Status:         string(question.JobStatusSucceeded),
		CandidateCount: len(drafts),
		CompletedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Operation:      question.OperationCompose,
		Intent:         string(intent),
		InputKind:      string(inputKind),
	})
}

// fail transitions to failed + refunds mana + emits the completion event.
func (s *QuestionSubscriber) fail(ctx context.Context, job *question.ComposeJob, category, message string) {
	_ = s.deps.JobRepo.UpdateStatus(ctx, job.TenantID, job.JobID, question.JobStatusFailed, nil, message)
	// Refund (best-effort, idempotent on key).
	if job.ManaCharged > 0 {
		idemKey := job.JobID + "-refund"
		_, err := s.deps.Mana.Refund(ctx, ports.RefundManaReq{
			GCID:           job.AuthorGCID,
			TenantID:       job.TenantID,
			ActionCode:     job.ManaActionCode,
			Units:          job.ManaCharged,
			IdempotencyKey: idemKey,
			Reason:         message,
		})
		if err != nil {
			log.Printf("question_subscriber: mana refund failed for job %s: %v", job.JobID, err)
		}
	}
	failIntent, failInputKind := composeModel(job)
	_ = s.deps.Publisher.PublishJobEvent(ctx, "chora.creation.question.generation_completed.v2", completionEvent{
		JobID:           job.JobID,
		AtomID:          job.AtomID,
		AuthorGCID:      job.AuthorGCID,
		TenantID:        job.TenantID,
		Status:          string(question.JobStatusFailed),
		FailureCategory: category,
		FailureMessage:  message,
		ManaRefunded:    job.ManaCharged,
		CompletedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Operation:       question.OperationCompose,
		Intent:          string(failIntent),
		InputKind:       string(failInputKind),
	})
}

// decodeSettings unmarshals a settings_json string into a map. Empty
// input yields an empty map.
func decodeSettings(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// metadataHints lifts the author's str->str hint map (Subject / Cognitive Level /
// Difficulty) out of settings_json. The composer forwards it under
// settings["metadata"]; the orchestrator stamps each non-empty value as a *_hint
// session key the ADK composer + critic read (reasoning_engine_executor
// ._stamp_metadata_hints) AND surfaces them as ADR-197 prompt_conditions in O+.
// Only string-typed, non-blank values survive (proto field 12 is map<string,string>;
// the protomarshal encoder fails loud on a non-string value, so a malformed hint is
// dropped here rather than stranding the job). Returns nil when absent/empty so
// legacy no-hint events stay byte-stable (CHO-1657; lost in the CHO-1826 canvas).
func metadataHints(settings map[string]any) map[string]string {
	raw, ok := settings["metadata"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	md := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			md[k] = s
		}
	}
	if len(md) == 0 {
		return nil
	}
	return md
}
