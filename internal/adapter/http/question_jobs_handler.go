// question_jobs_handler.go — HTTP handlers for the AI single-question
// async path (P5-D in golden-hopping-owl). Routes mounted under
// /api/atoms/{atom_id}/question-jobs[...] and
// /api/atoms/{atom_id}/questions/{question_id}/ai-model-answer-jobs.
//
// Per D4 (every AI call is async): handlers return 202 + job_id + poll_url
// immediately after debiting mana and publishing the
// chora.creation.question.generation_requested.v1 event. The Pub/Sub
// subscriber in services/chora-creation/internal/adapter/pubsub/
// processes the message, calls the QGen engine, and updates the job row
// in place.
//
// Editability invariant (design §1, /accept endpoint): the user MAY
// override every persisted field per candidate. Accept-time merge is:
//
//	prompt          := override_or_ai
//	mcq_payload     := override_or_ai
//	oe_payload      := override_or_ai
//	atom_meta       := override_or_unchanged
//
// On accept, a Question is persisted via the existing QuestionRepository.
// Error envelope codes:
//
//	CREATION_JOB_INVALID_BODY, CREATION_JOB_UNKNOWN_TYPE,
//	CREATION_JOB_NOT_FOUND, CREATION_JOB_NOT_READY,
//	CREATION_JOB_NOT_OWNED, CREATION_JOB_INTERNAL.
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	choraserver "github.com/apollo-chora/chora-common/http"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/adapter/extraction"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// JobEventPublisher is the minimal port used by this handler to emit
// chora.creation.question.* events. Production wires the outbox; tests
// inject an in-memory recorder.
type JobEventPublisher interface {
	PublishJobEvent(ctx context.Context, topic string, payload any) error
}

// QuestionJobsHandler exposes the question-jobs HTTP routes.
type QuestionJobsHandler struct {
	jobRepo      ports.QuestionJobRepository
	questionRepo ports.QuestionRepository
	atomRepo     atom.Repository
	mana         ports.ManaLedger
	publisher    JobEventPublisher

	// P6 — batch source-material BlobStore. Nil when not wired; the
	// multipart path returns 503 CREATION_BATCH_NOT_WIRED in that case.
	blob ports.BlobStore

	// Lane 1c (D9) — source-material chunk store. Nil skips the deterministic
	// extraction step at job creation (the batch path itself stays available;
	// citations on such jobs surface as AI-reported because no verification
	// corpus exists).
	chunks ports.SourceChunkRepository

	// Lane 1c W3 (D10) — outbox publisher for question_batch.accepted.v1.
	// Nil ⇒ test_set-carrying accepts 503 (fail loud, before persistence).
	batchAcceptedPub ports.QuestionBatchAcceptedPublisher

	// reHomer durably re-homes an accepted candidate's transient W8 images into
	// chora-atom-media BEFORE the first revision is persisted (ADR-210), so the
	// accepted question is durable from creation. Shared with the atom +
	// questions handlers; nil ⇒ re-home is skipped (images stay transient).
	reHomer *questionImageReHomer
}

// SetBlobStore wires the GCS BlobStore for the P6 batch path. Nil
// disables only the multipart upload variant; the JSON ai_draft path on
// the same endpoint remains available.
func (h *QuestionJobsHandler) SetBlobStore(b ports.BlobStore) {
	h.blob = b
}

// SetChunkStore wires the Lane 1c source-chunk repository. Nil skips the
// extraction step (best-effort feature — never blocks the batch path).
func (h *QuestionJobsHandler) SetChunkStore(c ports.SourceChunkRepository) {
	h.chunks = c
}

// SetBatchAcceptedPublisher wires the Lane 1c W3 outbox publisher for
// chora.creation.question_batch.accepted.v1. Nil ⇒ accept requests carrying
// a test_set block 503 (fail loud BEFORE persistence — never silently drop
// the author's composition intent).
func (h *QuestionJobsHandler) SetBatchAcceptedPublisher(p ports.QuestionBatchAcceptedPublisher) {
	h.batchAcceptedPub = p
}

// NewQuestionJobsHandler wires the handler.
func NewQuestionJobsHandler(
	jobRepo ports.QuestionJobRepository,
	questionRepo ports.QuestionRepository,
	atomRepo atom.Repository,
	mana ports.ManaLedger,
	publisher JobEventPublisher,
) *QuestionJobsHandler {
	return &QuestionJobsHandler{
		jobRepo:      jobRepo,
		questionRepo: questionRepo,
		atomRepo:     atomRepo,
		mana:         mana,
		publisher:    publisher,
	}
}

// -----------------------------------------------------------------------------
// Mana action codes (matches identity migration 0009 — see design §3.2)
// -----------------------------------------------------------------------------

const (
	manaActionModelAnswer = "question_authoring_model_answer"
	manaActionImageRegen  = "question_authoring_image_regen" // CHO-1819 P3 review image regenerate
	// CHO-1826 U3b — unified per-accepted-AI-question pricing (mig 0024) REPLACES
	// the retired upfront codes question_authoring_ai_draft / _batch_parse /
	// _batch_per_item (no longer referenced). One debit per accepted AI question
	// at accept time: base for text, 2× for any image (stem and/or answer).
	// Manual questions are not charged (U3c).
	manaActionGenerate      = "question_authoring_generate"
	manaActionGenerateImage = "question_authoring_generate_image"
	// FU-4(b): qgen authoring prices are no longer hard-coded here — the debit
	// sends Units==0 and chora-identity's ADR-178 price-plan layer resolves the
	// price (tenant-aware, with per_item × item_count for batch). The catalogue
	// floor still mirrors the historical 5/10/50/5×N, so the default is
	// behaviour-neutral; a tenant override now actually applies.

	// maxBatchUploadBytes mirrors `storage.MaxBlobBytes`. The handler
	// enforces here too so we 413 before streaming into GCS.
	maxBatchUploadBytes = 32 * 1024 * 1024
)

// allowedBatchMIMEs is the {PDF, DOCX, MD, TXT} + image allowlist.
// Images joined per Lane 1c D7 (CHO-1703 / ADR-180): they ground the crew
// multimodally but produce NO text chunks (citations stay AI-reported).
var allowedBatchMIMEs = map[string]bool{
	"application/pdf": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"text/markdown": true,
	"text/plain":    true,
	"image/png":     true,
	"image/jpeg":    true,
	"image/webp":    true,
}

// maxBatchSourceFiles caps the role="source" files per batch job (D7 v1).
const maxBatchSourceFiles = 5

// Topics published by this handler. ADR-195 WS7 step 5 — the 2 compose events
// publish to .v2 (operation/intent/input_kind; job_type dropped); v1 retired.
const (
	topicQuestionGenerationRequested = "chora.creation.question.generation_requested.v2"
	topicQuestionAuthored            = "chora.creation.question.authored.v1"
	topicQuestionGenerationCompleted = "chora.creation.question.generation_completed.v2"
	// topicAtomCreated is emitted per-candidate on the batch_source_material
	// accept path (Debt #28). For ai_draft / ai_model_answer the parent atom
	// already exists; no atom.created event fires here.
	topicAtomCreated = "chora.creation.atom.created.v1"
)

// -----------------------------------------------------------------------------
// Wire DTOs
// -----------------------------------------------------------------------------

// modelAnswerJobReq is the (optional) JSON body for the model-answer endpoint.
type modelAnswerJobReq struct {
	ToneHint string `json:"tone_hint,omitempty"`
}

// questionJobReq is the JSON body for POST /api/atoms/{atom_id}/question-jobs.
type questionJobReq struct {
	Type         string   `json:"type"`          // "ai_draft" only (P5; batch_source_material returns 501)
	QuestionType string   `json:"question_type"` // "mcq" | "oe"
	Prompt       string   `json:"prompt"`
	Difficulty   int      `json:"difficulty,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Count        int      `json:"count,omitempty"` // default 1, max 5
	// CHO-1826 Gap #4 follow-up — author forced-image opt-in for the no-files
	// ai_draft path. Mirrors the single-mode drawer (AiAssistRequest) + the
	// batch type_plan per-type flags. When true the subscriber forwards them on
	// ai_assist.started.v1 (proto fields 13/14) so the single qgen runner's
	// render_image node fires and the canvas trace widget shows the Illustration
	// card. Default false ⇒ byte-identical legacy ai_draft.
	ImageForStem   bool `json:"image_for_stem,omitempty"`
	ImageForAnswer bool `json:"image_for_answer,omitempty"`
	// CHO-1657 — author hint map (str->str): subject / cognitive_level /
	// difficulty (bucket). Carried into settings_json so the subscriber lifts it
	// onto ai_assist.started.v1 (proto field 12 metadata) → the orchestrator stamps
	// the *_hint session keys AND surfaces them as ADR-197 prompt_conditions.
	// Optional (omitempty) so legacy bodies stay byte-stable; the canvas (CHO-1826)
	// must resend it after the unify dropped it.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// jobAcceptedResponse is the 202 envelope.
type jobAcceptedResponse struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	PollURL string `json:"poll_url"`
}

// insufficientManaResponse is the 402 envelope.
type insufficientManaResponse struct {
	Error insufficientManaError `json:"error"`
}
type insufficientManaError struct {
	Code          string                    `json:"code"`
	Message       string                    `json:"message"`
	CorrelationID string                    `json:"correlation_id,omitempty"`
	Upsell        insufficientManaUpsellEnv `json:"upsell"`
}
type insufficientManaUpsellEnv struct {
	RequiredUnits         int64   `json:"required_units"`
	CurrentBalanceUnits   int64   `json:"current_balance_units"`
	RecommendedPlanCode   string  `json:"recommended_plan_code"`
	RecommendedTopupUnits int     `json:"recommended_topup_units"`
	StripeCheckoutURL     *string `json:"stripe_checkout_url"` // nullable
}

// jobResponse is the GET poll envelope.
type jobResponse struct {
	JobID       string          `json:"job_id"`
	AtomID      string          `json:"atom_id"`
	Status      string          `json:"status"`
	ManaCharged int             `json:"mana_charged"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Candidates  json.RawMessage `json:"candidate_questions,omitempty"`
	// ADR-251 D5 (CHO-2398): "k of n chunks" while the job RUNS; the
	// candidates above then carry questions-so-far (flattened chunk slots).
	// Omitted on terminal jobs and on running jobs with no applied chunk yet.
	ChunkProgress *chunkProgress `json:"chunk_progress,omitempty"`
	// Lane 1c (D4): the composer's LLM-proposed test-set structure (OpenAPI
	// ProposedTestSet). Omitted for single-question jobs / pre-1c batches.
	ProposedTestSet json.RawMessage `json:"proposed_test_set,omitempty"`
	// CHO-1819 P2: the mixed-batch outcome (requested vs generated totals +
	// per-type breakdown + shortfall_reason) so the FE can render a shortfall
	// banner. Omitted for single-type / legacy batches / pre-P2 orchestrator.
	GenerationSummary json.RawMessage `json:"generation_summary,omitempty"`
	// CHO-1826 Gap #4: the per-step QGen pipeline trace (PipelineTraceStep[])
	// so the canvas can render the <chora-aplus-trace-widget> transparency card
	// (IMDA D2). The SAME trace the single-mode AI-Assist drawer renders.
	// Omitted for refused / failed jobs and pre-fix jobs.
	PipelineTrace json.RawMessage `json:"pipeline_trace,omitempty"`
}

// acceptJobReq is the body for POST /question-jobs/{job_id}/accept.
type acceptJobReq struct {
	AcceptedCandidates []acceptCandidate `json:"accepted_candidates"`
	// TestSet — Lane 1c (CHO-1703 / ADR-180 D1): the author-curated test-set
	// composition from the batch review step. ABSENT/null ⇒ today's accept
	// path bit-identical (toggle OFF). PRESENT ⇒ after atom persistence the
	// handler outbox-publishes chora.creation.question_batch.accepted.v1
	// (exactly-once per job via the succeeded→accepted CAS).
	TestSet *acceptTestSet `json:"test_set,omitempty"`
}

// acceptTestSet mirrors the OpenAPI accept-request test_set block (v1.5.0).
type acceptTestSet struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Items       []acceptTestSetItem `json:"items,omitempty"`
}

