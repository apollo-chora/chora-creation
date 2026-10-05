-- =============================================================================
-- chora-creation : 0031_collection_audience.down.sql
--
-- Reverse of 0031: TEXT audience (private|friends|tenant) -> the retired PG enum
-- `collection_visibility` (PRIVATE|TENANT_INTERNAL|PUBLIC).
--
-- ⚠ THIS DOWN MIGRATION IS LOSSY, AND LOUDLY SO.
--
-- `friends` has NO legacy equivalent — the old enum simply could not express it
-- (that asymmetry is precisely what ADR-233 D7 fixed). Rolling back therefore
-- WIDENS every friends-audience collection to TENANT_INTERNAL: it becomes
-- readable by the whole tenant rather than the owner's friends.
--
-- That is a real privacy regression, so it is announced with a RAISE WARNING and
-- an exact row count rather than performed quietly. If the count is non-zero,
-- the operator is looking at collections whose owners chose a narrower audience
-- than this rollback can represent.
--
-- The PRIVATE/TENANT_INTERNAL/PUBLIC split is also NOT recoverable: 0031 mapped
-- PUBLIC and TENANT_INTERNAL onto the same value (`tenant`), because RLS made
-- them identical in effect. Every `tenant` row therefore comes back as
-- TENANT_INTERNAL. No row is restored to PUBLIC — and none should be: a PUBLIC
-- collection was never readable cross-tenant anyway.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Truth-telling: announce the lossy widening BEFORE it happens.
-- -----------------------------------------------------------------------------
DO $$
DECLARE
    friends_count BIGINT;
    tenant_count  BIGINT;
BEGIN
    SELECT count(*) INTO friends_count FROM collections WHERE visibility = 'friends';
    SELECT count(*) INTO tenant_count  FROM collections WHERE visibility = 'tenant';

    RAISE NOTICE 'ADR-233 D7 ROLLBACK:';
    RAISE NOTICE '  private -> PRIVATE         : (unchanged in meaning)';
    RAISE NOTICE '  tenant  -> TENANT_INTERNAL : % row(s)', tenant_count;

    IF friends_count > 0 THEN
        RAISE WARNING 'LOSSY: % collection(s) carry audience `friends`, which the legacy enum CANNOT express. They are being WIDENED to TENANT_INTERNAL - readable by the whole tenant instead of the owner''s friends. This is a privacy regression; re-narrow them by hand if you roll forward again.', friends_count;
    END IF;

    IF EXISTS (SELECT 1 FROM collections WHERE visibility NOT IN ('private', 'friends', 'tenant')) THEN
        RAISE EXCEPTION 'ADR-233 rollback: collections.visibility holds a value outside (private|friends|tenant); refusing to guess a legacy label for it';
    END IF;
END
$$;

-- -----------------------------------------------------------------------------
-- 1. Recreate the retired enum.
-- -----------------------------------------------------------------------------
CREATE TYPE collection_visibility AS ENUM ('PRIVATE', 'TENANT_INTERNAL', 'PUBLIC');

-- -----------------------------------------------------------------------------
-- 2. Drop the TEXT-era CHECK + default so the type can be altered back.
-- -----------------------------------------------------------------------------
ALTER TABLE collections
    DROP CONSTRAINT IF EXISTS collections_visibility_check;

ALTER TABLE collections
    ALTER COLUMN visibility DROP DEFAULT;

-- -----------------------------------------------------------------------------
-- 3. Convert TEXT -> enum.
--
--    friends COLLAPSES to TENANT_INTERNAL (warned above — the legacy vocabulary
--    has no narrower option between PRIVATE and TENANT_INTERNAL).
--    No row is restored to PUBLIC: 0031 collapsed PUBLIC into `tenant` precisely
--    because RLS made the two identical, and inventing a PUBLIC row here would
--    fabricate an audience nobody chose.
-- -----------------------------------------------------------------------------
ALTER TABLE collections
    ALTER COLUMN visibility TYPE collection_visibility
    USING (
        CASE visibility
            WHEN 'private' THEN 'PRIVATE'::collection_visibility
            WHEN 'tenant'  THEN 'TENANT_INTERNAL'::collection_visibility
            WHEN 'friends' THEN 'TENANT_INTERNAL'::collection_visibility  -- LOSSY
        END
    );

-- -----------------------------------------------------------------------------
-- 4. Restore NOT NULL + the legacy default.
-- -----------------------------------------------------------------------------
ALTER TABLE collections
    ALTER COLUMN visibility SET NOT NULL,
    ALTER COLUMN visibility SET DEFAULT 'PRIVATE';

-- -----------------------------------------------------------------------------
-- 5. Rebuild the index against the enum-typed column.
-- -----------------------------------------------------------------------------
DROP INDEX IF EXISTS idx_collections_visibility;
CREATE INDEX idx_collections_visibility
    ON collections (tenant_id, visibility)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN collections.visibility IS NULL;

COMMIT;
