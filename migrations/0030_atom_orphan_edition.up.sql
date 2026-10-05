-- =============================================================================
-- chora-creation : 0030_atom_orphan_edition.up.sql
--
-- ADR-229 Amendment A1 (CHO-2132) — the singleton orphan edition. When a
-- reused atom is withdrawn (reuse-visibility narrowed / un-shared / archived)
-- while >=1 consumer holds an active AtomUsageGrant, chora-creation
-- materialises exactly ONE immutable orphan edition per
-- (atom, last-published-revision): a clone of that revision carrying the
-- three provenance columns added here. Grants are NEVER revoked by a
-- withdrawal — stranded consumers repoint to the orphan instead.
--
--   orphaned_from_atom_id       — the withdrawn original (NULL for non-orphans)
--   orphaned_source_revision_id — the pinned last-published revision
--   orphaned_at                 — the mint instant
--
-- Invariants enforced at the DB layer:
--   * SINGLETON: partial UNIQUE on (orphaned_from_atom_id,
--     orphaned_source_revision_id) — the idempotent mint's conflict target.
--     Repeat withdrawals at the same revision converge on one orphan; a later
--     published revision withdrawn again gets its own.
--   * ALL-OR-NONE: the three columns are set together or not at all.
--   * PRIVATE-FOREVER: an orphan row can never carry a reuse_visibility other
--     than 'private' — the cross-lane picker contract (WS-2 excludes orphans
--     from tenant/saved disjuncts purely via reuse_visibility; orphans are
--     reachable ONLY via repointed grants).
--
-- Additive + idempotent (ADD COLUMN IF NOT EXISTS / guarded constraints) so a
-- re-run is a no-op. No RLS change: the columns live on learning_atoms and
-- inherit its tenant_isolation policy.
-- =============================================================================
BEGIN;

ALTER TABLE learning_atoms
    ADD COLUMN IF NOT EXISTS orphaned_from_atom_id UUID NULL,
    ADD COLUMN IF NOT EXISTS orphaned_source_revision_id UUID NULL,
    ADD COLUMN IF NOT EXISTS orphaned_at TIMESTAMPTZ NULL;

COMMENT ON COLUMN learning_atoms.orphaned_from_atom_id IS
'ADR-229 A1 (CHO-2132) — the withdrawn original this frozen orphan edition was minted from (NULL for non-orphans). Non-NULL rows are FROZEN + reuse_visibility=private forever.';
COMMENT ON COLUMN learning_atoms.orphaned_source_revision_id IS
'ADR-229 A1 — the last-published revision the orphan pins (second half of the singleton key).';
COMMENT ON COLUMN learning_atoms.orphaned_at IS
'ADR-229 A1 — when the orphan edition was materialised.';

-- SINGLETON — exactly one orphan per (atom, last-published-revision). The
-- idempotent mint INSERTs ON CONFLICT against this index.
CREATE UNIQUE INDEX IF NOT EXISTS learning_atoms_orphan_singleton_idx
    ON learning_atoms (orphaned_from_atom_id, orphaned_source_revision_id)
    WHERE orphaned_from_atom_id IS NOT NULL;

-- ALL-OR-NONE + PRIVATE-FOREVER (guarded for idempotent re-run).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'learning_atoms_orphan_fields_chk'
          AND conrelid = 'learning_atoms'::regclass
    ) THEN
        ALTER TABLE learning_atoms ADD CONSTRAINT learning_atoms_orphan_fields_chk CHECK (
            (orphaned_from_atom_id IS NULL
                AND orphaned_source_revision_id IS NULL
                AND orphaned_at IS NULL)
            OR (orphaned_from_atom_id IS NOT NULL
                AND orphaned_source_revision_id IS NOT NULL
                AND orphaned_at IS NOT NULL)
        );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'learning_atoms_orphan_private_chk'
          AND conrelid = 'learning_atoms'::regclass
    ) THEN
        ALTER TABLE learning_atoms ADD CONSTRAINT learning_atoms_orphan_private_chk CHECK (
            orphaned_from_atom_id IS NULL OR reuse_visibility = 'private'
        );
    END IF;
END $$;

COMMIT;
