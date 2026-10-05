// ai_assist_async_handler.go — async POST + GET surface for the qgen
// 2-agent crew per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md Step 4c.
//
// This is the ONLY /api/atoms/ai-assist implementation — the public
// `aiAssist` method (ai_assist_handler.go) delegates here unconditionally.
// The legacy Vertex AI Agent Engine sync dispatch (QGen-direct + AI Kernel
// Orchestrator) was retired with the engine decommission (ADR-169; CHO-1920).
//
//	POST /api/atoms/ai-assist
//	     → mint UUIDv7 job_id
//	     → INSERT ai_assist_jobs row (status: QUEUED)
//	     → outbox-publish chora.creation.ai_assist.started.v1
//	     → return 202 + AiAssistJob envelope
//
//	GET  /api/atoms/ai-assist/{job_id}
//	     → SELECT one row (tenant-scoped); return AiAssistJob envelope
//	       (status reflects orchestrator's terminal-event update)
//
// The Pub/Sub subscriber for ai_assist.completed.v1 / .refused.v1 lives
// in internal/adapter/events/ai_assist_completed_consumer.go (Step 4d).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// manaActionAtomAuthoringAssist is the configurable price-plan action_code for
// the qgen crew authoring path (POST /api/atoms/ai-assist). Resolved via the
// ADR-178 layer with Units==0 (tenant-aware); registered in mana_action_def +
// a platform-default rule of 0 (free) so the crew is free by default and a
// tenant override turns metering on. CHO-1661 / FU-4b.
const manaActionAtomAuthoringAssist = "atom_authoring_assist"

// meterCrewAssistFailOpen debits the configurable atom-authoring-assist price
// for the qgen crew via the price-plan layer (Units==0 ⇒ server-resolved). It
// is FAIL-OPEN: an unpriced action_code (default state) or a metering-infra
// error serves free — AI authoring must never be blocked by a metering hiccup —
// and ONLY a genuine insufficient balance writes the 402 envelope (halted=true).
// Returns the charged units (0 when free) + whether a 402 was written.
func (h *AtomHandler) meterCrewAssistFailOpen(w http.ResponseWriter, r *http.Request, gcid, tenantID, idemKey string) (charged int64, halted bool) {
	if h.mana == nil {
		return 0, false // metering not wired ⇒ free
	}
	resp, err := h.mana.Deduct(r.Context(), ports.DeductManaReq{
		GCID:           gcid,
		TenantID:       tenantID,
		ActionCode:     manaActionAtomAuthoringAssist,
		Units:          0, // resolve via the price-plan layer (tenant override > default 0)
		IdempotencyKey: idemKey,
		RequestID:      idemKey,
	})
	if err == nil {
		return resp.ChargedUnits, false
	}
	var iErr *clients.InsufficientManaError
	if errors.As(err, &iErr) {
		writeJSON(w, http.StatusPaymentRequired, insufficientManaResponse{
			Error: insufficientManaError{
				Code:          "INSUFFICIENT_MANA",
				Message:       "Insufficient mana balance; top up to continue",
				CorrelationID: idemKey,
				Upsell: insufficientManaUpsellEnv{
					RequiredUnits:         iErr.RequiredUnits,
					CurrentBalanceUnits:   iErr.CurrentBalanceUnits,
					RecommendedPlanCode:   iErr.RecommendedPlanCode,
					RecommendedTopupUnits: iErr.RecommendedTopupUnits,
					StripeCheckoutURL:     iErr.StripeCheckoutURL,
				},
			},
		})
		return 0, true
	}
	// Unpriced action_code (gRPC InvalidArgument) OR transient infra error:
	// serve free. Default state (no rule) lands here until a price is set.
	log.Printf("ai-assist crew metering: fail-open (served free): %v", err)
	return 0, false
}

// refundCrewAssist best-effort refunds a crew-assist debit when the job fails
// to persist/publish after a successful charge. No-op when nothing was charged.
func (h *AtomHandler) refundCrewAssist(ctx context.Context, gcid, tenantID string, units int64, idemKey string) {
	if h.mana == nil || units <= 0 {
		return
	}
	_, _ = h.mana.Refund(ctx, ports.RefundManaReq{
		GCID:           gcid,
		TenantID:       tenantID,
		ActionCode:     manaActionAtomAuthoringAssist,
		Units:          int(units),
		IdempotencyKey: idemKey + "-refund",
		RequestID:      idemKey,
		Reason:         "ai-assist crew job did not start",
	})
}