// acceptTestSetItem is the per-candidate curated placement. Absent fields
// fall back to the composer proposal, then uniform default 10 / submission
// order (D5).
type acceptTestSetItem struct {
	DraftID      string `json:"draft_id"`
	Points       *int   `json:"points,omitempty"`
	DisplayOrder *int   `json:"display_order,omitempty"`
}
type acceptCandidate struct {
	DraftID string `json:"draft_id"`
	// Type — REQUIRED for an INLINE manually-authored candidate (no draft_id):
	// "mcq" | "oe" (CHO-1826 U3c). Ignored for draft-backed candidates (the
	// AI draft's stored type wins). The prompt/payload come from the *Override
	// fields below, which carry the full manual body in the inline case.
	Type               string            `json:"type,omitempty"`
	PromptOverride     *string           `json:"prompt_override,omitempty"`
	MCQPayloadOverride *acceptMCQPayload `json:"mcq_payload_override,omitempty"`
	OEPayloadOverride  *acceptOEPayload  `json:"oe_payload_override,omitempty"`
	AtomMetaOverride   map[string]any    `json:"atom_meta_override,omitempty"`
	// W8 image-gen — generated QUESTION/STEM + MODEL-ANSWER illustration URLs.
	// The payload overrides above carry only editable text, so without these
	// the AI-generated images would be dropped on commit. Wire names IDENTICAL
	// across FE / BE DTO / payload / OpenAPI.
	ImageURL       *string `json:"image_url,omitempty"`
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
}
type acceptMCQPayload struct {
	Options []acceptMCQOption `json:"options"`
}
type acceptMCQOption struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	Explainer string `json:"explainer"`
}
type acceptOEPayload struct {
	ModelAnswer string `json:"model_answer"`
}

type acceptResponse struct {
	Persisted   []*question.Question `json:"persisted"`
	ManaDebited int                  `json:"mana_debited"`
}

// chunkProgress is the jobResponse "k of n" block (ADR-251 D5, CHO-2398).
type chunkProgress struct {
	ChunksApplied int `json:"chunks_applied"`
	ChunksTotal   int `json:"chunks_total"`
}

// flattenChunkSlots orders the chunk-slot object {"0": [...], "1": [...]}
// NUMERICALLY by chunk_index (10 sorts after 2) and concatenates the slot
// arrays into one questions-so-far array. Returns ok=false on a malformed
// slot object (the poll then falls back to the terminal candidates field
// rather than serving garbage).
func flattenChunkSlots(slotsJSON []byte) (json.RawMessage, int, bool) {
	var slots map[string][]json.RawMessage
	if err := json.Unmarshal(slotsJSON, &slots); err != nil {
		return nil, 0, false
	}
	if len(slots) == 0 {
		return nil, 0, true
	}
	indices := make([]int, 0, len(slots))
	byIndex := make(map[int][]json.RawMessage, len(slots))
	for key, arr := range slots {
		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, 0, false
		}
		indices = append(indices, idx)
		byIndex[idx] = arr
	}
	sort.Ints(indices)
	flat := make([]json.RawMessage, 0, 16)
	for _, idx := range indices {
		flat = append(flat, byIndex[idx]...)
	}
	out, err := json.Marshal(flat)
	if err != nil {
		return nil, 0, false
	}
	return out, len(indices), true
}

// candidateDraft is the persisted shape inside candidate_questions_jsonb.
type candidateDraft struct {
	DraftID    string               `json:"draft_id"`
	Type       string               `json:"type"`
	Prompt     string               `json:"prompt"`
	MCQPayload *question.MCQPayload `json:"mcq_payload,omitempty"`
	OEPayload  *question.OEPayload  `json:"oe_payload,omitempty"`
	QGenScore  float64              `json:"qgen_score,omitempty"`
	ModelUsed  string               `json:"model_used,omitempty"`
	// ImageURL / AnswerImageURL — CHO-1825: the canonical top-level draft image
	// location (render_image_set emit + regenerate patch write here). Accept
	// prefers the FE-sent acceptCandidate.ImageURL, else falls back to these.
	ImageURL       *string `json:"image_url,omitempty"`
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
	// ImageGcsURI / AnswerImageGcsURI — ADR-210: the canonical durable gs://
	// OBJECT path for each placement (persisted from the orchestrator's B2 stamp)
	// so image-to-image regen reads the object directly, never parsing the URL.
	ImageGcsURI       *string `json:"image_gcs_uri,omitempty"`
	AnswerImageGcsURI *string `json:"answer_image_gcs_uri,omitempty"`
}

// -----------------------------------------------------------------------------
// Dispatch — called from AtomHandler.atomsItem when the path matches a
// /question-jobs[...] or /questions/{qid}/ai-model-answer-jobs sub-resource.
// -----------------------------------------------------------------------------

// dispatch routes the matched paths. Called with:
//
//   - atomID  : the parent atom id
//   - rest    : everything after /api/atoms/{atom_id}, e.g.
//     "/question-jobs", "/question-jobs/{jid}",
//     "/question-jobs/{jid}/accept",
//     "/questions/{qid}/ai-model-answer-jobs"
//
// Returns false when the path is not in this handler's purview so the
// outer handler can fall through to other dispatchers.
func (h *QuestionJobsHandler) dispatch(w http.ResponseWriter, r *http.Request, atomID, rest string) bool {
	// /question-jobs[...]
	if strings.HasPrefix(rest, "/question-jobs") {
		sub := strings.TrimPrefix(rest, "/question-jobs")
		sub = strings.TrimPrefix(sub, "/")
		switch {
		case sub == "":
			if r.Method == http.MethodPost {
				h.createQuestionJob(w, r, atomID)
				return true
			}
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST is supported on /api/atoms/{atom_id}/question-jobs")
			return true
		default:
			// {jobID}[/accept]
			slash := strings.Index(sub, "/")
			if slash < 0 {
				if r.Method == http.MethodGet {
					h.getQuestionJob(w, r, atomID, sub)
					return true
				}
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only GET is supported on /api/atoms/{atom_id}/question-jobs/{job_id}")
				return true
			}
			jobID := sub[:slash]
			tail := sub[slash+1:]
			if tail == "accept" {
				if r.Method == http.MethodPost {
					h.acceptQuestionJob(w, r, atomID, jobID)
					return true
				}
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only POST is supported on /accept")
				return true
			}
			if tail == "regenerate-image" {
				if r.Method == http.MethodPost {
					h.createImageRegenJob(w, r, atomID, jobID)
					return true
				}
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only POST is supported on /regenerate-image")
				return true
			}
			writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
			return true
		}
	}

	// /questions/{qid}/ai-model-answer-jobs
	if strings.HasPrefix(rest, "/questions/") {
		tail := strings.TrimPrefix(rest, "/questions/")
		// Expect: {qid}/ai-model-answer-jobs OR {qid}/ai-model-answer-jobs/{jid}
		slash := strings.Index(tail, "/")
		if slash < 0 {
			return false // not our subroute
		}
		qid := tail[:slash]
		sub := tail[slash+1:]
		if sub == "ai-model-answer-jobs" {
			if r.Method == http.MethodPost {
				h.createModelAnswerJob(w, r, atomID, qid)
				return true
			}
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST is supported on /ai-model-answer-jobs")
			return true
		}
		return false
	}

	return false
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/questions/{qid}/ai-model-answer-jobs
// -----------------------------------------------------------------------------

func (h *QuestionJobsHandler) createModelAnswerJob(w http.ResponseWriter, r *http.Request, atomID, questionID string) {
	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	// Decode optional body (tone_hint) — empty body OK.
	var req modelAnswerJobReq
	_ = decodeOptionalJSON(r, &req)

	// Verify the target question exists + is owned by the caller.
	q, _, err := h.questionRepo.GetByID(r.Context(), tenantID, questionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
				"question not found")
			return
		}
		log.Printf("question-jobs: GetByID: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL",
			"failed to load question")
		return
	}
	if q.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
			"question does not belong to this atom")
		return
	}
	if q.AuthorGcid != authorGCID {
		writeError(w, http.StatusForbidden, "CREATION_JOB_NOT_OWNED",
			"caller does not own this question")
		return
	}

	// Pre-build job_id so the idempotency_key is stable through the mana debit.
	jobID := uuid.Must(uuid.NewV7()).String()
	idempKey := jobID + "-mana-debit"

	// Mana debit — 402 on insufficient. Units==0 ⇒ the price is resolved
	// server-side via the ADR-178 price-plan layer (tenant-aware); the charged
	// amount comes back for the job row / event / refund (FU-4(b)).
	charged, err := h.debitMana(w, r, ports.DeductManaReq{
		GCID:           authorGCID,
		TenantID:       tenantID,
		ActionCode:     manaActionModelAnswer,
		Units:          0,
		IdempotencyKey: idempKey,
		RequestID:      jobID,
	})
	if err != nil {
		return // h.debitMana already wrote the 402 / 500
	}

	// Persist the job row in REQUESTED status.
	job := &question.ComposeJob{
		JobID:          jobID,
		AtomID:         atomID,
		TenantID:       tenantID,
		AuthorGCID:     authorGCID,
		Intent:         question.IntentModelAnswerFill,
		Input:          question.Input{Prompt: req.ToneHint},
		Status:         question.JobStatusRequested,
		ManaActionCode: manaActionModelAnswer,
		ManaCharged:    int(charged),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := job.Validate(); err != nil {
		log.Printf("question-jobs: compose validate (model-answer): %v", err)
		_ = h.refundMana(r.Context(), authorGCID, tenantID, manaActionModelAnswer, int(charged), idempKey+"-refund", "compose validation failed")
		writeError(w, http.StatusBadRequest, "CREATION_COMPOSE_INVALID", err.Error())
		return
	}
	if err := h.jobRepo.Create(r.Context(), job); err != nil {
		log.Printf("question-jobs: Create model-answer: %v", err)
		// Best-effort refund; the LLM call never happened so the user should not be charged.
		_ = h.refundMana(r.Context(), authorGCID, tenantID, manaActionModelAnswer, int(charged), idempKey+"-refund", "job persistence failed")
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL",
			"failed to persist job")
		return
	}

	// Publish the generation_requested event.
	_ = h.publisher.PublishJobEvent(r.Context(), topicQuestionGenerationRequested, map[string]any{
		"job_id":             jobID,
		"atom_id":            atomID,
		"author_gcid":        authorGCID,
		"tenant_id":          tenantID,
		"question_type":      string(q.Type),
		"target_question_id": questionID,
		"mana_action_code":   manaActionModelAnswer,
		"mana_charged":       int(charged),
		"settings_json":      mustJSONString(map[string]any{"tone_hint": req.ToneHint}),
		"requested_at":       time.Now().UTC().Format(time.RFC3339Nano),
		// ADR-195 WS7 (D7) compose model — carried for the .v2 parallel-publish
		// (v1 encoder ignores these keys; v2 reads them + drops job_type).
		"operation":  question.OperationCompose,
		"intent":     string(question.IntentModelAnswerFill),
		"input_kind": string(question.InputPrompt),
	})

	writeJSON(w, http.StatusAccepted, jobAcceptedResponse{
		JobID:   jobID,
		Status:  string(question.JobStatusRequested),
		PollURL: fmt.Sprintf("/api/atoms/%s/question-jobs/%s", atomID, jobID),
	})
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/question-jobs
// -----------------------------------------------------------------------------

