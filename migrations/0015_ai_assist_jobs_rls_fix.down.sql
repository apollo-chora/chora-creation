-- 0015_ai_assist_jobs_rls_fix.down — restore the original (buggy) 0010
-- policy that read `app.tenant_id`. Only useful for forensic replay of
-- the 500 reproduction; you almost certainly do NOT want this on a
-- live database.

DROP POLICY IF EXISTS ai_assist_jobs_tenant_isolation ON ai_assist_jobs;

CREATE POLICY ai_assist_jobs_tenant_isolation ON ai_assist_jobs
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);
