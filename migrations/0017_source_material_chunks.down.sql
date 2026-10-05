-- =============================================================================
-- chora-creation : 0017_source_material_chunks.down.sql — reverse
-- 0017_source_material_chunks.up.sql.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    DROP COLUMN IF EXISTS proposed_test_set_jsonb;

DROP TABLE IF EXISTS source_material_chunks;

COMMIT;
