-- =============================================================================
-- chora-creation : 0008_question_generation_jobs.down.sql
--
-- Reverse of 0008_question_generation_jobs.up.sql.
-- =============================================================================

BEGIN;

DROP TRIGGER  IF EXISTS trg_question_jobs_updated_at ON question_generation_jobs;
DROP POLICY   IF EXISTS tenant_isolation             ON question_generation_jobs;
DROP INDEX    IF EXISTS idx_question_jobs_atom;
DROP INDEX    IF EXISTS idx_question_jobs_tenant;
DROP INDEX    IF EXISTS idx_question_jobs_status;
DROP INDEX    IF EXISTS idx_question_jobs_author;
DROP INDEX    IF EXISTS idx_question_jobs_active;
DROP TABLE    IF EXISTS question_generation_jobs;

COMMIT;