// -----------------------------------------------------------------------------
// Wire shapes — mirror chora-contracts/openapi/creation-questions.yaml
// -----------------------------------------------------------------------------

// aiAssistAsyncRequest mirrors AiAssistGenerateRequest in the OpenAPI spec.
type aiAssistAsyncRequest struct {
	QuestionType string            `json:"question_type"` // "mcq" | "oe"
	Prompt       string            `json:"prompt"`
	Metadata     map[string]string `json:"metadata"`
	MaxRetries   int               `json:"max_retries"` // 0 → use default per orchestrator
	// W8 author opt-in image flags (default false). Threaded into the
	// started.v1 event (proto fields 13/14) so the orchestrator decides
	// whether to route stem / model-answer image generation through the
	// Model Gateway. Absent in the body decodes to false per the OpenAPI
	// `default: false` contract.
	ImageForStem   bool `json:"image_for_stem"`
	ImageForAnswer bool `json:"image_for_answer"`
	// Optional MCQ shape hint (forwarded to orchestrator verbatim).
	MCQHint *aiAssistMCQHint `json:"mcq_hint,omitempty"`
}

type aiAssistMCQHint struct {
	OptionCount int    `json:"option_count,omitempty"`
	ScoringMode string `json:"scoring_mode,omitempty"`
}

// aiAssistJobEnvelope mirrors OpenAPI AiAssistJob. Returned by:
//   - POST /api/atoms/ai-assist           (status: QUEUED, 202)
//   - GET  /api/atoms/ai-assist/{job_id}  (current state, 200)
type aiAssistJobEnvelope struct {
	JobID          string              `json:"job_id"`
	TenantID       string              `json:"tenant_id,omitempty"`
	AuthorGCID     string              `json:"author_gcid,omitempty"`
	Status         string              `json:"status"`
	QuestionType   string              `json:"question_type"`
	RequestPayload json.RawMessage     `json:"request_payload,omitempty"`
	Candidate      json.RawMessage     `json:"candidate,omitempty"`
	PipelineTrace  json.RawMessage     `json:"pipeline_trace,omitempty"`
	AttemptCount   int                 `json:"attempt_count"`
	QualityWarning bool                `json:"quality_warning"`
	Refusal        *aiAssistJobRefusal `json:"refusal,omitempty"`
	ManaCharged    int                 `json:"mana_charged,omitempty"`
	CreatedAt      string              `json:"created_at"`
	UpdatedAt      string              `json:"updated_at"`
	CompletedAt    *string             `json:"completed_at,omitempty"`
}

type aiAssistJobRefusal struct {
	Reason            string `json:"reason"`
	ModelArmorVerdict string `json:"model_armor_verdict,omitempty"`
	UserFacingMessage string `json:"user_facing_message,omitempty"`
}

// -----------------------------------------------------------------------------
// POST handler — async dispatch
// -----------------------------------------------------------------------------

