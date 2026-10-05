-- =============================================================================
-- chora-creation : 0023_question_job_job_type_nullable.up.sql
--
-- ADR-195 WS9 step 3 — make the vestigial job_type column NULLABLE ahead of the
-- column drop (migration 0024).
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- ADR           : docs/architecture/adrs/adr-195-compose-authoring-unification.md
-- Jira          : CHO-1888 (WS9) · epic CHO-1875
--
-- WHY a two-step (nullable → drop) cutover: job_type was TEXT NOT NULL (migration
-- 0008). The WS9 step-3 chora-creation image stops reading AND writing job_type
-- (the compose model — intent/input_kind, migration 0022 — is the sole
-- discriminant). If the column stayed NOT NULL, the new image's INSERT (which
-- omits job_type) would violate the constraint. Making it nullable FIRST lets the
-- OLD image (still writing job_type) and the NEW image (omitting it → NULL)
-- coexist during the rollout with ZERO window; 0024 then drops the column once the
-- new image is live and nothing references it.
--
-- Idempotent: DROP NOT NULL on an already-nullable column is a no-op. Safe for the
-- 13-DB ordered runner (a bad early migration must not strand downstream DBs).
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs ALTER COLUMN job_type DROP NOT NULL;

COMMENT ON COLUMN question_generation_jobs.job_type IS
'ADR-195 WS9 step 3: DEPRECATED + nullable ahead of the drop (migration 0024). No
chora-creation code reads or writes this column after the WS9 step-3 image; the
intent + input_kind columns (migration 0022) are the sole compose discriminant.';

COMMIT;
