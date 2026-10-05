-- =============================================================================
-- chora-creation : 0028_exam_content_embargo_rls.up.sql
--
-- ADR-191 D2/D3 layer-1 — the exam content-embargo as a RESTRICTIVE,
-- intra-tenant RLS surface on the atom-content tables. This is Chora's FIRST
-- `AS RESTRICTIVE` policy.
--
-- Semantics (the categorical INVERSE of the ADR-165/184 cross-tenant BYPASSES,
-- which see MORE): a RESTRICTIVE policy AND-combines with the existing
-- PERMISSIVE `tenant_isolation` policy, so it can only REMOVE visibility. A
-- PROCTOR-claimed caller sees strictly LESS — excluded from SELECTing
-- learning_atoms / atom_revisions content — while every other caller is
-- unaffected. NO tenant boundary is crossed, so this does NOT amend the
-- ADR-165/184 bypass chain and does NOT consume the "exactly TWO bypass
-- surfaces" budget (PLATFORM_OPERATOR remains the sole RLS-BYPASS principal).
-- Recorded here for governance / IMDA D1 visibility (ADR-191 D2).
--
-- Matching (ADR-191 O3):
--   * Keyed on `chora.user_roles` — the multi-valued role GUC the pg tenant-tx
--     seam now emits (SET LOCAL chora.user_roles) from the caller's mesh roles,
--     bridged from `x-mesh-user-roles` by the tenantContext middleware. A
--     single-valued `chora.role` GUC would be insufficient for a multi-role
--     principal like INSTRUCTOR+PROCTOR — the presence of the `proctor` token
--     anywhere in the set embargoes, regardless of other roles.
--   * `x-mesh-user-roles` / chora.user_roles carries LOWERCASE tokens (see
--     chora-gateway + chora-delivery role handling), so we match `proctor`;
--     `!~*` (case-INSENSITIVE POSIX no-match) is belt-and-suspenders in case any
--     future path stamps a different case. The `(^|,)…(,|$)` anchors bound the
--     token so e.g. "proctoring" never matches.
--   * COALESCE(…, '') is MANDATORY: for a RESTRICTIVE USING expression Postgres
--     treats NULL as "row not visible". An UNSET GUC (every legacy / non-role
--     read path) yields NULL from current_setting(...,true); COALESCE maps that
--     to '' → not-a-proctor → row VISIBLE, so the embargo narrows visibility
--     for PROCTOR ONLY and does not regress any existing read.
--
-- Defence-in-depth: the content-boundary 403 EXAM_CONTENT_EMBARGO_VIOLATION
-- gate (internal/adapter/http/exam_embargo.go) is layer-2; this is the DB
-- backstop that yields ZERO rows even to read paths that bypass that handler.
-- =============================================================================

DROP POLICY IF EXISTS exam_content_embargo ON learning_atoms;
CREATE POLICY exam_content_embargo ON learning_atoms
    AS RESTRICTIVE
    FOR SELECT
    USING (COALESCE(current_setting('chora.user_roles', true), '') !~* '(^|,)proctor(,|$)');

DROP POLICY IF EXISTS exam_content_embargo ON atom_revisions;
CREATE POLICY exam_content_embargo ON atom_revisions
    AS RESTRICTIVE
    FOR SELECT
    USING (COALESCE(current_setting('chora.user_roles', true), '') !~* '(^|,)proctor(,|$)');
