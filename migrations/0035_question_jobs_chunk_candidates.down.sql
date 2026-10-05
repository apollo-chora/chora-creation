-- =============================================================================
-- chora-creation : 0035_question_jobs_chunk_candidates.down.sql
--
-- Reverts 0035 (ADR-251 D5 chunk-slot storage). Running jobs lose their
-- questions-so-far slots (the terminal full set is unaffected).
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs DROP COLUMN IF EXISTS chunk_candidates_jsonb;
ALTER TABLE question_generation_jobs DROP COLUMN IF EXISTS chunk_count_total;

COMMIT;
