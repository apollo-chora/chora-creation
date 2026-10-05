-- =============================================================================
-- chora-creation : 0028_exam_content_embargo_rls.down.sql
--
-- Reverse of 0028 — drop the RESTRICTIVE exam content-embargo policies. This
-- restores default visibility for a PROCTOR-claimed caller at the DB layer; the
-- content-boundary 403 gate (layer-2) is independent and unaffected. Tenant
-- isolation (the PERMISSIVE tenant_isolation policy) is NOT touched.
-- =============================================================================

DROP POLICY IF EXISTS exam_content_embargo ON atom_revisions;
DROP POLICY IF EXISTS exam_content_embargo ON learning_atoms;
