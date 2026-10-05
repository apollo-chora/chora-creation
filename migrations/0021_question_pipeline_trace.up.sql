-- =============================================================================
-- chora-creation : 0021_question_pipeline_trace.up.sql
--
-- CHO-1826 Gap #4 — surface the AI per-step pipeline trace (IMDA D2 transparency)
-- on the unified authoring canvas question-jobs path.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent CHO-1826 Gap #4 (2026-06-25)
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1) / IMDA D2
--
-- Additive, nullable column carrying the per-step QGen pipeline trace parsed
-- from chora.creation.ai_assist.completed.v1 pipeline_trace_json (field 12) —
-- the SAME trace the single-mode AI-Assist drawer renders. The orchestrator
-- already emits it for the canvas's ai_draft / batch crews (one qgen_crew graph
-- serves both); the ai_assist completed-event subscriber already DECODES it but
-- previously dropped it on the question-job branch (no column). The poll
-- response surfaces it so the canvas renders the <chora-aplus-trace-widget>.
-- NULL for refused / failed jobs (the refused.v1 schema omits the field) and
-- for pre-fix jobs. Operational state, not domain history — UPDATE-in-place like
-- the sibling candidate / proposal / generation_summary columns.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    ADD COLUMN IF NOT EXISTS pipeline_trace_jsonb JSONB NULL;

COMMENT ON COLUMN question_generation_jobs.pipeline_trace_jsonb IS
'CHO-1826 Gap #4: per-step QGen pipeline trace (PipelineTraceStep[] —
{name, status, started_at, completed_at, attempt, input_tokens, output_tokens,
notes, engine_resource}) parsed from the ai_assist completed.v1 field 12
pipeline_trace_json. NULL for refused / failed jobs and pre-fix jobs.';

COMMIT;
