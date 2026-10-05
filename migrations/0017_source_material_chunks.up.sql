-- =============================================================================
-- chora-creation : 0017_source_material_chunks.up.sql
--
-- Lane 1c — Batch → Test-Set Compose (CHO-1703 / ADR-180, D9 + D15).
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent lane-1c W1 (2026-06-10)
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Two changes:
--
--   1. source_material_chunks — page-referenced deterministic text chunks
--      extracted IN chora-creation at batch-job creation from every
--      text-bearing grounding file (PDF per-page; DOCX/MD/TXT ~2-4KB
--      splits; images produce NO chunks). The D15 citation-verification
--      pass matches crew-reported excerpts against these rows and stamps
--      `verified` + `chunk_id` into candidate_questions_jsonb BEFORE the
--      FE poll surfaces candidates. The store is DELIBERATELY reuse-ready
--      for KG / growth-edge / daily-dose grounding (owner strategy, D9):
--      `embedding vector` is pgvector-ready (extension installed in 0001)
--      but NULL in v1 — no embeddings are computed by this lane; the
--      retrieval seam is the existing ContentRetrieval.SearchEmbeddings
--      gRPC in chora-creation (cross-DB queries FORBIDDEN — consumers
--      arrive via gRPC/events, never SQL).
--
--   2. question_generation_jobs.proposed_test_set_jsonb — the composer
--      step's LLM-proposed test-set structure (title / description /
--      order / points; OpenAPI ProposedTestSet shape) persisted by the
--      completed-event subscriber alongside the enriched candidates and
--      surfaced on the job poll response. NULL for single-question jobs,
--      pre-1c batches, or when the composer step was skipped.
--
-- Chunks are immutable extraction artifacts (no UPDATE path, no
-- updated_at trigger, no soft-delete) — re-extraction replaces by job_id.
-- Cross-DB JOINs FORBIDDEN. job/tenant refs are UUIDs without FK to other
-- domains; job_id intentionally has NO FK to question_generation_jobs
-- either (chunks may be written before/independently of job-row updates
-- and the per-job lifecycle is owned by the application layer).
--
-- RLS per the chora_creation canonical convention (0015 lesson): the GUC
-- is `chora.tenant_id` (set via RunInTenantTx SET LOCAL), NEVER
-- `app.tenant_id`. chora_creation_app_rw is NOBYPASSRLS.
-- =============================================================================

BEGIN;

CREATE TABLE source_material_chunks (
    -- UUIDv7 minted app-side (ddd-enforcement #7 — new tables use UUIDv7).
    chunk_id     UUID         PRIMARY KEY,
    -- The owning question_generation_jobs.job_id (same-DB UUID ref, no FK —
    -- see header).
    job_id       UUID         NOT NULL,
    tenant_id    UUID         NOT NULL,
    -- gs:// URI of the source file this chunk was extracted from. Matches
    -- one of the job's settings.source_files[].blob_uri entries.
    file_uri     TEXT         NOT NULL,
    -- "source" | "rubric" — mirrors SourceFileRef.role (ai_assist.proto).
    file_role    TEXT         NOT NULL DEFAULT 'source'
        CHECK (file_role IN ('source', 'rubric')),
    -- 0-based extraction order within the file.
    chunk_index  INTEGER      NOT NULL DEFAULT 0
        CHECK (chunk_index >= 0),
    -- 1-based page number for paged formats (PDF). NULL for unpaged
    -- formats (DOCX/MD/TXT splits).
    page_no      INTEGER      NULL
        CHECK (page_no IS NULL OR page_no >= 1),
    -- Extracted chunk text (the citation-verification corpus).
    text         TEXT         NOT NULL,
    -- pgvector-ready embedding slot (768-d Vertex text-embedding when the
    -- KG/daily-dose reuse seam starts computing them). Unsized on purpose:
    -- v1 writes NULL only; sizing is decided by the embedding campaign.
    embedding    vector       NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_source_chunks_job
    ON source_material_chunks (job_id);

CREATE INDEX idx_source_chunks_tenant_job
    ON source_material_chunks (tenant_id, job_id);

ALTER TABLE source_material_chunks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON source_material_chunks
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE source_material_chunks IS
'Lane 1c (CHO-1703/ADR-180 D9): deterministic page-referenced text chunks
extracted in chora-creation from batch-job grounding files. Verification
corpus for crew-reported citations (D15) + reuse-ready (embedding slot) for
KG / growth-edge / daily-dose grounding via ContentRetrieval.SearchEmbeddings.
Immutable extraction artifacts — no UPDATE path.';

-- -----------------------------------------------------------------------------
-- question_generation_jobs.proposed_test_set_jsonb (OpenAPI ProposedTestSet)
-- -----------------------------------------------------------------------------
ALTER TABLE question_generation_jobs
    ADD COLUMN proposed_test_set_jsonb JSONB NULL;

COMMENT ON COLUMN question_generation_jobs.proposed_test_set_jsonb IS
'Lane 1c (D4/D5): composer-proposed test-set structure {title, description,
order:[draft_id], points:{draft_id:int}} parsed from the batch completed.v1
BatchCandidatePayload. NULL for single-question jobs / pre-1c batches.';

-- -----------------------------------------------------------------------------
-- GRANTs — explicit + idempotent (also covered by the default-privileges
-- ALTER in 9999_grant_app_roles.sql; specified here for clarity, mirroring
-- the 0016 convention).
-- -----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE source_material_chunks
    TO chora_creation_app_rw;
GRANT SELECT ON TABLE source_material_chunks
    TO chora_creation_app_ro;

COMMIT;
