// question_job_repository.go — pgx-backed implementation of
// ports.QuestionJobRepository for chora_creation.question_generation_jobs
// (migration 0008).
//
// Per /Users/daleleung/.claude/plans/golden-hopping-owl.md P5-C:
//
//   - Create(ctx, job) — INSERT a freshly-constructed job in REQUESTED status.
//     Idempotent on mana_idempotency_key (UNIQUE constraint at the DB level).
//   - Get(ctx, tenant, job_id) — SELECT one row + decode candidate_questions_jsonb.
//   - UpdateStatus(ctx, tenant_id, job_id, to, candidatesJSON, err) — atomic
//     UPDATE inside the caller's tenant RLS context.
//
// Every method wraps SQL in RunInTenantTx so SET LOCAL chora.tenant_id runs
// BEFORE any tenant-scoped read/write (chora_creation_app_rw is NOBYPASSRLS).
// Mirrors the pattern in question_repository.go.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// QuestionJobRepository is the pgx-backed implementation.
type QuestionJobRepository struct {
	tx TxQuerier
}

// NewQuestionJobRepository wraps a *PgxPoolQuerier for production wiring.
func NewQuestionJobRepository(querier *PgxPoolQuerier) *QuestionJobRepository {
	return &QuestionJobRepository{tx: querier}
}

// NewQuestionJobRepositoryFromTxQuerier wraps an explicit TxQuerier for tests.
func NewQuestionJobRepositoryFromTxQuerier(tq TxQuerier) *QuestionJobRepository {
	return &QuestionJobRepository{tx: tq}
}

// Compile-time check.
var _ ports.QuestionJobRepository = (*QuestionJobRepository)(nil)

