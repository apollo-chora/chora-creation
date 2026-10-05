-- 0029_atom_reuse_visibility.down.sql

DROP INDEX IF EXISTS learning_atoms_reuse_visibility_idx;

ALTER TABLE learning_atoms
    DROP COLUMN IF EXISTS reuse_visibility;
