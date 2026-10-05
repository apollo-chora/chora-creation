-- =============================================================================
-- chora-creation : 0020_question_job_manual_draft.down.sql
--
-- Revert the job_type CHECK to the pre-U3c 4-value set. Idempotent. (Rollback
-- only; the runner never applies .down.sql.) Any existing 'manual_draft' rows
-- would violate the reverted constraint — drop them first if rolling back.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;

ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen'));

COMMIT;
