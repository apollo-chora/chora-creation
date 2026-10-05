-- =============================================================================
-- chora-creation : 0016_collections.up.sql
--
-- WS-6a — Personal Collection aggregate (BE).
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent WS-6a (Personal Collections BE, 2026-05-26)
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- LearningAtom is the PRIMARY AGGREGATE ROOT — collections QUERY atoms but
-- DO NOT own them (.claude/rules/ddd-enforcement.md Aggregate Invariants #1).
-- This migration adds the personal-Collection aggregate:
--
--   - collections           — aggregate root rows
--   - collection_atoms      — child membership rows (cross-aggregate UUID
--                             reference to learning_atoms.atom_id; NO FK
--                             constraint per ddd-enforcement #3; existence
--                             validated by chora-creation domain service)
--
-- Soft-delete only (deleted_at) per Aggregate Invariant #5. Cascade
-- soft-delete within the same aggregate per Invariant #8 — the parent
-- collection's deleted_at IS NOT NULL filters child atoms out of active
-- queries; per-row deletion is NOT required.
--
-- Cross-DB JOINs FORBIDDEN per ddd-enforcement.md. RLS via tenant_isolation
-- policy + `current_setting('chora.tenant_id')`. GRANTs auto-apply via
-- 9999_grant_app_roles.sql default-privileges + the explicit GRANT at the
-- end of this migration.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Enum: collection_visibility
--
-- Aligned with chora.creation.v1.CollectionVisibility proto enum:
--   PRIVATE         — only the owning learner can read
--   TENANT_INTERNAL — any GCID in the same tenant can read
--   PUBLIC          — readable across tenants (sole path permitting
--                     cross-tenant atom inclusion per BP-01 learner-
--                     ownership; see CLAUDE.md §5)
-- -----------------------------------------------------------------------------
CREATE TYPE collection_visibility AS ENUM ('PRIVATE', 'TENANT_INTERNAL', 'PUBLIC');

-- -----------------------------------------------------------------------------
-- collections — aggregate root
-- -----------------------------------------------------------------------------
CREATE TABLE collections (
    collection_id   UUID                  PRIMARY KEY,
    tenant_id       UUID                  NOT NULL,
    owner_gcid      UUID                  NOT NULL,
    title           VARCHAR(200)          NOT NULL,
    description     TEXT                  NOT NULL DEFAULT '',
    visibility      collection_visibility NOT NULL DEFAULT 'PRIVATE',
    created_at      TIMESTAMPTZ           NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ           NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ           NULL,

    -- Title length guard mirrors the OpenAPI / domain MaxTitleLength.
    CONSTRAINT collections_title_len CHECK (length(title) BETWEEN 1 AND 200),
    -- Description cap mirrors the domain MaxDescriptionLength.
    CONSTRAINT collections_description_len CHECK (length(description) <= 2000)
);

CREATE INDEX idx_collections_tenant
    ON collections (tenant_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_collections_owner
    ON collections (tenant_id, owner_gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_collections_visibility
    ON collections (tenant_id, visibility)
    WHERE deleted_at IS NULL;

-- Reuse the shared updated_at trigger.
CREATE TRIGGER trg_collections_updated_at
    BEFORE UPDATE ON collections
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE collections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collections
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- collection_atoms — child membership rows
--
-- atom_id is a CROSS-AGGREGATE UUID reference to learning_atoms.atom_id;
-- per ddd-enforcement Aggregate Invariant #3 there is NO FK constraint —
-- existence is validated by the chora-creation domain service (AtomLookup
-- port) before INSERT. Within the same chora_creation database this is an
-- intra-DB cross-aggregate lookup, NOT a cross-DB query (cross-DB queries
-- remain FORBIDDEN per ddd-enforcement HARD RULE).
--
-- Position is 0-based. The unique (collection_id, position) constraint
-- guarantees ordering integrity; (collection_id, atom_id) uniqueness
-- prevents duplicate-atom inserts at the persistence layer (defence in
-- depth — the domain aggregate also enforces this).
-- -----------------------------------------------------------------------------
CREATE TABLE collection_atoms (
    collection_id   UUID         NOT NULL REFERENCES collections(collection_id) ON DELETE RESTRICT,
    atom_id         UUID         NOT NULL,
    tenant_id       UUID         NOT NULL,
    position        INTEGER      NOT NULL,
    added_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ  NULL,
    PRIMARY KEY (collection_id, atom_id),
    -- Position uniqueness is enforced only for active rows so a removed-
    -- then-readded atom round-trip does not collide with the tombstoned
    -- row. Partial unique index per ddd-enforcement #6 (default queries
    -- filter deleted_at IS NULL).
    CONSTRAINT collection_atoms_position_nonneg CHECK (position >= 0)
);

CREATE UNIQUE INDEX idx_collection_atoms_position_active
    ON collection_atoms (collection_id, position)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_collection_atoms_atom
    ON collection_atoms (atom_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_collection_atoms_tenant
    ON collection_atoms (tenant_id)
    WHERE deleted_at IS NULL;

ALTER TABLE collection_atoms ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collection_atoms
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- GRANTs — explicit + idempotent (also covered by the default-privileges
-- ALTER in 9999_grant_app_roles.sql, but specified here for clarity since
-- 9999 was applied BEFORE this migration on bring-up).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE collections, collection_atoms
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE collections, collection_atoms
    TO chora_creation_app_ro;

COMMIT;
