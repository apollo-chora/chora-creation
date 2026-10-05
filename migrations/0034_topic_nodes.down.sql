-- =============================================================================
-- chora-creation : 0034_topic_nodes.down.sql
--
-- Reverses 0034_topic_nodes.up.sql (CHO-2275). Drops the topic-tree tables and
-- their policies/trigger. Greenfield feature — nothing depends on these tables,
-- so the drop is clean. (The runner never applies *.down.sql; this exists for
-- golang-migrate reversibility.)
-- =============================================================================

BEGIN;

DROP POLICY IF EXISTS tenant_isolation ON topic_node_atoms;
DROP TABLE IF EXISTS topic_node_atoms;

DROP TRIGGER IF EXISTS trg_topic_nodes_updated_at ON topic_nodes;
DROP POLICY IF EXISTS tenant_isolation ON topic_nodes;
DROP TABLE IF EXISTS topic_nodes;

COMMIT;
