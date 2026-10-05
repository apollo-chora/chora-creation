-- =============================================================================
-- chora-creation : 0034_topic_nodes.up.sql
--
-- CHO-2275 (Sub-phase A) — the content topic-tree backend. Greenfield: GATE 0
-- confirmed there is NO topic_nodes table anywhere (the internal/legacy/atomic
-- TopicNode is dead, 0 importers). This builds the durable tree in the LIVE
-- domain.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : CHO-2275 (topic-tree backend, 2026-07-18)
--
-- MODEL
-- -----
-- Each topic_node is an aggregate ROOT; the tree is a projection over the flat
-- set (parent_id references another node of the SAME table). The API returns a
-- flat slice; the FE builds the tree client-side.
--
-- NO FOREIGN KEYS — deliberately, twice over:
--   * parent_id  → topic_nodes(topic_id): a self-FK would fight soft-delete
--     (a deleted parent still has live children pointing at it) and, under
--     FORCE RLS, an FK check runs privileged and sees ALL tenants' rows — it
--     could not stop a cross-tenant parent. So parent tenancy + existence + the
--     acyclic invariant are validated in the domain/repo (TopicNode.Move walks
--     the new parent's ancestry chain), not by a constraint.
--   * topic_node_atoms.atom_id → learning_atoms(atom_id): ATOM-CENTRIC (SP-02)
--     — a topic REFERENCES atoms, it never OWNS them; cross-aggregate refs are
--     UUIDs without FK (ddd-enforcement #3). The same no-untenanted-FK lesson
--     that bit collections' atom lookup (ADR-233 D7) applies here.
--
-- IDs are UUIDv7, minted in Go (topic.NewTopicNode → uuid.NewV7) — the modern
-- pattern (0032 atom_backfill_runs does the same). No DB default: a NULL id
-- must fail loud, never silently mint a v4.
--
-- Soft-delete only (deleted_at) per ddd-enforcement #4. Default queries filter
-- WHERE deleted_at IS NULL. Cross-DB queries FORBIDDEN.
--
-- RLS  : tenant_isolation keyed on current_setting('chora.tenant_id') + FORCE.
--        ⚠ THE GUC IS `chora.tenant_id`, NOT `app.tenant_id` — chora-creation's
--        RunInTenantTx sets `chora.tenant_id`; migration 0010 once keyed
--        `app.tenant_id` and every row silently failed the match until 0015
--        re-keyed it. Every mainline table (0001/0006/0032) reads chora.tenant_id.
-- GRANT: explicit app_rw/app_ro grants here (also covered by the default-
--        privileges ALTER in 9999_grant_app_roles.sql, but stated for clarity
--        per 0025/0032). A targeted hand-apply that skips 9999 still leaves
--        app_rw with DML because of these lines.
--
-- REPLAY-SAFE: every statement is guarded (IF NOT EXISTS / DROP-then-CREATE for
-- the trigger + policy), so the migration converges from a half-applied state
-- rather than wedging the lane (the 0031 lesson). One BEGIN/COMMIT unit.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- topic_nodes — one row per node in a tenant's topic taxonomy.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS topic_nodes (
    topic_id    UUID        PRIMARY KEY,              -- UUIDv7, minted in Go
    tenant_id   UUID        NOT NULL,
    name        TEXT        NOT NULL,
    parent_id   UUID        NULL,                     -- self-ref by UUID, NO FK
    sort_order  INTEGER     NOT NULL DEFAULT 0,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ NULL,

    CONSTRAINT topic_nodes_name_len
        CHECK (char_length(name) BETWEEN 1 AND 128),
    CONSTRAINT topic_nodes_sort_order_nonneg
        CHECK (sort_order >= 0),
    -- A node cannot be its own parent (the first, cheapest cycle guard; deeper
    -- cycles are prevented in the domain's Move).
    CONSTRAINT topic_nodes_no_self_parent
        CHECK (parent_id IS NULL OR parent_id <> topic_id)
);

-- Child-of-parent lookups + full-tenant tree scans (leading tenant_id column).
-- The task-required composite: (tenant_id, parent_id).
CREATE INDEX IF NOT EXISTS idx_topic_nodes_tenant_parent
    ON topic_nodes (tenant_id, parent_id)
    WHERE deleted_at IS NULL;

-- Reuse the shared updated_at trigger (defined in 0001_initial.sql).
DROP TRIGGER IF EXISTS trg_topic_nodes_updated_at ON topic_nodes;
CREATE TRIGGER trg_topic_nodes_updated_at
    BEFORE UPDATE ON topic_nodes
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE topic_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_nodes FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON topic_nodes;
CREATE POLICY tenant_isolation ON topic_nodes
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE topic_nodes
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE topic_nodes
    TO chora_creation_app_ro;

-- -----------------------------------------------------------------------------
-- topic_node_atoms — atom-centric attach join. The topic side holds the link;
-- the atom is referenced by UUID and never owned (no FK to learning_atoms).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS topic_node_atoms (
    topic_id    UUID        NOT NULL,                 -- ref topic_nodes, NO FK
    atom_id     UUID        NOT NULL,                 -- ref learning_atoms, NO FK
    tenant_id   UUID        NOT NULL,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ NULL,

    PRIMARY KEY (topic_id, atom_id)
);

-- Reverse lookup: which topics reference a given atom (per tenant).
CREATE INDEX IF NOT EXISTS idx_topic_node_atoms_tenant_atom
    ON topic_node_atoms (tenant_id, atom_id)
    WHERE deleted_at IS NULL;

ALTER TABLE topic_node_atoms ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_node_atoms FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON topic_node_atoms;
CREATE POLICY tenant_isolation ON topic_node_atoms
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE topic_node_atoms
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE topic_node_atoms
    TO chora_creation_app_ro;

COMMENT ON TABLE topic_nodes IS
    'CHO-2275 — content topic tree. Each row an aggregate root; parent_id is a same-table UUID reference (NO FK). RLS chora.tenant_id + FORCE. Soft-delete only; delete of a node with active children is refused in the domain.';
COMMENT ON TABLE topic_node_atoms IS
    'CHO-2275 — atom-centric attach: a topic REFERENCES an atom by UUID (NO FK to learning_atoms). RLS chora.tenant_id + FORCE.';

COMMIT;
