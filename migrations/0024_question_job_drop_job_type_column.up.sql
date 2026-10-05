-- =============================================================================
-- chora-creation : 0024_question_job_drop_job_type_column.up.sql
--
-- ADR-195 WS9 step 3 (D1) — DROP the vestigial five-value job_type column.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- ADR           : docs/architecture/adrs/adr-195-compose-authoring-unification.md
-- Jira          : CHO-1888 (WS9) · epic CHO-1875
--
-- APPLY ORDER (mandatory): this runs AFTER (a) migration 0023 made job_type
-- nullable AND (b) the WS9 step-3 chora-creation image is live (it no longer reads
-- or writes job_type — the dispatch + repo + events branch on intent/input_kind,
-- migration 0022). The column's information is fully carried by intent + input_kind
-- (0022 backfilled every row). Dropping it now references nothing.
--
-- Idempotent: DROP ... IF EXISTS. Drops the orphaned job_type CHECK first.
-- Safe for the 13-DB ordered runner.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;
ALTER TABLE question_generation_jobs DROP COLUMN IF EXISTS job_type;

COMMIT;
