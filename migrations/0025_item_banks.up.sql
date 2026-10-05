-- =============================================================================
-- chora-creation : 0025_item_banks.up.sql
--
-- W3.B.1 — ItemBank aggregate (reusable question-atom pool, BE).
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent W3.B.1 (ItemBank BE, 2026-06-28)
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- LearningAtom/Question are the PRIMARY AGGREGATE ROOTS — item banks QUERY
-- questions but DO NOT own them (.claude/rules/ddd-enforcement.md Aggregate
-- Invariant #1/#3). This migration adds the ItemBank aggregate (the proven
-- Collection template re-applied to question references + tags):
--
--   - item_banks        — aggregate root rows
--   - item_bank_items   — child membership rows (cross-aggregate UUID
--                         reference to questions.question_id; NO FK constraint
--                         per ddd-enforcement #3; existence validated by the
--                         chora-creation domain service / QuestionLookup port)
--
-- Soft-delete only (deleted_at) per Aggregate Invariant #4. Cascade soft-delete
-- within the same aggregate per Invariant #5 — the parent bank's deleted_at IS
-- NOT NULL filters child items out of active queries.
--
-- Visibility is PRIVATE | TENANT_INTERNAL ONLY — there is intentionally NO
-- PUBLIC value (exam-security: a question pool is never readable across
-- tenants in v1). This is the deliberate divergence from collection_visibility.
--
-- Cross-DB JOINs FORBIDDEN per ddd-enforcement.md. RLS via tenant_isolation
-- policy + current_setting('chora.tenant_id'). The whole migration runs inside
-- one BEGIN/COMMIT so a mid-statement failure rolls back atomically (no
-- stranded objects — see migration-runner fail-fast wedge learnings).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Enum: item_bank_visibility (NO PUBLIC — exam-security)
--   PRIVATE         — only the owning author/instructor can read
--   TENANT_INTERNAL — any GCID in the same tenant can read
-- -----------------------------------------------------------------------------
CREATE TYPE item_bank_visibility AS ENUM ('PRIVATE', 'TENANT_INTERNAL');

-- -----------------------------------------------------------------------------
-- item_banks — aggregate root
-- -----------------------------------------------------------------------------
CREATE TABLE item_banks (
    item_bank_id    UUID                 PRIMARY KEY,
    tenant_id       UUID                 NOT NULL,
    owner_gcid      UUID                 NOT NULL,
    name            VARCHAR(200)         NOT NULL,
    description     TEXT                 NOT NULL DEFAULT '',
    visibility      item_bank_visibility NOT NULL DEFAULT 'PRIVATE',
    tags            TEXT[]               NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ          NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ          NULL,

    -- Name length guard mirrors the OpenAPI / domain MaxNameLength.
    CONSTRAINT item_banks_name_len CHECK (length(name) BETWEEN 1 AND 200),
    -- Description cap mirrors the domain MaxDescriptionLength.
    CONSTRAINT item_banks_description_len CHECK (length(description) <= 2000),
    -- Tag count cap mirrors the domain MaxTags (per-tag length is enforced in
    -- the domain — a CHECK cannot scan array elements via subquery).
    CONSTRAINT item_banks_tags_card CHECK (cardinality(tags) <= 20)
);

CREATE INDEX idx_item_banks_tenant
    ON item_banks (tenant_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_item_banks_owner
    ON item_banks (tenant_id, owner_gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_item_banks_visibility
    ON item_banks (tenant_id, visibility)
    WHERE deleted_at IS NULL;

-- GIN index on tags for tag-filtered pool discovery.
CREATE INDEX idx_item_banks_tags
    ON item_banks USING GIN (tags)
    WHERE deleted_at IS NULL;

-- Reuse the shared updated_at trigger (defined in 0001_initial.sql).
CREATE TRIGGER trg_item_banks_updated_at
    BEFORE UPDATE ON item_banks
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE item_banks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON item_banks
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- item_bank_items — child membership rows
--
-- question_id is a CROSS-AGGREGATE UUID reference to questions.question_id;
-- per ddd-enforcement Aggregate Invariant #3 there is NO FK constraint —
-- existence is validated by the chora-creation domain service (QuestionLookup
-- port) before INSERT. Within the same chora_creation database this is an
-- intra-DB cross-aggregate lookup, NOT a cross-DB query (which remains
-- FORBIDDEN per ddd-enforcement HARD RULE).
--
-- Position is 0-based. (item_bank_id, question_id) uniqueness prevents
-- duplicate inserts; the partial unique (item_bank_id, position) index
-- guarantees ordering integrity among ACTIVE rows only (so a removed-then-
-- readded round-trip does not collide with the tombstoned row).
-- -----------------------------------------------------------------------------
CREATE TABLE item_bank_items (
    item_bank_id    UUID         NOT NULL REFERENCES item_banks(item_bank_id) ON DELETE RESTRICT,
    question_id     UUID         NOT NULL,
    tenant_id       UUID         NOT NULL,
    position        INTEGER      NOT NULL,
    added_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ  NULL,
    PRIMARY KEY (item_bank_id, question_id),
    CONSTRAINT item_bank_items_position_nonneg CHECK (position >= 0)
);

CREATE UNIQUE INDEX idx_item_bank_items_position_active
    ON item_bank_items (item_bank_id, position)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_item_bank_items_question
    ON item_bank_items (question_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_item_bank_items_tenant
    ON item_bank_items (tenant_id)
    WHERE deleted_at IS NULL;

ALTER TABLE item_bank_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON item_bank_items
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- GRANTs — explicit + idempotent (also covered by the default-privileges
-- ALTER in 9999_grant_app_roles.sql, but specified here for clarity).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE item_banks, item_bank_items
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE item_banks, item_bank_items
    TO chora_creation_app_ro;

COMMIT;
