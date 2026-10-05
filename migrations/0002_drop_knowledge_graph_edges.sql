-- =============================================================================
-- chora_creation migration 0002 — Drop knowledge_graph_edges (relocated)
--
-- Source-of-truth: docs/architecture/adrs/adr-143-per-user-knowledge-graph-hexagonal-fog.md
--
-- Per ADR-143, knowledge_graph_edges is RELOCATED from chora_creation to
-- chora_consumption (renamed atom_semantic_edges). The data copy is handled
-- by sibling migration:
--   services/chora-consumption/migrations/0002_per_user_knowledge_graph.sql
-- + an admin-tooling cross-DB pg_dump | psql import.
--
-- This migration runs ONLY AFTER:
--   1. chora_consumption 0002 is applied
--   2. atom_semantic_edges seed data import is verified (counts match)
--   3. ADR-143 P1 schema migration sub-phase signed off
--
-- Big-bang migration is acceptable pre-M12 (zero live users). Rollback path:
-- 7d Cloud SQL Enterprise Plus PITR per database-postgresql skill.
-- =============================================================================

BEGIN;

-- Drop in reverse-creation order to satisfy FK / type-dependency constraints.

DROP TRIGGER IF EXISTS trg_kg_edges_updated_at ON knowledge_graph_edges;

ALTER TABLE knowledge_graph_edges DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON knowledge_graph_edges;

DROP INDEX IF EXISTS idx_kg_edges_source;
DROP INDEX IF EXISTS idx_kg_edges_target;
DROP INDEX IF EXISTS idx_kg_edges_type;

DROP TABLE IF EXISTS knowledge_graph_edges;

DROP TYPE IF EXISTS knowledge_edge_type;

COMMIT;

-- =============================================================================
-- Down migration (rollback) — informational only; lean on PITR
-- =============================================================================
-- See services/chora-creation/migrations/0001_initial.sql:128-156 for the
-- original CREATE statements. Restore via PITR if needed within 7d window.