func (h *QuestionJobsHandler) createQuestionJob(w http.ResponseWriter, r *http.Request, atomID string) {
	// P6 — dispatch on Content-Type. multipart/form-data => batch path.
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		h.createBatchSourceMaterialJob(w, r, atomID)
		return
	}

	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	var req questionJobReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", err.Error())
		return
	}
	jobType := strings.TrimSpace(req.Type)
	if jobType == "manual" {
		// CHO-1826 U3c — a manual authoring session (no LLM). Created already
		// succeeded; questions are committed via /accept with inline candidates.
		h.createManualJob(w, r, atomID, authorGCID, tenantID)
		return
	}
	if jobType == "batch_source_material" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"batch_source_material requires Content-Type: multipart/form-data with file + settings parts")
		return
	}
	if jobType != "ai_draft" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_UNKNOWN_TYPE",
			"type must be ai_draft (batch_source_material is P6 multipart-only)")
		return
	}
	qt := question.QuestionType(strings.TrimSpace(req.QuestionType))
	if !qt.Valid() || !qt.Enabled() {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_UNKNOWN_TYPE",
			"question_type must be mcq or oe")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "prompt required")
		return
	}
	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > 5 {
		req.Count = 5
	}

	// Atom must exist.
	if _, err := h.atomRepo.Get(r.Context(), tenantID, atomID); err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "parent atom not found")
			return
		}
		log.Printf("question-jobs: atom lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load parent atom")
		return
	}

	jobID := uuid.Must(uuid.NewV7()).String()
	// ADR-195 — count ALWAYS drives a non-empty plan (count=1 ⇒ 1-entry),
	// retiring the SetMode shortcut. The plan rides the in-memory compose model
	// for Validate; the generation event/settings stay unchanged here (the
	// orchestrator honours the plan in WS4, the FE drives it in WS8).
	plan, perr := question.NewComposePlan(nil, string(qt), req.Count)
	if perr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", perr.Error())
		return
	}
	// U3b (CHO-1826) — generation is no longer charged; the author pays per
	// ACCEPTED question at accept time. No upfront debit / refund here.
	job := &question.ComposeJob{
		JobID:       jobID,
		AtomID:      atomID,
		TenantID:    tenantID,
		AuthorGCID:  authorGCID,
		Intent:      question.IntentNewQuestion,
		Input:       question.Input{Prompt: req.Prompt},
		Plan:        plan,
		Status:      question.JobStatusRequested,
		ManaCharged: 0,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := job.Validate(); err != nil {
		log.Printf("question-jobs: compose validate (ai_draft): %v", err)
		writeError(w, http.StatusBadRequest, "CREATION_COMPOSE_INVALID", err.Error())
		return
	}
	if err := h.jobRepo.Create(r.Context(), job); err != nil {
		log.Printf("question-jobs: Create ai-draft: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to persist job")
		return
	}

	aiDraftSettings := map[string]any{
		"prompt":     req.Prompt,
		"difficulty": req.Difficulty,
		"tags":       req.Tags,
		"count":      req.Count,
	}
	// CHO-1826 Gap #4 — forward the author's forced-image opt-in only when set
	// (proto3-false elision keeps legacy ai_draft settings byte-stable). The
	// subscriber lifts these onto ai_assist.started.v1 (proto fields 13/14).
	if req.ImageForStem {
		aiDraftSettings["image_for_stem"] = true
	}
	if req.ImageForAnswer {
		aiDraftSettings["image_for_answer"] = true
	}
	// CHO-1657 — forward the author's hint map only when present (byte-stable legacy
	// settings). The subscriber lifts it onto the started event's metadata map.
	if len(req.Metadata) > 0 {
		aiDraftSettings["metadata"] = req.Metadata
	}

	_ = h.publisher.PublishJobEvent(r.Context(), topicQuestionGenerationRequested, map[string]any{
		"job_id":           jobID,
		"atom_id":          atomID,
		"author_gcid":      authorGCID,
		"tenant_id":        tenantID,
		"question_type":    string(qt),
		"mana_action_code": "",
		"mana_charged":     0,
		"settings_json":    mustJSONString(aiDraftSettings),
		"requested_at":     time.Now().UTC().Format(time.RFC3339Nano),
		// W2 — propagate the FE-originated W3C trace context so the
		// subscriber's ai_assist.started.v1 continues the trace tree into the
		// orchestrator's qgen_crew_runner (OTLP-everywhere).
		"traceparent": tracing.TraceparentFromContext(r.Context()),
		// ADR-195 WS7 (D7) compose model — carried for the .v2 parallel-publish.
		"operation":  question.OperationCompose,
		"intent":     string(question.IntentNewQuestion),
		"input_kind": string(question.InputPrompt),
	})

	writeJSON(w, http.StatusAccepted, jobAcceptedResponse{
		JobID:   jobID,
		Status:  string(question.JobStatusRequested),
		PollURL: fmt.Sprintf("/api/atoms/%s/question-jobs/%s", atomID, jobID),
	})
}

// -----------------------------------------------------------------------------
// P6 — POST /api/atoms/{atom_id}/question-jobs (multipart/form-data)
// -----------------------------------------------------------------------------

