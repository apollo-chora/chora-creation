-- =============================================================================
-- chora-creation : 0007_question_revisions.up.sql
--
-- CR              : Question Authoring (MCQ + Open-Ended)
-- Design doc      : docs/m14/cr-question-authoring-design-2026-05-15.md §2.2 + §8
-- Domain          : Content Creation (5 core)
-- Database        : chora_creation
-- Author          : agent — P0+P1 question-authoring contracts/migrations
-- Date            : 2026-05-15
--
-- Purpose:
--   APPEND-ONLY history table for Question payload changes (ddd-enforcement
--   invariant #4 — mirrors atom_revisions). Every authoring mutation (manual
--   POST/PATCH, AI-draft accept, AI-model-answer save) creates a NEW
--   QuestionRevision and bumps `questions.latest_revision_id` to it.
--
--   UPDATE + DELETE are rejected at the trigger layer using the existing
--   `enforce_atom_revisions_append_only()` function (defined in 0001_initial.sql).
--   The function raises a generic exception that names the offending TG_OP,
--   so reusing it across question_revisions is safe — error message will read
--   "atom_revisions is append-only ..." which is correct domain framing
--   (append-only invariant per ddd-enforcement #4).
--
-- Cross-DB JOINs FORBIDDEN. authored_by_gcid stays a cross-domain UUID
-- without FK.
-- =============================================================================

BEGIN;

CREATE TABLE question_revisions (
    revision_id        UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    question_id        UUID                 NOT NULL REFERENCES questions(question_id) ON DELETE RESTRICT,
    atom_id            UUID                 NOT NULL,
    tenant_id          UUID                 NOT NULL,
    revision_number    INTEGER              NOT NULL,
    prompt             TEXT                 NULL,
    -- Discriminated snapshot — exactly one is populated per the parent
    -- question's question_type. JSONB structure documented in design §2.3 (MCQ)
    -- + §2.4 (OE).
    mcq_payload        JSONB                NULL,
    oe_payload         JSONB                NULL,
    source_type        revision_source_type NOT NULL,
    source_metadata    JSONB                NOT NULL DEFAULT '{}'::jsonb,
    authored_by_gcid   UUID                 NOT NULL,
    authored_at        TIMESTAMPTZ          NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ          NOT NULL DEFAULT now(),
    UNIQUE (question_id, revision_number)
);

CREATE INDEX idx_question_revisions_question
    ON question_revisions (question_id, revision_number);
CREATE INDEX idx_question_revisions_tenant
    ON question_revisions (tenant_id);
CREATE INDEX idx_question_revisions_atom
    ON question_revisions (atom_id);

-- Append-only invariant — reuse the shared function from 0001_initial.sql.
CREATE TRIGGER trg_question_revisions_no_update
    BEFORE UPDATE ON question_revisions
    FOR EACH ROW EXECUTE FUNCTION enforce_atom_revisions_append_only();

CREATE TRIGGER trg_question_revisions_no_delete
    BEFORE DELETE ON question_revisions
    FOR EACH ROW EXECUTE FUNCTION enforce_atom_revisions_append_only();

ALTER TABLE question_revisions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON question_revisions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE question_revisions IS
'APPEND-ONLY revision history of a Question payload (ddd-enforcement #4).
UPDATE/DELETE rejected by trigger. Each row is the full payload snapshot at
that revision_number. The corresponding `questions.latest_revision_id` points
at the most-recent row in this table.';

COMMIT;