// aiAssistAsync handles POST /api/atoms/ai-assist via the qgen crew async
// path. The public aiAssist method delegates here unconditionally (the
// legacy sync engine dispatch was retired in CHO-1920).
//
// Flow:
//  1. Validate method, tenant + gcid, request body
//  2. Mint UUIDv7 job_id
//  3. Construct aiassist.Job in QUEUED + INSERT via the repo
//  4. Publish chora.creation.ai_assist.started.v1 via outbox
//  5. Return 202 + AiAssistJob envelope
//
// Fail-loud per [[feedback-no-stubs-real-wiring]] — if the repo OR
// publisher is nil at boot the handler 503s with a stable code so the
// gap is visible.
func (h *AtomHandler) aiAssistAsync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/atoms/ai-assist (async)")
		return
	}
	if h.aiAssistJobs == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_assist_jobs_not_wired",
			"AiAssistJobsRepository is nil at boot — pg adapter not wired in main.go")
		return
	}
	if h.aiAssistPublisher == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_assist_publisher_not_wired",
			"JobEventPublisher is nil at boot — outbox publisher not wired in main.go")
		return
	}

	var req aiAssistAsyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ai_assist_invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "ai_assist_missing_prompt",
			"prompt is required")
		return
	}
	qt := aiassist.QuestionType(strings.ToLower(strings.TrimSpace(req.QuestionType)))
	if !qt.Valid() {
		writeError(w, http.StatusBadRequest, "ai_assist_invalid_question_type",
			"question_type must be mcq | oe (Phyllis scope)")
		return
	}

	tenantID, gcid := tenantAndGCIDFromRequest(r)
	if tenantID == "" || gcid == "" {
		writeError(w, http.StatusUnauthorized, "ai_assist_missing_actor",
			"X-Tenant-Id + gcid required")
		return
	}

	jobID := uuid.Must(uuid.NewV7()).String()

	// FU-4(b) — meter the qgen crew authoring path via the price-plan layer.
	// Debits the configurable `atom_authoring_assist` price (default 0 = free;
	// a tenant override turns metering on). Fail-open: only a genuine
	// insufficient balance halts here (402 already written).
	manaIdem := jobID + "-assist-mana"
	manaCharged, halted := h.meterCrewAssistFailOpen(w, r, gcid, tenantID, manaIdem)
	if halted {
		return
	}

	// Re-serialise the request body verbatim for audit + for the
	// orchestrator's started.v1 event to read out.
	requestPayload, err := json.Marshal(req)
	if err != nil {
		h.refundCrewAssist(r.Context(), gcid, tenantID, manaCharged, manaIdem)
		writeError(w, http.StatusInternalServerError, "ai_assist_marshal_request",
			err.Error())
		return
	}

	job, err := aiassist.NewQueuedJob(jobID, tenantID, gcid, qt, requestPayload)
	if err != nil {
		h.refundCrewAssist(r.Context(), gcid, tenantID, manaCharged, manaIdem)
		writeError(w, http.StatusBadRequest, "ai_assist_invalid_job",
			err.Error())
		return
	}

	if err := h.aiAssistJobs.Create(r.Context(), job); err != nil {
		h.refundCrewAssist(r.Context(), gcid, tenantID, manaCharged, manaIdem)
		writeError(w, http.StatusInternalServerError, "ai_assist_create_failed",
			err.Error())
		return
	}

	// Publish ai_assist.started.v1 via the outbox. Topic name mirrors
	// chora-infra/terraform/modules/m10-data-plane/main.tf:185.
	//
	// Inject the inbound W3C traceparent so the orchestrator's
	// qgen_crew_runner.handle_started span continues the FE-originated
	// trace tree (Pub/Sub trace context propagation per
	// [[ai-observability-cloud-trace]]). Without this, the orchestrator
	// starts a fresh root trace and the auditor can't reconstruct the
	// full request lifecycle in Cloud Trace.
	startedPayload := map[string]any{
		"assist_id":     jobID,
		"tenant_id":     tenantID,
		"author_gcid":   gcid,
		"content_type":  string(qt), // legacy proto field carries question_type via this
		"question_type": string(qt), // new field added 2026-05-17
		"prompt":        req.Prompt,
		"metadata":      req.Metadata,
		"max_retries":   req.MaxRetries,
		// W8 author-opt-in image flags (proto fields 13/14). Threaded
		// verbatim; the encoder omits false (proto3 default-elision) so a
		// non-opted-in request stays byte-compatible with pre-W8 producers.
		"image_for_stem":   req.ImageForStem,
		"image_for_answer": req.ImageForAnswer,
		"started_at":       time.Now().UTC().Format(time.RFC3339),
		"traceparent":      tracing.TraceparentFromContext(r.Context()),
		// ADR-195 WS7 (D7) compose model — the orchestrator routes on these.
		// (prompt-seeded new-question crew dispatch.)
		"operation":  question.OperationCompose,
		"intent":     string(question.IntentNewQuestion),
		"input_kind": string(question.InputPrompt),
	}
	if err := h.aiAssistPublisher.PublishJobEvent(
		r.Context(),
		// ADR-195 WS7 step 5 — .v2 (v1 retired); the orchestrator consumes only v2.
		"chora.creation.ai_assist.started.v2",
		startedPayload,
	); err != nil {
		// Publish failure: the row is already QUEUED in the DB; failing the
		// HTTP response is the right call here — the FE will retry, and the
		// outbox dispatcher won't have an event to dispatch until the next
		// POST. Per [[data-consistency]] outbox-publish-then-INSERT is the
		// alternative; ON CONFLICT idempotency on Create makes the order safe.
		h.refundCrewAssist(r.Context(), gcid, tenantID, manaCharged, manaIdem)
		writeError(w, http.StatusInternalServerError, "ai_assist_publish_failed",
			err.Error())
		return
	}

	out := jobToEnvelope(job, false /* hideRequestPayload */)
	out.ManaCharged = int(manaCharged)
	writeJSON(w, http.StatusAccepted, out)
}

// -----------------------------------------------------------------------------
// GET handler — status poll
// -----------------------------------------------------------------------------

