-- =============================================================================
-- chora-creation : 0018_question_generation_summary.up.sql
--
-- CHO-1819 P2 — Mixed-type AI batch question generation.
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent CHO-1819 P2 (2026-06-21)
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Additive, nullable column carrying the mixed-batch outcome projection parsed
-- from the chora.creation.ai_assist.completed.v1 GenerationSummary (field 16):
-- requested vs generated totals + per-type breakdown + shortfall_reason. The
-- ai_assist completed-event subscriber stamps it for batch jobs; the poll
-- response surfaces it so the FE can render a shortfall banner. NULL for
-- single-type / legacy batches and for a pre-P2 orchestrator that emits no
-- summary (back-compat). Operational state, not domain history — no append-only
-- invariant; UPDATE-in-place like the sibling candidate / proposal columns.
-- =============================================================================

BEGIN;

ALTER TABLE question_generation_jobs
    ADD COLUMN generation_summary_jsonb JSONB NULL;

COMMENT ON COLUMN question_generation_jobs.generation_summary_jsonb IS
'CHO-1819 P2: mixed-batch GenerationSummary {requested_total, generated_total,
generated_count, generated_per_type:{question_type:int}, shortfall_reason}
parsed from the ai_assist completed.v1 (field 16). NULL for single-type /
legacy batches and pre-P2 orchestrator emissions.';

COMMIT;
