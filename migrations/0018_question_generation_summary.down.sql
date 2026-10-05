-- =============================================================================
-- chora-creation : 0018_question_generation_summary.down.sql — reverse
-- 0018_question_generation_summary.up.sql.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP COLUMN IF EXISTS generation_summary_jsonb;

COMMIT;
