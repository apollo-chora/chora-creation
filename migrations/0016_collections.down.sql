-- =============================================================================
-- chora-creation : 0016_collections.down.sql — reverse 0016_collections.up.sql.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS collection_atoms;
DROP TABLE IF EXISTS collections;
DROP TYPE  IF EXISTS collection_visibility;

COMMIT;
