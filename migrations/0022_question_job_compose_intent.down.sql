-- =============================================================================
-- chora-creation : 0022_question_job_compose_intent.down.sql
--
-- Reverse ADR-195 WS2: drop the compose discriminant columns + their CHECKs and
-- restore the job_type CHECK to the pre-0022 (0020) five-value state.
--
-- NOTE: the job_type restore FAILS if any row carries job_type='compose' (a
-- WS3+ row). Down is for a dev rollback BEFORE WS3 ships — it mirrors the
-- 0019/0020 down migrations, which likewise restore the prior value list.
-- Idempotent (DROP ... IF EXISTS throughout).
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_input_kind_check;
ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_intent_check;

ALTER TABLE question_generation_jobs
    DROP COLUMN IF EXISTS input_kind;
ALTER TABLE question_generation_jobs
    DROP COLUMN IF EXISTS intent;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;
ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen', 'manual_draft'));

COMMIT;
