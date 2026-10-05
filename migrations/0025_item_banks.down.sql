-- =============================================================================
-- chora-creation : 0025_item_banks.down.sql — reverse 0025_item_banks.up.sql.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS item_bank_items;
DROP TABLE IF EXISTS item_banks;
DROP TYPE  IF EXISTS item_bank_visibility;

COMMIT;
