-- =============================================================================
-- chora-creation : 0022_question_job_compose_intent.up.sql
--
-- ADR-195 WS2 — unify AI-Assist authoring into ONE `compose` operation.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- ADR           : docs/architecture/adrs/adr-195-compose-authoring-unification.md
-- Jira          : CHO-1877 (WS2) · epic CHO-1875
--
-- The five-value job_type enum fragmented ONE capability across three
-- orthogonal axes (intent × input × cardinality). This migration introduces the
-- universal discriminant columns of the compose model:
--   - intent      {new_question, model_answer_fill, image_regen}  (the domain VO)
--   - input_kind  {prompt, source_files, by_hand}                 (Input.Kind())
-- and widens the job_type CHECK to ALSO allow 'compose' so the WS3 create
-- handler can write it for new jobs.
--
-- NON-DESTRUCTIVE + NON-BREAKING (deliberate refinement of ADR-195 D6's
-- "rows -> compose" wording): WS2 lands BEFORE WS3-WS5, so the still-live old
-- adapters (the 5-way generation dispatch, the :1837 accept gate, the image_regen
-- terminal branch) keep READING job_type. Rewriting a live-read discriminant
-- mid-transition would break them. So existing rows' job_type is LEFT INTACT; we
-- only backfill the NEW discriminant columns. The legacy job_type column is
-- dropped wholesale in WS9 once every adapter branches on intent.
--
-- Idempotent (mirrors 0019/0020 CHECK-widening + 0021 ADD COLUMN IF NOT EXISTS):
-- re-runs converge; columns add IF NOT EXISTS; CHECKs DROP IF EXISTS then ADD;
-- backfill touches only rows WHERE the column IS NULL. Safe for the 13-DB
-- ordered runner (one bad early migration must not strand downstream DBs).
-- =============================================================================

BEGIN;

-- 1. Compose discriminant columns. NULLABLE during the transition: an INSERT by
--    the pre-WS3 handler omits them and must still succeed; WS3 starts
--    populating them; a NOT NULL tightening (if wanted) lands in WS9.
ALTER TABLE question_generation_jobs
    ADD COLUMN IF NOT EXISTS intent     TEXT NULL,
    ADD COLUMN IF NOT EXISTS input_kind TEXT NULL;

-- 2. CHECK constraints for the new columns (NULL allowed during the transition).
ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_intent_check;
ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_intent_check
    CHECK (intent IS NULL OR intent IN ('new_question', 'model_answer_fill', 'image_regen'));

ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_input_kind_check;
ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_input_kind_check
    CHECK (input_kind IS NULL OR input_kind IN ('prompt', 'source_files', 'by_hand'));

-- 3. Widen the job_type CHECK to ALSO allow 'compose'. The five legacy values
--    STAY valid (the live old adapters still write/read them through the
--    transition). Mirrors the 0019/0020 DROP-IF-EXISTS + ADD pattern.
ALTER TABLE question_generation_jobs
    DROP CONSTRAINT IF EXISTS question_generation_jobs_job_type_check;
ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen', 'manual_draft', 'compose'));

-- 4. Backfill the discriminant columns for existing rows (idempotent — only
--    WHERE the column IS NULL, so re-runs and any post-WS3 rows are untouched).
--    intent: ai_model_answer -> model_answer_fill; image_regen -> image_regen;
--            ai_draft | batch_source_material | manual_draft -> new_question.
UPDATE question_generation_jobs
   SET intent = CASE
       WHEN job_type = 'ai_model_answer' THEN 'model_answer_fill'
       WHEN job_type = 'image_regen'     THEN 'image_regen'
       ELSE 'new_question'
   END
 WHERE intent IS NULL;

--    input_kind mirrors Input.Kind() (precedence by_hand > source_files > prompt):
--      manual_draft               -> by_hand (candidates supplied inline, no LLM)
--      ai_model_answer/image_regen -> prompt  (single-Q pure-LLM / patch refine)
--      source_blob_uri or settings.source_files present -> source_files (RAG)
--      else (ai_draft prompt path) -> prompt
UPDATE question_generation_jobs
   SET input_kind = CASE
       WHEN job_type = 'manual_draft'                     THEN 'by_hand'
       WHEN job_type IN ('ai_model_answer', 'image_regen') THEN 'prompt'
       WHEN (source_blob_uri IS NOT NULL AND source_blob_uri <> '')
         OR (jsonb_typeof(settings -> 'source_files') = 'array'
             AND jsonb_array_length(settings -> 'source_files') > 0)
                                                          THEN 'source_files'
       ELSE 'prompt'
   END
 WHERE input_kind IS NULL;

COMMENT ON COLUMN question_generation_jobs.intent IS
'ADR-195: the compose domain discriminant {new_question, model_answer_fill,
image_regen} — the universal replacement for the deprecated job_type enum,
backfilled from job_type in migration 0022. NULL only for transitional rows
written by a pre-WS3 handler.';

COMMENT ON COLUMN question_generation_jobs.input_kind IS
'ADR-195: the compose generation-seed classification {prompt, source_files,
by_hand} — mirrors the Input.Kind() VO and the .v2 event input_kind. NULL only
for transitional pre-WS3 rows.';

COMMIT;
