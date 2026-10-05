-- =============================================================================
-- chora-creation : 0032_atom_backfill_runs.up.sql
--
-- CHO-2159 — make the topic-tag backfill run report RETURNABLE.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : CHO-2159 (async backfill 202 + poll, 2026-07-14)
--
-- THE DEFECT. POST /api/internal/atoms/backfill-topic-tags (CHO-2142) was a long
-- SYNCHRONOUS endpoint: 74 atoms x ~3.5s of model-gateway classification ≈ 280s,
-- behind chora-creation's http.Server{WriteTimeout: 15s}. The server closed the
-- connection at 15s and the handler — still running — wrote its report to a
-- socket nobody was listening on. Measured live 2026-07-14: HTTP 000 / empty
-- reply after 280s, while the pod log showed the run HAD completed
-- (scanned=117 candidates=74 failed=2). A limit=3 run returned 200 in 11.9s,
-- pinning the boundary at the WriteTimeout exactly.
--
-- On a REAL run that is dangerous, not merely annoying: it writes
-- learning_atoms.tags and re-emits atom.published.v1 for up to 74 atoms while the
-- operator gets HTTP 000 and never sees failed[]. A mutating operation whose
-- report cannot be delivered is UNOBSERVABLE BY CONSTRUCTION — which is exactly
-- why that failed=2 was undiagnosable: the reasons existed only in the
-- unreturnable body.
--
-- THE FIX. The run is now accepted (202), executed DETACHED, and its report is
-- POLLED. WriteTimeout is untouched (it is a whole-server setting; widening it
-- for one internal endpoint would weaken every other route's slow-loris posture).
--
-- WHY THIS TABLE EXISTS — i.e. why the report is persisted and not held in a map.
-- A 280s run outlives a pod restart, and the operator's GET can land on a
-- different replica than the POST. An in-memory run record would reintroduce the
-- very unobservability this story removes: the report would be lost again, just
-- at a different layer. The report must be DURABLE or it is not a fix.
--
-- Soft-delete only (deleted_at) per ddd-enforcement Invariant #4. RLS via a
-- tenant_isolation policy on current_setting('chora.tenant_id') — see the ⚠
-- below. Cross-DB queries FORBIDDEN. The whole migration runs inside one
-- BEGIN/COMMIT so a mid-statement failure rolls back atomically.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- atom_backfill_runs — one row per backfill run (the durable run report)
--
-- status is the lifecycle:
--   running   — the detached execution is in flight (or its pod died)
--   completed — the run finished. PER-ATOM failures live in report->'failed' and
--               do NOT make the run itself failed: the run ran, and reported them
--   failed    — the run ABORTED (collaborator/DB error, or the run timeout), or
--               was reclaimed as stranded by the stale-sweep. `error` says why
--
-- report holds the bodies the operator actually came for:
--   {"proposed":[{atom_id,title,tags}], "failed":[{atom_id,title,reason}]}
-- -----------------------------------------------------------------------------
CREATE TABLE atom_backfill_runs (
    run_id       UUID        PRIMARY KEY,              -- UUIDv7, minted in Go
    tenant_id    UUID        NOT NULL,
    dry_run      BOOLEAN     NOT NULL DEFAULT FALSE,
    status       TEXT        NOT NULL,

    -- Echo of the operator's request, so a poll shows what was ACTUALLY run
    -- rather than what the poller assumed.
    limit_n      INTEGER     NOT NULL DEFAULT 0,
    reemit_salt  TEXT        NOT NULL DEFAULT '',

    scanned      INTEGER     NOT NULL DEFAULT 0,
    candidates   INTEGER     NOT NULL DEFAULT 0,
    tagged       INTEGER     NOT NULL DEFAULT 0,
    emitted      INTEGER     NOT NULL DEFAULT 0,

    report       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    error        TEXT        NOT NULL DEFAULT '',      -- RUN-level abort reason

    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ NULL,

    CONSTRAINT atom_backfill_runs_status_check
        CHECK (status IN ('running', 'completed', 'failed')),

    CONSTRAINT atom_backfill_runs_limit_nonneg
        CHECK (limit_n >= 0),

    -- TERMINALITY, ENFORCED IN THE DATABASE. A terminal run MUST carry
    -- finished_at, and a running run MUST NOT. This is the invariant the whole
    -- story turns on — a row that is terminal in status but never stamped (or
    -- stamped but still `running`) is precisely the "the operator polls a lie"
    -- state, and the schema refuses to represent it.
    CONSTRAINT atom_backfill_runs_terminal_is_stamped
        CHECK (
            (status = 'running' AND finished_at IS NULL)
            OR (status IN ('completed', 'failed') AND finished_at IS NOT NULL)
        )
);

-- Operator list / most-recent-run lookups.
CREATE INDEX idx_atom_backfill_runs_tenant_started
    ON atom_backfill_runs (tenant_id, started_at DESC)
    WHERE deleted_at IS NULL;

-- The stale-sweep's exact predicate (tenant + running + started_at < cutoff).
-- Partial: the sweep only ever looks at in-flight rows, which are a handful.
CREATE INDEX idx_atom_backfill_runs_stranded
    ON atom_backfill_runs (tenant_id, started_at)
    WHERE status = 'running' AND deleted_at IS NULL;

-- Reuse the shared updated_at trigger (defined in 0001_initial.sql).
CREATE TRIGGER trg_atom_backfill_runs_updated_at
    BEFORE UPDATE ON atom_backfill_runs
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

-- -----------------------------------------------------------------------------
-- RLS — tenant isolation.
--
-- ⚠ THE GUC IS `chora.tenant_id`, NOT `app.tenant_id`.
--
-- Migration 0010 (ai_assist_jobs) keyed its policy on current_setting('app.tenant_id')
-- while chora-creation's RunInTenantTx sets `chora.tenant_id` — so the policy
-- evaluated NULL, every row failed `tenant_id = NULL`, and POST /api/atoms/ai-assist
-- 500'd until 0015 re-keyed it. Every other table in this database
-- (0006/0007/0008/0016/0017/0025) reads `chora.tenant_id`. Getting this wrong is
-- not a loud failure — under FORCE RLS a read silently matches NOTHING and a
-- write is rejected, which is the CHO-2170 defect class (a purge that "succeeded"
-- having deleted nothing).
--
-- FORCE: the table owner (chora_creation_migrate) is subject to the policy too,
-- so no path — not even a migration — can read another tenant's run report.
-- FOR ALL USING (...) also supplies the INSERT/UPDATE WITH CHECK, so a row can
-- neither be read nor written outside its tenant's transaction context.
-- -----------------------------------------------------------------------------
ALTER TABLE atom_backfill_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE atom_backfill_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON atom_backfill_runs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- GRANTs — explicit + idempotent (also covered by the default-privileges ALTER
-- in 9999_grant_app_roles.sql, but specified here for clarity, per 0025).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE atom_backfill_runs
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE atom_backfill_runs
    TO chora_creation_app_ro;

COMMENT ON TABLE atom_backfill_runs IS
    'CHO-2159 — durable report for the async topic-tag backfill. The run is executed detached (a 74-atom run is ~280s vs a 15s WriteTimeout) and polled via GET /api/internal/atoms/backfill-topic-tags/{run_id}. Persisted, not in-memory: the run outlives a pod restart and the poll can land on any replica.';

COMMENT ON COLUMN atom_backfill_runs.report IS
    'The run report body: {"proposed":[{atom_id,title,tags}],"failed":[{atom_id,title,reason}]}. failed[] carries the per-atom reasons that were undiagnosable while the report could not be returned.';

COMMENT ON COLUMN atom_backfill_runs.error IS
    'RUN-level abort reason (empty on a completed run, even one carrying per-atom failures in report->failed). Also carries the stale-sweep''s "stranded" reason when a pod died mid-run.';

COMMIT;
