-- =============================================================================
-- chora-creation : 0026_rename_item_banks_to_question_banks.down.sql
--
-- Reverse 0026_rename_item_banks_to_question_banks.up.sql: rename the
-- QuestionBank schema back to the 0025 ItemBank names so that down(0026) leaves
-- the schema exactly as 0025_item_banks.up.sql created it (down(0025) then drops
-- it). Symmetric + guarded for idempotency; one atomic BEGIN/COMMIT.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1. Tables.
-- -----------------------------------------------------------------------------
ALTER TABLE IF EXISTS question_banks      RENAME TO item_banks;
ALTER TABLE IF EXISTS question_bank_items RENAME TO item_bank_items;

-- -----------------------------------------------------------------------------
-- 2. Columns.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'item_banks' AND column_name = 'question_bank_id'
    ) THEN
        ALTER TABLE item_banks RENAME COLUMN question_bank_id TO item_bank_id;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'item_bank_items' AND column_name = 'question_bank_id'
    ) THEN
        ALTER TABLE item_bank_items RENAME COLUMN question_bank_id TO item_bank_id;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 3. Visibility enum type.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'question_bank_visibility') THEN
        ALTER TYPE question_bank_visibility RENAME TO item_bank_visibility;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 4. Indexes.
-- -----------------------------------------------------------------------------
ALTER INDEX IF EXISTS idx_question_banks_tenant               RENAME TO idx_item_banks_tenant;
ALTER INDEX IF EXISTS idx_question_banks_owner                RENAME TO idx_item_banks_owner;
ALTER INDEX IF EXISTS idx_question_banks_visibility           RENAME TO idx_item_banks_visibility;
ALTER INDEX IF EXISTS idx_question_banks_tags                 RENAME TO idx_item_banks_tags;
ALTER INDEX IF EXISTS idx_question_bank_items_position_active RENAME TO idx_item_bank_items_position_active;
ALTER INDEX IF EXISTS idx_question_bank_items_question        RENAME TO idx_item_bank_items_question;
ALTER INDEX IF EXISTS idx_question_bank_items_tenant          RENAME TO idx_item_bank_items_tenant;

-- -----------------------------------------------------------------------------
-- 5. updated_at trigger.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgname = 'trg_question_banks_updated_at' AND NOT tgisinternal
    ) THEN
        ALTER TRIGGER trg_question_banks_updated_at ON item_banks
            RENAME TO trg_item_banks_updated_at;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 6. Constraints.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_banks_name_len') THEN
        ALTER TABLE item_banks RENAME CONSTRAINT question_banks_name_len TO item_banks_name_len;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_banks_description_len') THEN
        ALTER TABLE item_banks RENAME CONSTRAINT question_banks_description_len TO item_banks_description_len;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_banks_tags_card') THEN
        ALTER TABLE item_banks RENAME CONSTRAINT question_banks_tags_card TO item_banks_tags_card;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_banks_pkey') THEN
        ALTER TABLE item_banks RENAME CONSTRAINT question_banks_pkey TO item_banks_pkey;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_bank_items_position_nonneg') THEN
        ALTER TABLE item_bank_items RENAME CONSTRAINT question_bank_items_position_nonneg TO item_bank_items_position_nonneg;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_bank_items_pkey') THEN
        ALTER TABLE item_bank_items RENAME CONSTRAINT question_bank_items_pkey TO item_bank_items_pkey;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'question_bank_items_question_bank_id_fkey') THEN
        ALTER TABLE item_bank_items RENAME CONSTRAINT question_bank_items_question_bank_id_fkey TO item_bank_items_item_bank_id_fkey;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 7. Re-grant on the restored table names (idempotent; mirrors 0025).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE item_banks, item_bank_items
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE item_banks, item_bank_items
    TO chora_creation_app_ro;

COMMIT;
