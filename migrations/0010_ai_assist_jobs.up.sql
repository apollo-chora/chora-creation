-- 0010_ai_assist_jobs — async AI Assist job projection per
-- docs/m13/ack-oe-ai-assist-plan-2026-05-17.md Step 4.
--
-- Projects the orchestrator's qgen 2-agent crew runs into a chora-creation
-- queryable row. The chora-creation HTTP handler INSERTs in QUEUED state
-- at request time, then a Pub/Sub subscriber UPDATEs the row when
-- chora.creation.ai_assist.completed.v1 (or .refused.v1) arrives. FE polls
-- GET /api/atoms/ai-assist/{job_id} which SELECTs from this table.
--
-- Cross-DB queries FORBIDDEN per .claude/rules/ddd-enforcement.md — the
-- orchestrator's checkpointed state in chora_ai_kernel is its own concern;
-- this table is the chora_creation-owned author-facing projection.
--
-- ID semantics: UUIDv7 minted at POST time. Also serves as the
-- orchestrator's thread_id + correlation_id for LangGraph checkpoint
-- recovery + Cloud Trace span correlation.
--
-- Soft delete NOT applied — ai_assist_jobs are ephemeral (TTL eligible
-- post-author-acceptance). Hard delete is fine; no FK from author atoms
-- back to ai_assist_jobs.

CREATE TABLE IF NOT EXISTS ai_assist_jobs (
    -- Identity
    id              uuid PRIMARY KEY,                                       -- UUIDv7 minted by handler
    tenant_id       uuid NOT NULL,
    author_gcid     uuid NOT NULL,

    -- FSM status — mirrors openapi/creation-questions.yaml §AiAssistJob.status
    status          text NOT NULL CHECK (
                        status IN ('QUEUED', 'IN_PROGRESS', 'COMPLETED', 'REFUSED', 'FAILED')
                    ),

    -- Question shape discriminator per OpenAPI QuestionType enum.
    question_type   text NOT NULL CHECK (question_type IN ('mcq', 'oe')),

    -- Original AiAssistGenerateRequest body (verbatim JSON). Carried in
    -- result_payload-shaped responses + audit. <=4KiB practical (prompt
    -- 2KiB + metadata + max_retries).
    request_payload jsonb NOT NULL,

    -- Populated when status reaches COMPLETED. Mirrors OpenAPI
    -- AiAssistCandidate (stem + mcq_payload | oe_payload + critic_notes).
    -- NULL until orchestrator emits completed.v1.
    result_payload  jsonb,

    -- IMDA D2 transparency trace per OpenAPI PipelineTraceStep[].
    -- Populated incrementally as the subscriber processes lifecycle events
    -- (today: terminal only; future: streaming per-step updates).
    pipeline_trace  jsonb,

    -- True when the critic agent rejected on all `max_retries` attempts
    -- but the orchestrator still emitted completed.v1 (NOT refused.v1).
    -- Per user clarification 2026-05-17 — quality_warning + last
    -- critic_notes; refused.v1 is reserved for Cloud Model Armor blocks.
    quality_warning boolean NOT NULL DEFAULT FALSE,

    -- Populated when status = REFUSED. One of (GUARDRAIL_PRE,
    -- GUARDRAIL_POST, VALIDATION) per OpenAPI AiAssistRefusal.reason enum.
    refusal_reason  text CHECK (
        refusal_reason IS NULL OR refusal_reason IN ('GUARDRAIL_PRE', 'GUARDRAIL_POST', 'VALIDATION')
    ),
    refusal_armor_verdict     text,     -- e.g., 'armor:pii_high_risk_block'
    refusal_user_facing_msg   text,     -- short non-leaky FE message

    -- Loop accounting — populated from the orchestrator's pipeline_trace
    -- terminal row (the critic_loop max attempt or the success attempt).
    attempt_count   integer NOT NULL DEFAULT 0,

    -- Cost — finalised at COMPLETED/REFUSED time. Mirrors AiAssistJob.
    mana_charged    integer NOT NULL DEFAULT 0,

    -- Timestamps
    created_at      timestamptz NOT NULL DEFAULT NOW(),
    updated_at      timestamptz NOT NULL DEFAULT NOW(),
    completed_at    timestamptz                                              -- COMPLETED or REFUSED terminal time
);

-- RLS per .claude/skills/multi-tenant-rls/SKILL.md — tenant isolation
-- via current_setting('app.tenant_id'). The orchestrator's subscriber
-- writes with the bypass-RLS app_writer role; the handler's read-by-job
-- uses tenant-scoped reads via the app role with current_setting set
-- by middleware per service convention.
ALTER TABLE ai_assist_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_assist_jobs FORCE ROW LEVEL SECURITY;

CREATE POLICY ai_assist_jobs_tenant_isolation ON ai_assist_jobs
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Indexes:
--   - (tenant_id, status, created_at DESC) — FE poll path + admin list
--   - (tenant_id, author_gcid)             — author's own-jobs filter
--   - id (already PK)                      — GET /api/atoms/ai-assist/{job_id}
CREATE INDEX IF NOT EXISTS idx_ai_assist_jobs_tenant_status_created
    ON ai_assist_jobs (tenant_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_ai_assist_jobs_tenant_author
    ON ai_assist_jobs (tenant_id, author_gcid);

-- updated_at trigger — keep parity with existing chora-creation tables
-- per migration 0008 (question_generation_jobs). Reuses the shared
-- creation_set_updated_at() function defined in 0001 schema bootstrap
-- (the chora-creation database namespaces it with the creation_ prefix
-- per 0001_initial.sql line 34).
CREATE TRIGGER trg_ai_assist_jobs_updated_at
    BEFORE UPDATE ON ai_assist_jobs
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

-- App-role grants (deferred to 9999_grant_app_roles.sql per migration
-- convention — covered there by the wildcard ai_assist_* grant in M14.1
-- or added explicitly when 9999 is next revved).
