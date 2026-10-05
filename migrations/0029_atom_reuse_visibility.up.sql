-- 0029_atom_reuse_visibility.up.sql
--
-- ADR-229 WS-1 (CHO-2127): the author-owned reuse-consent flag. Who may
-- REUSE this atom (pickers / snapshots / quiz arming — playback untouched):
--   private (default — consent-first), friends, tenant.
-- Creation owns the bit; sharing owns license/promotion/grants (decoupled).
-- Existing atoms default to private: nothing widens without the author.

ALTER TABLE learning_atoms
    ADD COLUMN IF NOT EXISTS reuse_visibility TEXT NOT NULL DEFAULT 'private'
        CHECK (reuse_visibility IN ('private', 'friends', 'tenant'));

-- Picker disjunct scans (WS-2) filter published atoms by audience.
CREATE INDEX IF NOT EXISTS learning_atoms_reuse_visibility_idx
    ON learning_atoms (tenant_id, reuse_visibility)
    WHERE deleted_at IS NULL;
