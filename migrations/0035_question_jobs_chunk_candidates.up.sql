-- =============================================================================
-- chora-creation : 0035_question_jobs_chunk_candidates.up.sql
--
-- ADR           : ADR-251 D5 (CHO-2398) - incremental chunk publishing
-- Domain        : Content Creation
-- Database      : chora_creation
-- Date          : 2026-08-16
--
-- Purpose:
--   Chunk-slot storage for the AI Assist set lane's incremental publishing.
--   chora.creation.ai_assist.chunk_completed.v1 events persist each finished
--   chunk's published-shape candidates into chunk_candidates_jsonb, an object
--   keyed by chunk_index ({"0": [...], "1": [...]}), applied at most once per
--   slot and only while the job is RUNNING (sqlApplyChunkCandidates guards).
--   chunk_count_total records the plan total for "k of n" progress reads and
--   never regresses. The terminal completed.v1 still writes the authoritative
--   full set into candidate_questions_jsonb, unchanged; the chunk slots exist
--   so the poll can serve questions-so-far while the job runs.
--
--   RLS: question_generation_jobs already carries the tenant policy
--   (migration 0008); new columns inherit it. Idempotent: IF NOT EXISTS.
--   Grants ride 9999_grant_app_roles.sql (run the FULL migrations job).
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    ADD COLUMN IF NOT EXISTS chunk_candidates_jsonb JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE question_generation_jobs
    ADD COLUMN IF NOT EXISTS chunk_count_total INTEGER NOT NULL DEFAULT 0;

COMMIT;
