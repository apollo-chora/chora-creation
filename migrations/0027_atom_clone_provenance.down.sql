-- Reverts 0027_atom_clone_provenance.up.sql (drops the clone provenance column).
BEGIN;

ALTER TABLE learning_atoms
    DROP COLUMN IF EXISTS cloned_from_atom_id;

COMMIT;
