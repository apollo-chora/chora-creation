-- =============================================================================
-- chora-creation : 0023_question_job_job_type_nullable.down.sql  (rollback)
--
-- Restore the NOT NULL on job_type. NOTE: this only succeeds while NO row has a
-- NULL job_type — i.e. before the WS9 step-3 image (which INSERTs without
-- job_type) has written any rows. Once the cutover is live this is not cleanly
-- reversible (that is the expand/contract trade-off); recover via 0024's down,
-- which re-derives job_type from intent. The runner skips .down.sql (forward-only).
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs ALTER COLUMN job_type SET NOT NULL;

COMMIT;