const (
	sqlInsertJob = `
        INSERT INTO question_generation_jobs (
            job_id, atom_id, tenant_id, author_gcid,
            status,
            mana_action_code, mana_charged,
            source_blob_uri, source_mime_type,
            settings, candidate_questions_jsonb,
            mana_idempotency_key,
            error,
            created_at, updated_at,
            intent, input_kind
        ) VALUES (
            $1, $2, $3, $4,
            $5,
            $6, $7,
            NULLIF($8, ''), NULLIF($9, ''),
            COALESCE(NULLIF($10, '')::jsonb, '{}'::jsonb),
            COALESCE(NULLIF($11, '')::jsonb, '[]'::jsonb),
            $12,
            NULLIF($13, ''),
            $14, $15,
            NULLIF($16, ''), NULLIF($17, '')
        )
    `

	sqlSelectJob = `
        SELECT
            job_id::text,
            atom_id::text,
            tenant_id::text,
            author_gcid::text,
            status,
            mana_action_code,
            mana_charged,
            COALESCE(source_blob_uri, ''),
            COALESCE(source_mime_type, ''),
            COALESCE(candidate_questions_jsonb::text, ''),
            COALESCE(settings::text, ''),
            COALESCE(proposed_test_set_jsonb::text, ''),
            COALESCE(generation_summary_jsonb::text, ''),
            COALESCE(pipeline_trace_jsonb::text, ''),
            COALESCE(error, ''),
            mana_idempotency_key,
            created_at,
            started_at,
            completed_at,
            accepted_at,
            updated_at,
            COALESCE(intent, ''),
            COALESCE(input_kind, ''),
            COALESCE(chunk_candidates_jsonb::text, ''),
            COALESCE(chunk_count_total, 0)
        FROM question_generation_jobs
        WHERE tenant_id = $1 AND job_id = $2
    `

	sqlUpdateJobStatus = `
        UPDATE question_generation_jobs
        SET status = $2,
            candidate_questions_jsonb = COALESCE(NULLIF($3, '')::jsonb, candidate_questions_jsonb),
            error = NULLIF($4, ''),
            started_at = CASE
                WHEN $2 = 'running' AND started_at IS NULL THEN now()
                ELSE started_at
            END,
            completed_at = CASE
                WHEN $2 IN ('succeeded','failed','accepted','partially_accepted','cancelled') AND completed_at IS NULL THEN now()
                ELSE completed_at
            END,
            accepted_at = CASE
                WHEN $2 IN ('accepted','partially_accepted') AND accepted_at IS NULL THEN now()
                ELSE accepted_at
            END,
            updated_at = now()
        WHERE job_id = $1
    `

	// sqlUpdateJobStatusWithProposal — sqlUpdateJobStatus + the Lane 1c
	// composer proposal column (migration 0017) + the CHO-1819 P2 generation
	// summary (migration 0018) + the CHO-1826 Gap #4 pipeline trace (migration
	// 0021). $5/$6/$7 empty leaves THAT column unchanged (COALESCE-preserving,
	// matching the candidates column semantics).
	sqlUpdateJobStatusWithProposal = `
        UPDATE question_generation_jobs
        SET status = $2,
            candidate_questions_jsonb = COALESCE(NULLIF($3, '')::jsonb, candidate_questions_jsonb),
            proposed_test_set_jsonb = COALESCE(NULLIF($5, '')::jsonb, proposed_test_set_jsonb),
            generation_summary_jsonb = COALESCE(NULLIF($6, '')::jsonb, generation_summary_jsonb),
            pipeline_trace_jsonb = COALESCE(NULLIF($7, '')::jsonb, pipeline_trace_jsonb),
            error = NULLIF($4, ''),
            started_at = CASE
                WHEN $2 = 'running' AND started_at IS NULL THEN now()
                ELSE started_at
            END,
            completed_at = CASE
                WHEN $2 IN ('succeeded','failed','accepted','partially_accepted','cancelled') AND completed_at IS NULL THEN now()
                ELSE completed_at
            END,
            accepted_at = CASE
                WHEN $2 IN ('accepted','partially_accepted') AND accepted_at IS NULL THEN now()
                ELSE accepted_at
            END,
            updated_at = now()
        WHERE job_id = $1
    `

	// sqlTransitionFromSucceeded — the W3 exactly-once accept guard: the
	// UPDATE only matches while the row is still in 'succeeded'; RETURNING
	// detects whether THIS statement won the transition (ErrNoRows = a
	// concurrent/replayed accept already moved it).
	sqlTransitionFromSucceeded = `
        UPDATE question_generation_jobs
        SET status = $2,
            completed_at = COALESCE(completed_at, now()),
            accepted_at = CASE
                WHEN $2 IN ('accepted','partially_accepted') AND accepted_at IS NULL THEN now()
                ELSE accepted_at
            END,
            updated_at = now()
        WHERE job_id = $1 AND status = 'succeeded'
        RETURNING job_id::text
    `

	// sqlUpdatePipelineTraceOnly — CR Phase 1 live-trace streaming. Writes the
	// partial pipeline_trace mid-run WITHOUT a status transition. Two SQL guards:
	//   • status='running' — never clobbers a terminal job (a late progress
	//     event arriving AFTER completed.v1 set 'succeeded' loses this WHERE).
	//   • jsonb_array_length monotonic — only a STRICTLY-LONGER trace wins, so
	//     out-of-order / duplicate deliveries (the dev+prod outbox double-
	//     dispatch) are idempotent in SQL, not in racy Go read-then-write.
	// RETURNING detects whether THIS write applied (ErrNoRows = guard rejected:
	// terminal job OR not-newer trace — a normal no-op, never an error).
	sqlUpdatePipelineTraceOnly = `
        UPDATE question_generation_jobs
        SET pipeline_trace_jsonb = $2::jsonb,
            updated_at = now()
        WHERE job_id = $1
          AND status = 'running'
          AND jsonb_array_length($2::jsonb) > COALESCE(jsonb_array_length(pipeline_trace_jsonb), 0)
        RETURNING job_id::text
    `

	// sqlApplyChunkCandidates (ADR-251 D5, CHO-2398) - slot-keyed chunk
	// persistence. Guards, all in SQL so duplicate / out-of-order deliveries
	// are idempotent without racy Go read-then-write:
	//   - status='running' - a late chunk arriving AFTER the terminal never
	//     writes (the terminal's full set is authoritative).
	//   - NOT (chunk_candidates_jsonb ? $2) - the slot applies at most once;
	//     a redelivered or reordered chunk contributes nothing.
	// chunk_count_total never regresses (GREATEST) so the "k of n" progress
	// read stays stable across chunks. RETURNING detects whether THIS write
	// applied (ErrNoRows = guard rejected - a normal no-op, never an error).
	sqlApplyChunkCandidates = `
        UPDATE question_generation_jobs
        SET chunk_candidates_jsonb = jsonb_set(chunk_candidates_jsonb, ARRAY[$2::text], $3::jsonb),
            chunk_count_total = GREATEST(chunk_count_total, $4),
            updated_at = now()
        WHERE job_id = $1
          AND status = 'running'
          AND NOT (chunk_candidates_jsonb ? $2)
        RETURNING job_id::text
    `

	// sqlPatchCandidates overwrites candidate_questions_jsonb without touching
	// status (CHO-1819 P3 image-regen parent patch). The targeted parent job
	// stays 'succeeded' — there is no succeeded→succeeded transition, so this is
	// a pure candidates rewrite. $2 is always a non-empty JSON array (the caller
	// rejects empty), so a direct ::jsonb cast (no COALESCE-preserve) is correct.
	sqlPatchCandidates = `
        UPDATE question_generation_jobs
        SET candidate_questions_jsonb = $2::jsonb,
            updated_at = now()
        WHERE job_id = $1
    `

	// sqlSelectJobForUpdate row-locks the job + returns its status + candidates
	// for the PatchCandidatesUnderLock read-modify-write. FOR UPDATE serialises
	// concurrent image_regen patches to the same parent (lost-update guard).
	sqlSelectJobForUpdate = `
        SELECT status, COALESCE(candidate_questions_jsonb::text, '')
        FROM question_generation_jobs
        WHERE job_id = $1
        FOR UPDATE
    `
)

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

