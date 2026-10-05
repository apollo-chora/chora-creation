// QuestionJobRepository — port for the question_generation_jobs table (migration 0008).
//
// Separate from QuestionRepository because (a) the job lifecycle is
// transactionally independent from question creation (the job runs async, the
// question is only created at accept-time) and (b) the job row is mutated in
// place (status field — see the migration UPDATE-allowed comment), whereas
// QuestionRevision rows are append-only.
package ports

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// QuestionJobRepository is the hexagonal port for async generation jobs.
type QuestionJobRepository interface {
	// Create persists a freshly-constructed ComposeJob in
	// JobStatusRequested. Idempotent by ManaIdempotencyKey (the migration
	// declares a UNIQUE constraint there) so retries don't double-create.
	Create(ctx context.Context, job *question.ComposeJob) error

	// Get returns the job row for (tenantID, jobID); returns
	// question.ErrNotFound when missing or cross-tenant.
	Get(ctx context.Context, tenantID, jobID string) (*question.ComposeJob, error)

	// UpdateStatus is the worker-callable transition. The worker computes the
	// target Status via the ComposeJob.Transition state machine,
	// then persists the new status + completed-candidates JSON + (on failure)
	// the error message in a single UPDATE.
	//
	// tenantID is REQUIRED — the caller always holds the owning tenant (from the
	// event envelope, the resolved job, or the request context) and MUST pass it
	// so the RLS context (SET LOCAL chora.tenant_id) is set before the UPDATE.
	// chora_creation_app_rw is NOBYPASSRLS, so an all-zeros / empty tenant sees
	// zero rows and the UPDATE silently no-ops (the bug this signature fixes).
	//
	// completedCandidatesJSON is the raw JSONB payload to store in
	// question_generation_jobs.candidate_questions_jsonb — pass nil to leave
	// unchanged.
	UpdateStatus(ctx context.Context, tenantID, jobID string, to question.JobStatus, completedCandidatesJSON []byte, err string) error

	// UpdateStatusWithProposal is the batch-completion projection writer:
	// UpdateStatus + the Lane 1c composer proposal
	// (question_generation_jobs.proposed_test_set_jsonb, migration 0017) + the
	// CHO-1819 P2 mixed-batch generation summary (generation_summary_jsonb,
	// migration 0018) + the CHO-1826 Gap #4 per-step pipeline trace
	// (pipeline_trace_jsonb, migration 0021) in ONE atomic UPDATE. Each of
	// proposalJSON / generationSummaryJSON / pipelineTraceJSON nil/empty leaves
	// ITS column unchanged (single-question jobs / legacy batches / a pre-P2
	// orchestrator / refused jobs never stamp them).
	UpdateStatusWithProposal(ctx context.Context, tenantID, jobID string, to question.JobStatus, completedCandidatesJSON, proposalJSON, generationSummaryJSON, pipelineTraceJSON []byte, err string) error

	// PatchCandidatesUnderLock atomically rewrites
	// question_generation_jobs.candidate_questions_jsonb on (tenantID, jobID)
	// inside ONE tenant tx, row-locked (SELECT ... FOR UPDATE), WITHOUT a status
	// transition. It backs the CHO-1819 P3 review-image-regenerate terminal
	// patch: an image_regen completion rewrites ONE targeted candidate's image
	// on the PARENT batch job, which must stay in JobStatusSucceeded (review).
	//
	// The read-modify-write is serialised by the row lock so two concurrent
	// image_regen completions targeting DIFFERENT drafts of the SAME parent
	// cannot lost-update each other (a blind overwrite from a stale snapshot
	// would clobber the other's element).
	//
	// apply receives the locked row's current status + candidates and returns
	// the new candidates to persist, or an error to abort the tx WITHOUT writing
	// (the caller decides whether that error is fail-soft or transient). A nil/
	// empty return with nil error is a no-op. Returns question.ErrNotFound when
	// the row is missing. tenantID is REQUIRED (RLS, same as UpdateStatus).
	PatchCandidatesUnderLock(ctx context.Context, tenantID, jobID string, apply func(status string, current []byte) ([]byte, error)) error

	// TransitionFromSucceeded conditionally moves the job from
	// JobStatusSucceeded to an accept-terminal status (accepted |
	// partially_accepted | cancelled) and reports whether THIS call won the
	// transition (row matched). The W3 exactly-once guard: only the accept
	// request that wins publishes chora.creation.question_batch.accepted.v1
	// — a retry/concurrent duplicate sees false and MUST NOT publish.
	TransitionFromSucceeded(ctx context.Context, tenantID, jobID string, to question.JobStatus) (bool, error)

	// UpdatePipelineTraceOnly writes the partial pipeline_trace onto a RUNNING
	// job mid-generation (CR Phase 1 live-trace streaming) WITHOUT a status
	// transition. Returns applied=true when THIS write won (the trace strictly
	// grew AND the job is still running); applied=false (nil error) when the
	// guard rejected it (terminal job, or a stale/duplicate/out-of-order trace)
	// — a normal no-op. Monotonic on jsonb_array_length so duplicate / reordered
	// progress deliveries are idempotent; never regresses a terminal job.
	UpdatePipelineTraceOnly(ctx context.Context, tenantID, jobID string, pipelineTraceJSON []byte) (bool, error)

	// ApplyChunkCandidates persists one finished chunk's published-shape
	// candidates onto a RUNNING job (ADR-251 D5, CHO-2398), slot-keyed by
	// chunk_index inside chunk_candidates_jsonb. Idempotent + out-of-order
	// safe in SQL: the write applies ONLY when the job is running AND the
	// slot is still empty, so a duplicate or reordered delivery contributes
	// nothing (applied=false, nil error - a normal no-op, never an error).
	// chunkCount records the plan total for "k of n" progress reads. The
	// terminal completed.v1 remains authoritative for the full set.
	ApplyChunkCandidates(ctx context.Context, tenantID, jobID string, chunkIndex, chunkCount int, candidatesJSON []byte) (bool, error)
}
