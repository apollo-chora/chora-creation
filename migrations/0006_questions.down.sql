-- =============================================================================
-- chora-creation : 0006_questions.down.sql
--
-- Reverse of 0006_questions.up.sql. Drops indexes, policy, trigger, table.
-- Must run before 0005_question_type_enum.down.sql (depends on question_type).
-- =============================================================================

BEGIN;

DROP TRIGGER  IF EXISTS trg_questions_updated_at ON questions;
DROP POLICY   IF EXISTS tenant_isolation         ON questions;
DROP INDEX    IF EXISTS uq_questions_atom_alive;
DROP INDEX    IF EXISTS idx_questions_atom_tenant;
DROP INDEX    IF EXISTS idx_questions_tenant_type;
DROP INDEX    IF EXISTS idx_questions_author_gcid;
DROP TABLE    IF EXISTS questions;

COMMIT;