// Create implements ports.QuestionJobRepository.Create.
func (r *QuestionJobRepository) Create(ctx context.Context, job *question.ComposeJob) error {
	if job == nil {
		return errors.New("pg.QuestionJobRepository.Create: nil job")
	}
	if r.tx == nil {
		return errors.New("pg.QuestionJobRepository.Create: no TxQuerier wired")
	}
	if job.Status == "" {
		job.Status = question.JobStatusRequested
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}

	// Pre-compute the idempotency-key: `{job_id}-debit` so the same retry
	// of Create() with the same job_id never double-creates.
	idemKey := job.JobID + "-debit"
	candJSON := string(job.CandidateQuestionsJSON)

	// ADR-195 WS3 — persist the compose discriminant columns. input_kind is only
	// meaningful alongside an intent; an unset intent emits '' so the NULLIF in
	// sqlInsertJob writes NULL (back-compat for any caller not yet on the model).
	intentStr := string(job.Intent)
	var inputKind string
	if intentStr != "" {
		inputKind = string(job.Input.Kind())
	}

	return r.tx.RunInTenantTx(ctx, job.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sqlInsertJob,
			job.JobID, job.AtomID, job.TenantID, job.AuthorGCID,
			string(job.Status),
			job.ManaActionCode, job.ManaCharged,
			job.SourceBlobURI, job.SourceMimeType,
			string(job.SettingsJSON), // settings (empty defaults to '{}' via COALESCE)
			candJSON,
			idemKey,
			job.Error,
			job.CreatedAt, job.UpdatedAt,
			intentStr, inputKind,
		); err != nil {
			return fmt.Errorf("pg.QuestionJobRepository.Create: insert: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

// Get implements ports.QuestionJobRepository.Get.
func (r *QuestionJobRepository) Get(ctx context.Context, tenantID, jobID string) (*question.ComposeJob, error) {
	if r.tx == nil {
		return nil, errors.New("pg.QuestionJobRepository.Get: no TxQuerier wired")
	}
	var (
		got *question.ComposeJob
		nf  bool
	)
	if err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlSelectJob, tenantID, jobID)
		j, err := scanComposeJob(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				nf = true
				return nil
			}
			return err
		}
		got = j
		return nil
	}); err != nil {
		return nil, fmt.Errorf("pg.QuestionJobRepository.Get: %w", err)
	}
	if nf {
		return nil, question.ErrNotFound
	}
	return got, nil
}

