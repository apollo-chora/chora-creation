-- =============================================================================
-- chora-creation : 0019_question_job_image_regen.up.sql
--
-- CHO-1819 P3 — review image regenerate.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent CHO-1822 P3a (2026-06-22)
--
-- Widen the question_generation_jobs.job_type CHECK to allow a new lightweight
-- 'image_regen' job. It re-renders ONE image (stem or answer) for a single
-- candidate draft of a parent batch job (settings carry parent_job_id +
-- draft_id + placement + prompt) without re-running the whole batch; the
-- ai_assist terminal subscriber patches the parent job's candidate image.
--
-- Idempotent: the inline CHECK from migration 0008 is auto-named
-- `question_generation_jobs_job_type_check` (verified on the live DB). DROP it
-- by name IF EXISTS, then re-ADD with the 4th value. Re-runs converge; a drifted
-- env missing the constraint simply gets the widened one.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;

ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen'));

COMMIT;
