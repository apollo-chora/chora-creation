-- =============================================================================
-- chora-creation : 0021_question_pipeline_trace.down.sql — reverse
-- 0021_question_pipeline_trace.up.sql.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP COLUMN IF EXISTS pipeline_trace_jsonb;

COMMIT;
