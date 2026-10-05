-- =============================================================================
-- chora-creation : 0030_atom_orphan_edition.down.sql
--
-- Reverts the ADR-229 A1 orphan-edition columns + constraints. LOSSY: any
-- minted orphan rows lose their provenance markers (the rows themselves stay
-- — soft-delete-only invariant; hard-deleting content is forbidden). Grants /
-- collection entries already repointed at those orphan atom_ids keep working
-- as ordinary atom refs. Run only on a deliberate rollback.
-- =============================================================================
BEGIN;

ALTER TABLE learning_atoms
    DROP CONSTRAINT IF EXISTS learning_atoms_orphan_private_chk,
    DROP CONSTRAINT IF EXISTS learning_atoms_orphan_fields_chk;

DROP INDEX IF EXISTS learning_atoms_orphan_singleton_idx;

ALTER TABLE learning_atoms
    DROP COLUMN IF EXISTS orphaned_at,
    DROP COLUMN IF EXISTS orphaned_source_revision_id,
    DROP COLUMN IF EXISTS orphaned_from_atom_id;

COMMIT;