// scanComposeJob maps a single SELECT row into the domain
// struct. Mirrors the column order in sqlSelectJob.
func scanComposeJob(row interface{ Scan(...any) error }) (*question.ComposeJob, error) {
	var (
		jobIDStr       string
		atomID         string
		tenantID       string
		authorGCID     string
		status         string
		manaActionCode string
		manaCharged    int32
		sourceBlobURI  string
		sourceMimeType string
		candJSON       string
		settingsJSON   string
		proposalJSON   string
		genSummaryJSON string
		traceJSON      string
		errorMsg       string
		idemKey        string
		createdAt      time.Time
		startedAt      *time.Time
		completedAt    *time.Time
		acceptedAt     *time.Time
		updatedAt      time.Time
		intentStr      string
		inputKindStr   string
		chunkCandJSON  string
		chunkCountTot  int32
	)
	if err := row.Scan(
		&jobIDStr, &atomID, &tenantID, &authorGCID,
		&status,
		&manaActionCode, &manaCharged,
		&sourceBlobURI, &sourceMimeType,
		&candJSON, &settingsJSON, &proposalJSON, &genSummaryJSON, &traceJSON,
		&errorMsg, &idemKey,
		&createdAt, &startedAt, &completedAt, &acceptedAt, &updatedAt,
		&intentStr, &inputKindStr,
		&chunkCandJSON, &chunkCountTot,
	); err != nil {
		return nil, err
	}
	job := &question.ComposeJob{
		JobID:          jobIDStr,
		AtomID:         atomID,
		TenantID:       tenantID,
		AuthorGCID:     authorGCID,
		Status:         question.JobStatus(status),
		ManaActionCode: manaActionCode,
		ManaCharged:    int(manaCharged),
		SourceBlobURI:  sourceBlobURI,
		SourceMimeType: sourceMimeType,
		Error:          errorMsg,
		CreatedAt:      createdAt,
		StartedAt:      copyTimePtr(startedAt),
		CompletedAt:    copyTimePtr(completedAt),
		AcceptedAt:     copyTimePtr(acceptedAt),
		UpdatedAt:      updatedAt,
	}
	if candJSON != "" {
		job.CandidateQuestionsJSON = []byte(candJSON)
	}
	// '{}' is the column default — treat as unset so pre-1c jobs round-trip
	// with a nil SettingsJSON (byte-stable domain shape).
	if settingsJSON != "" && settingsJSON != "{}" {
		job.SettingsJSON = []byte(settingsJSON)
	}
	if proposalJSON != "" {
		job.ProposedTestSetJSON = []byte(proposalJSON)
	}
	if genSummaryJSON != "" {
		job.GenerationSummaryJSON = []byte(genSummaryJSON)
	}
	if traceJSON != "" {
		job.PipelineTraceJSON = []byte(traceJSON)
	}
	// '{}' is the 0035 column default - treat as unset (no chunk slots yet).
	if chunkCandJSON != "" && chunkCandJSON != "{}" {
		job.ChunkCandidatesJSON = []byte(chunkCandJSON)
	}
	job.ChunkCountTotal = int(chunkCountTot)
	// ADR-195 WS9 step 1 — reconstruct the compose VOs from the persisted intent +
	// input_kind columns (migration 0022) so a loaded ComposeJob carries its explicit
	// Intent/Input. ComposeModelForJob then branches on these, not the legacy job_type
	// fallback (which is dropped in WS9 step 2/3).
	job.Intent = question.Intent(intentStr)
	job.Input = reconstructComposeInput(question.InputKind(inputKindStr), settingsJSON, sourceBlobURI, sourceMimeType)
	return job, nil
}

// reconstructComposeInput rebuilds a loaded job's compose Input VO from the
// authoritative input_kind column plus the durable seed: the author's prompt /
// role-tagged source files recovered from settings, with the canonical
// source_blob_uri as the source-files fallback. ADR-195 WS9 step 1. Best-effort on
// settings — a malformed blob never fails the read (input_kind is authoritative for
// the classification; the source_blob_uri fallback guarantees a source_files job
// round-trips HasFiles()).
func reconstructComposeInput(inputKind question.InputKind, settingsJSON, sourceBlobURI, sourceMimeType string) question.Input {
	prompt, refs := parseComposeSeed(settingsJSON)
	if len(refs) == 0 && sourceBlobURI != "" {
		refs = []question.SourceFileRef{{BlobURI: sourceBlobURI, MimeType: sourceMimeType}}
	}
	return question.ReconstructInput(inputKind, prompt, refs)
}

