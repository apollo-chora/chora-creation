-- =============================================================================
-- chora-creation : 0020_question_job_manual_draft.up.sql
--
-- CHO-1826 U3c — unify single+batch authoring: manual authoring sessions.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent CHO-1826 U3c (2026-06-22)
--
-- Widen the question_generation_jobs.job_type CHECK to allow a new
-- 'manual_draft' job: a manual authoring session container created already
-- 'succeeded' (no LLM dispatch, no mana). The author writes questions by hand
-- and commits them via /accept with inline (no draft_id) candidates, which are
-- free and persist with source_type=manual.
--
-- Idempotent: the inline CHECK from migration 0008 is auto-named
-- `question_generation_jobs_job_type_check`. DROP it by name IF EXISTS, then
-- re-ADD with the 5th value. Re-runs converge; a drifted env missing the
-- constraint simply gets the widened one. Mirrors migration 0019.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;

ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen', 'manual_draft'));

COMMIT;
