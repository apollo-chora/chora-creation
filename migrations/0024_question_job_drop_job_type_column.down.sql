-- =============================================================================
-- chora-creation : 0024_question_job_drop_job_type_column.down.sql  (rollback)
--
-- Re-create the job_type column + CHECK and re-derive its value from the compose
-- discriminant (intent + input_kind), the inverse of migration 0022's backfill.
-- Restores the pre-drop shape. The runner skips .down.sql (forward-only); this is
-- for manual recovery only.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    ADD COLUMN IF NOT EXISTS job_type TEXT;

-- Re-derive job_type from intent + input_kind (inverse of 0022):
--   model_answer_fill            -> ai_model_answer
--   image_regen                  -> image_regen
--   new_question + by_hand       -> manual_draft
--   new_question + source_files  -> batch_source_material
--   new_question + prompt        -> ai_draft
UPDATE question_generation_jobs
   SET job_type = CASE
       WHEN intent = 'model_answer_fill'                         THEN 'ai_model_answer'
       WHEN intent = 'image_regen'                               THEN 'image_regen'
       WHEN intent = 'new_question' AND input_kind = 'by_hand'   THEN 'manual_draft'
       WHEN intent = 'new_question' AND input_kind = 'source_files' THEN 'batch_source_material'
       ELSE 'ai_draft'
   END
 WHERE job_type IS NULL;

ALTER TABLE question_generation_jobs
    ADD CONSTRAINT question_generation_jobs_job_type_check
    CHECK (job_type IN ('ai_model_answer', 'ai_draft', 'batch_source_material', 'image_regen', 'manual_draft', 'compose'));

ALTER TABLE question_generation_jobs ALTER COLUMN job_type SET NOT NULL;

COMMIT;
