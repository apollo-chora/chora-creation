-- =============================================================================
-- chora-creation : 0027_atom_clone_provenance.up.sql
--
-- ADR-199 clone-as-variant provenance (Wave 2). Adds
-- learning_atoms.cloned_from_atom_id — the source atom_id a clone was derived
-- from (NULL for originals). Additive + idempotent (ADD COLUMN IF NOT EXISTS)
-- so a re-run is a no-op. No RLS change: the new column lives on the existing
-- learning_atoms table and inherits its tenant_isolation policy.
-- =============================================================================
BEGIN;

ALTER TABLE learning_atoms
    ADD COLUMN IF NOT EXISTS cloned_from_atom_id UUID NULL;

COMMENT ON COLUMN learning_atoms.cloned_from_atom_id IS
'ADR-199 clone-as-variant provenance — the source atom_id this atom was cloned from (NULL for originals).';

COMMIT;
