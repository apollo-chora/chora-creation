-- =============================================================================
-- chora-creation : 0001_initial.sql
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : agent-a5e52e89b73ede1d2 (db-migrations-11-services)
-- Date          : 2026-05-08
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Aggregates owned by this database:
--   - LearningAtom (PRIMARY AGGREGATE ROOT — CLAUDE.md §1)
--   - AtomRevision (append-only — ddd-enforcement #4)
--   - KnowledgeGraph edges
--   - Atom variants (flashcard, code-editor, drag-drop, multimedia)
--   - Atom embeddings (pgvector — RAG retrieval at consumption time)
--
-- Cross-DB JOINs FORBIDDEN — all cross-domain references are UUIDs without FK.
-- Soft delete only (deleted_at). UUIDv7 generated server-side via gen_random_uuid()
-- as a transitional placeholder until the chora-contracts UUIDv7 helper lands.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Extensions
-- -----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";  -- pgvector 0.8+ (CLAUDE.md §1)

-- -----------------------------------------------------------------------------
-- Shared trigger: bump updated_at on row modification
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION creation_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- ENUMs (mirror Go domain enums in internal/domain/atom + atom_variants)
-- -----------------------------------------------------------------------------
CREATE TYPE atom_mode AS ENUM ('straight-up', 'graph-based');
CREATE TYPE atom_status AS ENUM ('draft', 'published', 'archived');
CREATE TYPE atom_type AS ENUM ('mcq', 'flashcard', 'video', 'essay', 'outline');
CREATE TYPE revision_source_type AS ENUM ('manual', 'ai_assist', 'community_atom_bank');
CREATE TYPE knowledge_edge_type AS ENUM ('prerequisite', 'related', 'curiosity');

-- -----------------------------------------------------------------------------
-- learning_atoms — primary aggregate root
-- -----------------------------------------------------------------------------
CREATE TABLE learning_atoms (
    atom_id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID        NOT NULL,
    gcid              UUID        NOT NULL,            -- author / owner; cross-domain ref
    course_id         UUID        NULL,                -- Phyllis MVP course-bound; cross-DB ref
    title             VARCHAR(256) NOT NULL,
    body              TEXT         NOT NULL DEFAULT '',
    tags              JSONB        NOT NULL DEFAULT '[]'::jsonb,
    mode              atom_mode    NOT NULL DEFAULT 'straight-up',
    status            atom_status  NOT NULL DEFAULT 'draft',
    atom_type         atom_type    NULL,
    difficulty        SMALLINT     NULL CHECK (difficulty IS NULL OR (difficulty BETWEEN 0 AND 5)),
    revision          INTEGER      NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL
);

CREATE INDEX idx_learning_atoms_tenant         ON learning_atoms (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_learning_atoms_gcid           ON learning_atoms (gcid)      WHERE deleted_at IS NULL;
CREATE INDEX idx_learning_atoms_course         ON learning_atoms (course_id) WHERE course_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_learning_atoms_status         ON learning_atoms (status)    WHERE deleted_at IS NULL;
CREATE INDEX idx_learning_atoms_tags_gin       ON learning_atoms USING GIN (tags);

CREATE TRIGGER trg_learning_atoms_updated_at
    BEFORE UPDATE ON learning_atoms
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE learning_atoms ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON learning_atoms
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_revisions — APPEND-ONLY history per ddd-enforcement #4
--
-- Append-only invariant enforced by trigger that rejects UPDATE / DELETE.
-- Soft-delete is on the parent aggregate (learning_atoms.deleted_at) only.
-- -----------------------------------------------------------------------------
CREATE TABLE atom_revisions (
    revision_id       UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id           UUID                 NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id         UUID                 NOT NULL,
    revision_number   INTEGER              NOT NULL,
    body              TEXT                 NOT NULL,
    authored_by       UUID                 NOT NULL,   -- gcid of author (cross-domain ref)
    authored_at       TIMESTAMPTZ          NOT NULL DEFAULT now(),
    source_type       revision_source_type NOT NULL,
    source_metadata   JSONB                NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ          NOT NULL DEFAULT now(),
    UNIQUE (atom_id, revision_number)
);

CREATE INDEX idx_atom_revisions_atom    ON atom_revisions (atom_id, revision_number);
CREATE INDEX idx_atom_revisions_tenant  ON atom_revisions (tenant_id);

CREATE OR REPLACE FUNCTION enforce_atom_revisions_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'atom_revisions is append-only (ddd-enforcement #4): % rejected', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_atom_revisions_no_update
    BEFORE UPDATE ON atom_revisions
    FOR EACH ROW EXECUTE FUNCTION enforce_atom_revisions_append_only();

CREATE TRIGGER trg_atom_revisions_no_delete
    BEFORE DELETE ON atom_revisions
    FOR EACH ROW EXECUTE FUNCTION enforce_atom_revisions_append_only();

ALTER TABLE atom_revisions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_revisions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- knowledge_graph_edges — Discovery-mode topology
-- -----------------------------------------------------------------------------
CREATE TABLE knowledge_graph_edges (
    edge_id           UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID                 NOT NULL,
    gcid              UUID                 NOT NULL,
    source_atom_id    UUID                 NOT NULL,
    target_atom_id    UUID                 NOT NULL,
    edge_type         knowledge_edge_type  NOT NULL,
    weight            REAL                 NOT NULL DEFAULT 0.5 CHECK (weight >= 0 AND weight <= 1),
    created_at        TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ          NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ          NULL,
    CHECK (source_atom_id <> target_atom_id),
    UNIQUE (tenant_id, source_atom_id, target_atom_id, edge_type)
);

CREATE INDEX idx_kg_edges_source  ON knowledge_graph_edges (tenant_id, source_atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_kg_edges_target  ON knowledge_graph_edges (tenant_id, target_atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_kg_edges_type    ON knowledge_graph_edges (edge_type)                 WHERE deleted_at IS NULL;

CREATE TRIGGER trg_kg_edges_updated_at
    BEFORE UPDATE ON knowledge_graph_edges
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE knowledge_graph_edges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_graph_edges
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_variants_flashcard — front + back text payload
-- -----------------------------------------------------------------------------
CREATE TABLE atom_variants_flashcard (
    variant_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id           UUID         NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id         UUID         NOT NULL,
    front             VARCHAR(1024) NOT NULL,
    back              VARCHAR(4096) NOT NULL,
    published_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL
);

CREATE INDEX idx_flashcard_atom    ON atom_variants_flashcard (atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_flashcard_tenant  ON atom_variants_flashcard (tenant_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_flashcard_updated_at
    BEFORE UPDATE ON atom_variants_flashcard
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE atom_variants_flashcard ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_variants_flashcard
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_variants_code_editor — language + starter + tests + solution
-- -----------------------------------------------------------------------------
CREATE TABLE atom_variants_code_editor (
    variant_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id           UUID         NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id         UUID         NOT NULL,
    language          VARCHAR(32)  NOT NULL,
    starter_code      TEXT         NOT NULL DEFAULT '',
    test_cases        JSONB        NOT NULL DEFAULT '[]'::jsonb,
    solution          TEXT         NOT NULL,
    published_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL,
    CHECK (language IN ('go','python','javascript','typescript','java','rust','sql'))
);

CREATE INDEX idx_code_editor_atom    ON atom_variants_code_editor (atom_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_code_editor_tenant  ON atom_variants_code_editor (tenant_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_code_editor_updated_at
    BEFORE UPDATE ON atom_variants_code_editor
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE atom_variants_code_editor ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_variants_code_editor
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_variants_drag_drop — drag-drop matching pairs
-- -----------------------------------------------------------------------------
CREATE TABLE atom_variants_drag_drop (
    variant_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id           UUID         NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id         UUID         NOT NULL,
    left_items        JSONB        NOT NULL DEFAULT '[]'::jsonb,
    right_items       JSONB        NOT NULL DEFAULT '[]'::jsonb,
    mappings          JSONB        NOT NULL DEFAULT '[]'::jsonb,
    published_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL
);

CREATE INDEX idx_drag_drop_atom    ON atom_variants_drag_drop (atom_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_drag_drop_tenant  ON atom_variants_drag_drop (tenant_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_drag_drop_updated_at
    BEFORE UPDATE ON atom_variants_drag_drop
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE atom_variants_drag_drop ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_variants_drag_drop
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_variants_multimedia — video URL + question + answer
-- -----------------------------------------------------------------------------
CREATE TABLE atom_variants_multimedia (
    variant_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    atom_id           UUID         NOT NULL REFERENCES learning_atoms(atom_id) ON DELETE RESTRICT,
    tenant_id         UUID         NOT NULL,
    video_url         TEXT         NOT NULL,
    question          VARCHAR(1024) NOT NULL,
    correct_answer    VARCHAR(512) NOT NULL,
    thumbnail_url     TEXT         NULL,
    published_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL,
    CHECK (video_url     LIKE 'https://%'),
    CHECK (thumbnail_url IS NULL OR thumbnail_url LIKE 'https://%')
);

CREATE INDEX idx_multimedia_atom    ON atom_variants_multimedia (atom_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_multimedia_tenant  ON atom_variants_multimedia (tenant_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_multimedia_updated_at
    BEFORE UPDATE ON atom_variants_multimedia
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE atom_variants_multimedia ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_variants_multimedia
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atom_embeddings — pgvector RAG embeddings (768-d Vertex AI Gemini default)
-- -----------------------------------------------------------------------------
CREATE TABLE atom_embeddings (
    atom_id           UUID         PRIMARY KEY REFERENCES learning_atoms(atom_id) ON DELETE CASCADE,
    tenant_id         UUID         NOT NULL,
    embedding         vector(768)  NOT NULL,
    model_id          VARCHAR(64)  NOT NULL,         -- e.g., 'text-embedding-004'
    embedding_version INTEGER      NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- IVFFLAT index for cosine similarity. Lists tuned for ~10k atoms; revisit at M14.
CREATE INDEX idx_atom_embeddings_cosine
    ON atom_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_atom_embeddings_tenant ON atom_embeddings (tenant_id);

CREATE TRIGGER trg_atom_embeddings_updated_at
    BEFORE UPDATE ON atom_embeddings
    FOR EACH ROW EXECUTE FUNCTION creation_set_updated_at();

ALTER TABLE atom_embeddings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_embeddings
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;
