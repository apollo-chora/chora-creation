-- Rollback for 0010_ai_assist_jobs — destructive (drops the projection
-- table entirely). On rollback the chora-creation HTTP handler will 503
-- on /api/atoms/ai-assist because the new code path expects this table;
-- the orchestrator-side LangGraph checkpoint state in chora_ai_kernel
-- is unaffected (cross-DB queries forbidden anyway).

DROP TRIGGER IF EXISTS trg_ai_assist_jobs_updated_at ON ai_assist_jobs;
DROP INDEX IF EXISTS idx_ai_assist_jobs_tenant_author;
DROP INDEX IF EXISTS idx_ai_assist_jobs_tenant_status_created;
DROP POLICY IF EXISTS ai_assist_jobs_tenant_isolation ON ai_assist_jobs;
DROP TABLE IF EXISTS ai_assist_jobs;