// parseComposeSeed best-effort extracts the recoverable compose seed (the author's
// prompt + role-tagged source files) from the settings JSONB so a reloaded job's
// Input VO is faithful. Returns zero values on an empty / "{}" / malformed blob (the
// caller's input_kind column is authoritative for the classification).
func parseComposeSeed(settingsJSON string) (prompt string, refs []question.SourceFileRef) {
	if settingsJSON == "" || settingsJSON == "{}" {
		return "", nil
	}
	var s struct {
		Prompt      string `json:"prompt"`
		SourceFiles []struct {
			BlobURI  string `json:"blob_uri"`
			MimeType string `json:"mime_type"`
			Role     string `json:"role"`
		} `json:"source_files"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
		return "", nil
	}
	for _, f := range s.SourceFiles {
		refs = append(refs, question.SourceFileRef{BlobURI: f.BlobURI, MimeType: f.MimeType, Role: f.Role})
	}
	return s.Prompt, refs
}

func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// -----------------------------------------------------------------------------
// UpdateStatus
// -----------------------------------------------------------------------------

// UpdateStatus implements ports.QuestionJobRepository.UpdateStatus.
//
// The state-machine semantics live in domain/question/job.go's Transition
// method — this adapter is a passthrough writer. The CASE expressions in
// sqlUpdateJobStatus stamp the started_at / completed_at / accepted_at
// timestamps to match the domain Transition's side effects so a worker
// that calls UpdateStatus directly (without going through Transition
// first) still gets consistent timestamps.
//
// tenantID is supplied by the caller (event envelope / resolved job / request
// context). It sets the RLS context (SET LOCAL chora.tenant_id) so the UPDATE
// targets the row. chora_creation_app_rw is NOBYPASSRLS, so passing an empty or
// all-zeros tenant would make the policy `tenant_id = current_setting(...)`
// match zero rows and the UPDATE would silently no-op — hence we fail loud on
// an empty tenant and we NEVER self-resolve via a placeholder context.
func (r *QuestionJobRepository) UpdateStatus(ctx context.Context, tenantID, jobID string, to question.JobStatus, candidatesJSON []byte, errMsg string) error {
	if r.tx == nil {
		return errors.New("pg.QuestionJobRepository.UpdateStatus: no TxQuerier wired")
	}
	if tenantID == "" {
		return errors.New("pg.QuestionJobRepository.UpdateStatus: tenantID required")
	}
	if jobID == "" {
		return errors.New("pg.QuestionJobRepository.UpdateStatus: jobID required")
	}
	if !to.Valid() {
		return fmt.Errorf("pg.QuestionJobRepository.UpdateStatus: invalid status %q", string(to))
	}

	candStr := string(candidatesJSON)
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sqlUpdateJobStatus, jobID, string(to), candStr, errMsg); err != nil {
			return fmt.Errorf("pg.QuestionJobRepository.UpdateStatus: update: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// UpdateStatusWithProposal (Lane 1c)
// -----------------------------------------------------------------------------

// UpdateStatusWithProposal implements ports.QuestionJobRepository.
// UpdateStatusWithProposal — UpdateStatus semantics + the composer's
// proposed_test_set_jsonb (migration 0017) + the CHO-1819 P2 mixed-batch
// generation_summary_jsonb (migration 0018) in the same UPDATE (no second
// round-trip; the candidate stamping, the proposal, and the summary land
// atomically). Each empty JSON arg leaves ITS column unchanged (COALESCE-
// preserving) so single-question / legacy / pre-P2 jobs never clobber them.
func (r *QuestionJobRepository) UpdateStatusWithProposal(ctx context.Context, tenantID, jobID string, to question.JobStatus, candidatesJSON, proposalJSON, generationSummaryJSON, pipelineTraceJSON []byte, errMsg string) error {
	if r.tx == nil {
		return errors.New("pg.QuestionJobRepository.UpdateStatusWithProposal: no TxQuerier wired")
	}
	if tenantID == "" {
		return errors.New("pg.QuestionJobRepository.UpdateStatusWithProposal: tenantID required")
	}
	if jobID == "" {
		return errors.New("pg.QuestionJobRepository.UpdateStatusWithProposal: jobID required")
	}
	if !to.Valid() {
		return fmt.Errorf("pg.QuestionJobRepository.UpdateStatusWithProposal: invalid status %q", string(to))
	}
	candStr := string(candidatesJSON)
	propStr := string(proposalJSON)
	summaryStr := string(generationSummaryJSON)
	traceStr := string(pipelineTraceJSON)
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sqlUpdateJobStatusWithProposal, jobID, string(to), candStr, errMsg, propStr, summaryStr, traceStr); err != nil {
			return fmt.Errorf("pg.QuestionJobRepository.UpdateStatusWithProposal: update: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// PatchCandidates (CHO-1819 P3 image-regen parent patch)
// -----------------------------------------------------------------------------

// PatchCandidatesUnderLock implements ports.QuestionJobRepository.
// PatchCandidatesUnderLock — an atomic read-modify-write of
// candidate_questions_jsonb inside ONE tenant tx, row-locked via SELECT ... FOR
// UPDATE, WITHOUT a status transition. Loads the job's current status +
// candidates, invokes apply, and writes apply's result back. The row lock
// serialises concurrent patches to the same job so they can't lost-update each
// other (the image_regen lost-update guard — CHO-1819 P3c review). Fails loud on
// an empty tenant (RLS no-op guard) or a nil apply.
func (r *QuestionJobRepository) PatchCandidatesUnderLock(
	ctx context.Context,
	tenantID, jobID string,
	apply func(status string, current []byte) ([]byte, error),
) error {
	if r.tx == nil {
		return errors.New("pg.QuestionJobRepository.PatchCandidatesUnderLock: no TxQuerier wired")
	}
	if tenantID == "" {
		return errors.New("pg.QuestionJobRepository.PatchCandidatesUnderLock: tenantID required")
	}
	if jobID == "" {
		return errors.New("pg.QuestionJobRepository.PatchCandidatesUnderLock: jobID required")
	}
	if apply == nil {
		return errors.New("pg.QuestionJobRepository.PatchCandidatesUnderLock: apply required")
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		var status, cands string
		row := tx.QueryRow(ctx, sqlSelectJobForUpdate, jobID)
		if err := row.Scan(&status, &cands); err != nil {
			if errors.Is(err, ErrNoRows) {
				return question.ErrNotFound
			}
			return fmt.Errorf("pg.QuestionJobRepository.PatchCandidatesUnderLock: select for update: %w", err)
		}
		// apply's error aborts the tx WITHOUT a write (returned verbatim so the
		// caller's errors.Is fail-soft/transient classification works).
		patched, aerr := apply(status, []byte(cands))
		if aerr != nil {
			return aerr
		}
		if len(patched) == 0 {
			return nil // apply opted out — no write
		}
		if err := tx.Exec(ctx, sqlPatchCandidates, jobID, string(patched)); err != nil {
			return fmt.Errorf("pg.QuestionJobRepository.PatchCandidatesUnderLock: update: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// TransitionFromSucceeded (Lane 1c W3 exactly-once guard)
// -----------------------------------------------------------------------------

// TransitionFromSucceeded implements ports.QuestionJobRepository.
// TransitionFromSucceeded — a compare-and-swap on status: the UPDATE matches
// only while the row is still 'succeeded'. Returns (true, nil) when THIS
// call performed the transition; (false, nil) when the row was already
// moved (idempotent replay / lost race — the caller must NOT publish).
func (r *QuestionJobRepository) TransitionFromSucceeded(ctx context.Context, tenantID, jobID string, to question.JobStatus) (bool, error) {
	if r.tx == nil {
		return false, errors.New("pg.QuestionJobRepository.TransitionFromSucceeded: no TxQuerier wired")
	}
	if tenantID == "" {
		return false, errors.New("pg.QuestionJobRepository.TransitionFromSucceeded: tenantID required")
	}
	if jobID == "" {
		return false, errors.New("pg.QuestionJobRepository.TransitionFromSucceeded: jobID required")
	}
	switch to {
	case question.JobStatusAccepted, question.JobStatusPartiallyAccepted, question.JobStatusCancelled:
		// allowed accept-terminal targets (succeeded → X per the state machine)
	default:
		return false, fmt.Errorf("pg.QuestionJobRepository.TransitionFromSucceeded: target %q is not an accept-terminal status", string(to))
	}

	won := false
	if err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlTransitionFromSucceeded, jobID, string(to))
		var returnedID string
		if err := row.Scan(&returnedID); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil // no row in 'succeeded' — lost the race / replay
			}
			return err
		}
		won = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("pg.QuestionJobRepository.TransitionFromSucceeded: %w", err)
	}
	return won, nil
}

// UpdatePipelineTraceOnly writes the partial pipeline_trace onto a RUNNING job
// mid-generation (CR Phase 1 live-trace streaming) WITHOUT transitioning the
// status. Returns applied=true when THIS write won (the trace strictly grew AND
// the job is still running); applied=false (nil error) when the SQL guard
// rejected it (a terminal job, or a stale / duplicate / out-of-order trace) —
// both are normal no-ops, never an error. Wrapped in RunInTenantTx so the
// RLS-scoped UPDATE matches rows (an empty tenant → zero rows → silent no-op,
// so tenantID is required + fail-loud).
// ApplyChunkCandidates persists one finished chunk's candidates onto a
// RUNNING job, slot-keyed by chunk_index (ADR-251 D5, CHO-2398). See
// sqlApplyChunkCandidates for the idempotence + terminal guards.
func (r *QuestionJobRepository) ApplyChunkCandidates(ctx context.Context, tenantID, jobID string, chunkIndex, chunkCount int, candidatesJSON []byte) (bool, error) {
	if r.tx == nil {
		return false, errors.New("pg.QuestionJobRepository.ApplyChunkCandidates: no TxQuerier wired")
	}
	if tenantID == "" {
		return false, errors.New("pg.QuestionJobRepository.ApplyChunkCandidates: tenantID required")
	}
	if jobID == "" {
		return false, errors.New("pg.QuestionJobRepository.ApplyChunkCandidates: jobID required")
	}
	if chunkIndex < 0 {
		return false, fmt.Errorf("pg.QuestionJobRepository.ApplyChunkCandidates: negative chunk_index %d", chunkIndex)
	}
	if len(candidatesJSON) == 0 {
		return false, errors.New("pg.QuestionJobRepository.ApplyChunkCandidates: empty candidates JSON (never a blank slot)")
	}

	applied := false
	if err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlApplyChunkCandidates,
			jobID, strconv.Itoa(chunkIndex), string(candidatesJSON), chunkCount,
		)
		var returnedID string
		if err := row.Scan(&returnedID); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil // guard rejected (terminal job / slot filled) - no-op
			}
			return err
		}
		applied = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("pg.QuestionJobRepository.ApplyChunkCandidates: %w", err)
	}
	return applied, nil
}

func (r *QuestionJobRepository) UpdatePipelineTraceOnly(ctx context.Context, tenantID, jobID string, pipelineTraceJSON []byte) (bool, error) {
	if r.tx == nil {
		return false, errors.New("pg.QuestionJobRepository.UpdatePipelineTraceOnly: no TxQuerier wired")
	}
	if tenantID == "" {
		return false, errors.New("pg.QuestionJobRepository.UpdatePipelineTraceOnly: tenantID required")
	}
	if jobID == "" {
		return false, errors.New("pg.QuestionJobRepository.UpdatePipelineTraceOnly: jobID required")
	}
	if len(pipelineTraceJSON) == 0 {
		// Nothing to write — never a status side-effect, never an error.
		return false, nil
	}

	applied := false
	if err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlUpdatePipelineTraceOnly, jobID, string(pipelineTraceJSON))
		var returnedID string
		if err := row.Scan(&returnedID); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil // guard rejected (terminal job / not-newer trace) — no-op
			}
			return err
		}
		applied = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("pg.QuestionJobRepository.UpdatePipelineTraceOnly: %w", err)
	}
	return applied, nil
}