// batchSettings is the parsed `settings` part of the multipart body.
type batchSettings struct {
	JobType      string   `json:"job_type"`      // MUST be "batch_source_material"
	QuestionType string   `json:"question_type"` // mcq | oe (single-type) | mixed (type_plan)
	Count        int      `json:"count"`         // 1..MaxBatchCount; == sum(type_plan.count) when mixed
	Difficulty   int      `json:"difficulty,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	// TypePlan — CHO-1819 P2 mixed-type breakdown. Empty ⇒ legacy single-type
	// path (question_type + count). Non-empty ⇒ validated via aiassist.NewTypePlan
	// (each {question_type∈{mcq,oe}, count>=1, 0<=max_images<=count}; sum<=ceiling
	// and == count). content_type rides "mixed" on the started event.
	TypePlan []typePlanEntry `json:"type_plan,omitempty"`
}

// typePlanEntry is one wire-shape quota from the settings JSON, mapped onto
// aiassist.TypeQuota (the domain VO owns the invariants).
type typePlanEntry struct {
	QuestionType string `json:"question_type"`
	Count        int    `json:"count"`
	MaxImages    int    `json:"max_images"`
	// CHO-1825 2b: per-type deterministic author image opt-in (default false).
	ImageForStem   bool `json:"image_for_stem,omitempty"`
	ImageForAnswer bool `json:"image_for_answer,omitempty"`
}

// createBatchSourceMaterialJob handles the multipart upload path.
//
// Fail-loud sequence (no silent fallback):
//  1. BlobStore wired?            no → 503 CREATION_BATCH_NOT_WIRED
//  2. multipart parse + size cap  no → 400/413
//  3. MIME allowlist              no → 415
//  4. settings JSON valid?        no → 400
//  5. Atom exists?                no → 404
//  6. Mana debit (50 parse)       no → 402
//  7. Blob upload to GCS          fail → 502 + REFUND mana
//  8. Job persist                 fail → 500 + REFUND mana
//  9. Publish event               (best-effort)
//  10. 202 + job_id
//
// createManualJob (CHO-1826 U3c) opens a manual authoring session — a
// manual_draft job created already 'succeeded' (no LLM dispatch, no mana, no
// generation_requested event). The author writes questions by hand in the
// review canvas and commits them via POST /accept with inline (no draft_id)
// candidates, which are free.
func (h *QuestionJobsHandler) createManualJob(w http.ResponseWriter, r *http.Request, atomID, authorGCID, tenantID string) {
	if _, err := h.atomRepo.Get(r.Context(), tenantID, atomID); err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "parent atom not found")
			return
		}
		log.Printf("question-jobs: manual atom lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load parent atom")
		return
	}
	jobID := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()
	job := &question.ComposeJob{
		JobID:                  jobID,
		AtomID:                 atomID,
		TenantID:               tenantID,
		AuthorGCID:             authorGCID,
		Intent:                 question.IntentNewQuestion,
		Input:                  question.Input{ByHand: true},
		Status:                 question.JobStatusSucceeded, // no LLM step; ready to accept now
		ManaCharged:            0,
		CandidateQuestionsJSON: []byte("[]"),
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := job.Validate(); err != nil {
		log.Printf("question-jobs: compose validate (manual): %v", err)
		writeError(w, http.StatusBadRequest, "CREATION_COMPOSE_INVALID", err.Error())
		return
	}
	if err := h.jobRepo.Create(r.Context(), job); err != nil {
		log.Printf("question-jobs: manual Create: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to persist job")
		return
	}
	writeJSON(w, http.StatusAccepted, jobAcceptedResponse{
		JobID:   jobID,
		Status:  string(question.JobStatusSucceeded),
		PollURL: fmt.Sprintf("/api/atoms/%s/question-jobs/%s", atomID, jobID),
	})
}

func (h *QuestionJobsHandler) createBatchSourceMaterialJob(w http.ResponseWriter, r *http.Request, atomID string) {
	if h.blob == nil {
		writeError(w, http.StatusServiceUnavailable, "CREATION_BATCH_NOT_WIRED",
			"batch upload BlobStore is not wired (env GCS_BUCKET_BATCH_UPLOADS missing)")
		return
	}

	// Lift the read/write deadline for THIS upload only: a multi-MB multipart
	// body can exceed the 15s server-wide timeout on the wire alone, and was
	// being severed mid-upload (the 504 that read as a forever-spinner). Every
	// other route keeps the tight default. A writer that does not support
	// deadlines degrades to the server timeout (logged, not fatal).
	if err := choraserver.ExtendRequestDeadlines(w, batchUploadDeadline); err != nil {
		log.Printf("question-jobs: batch upload deadline extension unsupported (falling back to server timeout): %v", err)
	}

	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	// Cap the request body BEFORE multipart parse so a malicious caller
	// can't exhaust memory streaming a >32MB form. The +4KB headroom
	// covers the multipart boundary + headers.
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchUploadBytes+4096)
	if err := r.ParseMultipartForm(maxBatchUploadBytes); err != nil {
		// http.MaxBytesReader-wrapped errors include "request body too
		// large" — surface as 413.
		if strings.Contains(err.Error(), "request body too large") || strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "CREATION_BATCH_FILE_TOO_LARGE",
				fmt.Sprintf("file exceeds %d byte limit", maxBatchUploadBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"multipart parse: "+err.Error())
		return
	}

	// settings part.
	settingsRaw := r.FormValue("settings")
	if strings.TrimSpace(settingsRaw) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"missing settings part")
		return
	}
	var settings batchSettings
	if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"invalid settings JSON: "+err.Error())
		return
	}
	if settings.JobType != "batch_source_material" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_UNKNOWN_TYPE",
			"settings.job_type must be batch_source_material")
		return
	}
	qt := question.QuestionType(strings.TrimSpace(settings.QuestionType))

	// CHO-1819 P2 — validate the mixed-type breakdown (empty ⇒ legacy path).
	// The aiassist.TypePlan VO owns every invariant; the handler only maps the
	// wire shape onto TypeQuota + maps a VO error to a specific 400 (no silent
	// coercion). requested_count stays the sum (the VO enforces sum == count).
	quotas := make([]aiassist.TypeQuota, len(settings.TypePlan))
	for i, e := range settings.TypePlan {
		quotas[i] = aiassist.TypeQuota{
			QuestionType:   e.QuestionType,
			Count:          e.Count,
			MaxImages:      e.MaxImages,
			ImageForStem:   e.ImageForStem,
			ImageForAnswer: e.ImageForAnswer,
		}
	}
	typePlan, tpErr := aiassist.NewTypePlan(quotas, settings.Count)
	if tpErr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_TYPE_PLAN", tpErr.Error())
		return
	}

	// reqQType is the generation_requested event's question_type — ALWAYS a valid
	// enum ("mixed" would dead-letter at the binary encoder). Mixed batches carry
	// the first quota's type here; the started event flips content_type to "mixed"
	// (the subscriber lifts type_plan out of settings_json).
	reqQType := qt
	if typePlan.IsEmpty() {
		// Legacy single-type path — unchanged: gate question_type + clamp count.
		if !qt.Valid() || !qt.Enabled() {
			writeError(w, http.StatusBadRequest, "CREATION_JOB_UNKNOWN_TYPE",
				"settings.question_type must be mcq or oe (Phyllis scope)")
			return
		}
		if settings.Count < 1 {
			settings.Count = 1
		}
		if settings.Count > aiassist.MaxBatchCount {
			settings.Count = aiassist.MaxBatchCount
		}
	} else {
		// Mixed: per-quota types validated by the VO; count == sum ∈ [1, ceiling].
		reqQType = question.QuestionType(typePlan[0].QuestionType)
	}

	// File parts — Lane 1c D7 multi-file: exactly ONE of `file` (legacy
	// single source) or `files` (1..5 sources) + optional `rubric_file`.
	plan, planErr := buildBatchUploadPlan(r.MultipartForm)
	if planErr != nil {
		writeError(w, planErr.status, planErr.code, planErr.msg)
		return
	}

	// Atom must exist (RLS-scoped to the caller's tenant).
	if _, err := h.atomRepo.Get(r.Context(), tenantID, atomID); err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "parent atom not found")
			return
		}
		log.Printf("question-jobs: batch atom lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load parent atom")
		return
	}

	// U3b (CHO-1826) — batch generation (parse) is no longer charged; the
	// author pays per ACCEPTED question at accept time. No upfront debit.
	jobID := uuid.Must(uuid.NewV7()).String()

	// Per-file blob upload — refund mana on any failure (the parse never
	// happens; objects already uploaded are orphaned blobs GC'd by bucket
	// lifecycle, never referenced).
	uploaded := make([]uploadedSourceFile, 0, len(plan))
	for _, spec := range plan {
		f, oerr := spec.header.Open()
		if oerr != nil {
			writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "file open: "+oerr.Error())
			return
		}
		uploadResp, uerr := h.blob.Upload(r.Context(), ports.UploadBlobReq{
			TenantID: tenantID,
			JobID:    jobID,
			MIME:     spec.mime,
			Filename: spec.header.Filename,
			Body:     f,
			Size:     spec.header.Size,
			Slot:     spec.slot,
		})
		_ = f.Close()
		if uerr != nil {
			log.Printf("question-jobs: batch blob upload (slot=%s): %v", spec.slot, uerr)
			if errors.Is(uerr, ports.ErrBlobStoreNotWired) {
				writeError(w, http.StatusServiceUnavailable, "CREATION_BATCH_NOT_WIRED",
					"batch upload BlobStore not configured")
				return
			}
			writeError(w, http.StatusBadGateway, "CREATION_BATCH_BLOB_UPLOAD_FAILED", uerr.Error())
			return
		}
		uploaded = append(uploaded, uploadedSourceFile{
			BlobURI:  uploadResp.BlobURI,
			MimeType: spec.mime,
			Role:     spec.role,
			Filename: spec.header.Filename,
			header:   spec.header,
		})
	}

	// source_files[] settings entries (role-tagged) — persisted on the job
	// row AND riding the generation_requested settings_json so the batch
	// dispatcher can lift them onto ai_assist.started.v1 f20.
	sourceFileEntries := make([]map[string]any, 0, len(uploaded))
	firstSource := uploaded[0] // plan ordering guarantees sources first
	for _, u := range uploaded {
		sourceFileEntries = append(sourceFileEntries, map[string]any{
			"blob_uri":  u.BlobURI,
			"mime_type": u.MimeType,
			"role":      u.Role,
			"filename":  u.Filename,
		})
	}

	// Preserve EVERY author-sent settings key (grounding_mode / context /
	// target_growth_edges / ... flow through untouched — the narrow
	// batchSettings decode is validation-only) + stamp the canonical
	// count/filename/source_files.
	var settingsMap map[string]any
	if err := json.Unmarshal([]byte(settingsRaw), &settingsMap); err != nil || settingsMap == nil {
		settingsMap = map[string]any{}
	}
	settingsMap["count"] = settings.Count
	settingsMap["difficulty"] = settings.Difficulty
	settingsMap["tags"] = settings.Tags
	settingsMap["filename"] = firstSource.Filename
	settingsMap["source_files"] = sourceFileEntries
	settingsJSON := mustJSONString(settingsMap)

	// ADR-195 — the unified compose model for a source-material (RAG) authoring
	// job: intent=new_question, input=source_files, count-driven plan (single-type
	// builds a 1-entry plan from {qt,count}; mixed reuses the validated quotas).
	composePlan, cpErr := question.NewComposePlan(quotas, string(qt), settings.Count)
	if cpErr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_TYPE_PLAN", cpErr.Error())
		return
	}
	srcRefs := make([]question.SourceFileRef, 0, len(uploaded))
	for _, u := range uploaded {
		srcRefs = append(srcRefs, question.SourceFileRef{BlobURI: u.BlobURI, MimeType: u.MimeType, Role: u.Role})
	}

	// Persist the job row. f17/18 mirror source_files[0] (role=source) for
	// back-compat with pre-1c consumers.
	now := time.Now().UTC()
	job := &question.ComposeJob{
		JobID:          jobID,
		AtomID:         atomID,
		TenantID:       tenantID,
		AuthorGCID:     authorGCID,
		Intent:         question.IntentNewQuestion,
		Input:          question.Input{SourceFiles: srcRefs},
		Plan:           composePlan,
		Status:         question.JobStatusRequested,
		ManaCharged:    0,
		SourceBlobURI:  firstSource.BlobURI,
		SourceMimeType: firstSource.MimeType,
		SettingsJSON:   []byte(settingsJSON),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := job.Validate(); err != nil {
		log.Printf("question-jobs: compose validate (batch): %v", err)
		writeError(w, http.StatusBadRequest, "CREATION_COMPOSE_INVALID", err.Error())
		return
	}
	if err := h.jobRepo.Create(r.Context(), job); err != nil {
		log.Printf("question-jobs: batch Create: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to persist job")
		return
	}

	// Capture the text-bearing file bytes NOW, while the multipart form is
	// still alive: the heavy PDF text extraction is moved off the request path
	// (below) so a large upload's 202 is not blocked on parsing, but the bytes
	// themselves must be read before the request ends. This read is cheap
	// (already-received data); ExtractPages is the slow part.
	captured := captureTextBearingFiles(uploaded)

	// Build the generation_requested payload synchronously so it captures the
	// exact request-scoped values (trace context, timestamps) before the
	// request ends.
	eventPayload := map[string]any{
		"job_id":           jobID,
		"atom_id":          atomID,
		"author_gcid":      authorGCID,
		"tenant_id":        tenantID,
		"question_type":    string(reqQType),
		"source_blob_uri":  firstSource.BlobURI,
		"source_mime_type": firstSource.MimeType,
		"mana_action_code": "",
		"mana_charged":     0,
		"settings_json":    settingsJSON,
		"requested_at":     now.Format(time.RFC3339Nano),
		// W2 — propagate the FE-originated W3C trace context (OTLP-everywhere).
		"traceparent": tracing.TraceparentFromContext(r.Context()),
		// ADR-195 WS7 (D7) compose model — carried for the .v2 parallel-publish.
		"operation":  question.OperationCompose,
		"intent":     string(question.IntentNewQuestion),
		"input_kind": string(question.InputSourceFiles),
	}

	// Lane 1c D9: deterministic extraction into source_material_chunks, then
	// the event publish, on a detached goroutine so the 202 returns as soon as
	// the job row exists. The ORDER is preserved: extraction persists the
	// verification corpus BEFORE the generation_requested publish that triggers
	// the crew, so a completed event never precedes its corpus. Extraction is
	// best-effort by design (failure surfaces citations as AI-reported, never
	// fails the job); the publish is fire-and-forget as before.
	h.finishBatchJobAsync(r.Context(), tenantID, jobID, captured, eventPayload)

	writeJSON(w, http.StatusAccepted, jobAcceptedResponse{
		JobID:   jobID,
		Status:  string(question.JobStatusRequested),
		PollURL: fmt.Sprintf("/api/atoms/%s/question-jobs/%s", atomID, jobID),
	})
}

// finishBatchJobAsync runs the post-persist tail of a batch job off the request
// path: extract + persist the source chunks, THEN publish generation_requested.
// It runs on a detached context (WithoutCancel so trace values survive but the
// request's cancellation does not) with its own timeout. Ordering is the point:
// the crew must not start before its verification corpus is in place.
//
// Trade-off recorded honestly: a pod death in the brief window after the 202
// and before this goroutine publishes would leave the job in `requested` with
// no crew run (the job row survives for reconciliation). The publish was
// already fire-and-forget before this change; the added exposure is that short
// window. A durable outbox for this lane is the future hardening.
func (h *QuestionJobsHandler) finishBatchJobAsync(ctx context.Context, tenantID, jobID string, captured []capturedSourceFile, eventPayload map[string]any) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), batchFinishTimeout)
	go func() {
		defer cancel()
		h.extractAndPersistChunks(detached, tenantID, jobID, captured)
		if err := h.publisher.PublishJobEvent(detached, topicQuestionGenerationRequested, eventPayload); err != nil {
			log.Printf("question-jobs: batch generation_requested publish (async) job=%s: %v", jobID, err)
		}
	}()
}

// captureTextBearingFiles reads the bytes of every text-bearing uploaded file
// while the multipart form is still available, so extraction can run after the
// request returns. Non-text-bearing files (images) are skipped, matching the
// extraction filter. A per-file open/read failure is logged and dropped, never
// fatal (extraction is best-effort).
func captureTextBearingFiles(uploaded []uploadedSourceFile) []capturedSourceFile {
	captured := make([]capturedSourceFile, 0, len(uploaded))
	for _, u := range uploaded {
		if !extraction.TextBearing(u.MimeType) {
			continue
		}
		f, err := u.header.Open()
		if err != nil {
			log.Printf("question-jobs: capture open %q: %v (skipping extraction for this file)", u.Filename, err)
			continue
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			log.Printf("question-jobs: capture read %q: %v (skipping extraction for this file)", u.Filename, err)
			continue
		}
		captured = append(captured, capturedSourceFile{
			BlobURI:  u.BlobURI,
			MimeType: u.MimeType,
			Role:     u.Role,
			Filename: u.Filename,
			Data:     data,
		})
	}
	return captured
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/question-jobs/{job_id}/regenerate-image — CHO-1819 P3
// -----------------------------------------------------------------------------

// imageRegenReq is the POST body for .../regenerate-image.
type imageRegenReq struct {
	DraftID   string `json:"draft_id"`
	Placement string `json:"placement"` // "stem" | "answer"
	Prompt    string `json:"prompt"`
	Mode      string `json:"mode,omitempty"`
}

// createImageRegenJob re-renders ONE image (stem|answer) for a single candidate
// draft of a succeeded parent batch job, without re-running the batch. It creates
// a lightweight image_regen job whose settings carry {parent_job_id, draft_id,
// placement, prompt, mode} + publishes generation_requested (job_type=image_regen).
// The in-proc subscriber then publishes ai_assist.started.v1 (job_kind=image_regen
// + regen f22); the ai_assist terminal subscriber patches the parent candidate's
// image_url/answer_image_url in place (parent stays succeeded).
//
// Validation precedes any mana charge: bad placement/prompt → 400; parent missing
// → 404; not owned → 403; parent not in review (succeeded) → 409; draft not in the
// parent's candidates → 404.
func (h *QuestionJobsHandler) createImageRegenJob(w http.ResponseWriter, r *http.Request, atomID, parentJobID string) {
	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	var req imageRegenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", err.Error())
		return
	}
	req.DraftID = strings.TrimSpace(req.DraftID)
	req.Placement = strings.TrimSpace(req.Placement)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Mode = strings.TrimSpace(req.Mode)
	if req.DraftID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "draft_id required")
		return
	}
	if req.Placement != "stem" && req.Placement != "answer" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "placement must be 'stem' or 'answer'")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "prompt required")
		return
	}

	// Load the parent job (RLS-scoped). Must exist, belong to this atom + caller,
	// and be in review (succeeded) with the target draft present.
	parent, err := h.jobRepo.Get(r.Context(), tenantID, parentJobID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "parent job not found")
			return
		}
		log.Printf("question-jobs: image_regen parent Get: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load parent job")
		return
	}
	if parent.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job does not belong to this atom")
		return
	}
	if parent.AuthorGCID != authorGCID {
		writeError(w, http.StatusForbidden, "CREATION_JOB_NOT_OWNED", "caller does not own this job")
		return
	}
	if parent.Status != question.JobStatusSucceeded {
		writeError(w, http.StatusConflict, "CREATION_JOB_NOT_REVIEWABLE",
			"parent job must be in review (succeeded) to regenerate an image")
		return
	}
	regenCtx, ok := findCandidateRegenContext(parent.CandidateQuestionsJSON, req.DraftID)
	if !ok {
		writeError(w, http.StatusNotFound, "CREATION_DRAFT_NOT_FOUND", "draft_id not found in this job's candidates")
		return
	}
	qType := regenCtx.qType
	// Bug 1 — the original grounding the question was authored from, biasing the
	// regenerate toward the same material (ImageRegenSpec f7).
	originalSrc := originalAuthoringSource(parent)

	// Mana debit — units=0 → server-side price-plan resolution (ADR-178). Refund
	// on persistence failure. Charged AFTER all validation so a bad request never
	// debits.
	jobID := uuid.Must(uuid.NewV7()).String()
	idempKey := jobID + "-mana-debit"
	charged, err := h.debitMana(w, r, ports.DeductManaReq{
		GCID:           authorGCID,
		TenantID:       tenantID,
		ActionCode:     manaActionImageRegen,
		Units:          0,
		IdempotencyKey: idempKey,
		RequestID:      jobID,
	})
	if err != nil {
		return // debitMana already wrote 402 / 503
	}

	// Settings carry the parent + target so the terminal subscriber can patch the
	// right candidate image when the orchestrator's render completes.
	settingsMap := map[string]any{
		"parent_job_id": parentJobID,
		"draft_id":      req.DraftID,
		"placement":     req.Placement,
		"prompt":        req.Prompt,
		"mode":          req.Mode,
	}
	// Bug 1 — carry the CURRENT question context (stem + model answer) + the
	// original authoring grounding so the orchestrator's ImageRegenRunner renders
	// an image of the ACTUAL question instead of free-associating (the "red apple"
	// symptom). The subscriber lifts these into ImageRegenSpec f5/f6/f7; empties are
	// omitted to keep the spec byte-identical to a pre-fix producer (proto3
	// default-elision). original_mode mirrors the requested render mode (proto f4),
	// surfaced to the composer as the previous image's mode to refine from.
	if regenCtx.currentStem != "" {
		settingsMap["current_stem"] = regenCtx.currentStem
	}
	if regenCtx.currentModelAnswer != "" {
		settingsMap["current_model_answer"] = regenCtx.currentModelAnswer
	}
	if originalSrc != "" {
		settingsMap["original_source"] = originalSrc
	}
	// ADR-210 B3 — the persisted durable gs:// OBJECT path of the placement's
	// CURRENT image, so the orchestrator edits the existing image (true
	// image-to-image, ImageRegenSpec f8) instead of regenerating from scratch.
	// Sourced from the candidate's B2 stamp (never a parsed signed URL); empty ⇒
	// text-to-image. The orchestrator FAILS LOUD if it is set but unfetchable.
	originalImageGcs := regenCtx.imageGcsURI
	if req.Placement == "answer" {
		originalImageGcs = regenCtx.answerImageGcsURI
	}
	if originalImageGcs != "" {
		settingsMap["original_image_gcs_uri"] = originalImageGcs
	}
	if req.Mode != "" {
		settingsMap["original_mode"] = req.Mode
	}
	settingsJSON := mustJSONString(settingsMap)
	now := time.Now().UTC()
	job := &question.ComposeJob{
		JobID:          jobID,
		AtomID:         atomID,
		TenantID:       tenantID,
		AuthorGCID:     authorGCID,
		Intent:         question.IntentImageRegen,
		Input:          question.Input{Prompt: req.Prompt},
		Status:         question.JobStatusRequested,
		ManaActionCode: manaActionImageRegen,
		ManaCharged:    int(charged),
		SettingsJSON:   []byte(settingsJSON),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := job.Validate(); err != nil {
		log.Printf("question-jobs: compose validate (image_regen): %v", err)
		_ = h.refundMana(r.Context(), authorGCID, tenantID, manaActionImageRegen, int(charged), idempKey+"-refund", "compose validation failed")
		writeError(w, http.StatusBadRequest, "CREATION_COMPOSE_INVALID", err.Error())
		return
	}
	if err := h.jobRepo.Create(r.Context(), job); err != nil {
		log.Printf("question-jobs: image_regen Create: %v", err)
		_ = h.refundMana(r.Context(), authorGCID, tenantID, manaActionImageRegen, int(charged), idempKey+"-refund", "job persistence failed")
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to persist job")
		return
	}

	_ = h.publisher.PublishJobEvent(r.Context(), topicQuestionGenerationRequested, map[string]any{
		"job_id":           jobID,
		"atom_id":          atomID,
		"author_gcid":      authorGCID,
		"tenant_id":        tenantID,
		"question_type":    qType, // the parent candidate's type (mcq|oe) — valid enum for the wire
		"mana_action_code": manaActionImageRegen,
		"mana_charged":     int(charged),
		"settings_json":    settingsJSON,
		"requested_at":     now.Format(time.RFC3339Nano),
		"traceparent":      tracing.TraceparentFromContext(r.Context()),
		// ADR-195 WS7 (D7) compose model — carried for the .v2 parallel-publish.
		"operation":  question.OperationCompose,
		"intent":     string(question.IntentImageRegen),
		"input_kind": string(question.InputPrompt),
	})

	writeJSON(w, http.StatusAccepted, jobAcceptedResponse{
		JobID:   jobID,
		Status:  string(question.JobStatusRequested),
		PollURL: fmt.Sprintf("/api/atoms/%s/question-jobs/%s", atomID, jobID),
	})
}

// imageRegenContext is the parent candidate's CURRENT persisted question context
// for a regenerate — lifted so the regenerate prompt reflects the real question
// (stem + model answer) instead of re-rendering against an empty spec (Bug 1).
type imageRegenContext struct {
	qType              string
	currentStem        string
	currentModelAnswer string
	// imageGcsURI / answerImageGcsURI — ADR-210: the persisted durable gs://
	// object path of each placement's CURRENT image, so the regenerate producer
	// stamps ImageRegenSpec.original_image_gcs_uri (f8) for true image-to-image.
	imageGcsURI       string
	answerImageGcsURI string
}

// findCandidateRegenContext scans a job's persisted candidate_questions_jsonb for
// the draft with the given id and returns its question type (mcq|oe, defaulting
// to mcq) + the current stem (prompt) + the current OE model answer (empty for
// MCQ) + whether it was found. The stem/model-answer feed ImageRegenSpec f5/f6 so
// the orchestrator regenerates an image of the ACTUAL question.
func findCandidateRegenContext(candJSON []byte, draftID string) (imageRegenContext, bool) {
	if len(candJSON) == 0 {
		return imageRegenContext{}, false
	}
	var drafts []candidateDraft
	if err := json.Unmarshal(candJSON, &drafts); err != nil {
		return imageRegenContext{}, false
	}
	for _, d := range drafts {
		if d.DraftID != draftID {
			continue
		}
		qt := strings.TrimSpace(d.Type)
		if qt == "" {
			qt = string(question.TypeMCQ)
		}
		rc := imageRegenContext{qType: qt, currentStem: strings.TrimSpace(d.Prompt)}
		if d.OEPayload != nil {
			rc.currentModelAnswer = strings.TrimSpace(d.OEPayload.ModelAnswer)
		}
		if d.ImageGcsURI != nil {
			rc.imageGcsURI = strings.TrimSpace(*d.ImageGcsURI)
		}
		if d.AnswerImageGcsURI != nil {
			rc.answerImageGcsURI = strings.TrimSpace(*d.AnswerImageGcsURI)
		}
		return rc, true
	}
	return imageRegenContext{}, false
}

// originalAuthoringSource assembles a concise grounding hint from the parent job's
// authoring context — its original prompt, any free-text author context/topic
// preserved on settings, and the source-material filenames — so the image
// regenerate is biased toward the same material the question was authored from
// (ImageRegenSpec f7). Raw gs:// blob URIs are intentionally excluded (not useful
// model context). Bounded (rune-safe) for envelope-size hygiene; returns "" when
// no grounding is recoverable.
func originalAuthoringSource(parent *question.ComposeJob) string {
	const maxLen = 2000
	seen := map[string]bool{}
	var parts []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		parts = append(parts, s)
	}

	add(parent.Input.Prompt)

	var settings map[string]any
	if len(parent.SettingsJSON) > 0 {
		_ = json.Unmarshal(parent.SettingsJSON, &settings)
	}
	for _, k := range []string{"prompt", "context", "topic"} {
		if v, ok := settings[k].(string); ok {
			add(v)
		}
	}
	if fn, ok := settings["filename"].(string); ok {
		add(fn)
	}
	if raw, ok := settings["source_files"].([]any); ok {
		for _, e := range raw {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if fn, ok := entry["filename"].(string); ok {
				add(fn)
			}
		}
	}

	out := strings.Join(parts, "; ")
	if r := []rune(out); len(r) > maxLen {
		out = string(r[:maxLen])
	}
	return out
}

// -----------------------------------------------------------------------------
// Lane 1c upload-plan + extraction helpers
// -----------------------------------------------------------------------------

// uploadSpec is one planned upload: the multipart part + its role/slot.
type uploadSpec struct {
	header *multipart.FileHeader
	mime   string
	role   string // sourcechunk.RoleSource | sourcechunk.RoleRubric
	slot   string // "" legacy /source · "source-{n}" · "rubric"
}

// uploadedSourceFile is one completed upload (role-tagged provenance).
type uploadedSourceFile struct {
	BlobURI  string
	MimeType string
	Role     string
	Filename string
	header   *multipart.FileHeader
}

// capturedSourceFile is a text-bearing uploaded file whose bytes were read off
// the multipart form during the request, so extraction can run on the detached
// finish goroutine after the request (and its form) has ended.
type capturedSourceFile struct {
	BlobURI  string
	MimeType string
	Role     string
	Filename string
	Data     []byte
}

const (
	// batchUploadDeadline bounds the batch upload REQUEST (client upload + blob
	// writes + job persist). It overrides the 15s server-wide timeout for this
	// one route so a large multipart upload is not severed mid-flight.
	batchUploadDeadline = 120 * time.Second

	// batchFinishTimeout bounds the detached extract+publish tail.
	batchFinishTimeout = 120 * time.Second
)

// batchPlanError carries the HTTP rejection for an invalid upload shape.
type batchPlanError struct {
	status int
	code   string
	msg    string
}

// buildBatchUploadPlan validates the multipart file parts per D7 and returns
// the ordered upload plan (sources first — source_files[0] is the f17/18
// mirror — then the rubric).
func buildBatchUploadPlan(form *multipart.Form) ([]uploadSpec, *batchPlanError) {
	if form == nil {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", "missing multipart form"}
	}
	legacy := form.File["file"]
	multi := form.File["files"]
	rubrics := form.File["rubric_file"]

	if len(legacy) > 0 && len(multi) > 0 {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"exactly one of `file` (legacy single source) or `files` (1..5 sources) may be present"}
	}
	if len(legacy) > 1 {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"legacy `file` part must appear at most once (use `files` for multi-file)"}
	}
	sources := legacy
	legacyMode := len(legacy) == 1
	if !legacyMode {
		sources = multi
	}
	if len(sources) == 0 {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"missing file part: supply `file` or `files`"}
	}
	if len(sources) > maxBatchSourceFiles {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			fmt.Sprintf("at most %d source files per batch job (got %d)", maxBatchSourceFiles, len(sources))}
	}
	if len(rubrics) > 1 {
		return nil, &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"at most one rubric_file per batch job"}
	}

	plan := make([]uploadSpec, 0, len(sources)+1)
	for i, fh := range sources {
		spec, perr := validateBatchPart(fh, sourcechunk.RoleSource)
		if perr != nil {
			return nil, perr
		}
		if !legacyMode {
			spec.slot = fmt.Sprintf("source-%d", i+1)
		}
		plan = append(plan, spec)
	}
	for _, fh := range rubrics {
		spec, perr := validateBatchPart(fh, sourcechunk.RoleRubric)
		if perr != nil {
			return nil, perr
		}
		spec.slot = "rubric"
		plan = append(plan, spec)
	}
	return plan, nil
}

// validateBatchPart applies the per-file MIME + size gates.
func validateBatchPart(fh *multipart.FileHeader, role string) (uploadSpec, *batchPlanError) {
	if fh.Size > maxBatchUploadBytes {
		return uploadSpec{}, &batchPlanError{http.StatusRequestEntityTooLarge, "CREATION_BATCH_FILE_TOO_LARGE",
			fmt.Sprintf("file %q size %d exceeds %d byte limit", fh.Filename, fh.Size, maxBatchUploadBytes)}
	}
	mime := fh.Header.Get("Content-Type")
	if !allowedBatchMIMEs[mime] {
		return uploadSpec{}, &batchPlanError{http.StatusUnsupportedMediaType, "CREATION_BATCH_UNSUPPORTED_MIME",
			fmt.Sprintf("MIME %q not in allowlist {PDF, DOCX, MD, TXT, PNG, JPEG, WebP}", mime)}
	}
	return uploadSpec{header: fh, mime: mime, role: role}, nil
}

// extractAndPersistChunks runs the D9 deterministic extraction over every
// text-bearing uploaded file and bulk-inserts the chunks. Best-effort: every
// failure logs + continues (extraction must NEVER fail the job). Images are
// skipped by design (no text layer in v1 — citations stay AI-reported).
func (h *QuestionJobsHandler) extractAndPersistChunks(ctx context.Context, tenantID, jobID string, files []capturedSourceFile) {
	if h.chunks == nil {
		return
	}
	now := time.Now().UTC()
	var all []sourcechunk.Chunk
	for _, u := range files {
		pages, paged, err := extraction.ExtractPages(u.MimeType, u.Data)
		if err != nil {
			log.Printf("question-jobs: chunk extraction %q (%s): %v (continuing)", u.Filename, u.MimeType, err)
			continue
		}
		var chunks []sourcechunk.Chunk
		if paged {
			chunks, err = sourcechunk.FromPages(sourcechunk.FromPagesParams{
				JobID: jobID, TenantID: tenantID, FileURI: u.BlobURI, FileRole: u.Role,
				Pages: pages, Now: now,
			})
		} else {
			var text strings.Builder
			for _, p := range pages {
				text.WriteString(p.Text)
				text.WriteString("\n")
			}
			chunks, err = sourcechunk.FromUnpagedText(sourcechunk.FromTextParams{
				JobID: jobID, TenantID: tenantID, FileURI: u.BlobURI, FileRole: u.Role,
				Text: text.String(), Now: now,
			})
		}
		if err != nil {
			log.Printf("question-jobs: chunk build %q: %v (continuing)", u.Filename, err)
			continue
		}
		all = append(all, chunks...)
	}
	if len(all) == 0 {
		return
	}
	if err := h.chunks.InsertChunks(ctx, all); err != nil {
		log.Printf("question-jobs: chunk persist job=%s: %v (continuing — citations will surface AI-reported)", jobID, err)
		return
	}
	log.Printf("question-jobs: persisted %d source chunks for job=%s", len(all), jobID)
}

// -----------------------------------------------------------------------------
// GET /api/atoms/{atom_id}/question-jobs/{job_id}
// -----------------------------------------------------------------------------

func (h *QuestionJobsHandler) getQuestionJob(w http.ResponseWriter, r *http.Request, atomID, jobID string) {
	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	job, err := h.jobRepo.Get(r.Context(), tenantID, jobID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job not found")
			return
		}
		log.Printf("question-jobs: Get: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load job")
		return
	}
	if job.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job does not belong to this atom")
		return
	}
	if job.AuthorGCID != authorGCID {
		writeError(w, http.StatusForbidden, "CREATION_JOB_NOT_OWNED", "caller does not own this job")
		return
	}

	resp := jobResponse{
		JobID:       job.JobID,
		AtomID:      job.AtomID,
		Status:      string(job.Status),
		ManaCharged: job.ManaCharged,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
		StartedAt:   job.StartedAt,
		CompletedAt: job.CompletedAt,
	}
	if len(job.CandidateQuestionsJSON) > 0 {
		resp.Candidates = job.CandidateQuestionsJSON
	}
	// ADR-251 D5 (CHO-2398): while the job RUNS, serve questions-so-far from
	// the chunk slots (flattened in chunk_index order) + the k-of-n progress.
	// The terminal candidate_questions_jsonb above stays authoritative once
	// the job leaves running - chunk slots never override it.
	if job.Status == question.JobStatusRunning && len(job.ChunkCandidatesJSON) > 0 {
		if flat, applied, ok := flattenChunkSlots(job.ChunkCandidatesJSON); ok && applied > 0 {
			resp.Candidates = flat
			resp.ChunkProgress = &chunkProgress{
				ChunksApplied: applied,
				ChunksTotal:   job.ChunkCountTotal,
			}
		}
	}
	// Lane 1c (D4): surface the composer's proposed test set so the review
	// composer can seed title/description/order/points. Candidates above
	// pass through verbatim — verification-stamped citations included.
	if len(job.ProposedTestSetJSON) > 0 {
		resp.ProposedTestSet = job.ProposedTestSetJSON
	}
	// CHO-1819 P2: surface the mixed-batch outcome so the FE can render a
	// shortfall banner (requested vs generated + per-type + reason).
	if len(job.GenerationSummaryJSON) > 0 {
		resp.GenerationSummary = job.GenerationSummaryJSON
	}
	// CHO-1826 Gap #4: surface the per-step pipeline trace so the canvas can
	// render the transparency card (IMDA D2). Empty for refused / failed jobs.
	if len(job.PipelineTraceJSON) > 0 {
		resp.PipelineTrace = job.PipelineTraceJSON
	}
	writeJSON(w, http.StatusOK, resp)
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/question-jobs/{job_id}/accept
// -----------------------------------------------------------------------------

func (h *QuestionJobsHandler) acceptQuestionJob(w http.ResponseWriter, r *http.Request, atomID, jobID string) {
	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	var body acceptJobReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", err.Error())
		return
	}

	job, err := h.jobRepo.Get(r.Context(), tenantID, jobID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load job")
		return
	}
	if job.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job does not belong to this atom")
		return
	}
	if job.AuthorGCID != authorGCID {
		writeError(w, http.StatusForbidden, "CREATION_JOB_NOT_OWNED", "caller does not own this job")
		return
	}
	if job.Status != question.JobStatusSucceeded {
		writeError(w, http.StatusConflict, "CREATION_JOB_NOT_READY",
			fmt.Sprintf("job must be in 'succeeded' status to accept (current: %s)", job.Status))
		return
	}

	// Lane 1c W3 — validate the optional test_set block BEFORE any side
	// effect (mana debit / persistence) so a rejected request leaves the
	// job untouched. Absent block ⇒ everything below is bit-identical to
	// the pre-1c path.
	if body.TestSet != nil {
		if perr := h.validateAcceptTestSet(job, &body); perr != nil {
			writeError(w, perr.status, perr.code, perr.msg)
			return
		}
	}

	// Empty accepted_candidates => cancel the job.
	if len(body.AcceptedCandidates) == 0 {
		if err := h.jobRepo.UpdateStatus(r.Context(), tenantID, jobID, question.JobStatusCancelled, nil, ""); err != nil {
			writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to cancel")
			return
		}
		writeJSON(w, http.StatusOK, acceptResponse{Persisted: []*question.Question{}, ManaDebited: 0})
		return
	}

	// Parse candidate drafts from job.CandidateQuestionsJSON.
	drafts, err := parseCandidateDrafts(job.CandidateQuestionsJSON)
	if err != nil {
		log.Printf("question-jobs: parse drafts: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to parse candidates")
		return
	}
	draftByID := map[string]candidateDraft{}
	for _, d := range drafts {
		draftByID[d.DraftID] = d
	}

	// U3b (CHO-1826) — per-accepted-AI-question accept-time pricing. Each
	// accepted AI question is charged ONCE: question_authoring_generate (base
	// B=10) for a pure-text question, question_authoring_generate_image (2B=20)
	// when it carries any image (a stem and/or answer image is still 20).
	// Generation (job creation) is no longer charged — you pay for what you
	// keep. Charged BEFORE persistence so an insufficient balance 402s cleanly
	// without minting atoms; the defer below refunds every debit if persistence
	// then fails on ANY path. Units==0 + item_count ⇒ the ADR-178 price-plan
	// layer resolves the per-unit price server-side (tenant-overridable).
	var textCount, imageCount int
	for _, ac := range body.AcceptedCandidates {
		if ac.DraftID == "" {
			continue // inline manual candidate (U3c) — free, not AI-generated
		}
		d, ok := draftByID[ac.DraftID]
		if !ok {
			writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
				fmt.Sprintf("draft_id %q not found in job", ac.DraftID))
			return
		}
		if candidateHasImage(ac, d) {
			imageCount++
		} else {
			textCount++
		}
	}

	type acceptDebit struct {
		code   string
		amount int
		idem   string
	}
	var debits []acceptDebit
	committed := false
	// Refund every accept-time debit if the handler returns before the
	// persistence loop has fully committed (insufficient mana on a later
	// charge step, an invalid candidate, a repo failure, a D2 conflict, ...).
	defer func() {
		if committed {
			return
		}
		for _, db := range debits {
			_ = h.refundMana(r.Context(), authorGCID, tenantID, db.code, db.amount, db.idem+"-refund", "accept failed — mana refunded")
		}
	}()
	totalAcceptMana := 0
	chargeAccept := func(code string, n int) bool {
		if n == 0 {
			return true
		}
		idem := jobID + "-accept-" + code
		charged, derr := h.debitMana(w, r, ports.DeductManaReq{
			GCID:           authorGCID,
			TenantID:       tenantID,
			ActionCode:     code,
			Units:          0,
			Context:        map[string]string{"item_count": strconv.Itoa(n)},
			IdempotencyKey: idem,
			RequestID:      jobID,
		})
		if derr != nil {
			return false // h.debitMana wrote the 402; the defer refunds prior steps.
		}
		debits = append(debits, acceptDebit{code: code, amount: int(charged), idem: idem})
		totalAcceptMana += int(charged)
		return true
	}
	if !chargeAccept(manaActionGenerate, textCount) {
		return
	}
	if !chargeAccept(manaActionGenerateImage, imageCount) {
		return
	}

	// Per-candidate persistence under the unified 1q=1atom model (CHO-1826
	// U3a). The legacy single-on-parent special case is retired: EVERY
	// accepted question becomes its own LearningAtom (the existing D2 law —
	// 1 atom = 1 non-deleted question), with the FIRST accepted candidate
	// reusing the FE-created "route" atom (atomID in the path) so a count=1
	// author never strands an empty draft container. Subsequent candidates
	// mint fresh atoms cloning the route atom's metadata. A route atom that
	// already carries a non-deleted question (editing an existing atom) is
	// NEVER reused — all candidates mint fresh atoms.
	//
	// Persistence is per-candidate (atom then question), not a single
	// cross-aggregate transaction — cross-DB transactions are forbidden per
	// ddd-enforcement, and the atom + question repositories may each own
	// their own tx scope in production. If question.Save fails for a
	// candidate after its atom was persisted, the atom remains a fresh DRAFT
	// and the handler surfaces 500 so the caller knows the accept is only
	// partially persisted.
	routeAtom, err := h.atomRepo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		log.Printf("question-jobs: route atom lookup for accept: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to load atom for accept")
		return
	}
	// routeAtomFree — the route atom carries no non-deleted question, so the
	// first accepted candidate may reuse it (else every candidate mints a
	// fresh atom and the route atom keeps its existing question).
	routeAtomFree := false
	if _, _, gerr := h.questionRepo.GetByAtomID(r.Context(), tenantID, atomID); errors.Is(gerr, question.ErrNotFound) {
		routeAtomFree = true
	}
	routeReused := false

	persisted := make([]*question.Question, 0, len(body.AcceptedCandidates))
	// Lane 1c W3 — per-accepted-candidate provenance for the test-set event
	// (draft_id → new atom + question ids, in SUBMISSION order).
	acceptedItems := make([]acceptedItemInfo, 0, len(body.AcceptedCandidates))
	for _, ac := range body.AcceptedCandidates {
		// U3c — an inline manual candidate (no draft_id) carries its full body
		// in the accept request; synthesize a draft from it so the merge logic
		// below is uniform. Manual candidates are free + source_type=manual.
		manual := ac.DraftID == ""
		srcType := atom.SourceAIAssist
		var d candidateDraft
		if manual {
			srcType = atom.SourceManual
			d = candidateDraft{
				Type:           strings.TrimSpace(ac.Type),
				MCQPayload:     mcqOverrideToDomain(ac.MCQPayloadOverride),
				ImageURL:       ac.ImageURL,
				AnswerImageURL: ac.AnswerImageURL,
			}
			if ac.OEPayloadOverride != nil {
				d.OEPayload = &question.OEPayload{ModelAnswer: ac.OEPayloadOverride.ModelAnswer}
			}
		} else {
			var ok bool
			d, ok = draftByID[ac.DraftID]
			if !ok {
				writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
					fmt.Sprintf("draft_id %q not found in job", ac.DraftID))
				return
			}
		}
		// Editability invariant — merge overrides over the (AI or manual) body.
		prompt := d.Prompt
		if ac.PromptOverride != nil {
			prompt = *ac.PromptOverride
		}
		var mcq *question.MCQPayload
		var oe *question.OEPayload
		qType := question.QuestionType(d.Type)
		switch qType {
		case question.TypeMCQ:
			mcq = d.MCQPayload
			if ac.MCQPayloadOverride != nil {
				mcq = mcqOverrideToDomain(ac.MCQPayloadOverride)
			}
			// W8 image-gen: the text-only override drops the AI-generated
			// illustration URLs. Re-attach them — prefer the explicit candidate
			// value (FE-sent), else preserve the stored AI draft payload's.
			// The author never edits images directly, so they must survive the
			// commit verbatim into the persisted mcq_payload.
			if mcq != nil {
				// Prefer the FE-sent value; else fall back to the draft's canonical
				// top-level image URL (CHO-1825). Maps onto the published question's
				// nested mcq_payload image field.
				if ac.ImageURL != nil {
					mcq.ImageURL = ac.ImageURL
				} else if d.ImageURL != nil {
					mcq.ImageURL = d.ImageURL
				}
				if ac.AnswerImageURL != nil {
					mcq.AnswerImageURL = ac.AnswerImageURL
				} else if d.AnswerImageURL != nil {
					mcq.AnswerImageURL = d.AnswerImageURL
				}
			}
		case question.TypeOpenEnded:
			oe = d.OEPayload
			if ac.OEPayloadOverride != nil {
				// The OE override carries ONLY the editable model answer
				// (acceptOEPayload). Clone the AI draft's payload and overwrite
				// just ModelAnswer so the rubric + grader_tier + min/max survive
				// the edit — mirrors the MCQ image re-attach below. Rebuilding
				// from scratch here silently dropped the rubric (§4.2b).
				if d.OEPayload != nil {
					cloned := *d.OEPayload
					cloned.ModelAnswer = ac.OEPayloadOverride.ModelAnswer
					oe = &cloned
				} else {
					// No AI draft payload to preserve (the candidate had no
					// oe_payload) — nothing to drop; build from the override.
					oe = &question.OEPayload{ModelAnswer: ac.OEPayloadOverride.ModelAnswer}
				}
			}
			// W8 image-gen: re-attach the generated illustration URLs (see MCQ).
			if oe != nil {
				// Prefer the FE-sent value; else the draft's canonical top-level
				// image URL (CHO-1825). Maps onto the published question's nested
				// oe_payload image field.
				if ac.ImageURL != nil {
					oe.ImageURL = ac.ImageURL
				} else if d.ImageURL != nil {
					oe.ImageURL = d.ImageURL
				}
				if ac.AnswerImageURL != nil {
					oe.AnswerImageURL = ac.AnswerImageURL
				} else if d.AnswerImageURL != nil {
					oe.AnswerImageURL = d.AnswerImageURL
				}
			}
		}

		// Resolve the target atom under the unified model: the FIRST accepted
		// candidate reuses the route atom when it is free; every other
		// candidate (and all candidates when the route atom is occupied)
		// mints a fresh atom cloning the route atom's metadata.
		var targetAtom *atom.LearningAtom
		if routeAtomFree && !routeReused {
			if err := applyAtomMetaOverride(routeAtom, ac.AtomMetaOverride); err != nil {
				writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
					fmt.Sprintf("atom_meta_override invalid: %v", err))
				return
			}
			if err := h.atomRepo.Save(r.Context(), routeAtom); err != nil {
				log.Printf("question-jobs: route atom re-save: %v", err)
				writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL",
					"failed to persist route atom metadata")
				return
			}
			targetAtom = routeAtom
			routeReused = true
		} else {
			newAtom, err := buildBatchCandidateAtom(routeAtom, ac.AtomMetaOverride, authorGCID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
					fmt.Sprintf("atom_meta_override invalid: %v", err))
				return
			}
			if err := h.atomRepo.Save(r.Context(), newAtom); err != nil {
				log.Printf("question-jobs: new-atom Save: %v", err)
				writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL",
					"failed to persist new atom for candidate")
				return
			}
			targetAtom = newAtom
		}
		targetAtomID := targetAtom.AtomID

		// ADR-210 — durably re-home the candidate's transient W8 images into
		// chora-atom-media BEFORE persisting, so the accepted question's first
		// revision carries durable gs:// refs that survive the 7-day transient
		// TTL. Done pre-persist (not append-after) so a copy failure aborts this
		// candidate before any atom/question row is written — no orphaned draft
		// that would force a duplicate on retry. The defer above refunds the
		// accept-time mana. Fail-loud.
		if h.reHomer != nil {
			if err := h.reHomer.ReHomePayloadImages(r.Context(), tenantID, targetAtomID, qType, mcq, oe); err != nil {
				log.Printf("question-jobs: media re-home (accept): %v", err)
				writeError(w, http.StatusInternalServerError, "CREATION_ATOM_MEDIA_REHOME_FAILED",
					"failed to durably re-home images")
				return
			}
		}

		// Build the Question + first revision (source=ai_assist) on the
		// resolved target atom.
		q, err := question.New(question.NewParams{
			TenantID:   tenantID,
			AtomID:     targetAtomID,
			AuthorGcid: authorGCID,
			Type:       qType,
			Prompt:     prompt,
			SourceType: srcType,
			MCQ:        mcq,
			OE:         oe,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", err.Error())
			return
		}
		rev, err := question.NewRevision(q, q.Prompt, mcq, oe, authorGCID, srcType)
		if err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_JOB_INVALID_BODY", err.Error())
			return
		}
		q.LatestRevisionID = rev.RevisionID

		if err := h.questionRepo.Save(r.Context(), q, rev); err != nil {
			if errors.Is(err, question.ErrAtomHasQuestion) {
				writeError(w, http.StatusConflict, "CREATION_QUESTION_DUPLICATE",
					"atom already has a non-deleted question (D2)")
				return
			}
			log.Printf("question-jobs: Save: %v", err)
			writeError(w, http.StatusInternalServerError, "CREATION_JOB_INTERNAL", "failed to persist question")
			return
		}
		persisted = append(persisted, q)
		acceptedItems = append(acceptedItems, acceptedItemInfo{
			DraftID:        ac.DraftID,
			QuestionAtomID: targetAtomID,
			QuestionID:     q.QuestionID,
			QuestionType:   string(qType),
		})

		// Emit chora.creation.atom.created.v1 (upsert) for EVERY accepted atom
		// — incl. the reused route atom — carrying the MCQ grading ground-truth
		// (CHO-1627) so chora-consumption can score submissions server-side.
		// atom.created precedes question.authored in the aggregate ordering.
		atomEvent := map[string]any{
			"atom_id":       targetAtom.AtomID,
			"tenant_id":     targetAtom.TenantID,
			"author_gcid":   targetAtom.Gcid,
			"title":         targetAtom.Title,
			"tags":          targetAtom.Tags,
			"difficulty":    targetAtom.Difficulty,
			"atom_type":     string(targetAtom.QuestionType),
			"status":        string(targetAtom.Status),
			"source_job_id": jobID,
			"created_at":    targetAtom.CreatedAt.Format(time.RFC3339Nano),
		}
		// parent_atom_id links a minted atom back to the route atom; omitted
		// when the route atom is itself the target (it has no parent).
		if targetAtom.AtomID != atomID {
			atomEvent["parent_atom_id"] = atomID
		}
		// Only MCQ candidates carry a deterministic answer key — OE / nil
		// payloads leave the fields unset (proto3 default, elided on the wire).
		if qType == question.TypeMCQ && mcq != nil {
			// CHO-2272 — ship the DERIVED served id (what the learner is graded
			// on), not the stored id. Fail loud: an underivable payload ships no
			// key (non-gradable) rather than a wrong one that mis-grades.
			if cid, cerr := mcq.ServedCorrectOptionID(); cerr != nil {
				log.Printf("question_jobs: cannot derive served correct id for atom=%s (non-gradable): %v", targetAtom.AtomID, cerr)
			} else if cid != "" {
				atomEvent["correct_option_id"] = cid
			}
			atomEvent["answer_count"] = int32(mcq.AnswerCount())
		}
		_ = h.publisher.PublishJobEvent(r.Context(), topicAtomCreated, atomEvent)

		// Emit chora.creation.question.authored.v1 per persisted question.
		_ = h.publisher.PublishJobEvent(r.Context(), topicQuestionAuthored, map[string]any{
			"question_id":     q.QuestionID,
			"atom_id":         targetAtomID,
			"tenant_id":       tenantID,
			"author_gcid":     authorGCID,
			"question_type":   string(qType),
			"source_type":     string(srcType),
			"revision_number": rev.RevisionNumber,
			"revision_id":     rev.RevisionID,
			"source_job_id":   jobID,
			"authored_at":     q.CreatedAt.Format(time.RFC3339Nano),
		})
	}

	// All candidates persisted — mark committed so the defer does NOT refund
	// the accept-time charges (the transition + events below are best-effort).
	committed = true

	// Transition: full vs partial accept.
	// Partial = fewer AI drafts accepted than were generated. Inline manual
	// additions (no draft_id) are orthogonal and never make an accept partial
	// (textCount+imageCount counts only the accepted draft-backed candidates).
	terminal := question.JobStatusAccepted
	if (textCount + imageCount) < len(drafts) {
		terminal = question.JobStatusPartiallyAccepted
	}
	if body.TestSet == nil {
		// Pre-1c path — bit-identical (unconditional best-effort update).
		if err := h.jobRepo.UpdateStatus(r.Context(), tenantID, jobID, terminal, nil, ""); err != nil {
			log.Printf("question-jobs: UpdateStatus to %s: %v", terminal, err)
			// non-fatal — the questions are persisted; surface a 200 with notice via log.
		}
	} else {
		// Lane 1c W3 — exactly-once publish guard: only the accept that WINS
		// the succeeded→{accepted,partially_accepted} CAS publishes
		// question_batch.accepted.v1. A replay / lost race persists nothing
		// new here and publishes nothing (delivery additionally keys
		// test_sets.source_job_id UNIQUE).
		won, terr := h.jobRepo.TransitionFromSucceeded(r.Context(), tenantID, jobID, terminal)
		if terr != nil {
			log.Printf("question-jobs: TransitionFromSucceeded to %s: %v", terminal, terr)
			// Same non-fatal posture as the legacy path: atoms are persisted;
			// the author can retry the transition. NO publish without the CAS.
		}
		if won {
			h.publishQuestionBatchAccepted(r.Context(), job, body.TestSet, acceptedItems)
		} else if terr == nil {
			log.Printf("question-jobs: job %s already left succeeded — skipping question_batch.accepted publish (exactly-once)", jobID)
		}
	}

	// Emit the generation_completed.v2 event describing acceptance. The loaded job
	// now carries the reconstructed compose VOs (WS9 step 1), so ComposeModelForJob
	// takes the explicit-VO branch (intent/input_kind), not the job_type fallback.
	acceptIntent, acceptInputKind := question.ComposeModelForJob(job)
	_ = h.publisher.PublishJobEvent(r.Context(), topicQuestionGenerationCompleted, map[string]any{
		"job_id":          jobID,
		"atom_id":         atomID,
		"author_gcid":     authorGCID,
		"status":          string(terminal),
		"candidate_count": len(drafts),
		"completed_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"operation":       question.OperationCompose,
		"intent":          string(acceptIntent),
		"input_kind":      string(acceptInputKind),
	})

	// mana_debited surfaced to the caller is the per-accepted-question accept
	// charge (U3b). job.ManaCharged is 0 for jobs created after U3b removed the
	// upfront generation charges; it is kept in the sum so any in-flight job
	// that carried an upfront charge still reports its true total.
	writeJSON(w, http.StatusOK, acceptResponse{
		Persisted:   persisted,
		ManaDebited: job.ManaCharged + totalAcceptMana,
	})
}

// -----------------------------------------------------------------------------
// Lane 1c W3 — test-set accept helpers
// -----------------------------------------------------------------------------

// acceptedItemInfo links one accepted candidate to the artefacts the
// persistence loop minted for it.
type acceptedItemInfo struct {
	DraftID        string
	QuestionAtomID string
	QuestionID     string
	QuestionType   string
}

// validateAcceptTestSet gates the optional test_set block BEFORE any side
// effect. Returns nil when valid.
func (h *QuestionJobsHandler) validateAcceptTestSet(job *question.ComposeJob, body *acceptJobReq) *batchPlanError {
	ts := body.TestSet
	// ADR-195 WS5 (D4) — test_set is INPUT-AGNOSTIC. The legacy
	// `job_type == batch_source_material` gate (the :1837 false coupling) is
	// retired: a test set is valid for ANY compose job with accepted candidates.
	// The composer proposal is empty for non-source jobs, so resolveTestSetItems
	// falls back to the author-curated order — no batch dependency remains.
	if len(body.AcceptedCandidates) == 0 {
		return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"test_set requires at least one accepted candidate (cannot compose an empty test set)"}
	}
	title := strings.TrimSpace(ts.Title)
	if title == "" {
		return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"test_set.title is required"}
	}
	if len(title) > 256 {
		return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"test_set.title exceeds 256 characters"}
	}
	if len(ts.Description) > 2048 {
		return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
			"test_set.description exceeds 2048 characters"}
	}
	acceptedIDs := make(map[string]bool, len(body.AcceptedCandidates))
	for _, ac := range body.AcceptedCandidates {
		acceptedIDs[ac.DraftID] = true
	}
	seen := map[string]bool{}
	for i, item := range ts.Items {
		if !acceptedIDs[item.DraftID] {
			return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
				fmt.Sprintf("test_set.items[%d].draft_id %q does not reference an accepted candidate", i, item.DraftID)}
		}
		if seen[item.DraftID] {
			return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
				fmt.Sprintf("test_set.items[%d].draft_id %q appears more than once", i, item.DraftID)}
		}
		seen[item.DraftID] = true
		if item.Points != nil && (*item.Points < 1 || *item.Points > 100) {
			return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
				fmt.Sprintf("test_set.items[%d].points %d out of range [1..100]", i, *item.Points)}
		}
		if item.DisplayOrder != nil && *item.DisplayOrder < 1 {
			return &batchPlanError{http.StatusBadRequest, "CREATION_JOB_INVALID_BODY",
				fmt.Sprintf("test_set.items[%d].display_order %d must be >= 1", i, *item.DisplayOrder)}
		}
	}
	// Fail loud BEFORE persistence when the publish path is not wired —
	// silently dropping the author's composition intent is worse than 503.
	if h.batchAcceptedPub == nil {
		return &batchPlanError{http.StatusServiceUnavailable, "CREATION_TESTSET_PUBLISH_NOT_WIRED",
			"question_batch.accepted publisher is not wired (outbox store unavailable)"}
	}
	return nil
}

// proposedTestSetDoc is the decode target for the composer proposal
// (OpenAPI ProposedTestSet) persisted on the job row.
type proposedTestSetDoc struct {
	Order  []string       `json:"order"`
	Points map[string]int `json:"points"`
}

// resolveTestSetItems applies the D5 resolution chain over the accepted
// candidates (in submission order):
//
//	points        : item override → proposal.points[draft_id] (sane 1..100)
//	                → uniform default 10
//	display_order : item override → proposal.order position → submission
//	                order; then re-packed contiguous 1-based.
//
// Override ranks interleave AHEAD of equal proposal ranks (rank 10n vs
// 10k+5) so an author pin always claims its slot; unplaced candidates sort
// last in submission order (stable sort).
func resolveTestSetItems(ts *acceptTestSet, accepted []acceptedItemInfo, proposalJSON []byte) []ports.QuestionBatchTestSetItem {
	var proposal proposedTestSetDoc
	if len(proposalJSON) > 0 {
		_ = json.Unmarshal(proposalJSON, &proposal) // best-effort — absent/corrupt ⇒ defaults
	}
	proposalRank := make(map[string]int, len(proposal.Order))
	for i, id := range proposal.Order {
		proposalRank[id] = i + 1
	}
	overrides := make(map[string]acceptTestSetItem, len(ts.Items))
	for _, item := range ts.Items {
		overrides[item.DraftID] = item
	}

	type ranked struct {
		item ports.QuestionBatchTestSetItem
		rank int
	}
	const unplacedBase = 1 << 20
	rankedItems := make([]ranked, 0, len(accepted))
	for i, info := range accepted {
		points := 10
		if p, ok := proposal.Points[info.DraftID]; ok && p >= 1 && p <= 100 {
			points = p
		}
		rank := unplacedBase + i
		if pr, ok := proposalRank[info.DraftID]; ok {
			rank = pr*10 + 5
		}
		if ov, ok := overrides[info.DraftID]; ok {
			if ov.Points != nil {
				points = *ov.Points
			}
			if ov.DisplayOrder != nil {
				rank = *ov.DisplayOrder * 10
			}
		}
		rankedItems = append(rankedItems, ranked{
			item: ports.QuestionBatchTestSetItem{
				QuestionAtomID: info.QuestionAtomID,
				QuestionID:     info.QuestionID,
				QuestionType:   info.QuestionType,
				Points:         points,
			},
			rank: rank,
		})
	}
	sort.SliceStable(rankedItems, func(a, b int) bool { return rankedItems[a].rank < rankedItems[b].rank })

	out := make([]ports.QuestionBatchTestSetItem, 0, len(rankedItems))
	for i, ri := range rankedItems {
		ri.item.DisplayOrder = i + 1 // contiguous 1-based
		out = append(out, ri.item)
	}
	return out
}

// publishQuestionBatchAccepted assembles + outbox-publishes the D10 event.
// Best-effort AFTER the CAS win: a publish failure logs loud — the outbox
// insert is the durability boundary; failing the HTTP response here would
// not un-persist the atoms.
func (h *QuestionJobsHandler) publishQuestionBatchAccepted(
	ctx context.Context,
	job *question.ComposeJob,
	ts *acceptTestSet,
	accepted []acceptedItemInfo,
) {
	items := resolveTestSetItems(ts, accepted, job.ProposedTestSetJSON)
	evt := ports.QuestionBatchAcceptedEvent{
		JobID:              job.JobID,
		HostAtomID:         job.AtomID,
		TenantID:           job.TenantID,
		AuthorGCID:         job.AuthorGCID,
		TestSetTitle:       strings.TrimSpace(ts.Title),
		TestSetDescription: ts.Description,
		Items:              items,
		SourceFiles:        jobSourceFiles(job),
		AcceptedAt:         time.Now().UTC(),
		Traceparent:        tracing.TraceparentFromContext(ctx),
	}
	if err := h.batchAcceptedPub.PublishQuestionBatchAccepted(ctx, evt); err != nil {
		log.Printf("question-jobs: question_batch.accepted publish job=%s FAILED: %v (atoms persisted; delivery assembly will not run)", job.JobID, err)
		return
	}
	log.Printf("question-jobs: question_batch.accepted enqueued job=%s items=%d title=%q", job.JobID, len(items), evt.TestSetTitle)
}

// jobSourceFiles extracts the role-tagged grounding-file provenance from the
// job settings (Lane 1c multipart handler). Pre-1c jobs fall back to the
// single f17/18 mirror columns.
func jobSourceFiles(job *question.ComposeJob) []ports.QuestionBatchSourceFile {
	if len(job.SettingsJSON) > 0 {
		var settings struct {
			SourceFiles []struct {
				BlobURI  string `json:"blob_uri"`
				MimeType string `json:"mime_type"`
				Role     string `json:"role"`
			} `json:"source_files"`
		}
		if err := json.Unmarshal(job.SettingsJSON, &settings); err == nil && len(settings.SourceFiles) > 0 {
			out := make([]ports.QuestionBatchSourceFile, 0, len(settings.SourceFiles))
			for _, f := range settings.SourceFiles {
				if f.BlobURI == "" {
					continue
				}
				role := f.Role
				if role == "" {
					role = sourcechunk.RoleSource
				}
				out = append(out, ports.QuestionBatchSourceFile{
					BlobURI: f.BlobURI, MimeType: f.MimeType, Role: role,
				})
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if job.SourceBlobURI != "" {
		return []ports.QuestionBatchSourceFile{{
			BlobURI: job.SourceBlobURI, MimeType: job.SourceMimeType, Role: sourcechunk.RoleSource,
		}}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// debitMana wraps the ManaLedger.Deduct call and renders the 402 envelope
// on InsufficientManaError. Returns the charged units + nil on success;
// returns 0 + a non-nil error after writing the response so the caller exits.
//
// FU-4(b): the price is resolved server-side (req.Units==0 → ADR-178 price-plan
// layer, tenant-aware + per_item × item_count). The returned charged amount is
// what the caller stamps on the job + event + refunds on failure — creation no
// longer hard-codes the price.
func (h *QuestionJobsHandler) debitMana(w http.ResponseWriter, r *http.Request, req ports.DeductManaReq) (int64, error) {
	resp, err := h.mana.Deduct(r.Context(), req)
	if err == nil {
		return resp.ChargedUnits, nil
	}
	var iErr *clients.InsufficientManaError
	if errors.As(err, &iErr) {
		writeJSON(w, http.StatusPaymentRequired, insufficientManaResponse{
			Error: insufficientManaError{
				Code:          "INSUFFICIENT_MANA",
				Message:       "Insufficient mana balance; top up to continue",
				CorrelationID: req.RequestID,
				Upsell: insufficientManaUpsellEnv{
					RequiredUnits:         iErr.RequiredUnits,
					CurrentBalanceUnits:   iErr.CurrentBalanceUnits,
					RecommendedPlanCode:   iErr.RecommendedPlanCode,
					RecommendedTopupUnits: iErr.RecommendedTopupUnits,
					StripeCheckoutURL:     iErr.StripeCheckoutURL,
				},
			},
		})
		return 0, err
	}
	log.Printf("question-jobs: mana debit: %v", err)
	writeError(w, http.StatusBadGateway, "CREATION_MANA_RPC_ERROR", err.Error())
	return 0, err
}

func (h *QuestionJobsHandler) refundMana(ctx context.Context, gcid, tenantID, actionCode string, units int, idemKey, reason string) error {
	_, err := h.mana.Refund(ctx, ports.RefundManaReq{
		GCID:           gcid,
		TenantID:       tenantID,
		ActionCode:     actionCode,
		Units:          units,
		IdempotencyKey: idemKey,
		Reason:         reason,
	})
	return err
}

// decodeOptionalJSON decodes the request body if non-empty; tolerates an
// empty body as a no-op.
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	// Try to peek; on empty body succeed.
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, fmt.Errorf("EOF")) {
			return nil
		}
		// EOF (json package returns io.EOF directly).
		if err.Error() == "EOF" {
			return nil
		}
		return err
	}
	return nil
}

func mustJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseCandidateDrafts(raw []byte) ([]candidateDraft, error) {
	if len(raw) == 0 {
		return []candidateDraft{}, nil
	}
	var out []candidateDraft
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// buildBatchCandidateAtom mints a NEW LearningAtom for one candidate of a
// batch_source_material accept. Per Debt #28 the per-atom metadata source
// matrix is:
//
//	atom_meta_override.title       provided → use; else clone parent.Title
//	atom_meta_override.tags        provided → use; else clone parent.Tags
//	atom_meta_override.difficulty  provided → use; else clone parent.Difficulty
//
// The Body is empty by design — the Question (created in the same accept
// loop) is the atom's payload; the atom carries title/tags/difficulty only.
// AtomType is inherited from the parent (typically TypeMCQ).
//
// The new atom starts in DRAFT status (Status set to draft) — authors can
// open each new atom in the editor + publish independently.
func buildBatchCandidateAtom(parent *atom.LearningAtom, override map[string]any, authorGCID string) (*atom.LearningAtom, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent atom is required for batch candidate atom")
	}
	title := strings.TrimSpace(parent.Title)
	tags := append([]string(nil), parent.Tags...)
	difficulty := parent.Difficulty

	if override != nil {
		if v, ok := override["title"]; ok && v != nil {
			if s, isStr := v.(string); isStr {
				if t := strings.TrimSpace(s); t != "" {
					title = t
				}
			}
		}
		if v, ok := override["tags"]; ok && v != nil {
			if rawTags, isSlice := v.([]any); isSlice {
				out := make([]string, 0, len(rawTags))
				for _, t := range rawTags {
					if ts, isStr := t.(string); isStr {
						out = append(out, ts)
					}
				}
				tags = out
			}
		}
		if v, ok := override["difficulty"]; ok && v != nil {
			switch d := v.(type) {
			case float64:
				difficulty = int(d)
			case int:
				difficulty = d
			}
			if difficulty < 0 || difficulty > 5 {
				return nil, fmt.Errorf("difficulty %d out of range [0..5]", difficulty)
			}
		}
	}

	if title == "" {
		return nil, fmt.Errorf("atom title is required (override empty AND parent has no title)")
	}

	newAtom, err := atom.New(atom.NewParams{
		TenantID: parent.TenantID,
		Gcid:     authorGCID, // authoring user owns the new atom (NOT the parent's gcid)
		Title:    title,
		Body:     "", // body lives in the Question; atom carries metadata only
		Tags:     tags,
		Mode:     parent.Mode,
	})
	if err != nil {
		return nil, fmt.Errorf("new atom: %w", err)
	}
	if difficulty > 0 {
		newAtom.Difficulty = difficulty
	}
	if parent.QuestionType != "" {
		newAtom.QuestionType = parent.QuestionType
	}
	return newAtom, nil
}

// candidateHasImage reports whether an accepted candidate carries any image
// (a stem and/or model-answer illustration), preferring the FE-sent override
// values and falling back to the stored AI draft's canonical top-level URLs
// (CHO-1825). Drives the 2× generate_image price tier (U3b).
func candidateHasImage(ac acceptCandidate, d candidateDraft) bool {
	nonEmpty := func(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }
	return nonEmpty(ac.ImageURL) || nonEmpty(ac.AnswerImageURL) ||
		nonEmpty(d.ImageURL) || nonEmpty(d.AnswerImageURL)
}

// applyAtomMetaOverride mutates an existing atom's metadata from the
// accept-time atom_meta_override map (title / tags / difficulty). Absent or
// empty fields leave the current value untouched. Mirrors
// buildBatchCandidateAtom's override parsing so the reuse-route-atom and
// mint-new-atom paths apply identical semantics.
func applyAtomMetaOverride(a *atom.LearningAtom, override map[string]any) error {
	if a == nil || override == nil {
		return nil
	}
	if v, ok := override["title"]; ok && v != nil {
		if s, isStr := v.(string); isStr {
			if t := strings.TrimSpace(s); t != "" {
				a.Title = t
			}
		}
	}
	if v, ok := override["tags"]; ok && v != nil {
		if rawTags, isSlice := v.([]any); isSlice {
			out := make([]string, 0, len(rawTags))
			for _, t := range rawTags {
				if ts, isStr := t.(string); isStr {
					out = append(out, ts)
				}
			}
			a.Tags = out
		}
	}
	if v, ok := override["difficulty"]; ok && v != nil {
		d := a.Difficulty
		switch dv := v.(type) {
		case float64:
			d = int(dv)
		case int:
			d = dv
		}
		if d < 0 || d > 5 {
			return fmt.Errorf("difficulty %d out of range [0..5]", d)
		}
		a.Difficulty = d
	}
	return nil
}

func mcqOverrideToDomain(in *acceptMCQPayload) *question.MCQPayload {
	if in == nil {
		return nil
	}
	out := &question.MCQPayload{
		Options: make([]question.MCQOption, len(in.Options)),
	}
	for i, o := range in.Options {
		oid := o.OptionID
		if oid == "" {
			oid = mintOptionID()
		}
		out.Options[i] = question.MCQOption{
			OptionID:  oid,
			Label:     o.Label,
			IsCorrect: o.IsCorrect,
			Explainer: o.Explainer,
		}
	}
	return out
}
