-- =============================================================================
-- chora-creation : 0031_collection_audience.up.sql
--
-- ADR-233 D7 — ONE audience vocabulary: private | friends | tenant.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : WS-4 (Collections → study list, 2026-07-14)
--
-- Before this migration the platform expressed ONE domain concept — audience —
-- two incompatible ways:
--
--   learning_atoms.reuse_visibility  TEXT + CHECK    private | friends | tenant
--   collections.visibility           PG ENUM         PRIVATE | TENANT_INTERNAL | PUBLIC
--
-- Different set, different type, different case. This migration collapses the
-- collection side onto the atom side, so `friends` resolves against the SAME
-- friend set the ADR-229 reuse gate already dials (GetReuseContext, ADR-230).
--
-- PUBLIC IS RETIRED — and it is not a downgrade, it is truth-telling:
--
--   RLS on `collections` is `tenant_id = current_setting('chora.tenant_id')`
--   (0016_collections.up.sql:84-85), so a PUBLIC collection was NEVER readable
--   cross-tenant — the row is invisible. ADR-229 fork (a) locks cross-tenant
--   distribution as a *syndication* concern (clone + provenance), never RLS
--   widening. So PUBLIC ≡ TENANT_INTERNAL in effect, and a visibility level that
--   silently means the same as the one below it is a trap for the next reader.
--
--   PUBLIC also did double duty as the BP-01 gate for cross-tenant atom
--   membership (collection.go AddAtom) — a path RLS made unreachable anyway.
--   One enum value doing two incoherent jobs, neither of which worked. Retiring
--   the value retires the rule (see collection.go invariant 4).
--
-- Value map:  PRIVATE → private · TENANT_INTERNAL → tenant · PUBLIC → tenant
--
-- The count of collapsed PUBLIC rows is RAISE NOTICE'd — a silent widening of
-- somebody's audience is exactly the kind of thing that must never happen
-- quietly, even when (as here) the effective audience does not change.
--
-- Soft-delete untouched; RLS untouched (no new bypass surface — the register
-- stays at ADR-165/184 + the ADR-191 restrictive embargo).
--
-- -----------------------------------------------------------------------------
-- REPLAY SAFETY (added 2026-07-14 — this migration used to be a lane-wedger)
-- -----------------------------------------------------------------------------
-- Both migration runners (chora-infra/scripts/migrations-runner/runner.sh and
-- apply-cloudsql-migrations.sh) re-apply EVERY *.up.sql for a service in lex
-- order, under `psql --set ON_ERROR_STOP=1`. The runner's own contract says so:
-- "every migration is guarded with IF NOT EXISTS ... Re-runs produce no errors
-- and identical end state" — and "first failure halts the job".
--
-- As first written, a SECOND run of this file mapped every already-lowercased
-- value through a CASE that only knew the OLD uppercase labels, yielding NULL
-- for every row and aborting with:
--
--     ERROR: column "visibility" of relation "collections" contains null values
--
-- That string matches neither of the runner's lenient recovery patterns
-- (`already exists` / `does not exist, skipping`), so the runner declares it
-- FATAL — wedging chora_creation's ENTIRE migration lane: every migration lex-
-- after this one silently never applies. That is not hypothetical; it was the
-- live state of the DB (0029/0030/0031 were hand-applied without their
-- chora_runner_schema_migrations rows, so the runner still considered all three
-- pending and would have replayed them).
--
-- So: the conversion is now guarded on the column still being the enum, and
-- every step below it is individually idempotent — the migration CONVERGES from
-- a half-applied state (hand-applied migrations are exactly how we got here)
-- instead of aborting. End state on a fresh DB is unchanged.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- The conversion itself. Runs ONLY while `visibility` is still the PG enum;
-- on a replay it is a no-op that says so.
-- -----------------------------------------------------------------------------
DO $mig$
DECLARE
    still_enum            BOOLEAN;
    public_count          BIGINT;
    tenant_internal_count BIGINT;
    private_count         BIGINT;
    unmapped              BIGINT;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name   = 'collections'
          AND column_name  = 'visibility'
          AND udt_name     = 'collection_visibility'
    ) INTO still_enum;

    IF NOT still_enum THEN
        RAISE NOTICE 'ADR-233 D7: collections.visibility is already TEXT — conversion previously applied; replaying as a no-op.';
        RETURN;
    END IF;

    -- Truth-telling: report what is about to change BEFORE it changes.
    SELECT count(*) INTO private_count
        FROM collections WHERE visibility::text = 'PRIVATE';
    SELECT count(*) INTO tenant_internal_count
        FROM collections WHERE visibility::text = 'TENANT_INTERNAL';
    SELECT count(*) INTO public_count
        FROM collections WHERE visibility::text = 'PUBLIC';

    RAISE NOTICE 'ADR-233 D7 collection audience migration:';
    RAISE NOTICE '  PRIVATE         -> private : % row(s)', private_count;
    RAISE NOTICE '  TENANT_INTERNAL -> tenant  : % row(s)', tenant_internal_count;
    RAISE NOTICE '  PUBLIC          -> tenant  : % row(s)  << RETIRED level collapsed', public_count;

    IF public_count > 0 THEN
        RAISE NOTICE 'NB: those % PUBLIC collection(s) were never actually public - RLS capped them at the tenant. Collapsing to `tenant` preserves their EFFECTIVE audience exactly.', public_count;
    END IF;

    -- Drop the enum-typed default so the column type can be altered.
    EXECUTE 'ALTER TABLE collections ALTER COLUMN visibility DROP DEFAULT';

    -- Convert the column: PG enum `collection_visibility` -> TEXT, rewriting
    -- every value through the ADR-233 D7 map in the same statement.
    --
    -- The CASE is exhaustive over the enum; the absent ELSE is a fail-loud guard
    -- rather than a silent coercion — an unmapped value must abort the
    -- migration, not quietly become something else.
    EXECUTE $ddl$
        ALTER TABLE collections
            ALTER COLUMN visibility TYPE TEXT
            USING (
                CASE visibility::text
                    WHEN 'PRIVATE'         THEN 'private'
                    WHEN 'TENANT_INTERNAL' THEN 'tenant'
                    WHEN 'PUBLIC'          THEN 'tenant'
                END
            )
    $ddl$;

    -- Fail loud if the USING clause produced a NULL (an enum label we did not
    -- map). NOT NULL on the column means PG will normally have aborted above
    -- already; this is the belt to that braces.
    SELECT count(*) INTO unmapped FROM collections WHERE visibility IS NULL;
    IF unmapped > 0 THEN
        RAISE EXCEPTION 'ADR-233: % collection row(s) carried an unmapped visibility label; refusing to coerce them silently', unmapped;
    END IF;
