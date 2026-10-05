-- =============================================================================
-- chora-creation : 0007_question_revisions.down.sql
--
-- Reverse of 0007_question_revisions.up.sql. The shared append-only function
-- enforce_atom_revisions_append_only() is NOT dropped — it is owned by
-- 0001_initial.sql and used by atom_revisions.
-- =============================================================================

BEGIN;

DROP TRIGGER  IF EXISTS trg_question_revisions_no_update ON question_revisions;
DROP TRIGGER  IF EXISTS trg_question_revisions_no_delete ON question_revisions;
DROP POLICY   IF EXISTS tenant_isolation                 ON question_revisions;
DROP INDEX    IF EXISTS idx_question_revisions_question;
DROP INDEX    IF EXISTS idx_question_revisions_tenant;
DROP INDEX    IF EXISTS idx_question_revisions_atom;
DROP TABLE    IF EXISTS question_revisions;

COMMIT;
