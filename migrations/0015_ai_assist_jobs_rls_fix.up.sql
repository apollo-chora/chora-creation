-- 0015_ai_assist_jobs_rls_fix — align RLS policy GUC name with the
-- chora-creation canonical convention.
--
-- BUG (0010): the ai_assist_jobs_tenant_isolation policy used
--     current_setting('app.tenant_id', true)::uuid
-- as its tenant predicate, but chora-creation's RunInTenantTx wrapper
-- (libs/chora-go-common txqx + every other 0001..0014 migration) sets
-- `chora.tenant_id` via SET LOCAL. Result: `chora.tenant_id` was set
-- per-tx but the policy evaluated `app.tenant_id` → NULL::uuid → row
-- failed `tenant_id = NULL` → `new row violates row-level security
-- policy for table "ai_assist_jobs"` → POST /api/atoms/ai-assist
-- returned 500 with `ai_assist_create_failed` (filed by FE as
-- E2E-BE-MCQ-AI-ASSIST-500 in docs/m13/e2e-fe-coord-directive-2026-05-16.md §3
-- after the configmap fix in 0e36679c cleared the prior 502).
--
-- Convention: every other RLS-protected table in chora_creation
-- (0001 atoms/revisions/edges/variants_* + 0006 questions + 0007 question_revisions
-- + 0008 question_generation_jobs) reads `chora.tenant_id`. Migration 0010
-- was the lone outlier and is the bug source.
--
-- This migration drops the broken policy + recreates it with the
-- canonical GUC. Idempotent (DROP IF EXISTS). Safe to re-apply.

DROP POLICY IF EXISTS ai_assist_jobs_tenant_isolation ON ai_assist_jobs;

CREATE POLICY ai_assist_jobs_tenant_isolation ON ai_assist_jobs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