// getAiAssistJob handles GET /api/atoms/ai-assist/{job_id}.
//
// Tenant-scoped via the repo's Get which wraps in RunInTenantTx. Cross-
// tenant accesses return 404 (RLS-style — the row pretends not to exist).
func (h *AtomHandler) getAiAssistJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /api/atoms/ai-assist/{job_id}")
		return
	}
	if h.aiAssistJobs == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_assist_jobs_not_wired",
			"AiAssistJobsRepository is nil at boot")
		return
	}

	// Parse the {job_id} segment from the URL path.
	rest := strings.TrimPrefix(r.URL.Path, "/api/atoms/ai-assist/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.Contains(rest, "/") {
		writeError(w, http.StatusBadRequest, "ai_assist_invalid_job_id",
			"job_id path segment required (single UUID, no further path)")
		return
	}
	jobID := rest

	tenantID, callerGCID := tenantAndGCIDFromRequest(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "ai_assist_missing_tenant",
			"X-Tenant-Id required")
		return
	}

	job, err := h.aiAssistJobs.Get(r.Context(), tenantID, jobID)
	if err != nil {
		if err == aiassist.ErrNotFound {
			writeError(w, http.StatusNotFound, "ai_assist_job_not_found",
				"no job for the given (tenant, job_id)")
			return
		}
		writeError(w, http.StatusInternalServerError, "ai_assist_get_failed",
			err.Error())
		return
	}
	// CHO-2268 (SECURITY) — job-owner check. The envelope below carries the
	// generated Candidate (the qgen MCQ, WITH is_correct + explainer) plus the
	// RequestPayload and PipelineTrace, so tenant scoping alone left a cross-USER
	// IDOR: any caller sharing the tenant could read another author's answer key.
	// The caller gcid was previously discarded outright (`tenantID, _ := ...`).
	//
	// Mirrors the sibling getQuestionJob (question_jobs_handler.go), which has
	// enforced this all along — the divergence was an oversight, not a decision.
	// Fail-CLOSED: a blank caller gcid owns nothing.
	if callerGCID == "" || job.AuthorGCID != callerGCID {
		writeError(w, http.StatusForbidden, "ai_assist_job_not_owned",
			"caller does not own this job")
		return
	}

	out := jobToEnvelope(job, true /* includeRequestPayload */)
	writeJSON(w, http.StatusOK, out)
}

// -----------------------------------------------------------------------------
// Routing dispatcher for the /api/atoms/ai-assist/ subtree (trailing-slash
// path; longest-prefix match wins over /api/atoms/ catch-all).
// -----------------------------------------------------------------------------

// aiAssistSubtree handles any request under /api/atoms/ai-assist/{...}.
// Today only GET status is supported; extension points are guarded
// behind the method switch so non-GET returns 405.
func (h *AtomHandler) aiAssistSubtree(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getAiAssistJob(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /api/atoms/ai-assist/{job_id}")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// jobToEnvelope converts the domain Job to the wire envelope.
// hideRequestPayload=true on POST 202 returns to keep the response tight
// (FE already has the request body it just sent); GET status returns
// the full envelope including request_payload for audit replay.
func jobToEnvelope(job *aiassist.Job, includeRequestPayload bool) aiAssistJobEnvelope {
	out := aiAssistJobEnvelope{
		JobID:          job.ID,
		TenantID:       job.TenantID,
		AuthorGCID:     job.AuthorGCID,
		Status:         string(job.Status),
		QuestionType:   string(job.QuestionType),
		AttemptCount:   job.AttemptCount,
		QualityWarning: job.QualityWarning,
		ManaCharged:    job.ManaCharged,
		CreatedAt:      job.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      job.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if includeRequestPayload && len(job.RequestPayload) > 0 {
		out.RequestPayload = json.RawMessage(job.RequestPayload)
	}
	if len(job.ResultPayload) > 0 {
		out.Candidate = json.RawMessage(job.ResultPayload)
	}
	if len(job.PipelineTrace) > 0 {
		out.PipelineTrace = json.RawMessage(job.PipelineTrace)
	}
	if job.RefusalReason != "" {
		out.Refusal = &aiAssistJobRefusal{
			Reason:            string(job.RefusalReason),
			ModelArmorVerdict: job.RefusalArmorVerdict,
			UserFacingMessage: job.RefusalUserFacingMsg,
		}
	}
	if job.CompletedAt != nil {
		s := job.CompletedAt.UTC().Format(time.RFC3339)
		out.CompletedAt = &s
	}
	return out
}