END
$mig$;

-- -----------------------------------------------------------------------------
-- Everything below is individually idempotent, so the migration converges from
-- a half-applied state rather than aborting on a re-run.
--
-- NOT NULL + the consent-safe default. Default `private` mirrors
-- learning_atoms.reuse_visibility (ADR-229 D1): absent an explicit author
-- choice, a thing is exposed to NOBODY.
-- -----------------------------------------------------------------------------
ALTER TABLE collections
    ALTER COLUMN visibility SET NOT NULL,
    ALTER COLUMN visibility SET DEFAULT 'private';

-- The audience CHECK — guarded on pg_constraint, the same idiom 0030 uses.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname  = 'collections_visibility_check'
          AND conrelid = 'collections'::regclass
    ) THEN
        ALTER TABLE collections
            ADD CONSTRAINT collections_visibility_check
                CHECK (visibility IN ('private', 'friends', 'tenant'));
    END IF;
END
$$;

-- Drop the retired enum type. Nothing else references it (verified: the only
-- column was collections.visibility).
DROP TYPE IF EXISTS collection_visibility;

-- The visibility index is invalidated by the type change; recreate it
-- explicitly so the (tenant_id, visibility) B-tree is TEXT-typed and the
-- ADR-233 D8 read predicate (owner ∪ tenant ∪ friends) can use it. The
-- drop-then-create pair is idempotent; rebuilding a small index on a replay
-- costs less than reasoning about which half of it survived.
DROP INDEX IF EXISTS idx_collections_visibility;
CREATE INDEX idx_collections_visibility
    ON collections (tenant_id, visibility)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN collections.visibility IS
    'ADR-233 D7 audience: private | friends | tenant. Same vocabulary + same friend set as learning_atoms.reuse_visibility. PUBLIC is RETIRED (RLS capped it at the tenant; cross-tenant distribution is a syndication concern per ADR-229 fork (a)).';

COMMIT;
