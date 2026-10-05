-- =============================================================================
-- chora-creation : 0026_rename_item_banks_to_question_banks.up.sql
--
-- Owner-approved domain-vocabulary refinement (2026-06-28): the aggregate built
-- in 0025 as "ItemBank" is renamed to "QuestionBank". It pools `Question`
-- entities and keys on `question_id`; "item" is a forbidden synonym in Chora's
-- canonical domain vocabulary (see .claude/skills/domain-vocabulary), and
-- `Question` is the real existing chora-creation entity.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
--
-- This migration RENAMES forward the schema created by 0025_item_banks.up.sql
-- (which is intentionally left untouched as history). Every environment applies
-- 0025 (create `item_banks` / `item_bank_items`) and then 0026 (rename to
-- `question_banks` / `question_bank_items`) in order — there is no env where the
-- table is born under the new name.
--
-- It renames every object that 0025 EXPLICITLY NAMED (tables, the visibility
-- enum, indexes, the updated_at trigger, the CHECK constraints) plus the columns
-- (REQUIRED: the chora-creation pg adapter's SQL references `question_bank_id`
-- and `::question_bank_visibility` after the Go rename, so the live schema must
-- match) and the auto-generated PK/FK constraint names. After this migration the
-- live schema carries ZERO `item_bank` identifiers.
--
-- Whole migration runs inside one BEGIN/COMMIT so a mid-statement failure rolls
-- back atomically (no stranded objects — migration-runner fail-fast wedge
-- learnings). IF EXISTS / existence-guarded DO blocks make every rename a no-op
-- on re-run (idempotent).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1. Tables (IF EXISTS → re-run no-op once already renamed).
-- -----------------------------------------------------------------------------
ALTER TABLE IF EXISTS item_banks      RENAME TO question_banks;
ALTER TABLE IF EXISTS item_bank_items RENAME TO question_bank_items;

-- -----------------------------------------------------------------------------
-- 2. Columns — REQUIRED so the pg adapter's `question_bank_id` SQL matches the
--    live column. Guarded (RENAME COLUMN has no IF EXISTS for the column).
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'question_banks' AND column_name = 'item_bank_id'
    ) THEN
        ALTER TABLE question_banks RENAME COLUMN item_bank_id TO question_bank_id;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'question_bank_items' AND column_name = 'item_bank_id'
    ) THEN
        ALTER TABLE question_bank_items RENAME COLUMN item_bank_id TO question_bank_id;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 3. Visibility enum type — REQUIRED (the pg adapter casts
--    `::question_bank_visibility`). ALTER TYPE has no IF EXISTS → guard.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'item_bank_visibility') THEN
        ALTER TYPE item_bank_visibility RENAME TO question_bank_visibility;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 4. Indexes.
-- -----------------------------------------------------------------------------
ALTER INDEX IF EXISTS idx_item_banks_tenant               RENAME TO idx_question_banks_tenant;
ALTER INDEX IF EXISTS idx_item_banks_owner                RENAME TO idx_question_banks_owner;
ALTER INDEX IF EXISTS idx_item_banks_visibility           RENAME TO idx_question_banks_visibility;
ALTER INDEX IF EXISTS idx_item_banks_tags                 RENAME TO idx_question_banks_tags;
ALTER INDEX IF EXISTS idx_item_bank_items_position_active RENAME TO idx_question_bank_items_position_active;
ALTER INDEX IF EXISTS idx_item_bank_items_question        RENAME TO idx_question_bank_items_question;
ALTER INDEX IF EXISTS idx_item_bank_items_tenant          RENAME TO idx_question_bank_items_tenant;

-- -----------------------------------------------------------------------------
-- 5. updated_at trigger (named in 0025). ALTER TRIGGER has no IF EXISTS → guard.
--    By now the trigger lives on the renamed `question_banks` table.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgname = 'trg_item_banks_updated_at' AND NOT tgisinternal
    ) THEN
        ALTER TRIGGER trg_item_banks_updated_at ON question_banks
            RENAME TO trg_question_banks_updated_at;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 6. Constraints — the explicit CHECKs named in 0025 plus the auto-generated
--    PK/FK names, so no `item_bank` identifier survives. RENAME CONSTRAINT has
--    no IF EXISTS → guard each on pg_constraint.conname.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_banks_name_len') THEN
        ALTER TABLE question_banks RENAME CONSTRAINT item_banks_name_len TO question_banks_name_len;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_banks_description_len') THEN
        ALTER TABLE question_banks RENAME CONSTRAINT item_banks_description_len TO question_banks_description_len;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_banks_tags_card') THEN
        ALTER TABLE question_banks RENAME CONSTRAINT item_banks_tags_card TO question_banks_tags_card;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_banks_pkey') THEN
        ALTER TABLE question_banks RENAME CONSTRAINT item_banks_pkey TO question_banks_pkey;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_bank_items_position_nonneg') THEN
        ALTER TABLE question_bank_items RENAME CONSTRAINT item_bank_items_position_nonneg TO question_bank_items_position_nonneg;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_bank_items_pkey') THEN
        ALTER TABLE question_bank_items RENAME CONSTRAINT item_bank_items_pkey TO question_bank_items_pkey;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'item_bank_items_item_bank_id_fkey') THEN
        ALTER TABLE question_bank_items RENAME CONSTRAINT item_bank_items_item_bank_id_fkey TO question_bank_items_question_bank_id_fkey;
    END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 7. RLS policy + GRANTs.
--    The `tenant_isolation` policy auto-follows the renamed table and its name
--    carries no `item_bank` token, so there is nothing to rename. Re-grant the
--    app roles on the renamed tables (privileges follow the OID on rename;
--    restated for clarity + idempotency, mirroring 0025).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE question_banks, question_bank_items
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE question_banks, question_bank_items
    TO chora_creation_app_ro;

COMMIT;
