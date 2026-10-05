-- =============================================================================
-- chora-creation : 0019_question_job_image_regen.down.sql
-- Revert the job_type CHECK to the pre-P3 3-value set. Idempotent. (Rollback
-- only; the runner never applies .down.sql.) Any existing 'image_regen' rows
-- must be removed first or the ADD will fail — acceptable for a dev rollback.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;

ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material'));

COMMIT;
