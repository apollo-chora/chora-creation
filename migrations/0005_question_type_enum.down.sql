-- =============================================================================
-- chora-creation : 0005_question_type_enum.down.sql
--
-- Reverse of 0005_question_type_enum.up.sql. Safe only when the dependent
-- tables (0006_questions, 0007_question_revisions, 0008_question_generation_jobs)
-- have been rolled back first — otherwise the DROP TYPE fails with
-- "cannot drop type question_type because other objects depend on it".
-- =============================================================================

BEGIN;

DROP TYPE IF EXISTS question_type;

COMMIT;
