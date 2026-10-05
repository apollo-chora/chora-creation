-- =============================================================================
-- chora-creation : 0008_question_generation_jobs.up.sql
--
-- CR              : Question Authoring (MCQ + Open-Ended)
-- Design doc      : docs/m14/cr-question-authoring-design-2026-05-15.md §2.5 + §8
-- Plan            : ~/.claude/plans/golden-hopping-owl.md (D4 — all AI is async)
-- Domain          : Content Creation (5 core)
-- Database        : chora_creation
-- Author          : agent — P0+P1 question-authoring contracts/migrations
-- Date            : 2026-05-15
--
-- Purpose:
--   Tracks async AI question-generation jobs. Three job_types per the plan:
--     - ai_model_answer       (5 mana,  single question — fill model answer)
--     - ai_draft              (10 mana, single question — full draft)
--     - batch_source_material (50+ mana, N questions from uploaded source)
--
--   Per D4 every AI call is async — handler returns 202 + job_id, FE polls
--   GET /api/atoms/{atom_id}/question-jobs/{job_id} until status in
--   {succeeded, partially_accepted, accepted, failed, cancelled} then POSTs
--   /accept to persist a subset of candidate_questions into questions +
--   question_revisions tables.
--
-- Cross-DB JOINs FORBIDDEN. requester / atom / tenant refs are UUIDs without FK.
-- =============================================================================

BEGIN;

CREATE TABLE question_generation_jobs (
    job_id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id                   UUID         NOT NULL,
    tenant_id                 UUID         NOT NULL,
    author_gcid               UUID         NOT NULL,
    -- 3-value job_type (per design §2.5 + plan D4 uniform async lifecycle).
    job_type                  TEXT         NOT NULL
        CHECK (job_type IN ('ai_model_answer','ai_draft','batch_source_material')),
    -- 7-state lifecycle per design §2.5 — additive over the agent's 7-state
    -- proposal (requested/running/succeeded/failed/accepted/partially_accepted/
    -- cancelled). The values are stable wire-format strings.
    status                    TEXT         NOT NULL DEFAULT 'requested'
        CHECK (status IN (
            'requested',
            'running',
            'succeeded',
            'failed',
            'accepted',
            'partially_accepted',
            'cancelled'
        )),
    -- The mana_action_pricing key the debit was booked under (e.g.
    -- 'question_authoring_ai_draft'). Kept as TEXT for forward-compat —
    -- the action_code list is owned by chora-identity (cross-DB forbidden).
    mana_action_code          TEXT         NOT NULL,
    mana_charged              INTEGER      NOT NULL DEFAULT 0,
    -- Only set when job_type='batch_source_material'. gs:// URI of the
    -- source material uploaded to GCS.
    source_blob_uri           TEXT         NULL,
    source_mime_type          TEXT         NULL,
    -- Settings echoed back to poll callers (requested_count, type_mix, etc.).
    settings                  JSONB        NOT NULL DEFAULT '{}'::jsonb,
    -- Candidate questions JSONB — populated by the worker on completion.
    -- Shape documented in design §4 (QuestionGenerationJob.candidate_drafts).
    candidate_questions_jsonb JSONB        NOT NULL DEFAULT '[]'::jsonb,
    -- Idempotency key for retry-safe ManaService.DeductMana calls. Format:
    -- "{job_id}-parse" for parse-step debit, "{job_id}-per_item-{n}" for
    -- per-accepted-candidate debits.
    mana_idempotency_key      TEXT         NOT NULL UNIQUE,
    -- Failure mode message (free text; e.g. "Vertex AI 5xx after 3 retries").
    error                     TEXT         NULL,
    created_at                TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at                TIMESTAMPTZ  NULL,
    completed_at              TIMESTAMPTZ  NULL,
    accepted_at               TIMESTAMPTZ  NULL,
    updated_at                TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_question_jobs_atom
    ON question_generation_jobs (atom_id);
CREATE INDEX idx_question_jobs_tenant
    ON question_generation_jobs (tenant_id);
CREATE INDEX idx_question_jobs_status
    ON question_generation_jobs (status);
CREATE INDEX idx_question_jobs_author
    ON question_generation_jobs (author_gcid);
CREATE INDEX idx_question_jobs_active
    ON question_generation_jobs (created_at DESC)
    WHERE status IN ('requested','running');

CREATE TRIGGER trg_question_jobs_updated_at
    BEFORE UPDATE ON question_generation_jobs
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE question_generation_jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON question_generation_jobs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE question_generation_jobs IS
'Async AI question-generation jobs (D4). job_type ∈ {ai_model_answer, ai_draft,
batch_source_material}. status transitions through 7 states per design §2.5.
Mana is debited at job creation (parse / single-call) and again at accept-time
(per-item for batch). The worker subscribes the .generation_requested.v1
topic and updates rows in place — only triggers + indexes; no append-only
invariant on this table (it is operational state, not domain history).';

COMMIT;
