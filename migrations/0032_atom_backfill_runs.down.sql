-- =============================================================================
-- chora-creation : 0032_atom_backfill_runs.down.sql
--
-- Reverse of 0032: drop the durable topic-tag backfill run report.
--
-- ⚠ LOSSY, AND DELIBERATELY SO. Every run report — including the per-atom
-- failed[] reasons for runs that mutated learning_atoms.tags and re-emitted
-- atom.published.v1 — is destroyed. Those reports are the ONLY record that a
-- given backfill ran and what it did to which atom.
--
-- Rolling this back also returns POST /api/internal/atoms/backfill-topic-tags to
-- the state CHO-2159 exists to fix: the route fails loud with
-- CREATION_BACKFILL_RUNS_NOT_WIRED (it refuses to run a mutation whose report
-- cannot be persisted) rather than silently reverting to the old synchronous
-- handler whose report could never be returned. That refusal is the correct
-- behaviour — a mutating run nobody can observe is worse than no run.
--
-- The count of reports about to be destroyed is announced BEFORE it happens; a
-- silent loss of the audit trail of a mutating operation is exactly the kind of
-- thing that must never happen quietly.
--
-- The policy/indexes/trigger are dropped implicitly with the table; the DROPs
-- below are explicit only where DROP TABLE would not cover them.
-- =============================================================================

BEGIN;

DO $$
DECLARE
    total     BIGINT;
    mutating  BIGINT;
BEGIN
    SELECT count(*) INTO total FROM atom_backfill_runs;
    SELECT count(*) INTO mutating
        FROM atom_backfill_runs
        WHERE dry_run = FALSE AND (tagged > 0 OR emitted > 0);

    RAISE NOTICE 'CHO-2159 ROLLBACK: destroying % backfill run report(s).', total;

    IF mutating > 0 THEN
        RAISE WARNING 'LOSSY: % of those recorded a REAL run that wrote learning_atoms.tags and/or re-emitted atom.published.v1. Their per-atom failed[] reasons are the only record of what that mutation did, and they are being destroyed. Export them first if you need them.', mutating;
    END IF;
END
$$;

DROP TABLE IF EXISTS atom_backfill_runs;

COMMIT;
