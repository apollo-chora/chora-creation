-- =============================================================================
-- chora-creation : 0014_atom_phase1.down.sql
--
-- Reverse of 0014_atom_phase1.up.sql. Restores:
--   - learning_atom_question_type ENUM type back to `atom_type`.
--   - atom_type column (5-enum) backfilled from question_type::text where it
--     parses; rows with unknown values fall back to NULL.
--   - title NOT NULL (guarded by NULL-check; rows with empty title would
--     fail the rename — handler enforced non-empty pre-rollback, so this is
--     the safe assumption).
--   - DROP the new columns: stem, cognitive_level, subject, media_assets,
--     imda_dimension_tags, author_note.
--
-- WARNING: this rollback is data-lossy for atoms whose question_type is
-- outside the original 5-value set (mcq/flashcard/video/essay/outline) —
-- those rows land NULL on atom_type. Caller MUST audit before rollback.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Drop new constraints + columns added in up.sql, reverse order.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS author_note;
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS imda_dimension_tags;
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS media_assets;
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS subject;
ALTER TABLE learning_atoms DROP CONSTRAINT IF EXISTS learning_atoms_cognitive_level_check;
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS cognitive_level;
ALTER TABLE learning_atoms DROP COLUMN IF EXISTS stem;

-- -----------------------------------------------------------------------------
-- Restore title NOT NULL. Guard with NULL-check + set default empty so
-- ALTER NOT NULL does not fail on rows the down-script can't observe.
-- -----------------------------------------------------------------------------
UPDATE learning_atoms SET title = '' WHERE title IS NULL;
ALTER TABLE learning_atoms ALTER COLUMN title SET NOT NULL;

-- -----------------------------------------------------------------------------
-- Restore the `atom_type` ENUM type identifier.
-- -----------------------------------------------------------------------------
ALTER TYPE learning_atom_question_type RENAME TO atom_type;

-- -----------------------------------------------------------------------------
-- Restore atom_type column. Backfill from question_type::text where it
-- belongs to the original 5-value set; else NULL.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN atom_type atom_type;

UPDATE learning_atoms
SET atom_type = (
    CASE
        WHEN question_type IN ('mcq', 'flashcard', 'video', 'essay', 'outline')
            THEN question_type::atom_type
        ELSE NULL
    END
);

ALTER TABLE learning_atoms DROP COLUMN question_type;

COMMIT;
