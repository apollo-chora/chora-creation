-- =============================================================================
-- chora-creation : 0006_questions.up.sql
--
-- CR              : Question Authoring (MCQ + Open-Ended)
-- Design doc      : docs/m14/cr-question-authoring-design-2026-05-15.md §2 + §8
-- Plan            : ~/.claude/plans/golden-hopping-owl.md (D2 — 1 atom = 1 question)
-- Domain          : Content Creation (5 core)
-- Database        : chora_creation
-- Author          : agent — P0+P1 question-authoring contracts/migrations
-- Date            : 2026-05-15
-- Architecture    : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Purpose:
--   Creates the `questions` table — the Question sub-entity of LearningAtom.
--   One Question per atom enforced by a partial UNIQUE index on
--   (atom_id) WHERE deleted_at IS NULL — per D2 in the plan.
--
--   The discriminated payload (mcq_payload JSONB OR oe_payload JSONB) is
--   guarded by a CHECK constraint that exactly one is populated for the
--   in-scope MCQ + OE types. Reserved_* values are NOT enforced here; the
--   handler returns 501 for those.
--
-- Cross-DB JOINs FORBIDDEN per ddd-enforcement.md. Cross-domain references
-- (author_gcid, course_id) are UUIDs without FK.
--
-- FK to learning_atoms uses ON DELETE RESTRICT to match the existing parent-
-- aggregate cascade rule (see 0001_initial.sql L94). Soft delete only —
-- learning_atoms.deleted_at and questions.deleted_at are independent
-- soft-delete columns per the aggregate invariant.
--
-- GRANT to chora_creation_app_rw / app_ro is auto-applied via the
-- ALTER DEFAULT PRIVILEGES clause in 9999_grant_app_roles.sql.
-- =============================================================================

BEGIN;

CREATE TABLE questions (
    question_id        UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id            UUID           NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id          UUID           NOT NULL,
    author_gcid        UUID           NOT NULL,
    question_type      question_type  NOT NULL,
    prompt             TEXT           NOT NULL,
    metadata           JSONB          NOT NULL DEFAULT '{}'::jsonb,
    source_type        revision_source_type NOT NULL DEFAULT 'manual',
    latest_revision_id UUID           NULL,
    -- Discriminated payload — exactly one is populated for in-scope types.
    -- Reserved_* values may have both NULL (handler rejects with 501).
    mcq_payload        JSONB          NULL,
    oe_payload         JSONB          NULL,
    created_at         TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ    NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ    NULL,
    -- D2: 1 atom = 1 question per the plan. Multi-question per atom is the
    -- §9.5 open question — design choice is to enforce 1:0..1 cardinality at
    -- the DB layer so future relaxation is an explicit migration.
    CHECK (
        (question_type = 'mcq' AND mcq_payload IS NOT NULL AND oe_payload IS NULL)
        OR (question_type = 'oe'  AND oe_payload  IS NOT NULL AND mcq_payload IS NULL)
        OR (question_type NOT IN ('mcq','oe'))
    )
);

-- D2 — 1 atom = 1 non-deleted question (per plan). Partial UNIQUE so
-- soft-deleted rows do not block re-authoring on the same atom.
CREATE UNIQUE INDEX uq_questions_atom_alive
    ON questions (atom_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_questions_atom_tenant
    ON questions (atom_id, tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_questions_tenant_type
    ON questions (tenant_id, question_type) WHERE deleted_at IS NULL;
CREATE INDEX idx_questions_author_gcid
    ON questions (author_gcid) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_questions_updated_at
    BEFORE UPDATE ON questions
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE questions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON questions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE questions IS
'Question sub-entity of LearningAtom (Content Creation). Cardinality is 1:0..1
per atom (D2). Discriminated payload (mcq_payload | oe_payload) determined by
question_type. Soft delete only; never hard-delete.';

COMMIT;
