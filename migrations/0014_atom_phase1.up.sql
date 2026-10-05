-- =============================================================================
-- chora-creation : 0014_atom_phase1.up.sql
--
-- ADR-156 Phase 1 LearningAtom redesign per:
--   - docs/architecture/atom-aggregate-holistic-assessment-2026-05-16.md §6
--   - User lock 2026-05-17 (8 design decisions)
--   - docs/m13/atom-phase1-execution-plan-2026-05-17.md §0
--
-- Domain        : Content Creation (5 core)
-- Database      : chora_creation
-- Author        : Agent B1 (ATOM Phase 1 Wave 1 parallel dispatch)
-- Date          : 2026-05-17
--
-- SIT pre-MVP per Decision #8 — in-place rename + new columns; NO version
-- bump; old atom_type column dropped after backfill in this same migration.
--
-- Changes:
--   1. Decision #1: relax title NOT NULL.
--   2. Decision #2: rename atom_type column → question_type. Old column
--      dropped in this migration after backfill (single rolling deploy).
--      The PG ENUM type `atom_type` (5-value: mcq/flashcard/video/essay/
--      outline) is RENAMED to `learning_atom_question_type` to free the
--      `atom_type` identifier for future use; the column on learning_atoms
--      becomes a VARCHAR(32) to align with chora-contracts Phase 1 wire
--      format. The separate `question_type` PG ENUM (16-value MCQ/OE +
--      reserved_*) on questions.question_type is UNTOUCHED — it lives in a
--      different namespace by column.
--   3. NEW: stem TEXT NOT NULL — promoted to top-level. Backfilled from
--      questions.prompt joined by atom_id where present, else empty string.
--   4. Decision #3: cognitive_level VARCHAR(32) with Bloom 6-enum check.
--   5. Decision #4: subject VARCHAR(64).
--   6. Decision #5: media_assets JSONB ≤1 image entry (Phase 1).
--   7. Decision #6: imda_dimension_tags JSONB (enum array, 4-value IMDA).
--   8. NEW: author_note TEXT NULL — author's private free-text note.
--
-- Cross-DB JOINs FORBIDDEN per ddd-enforcement.md. RLS policies on
-- learning_atoms unchanged. GRANTs auto-apply via 9999_grant_app_roles.sql.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Decision #2: rename atom_type column → question_type. The new column is
-- VARCHAR(32) — aligns with chora-contracts wire shape and decouples from
-- the legacy 5-value enum type (which is reserved for future reintroduction
-- or eventual drop after callers migrate).
--
-- Backfill: idempotent — UPDATE only NULL rows. Tighten with NOT NULL after.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN IF NOT EXISTS question_type VARCHAR(32);

UPDATE learning_atoms
SET question_type = atom_type::text
WHERE question_type IS NULL AND atom_type IS NOT NULL;

-- The rare NULL case (legacy rows that never had atom_type set) default to
-- 'outline' — the Phyllis MVP fallback flavour per phyllis.go.
UPDATE learning_atoms
SET question_type = 'outline'
WHERE question_type IS NULL;

ALTER TABLE learning_atoms ALTER COLUMN question_type SET NOT NULL;
ALTER TABLE learning_atoms DROP COLUMN atom_type;

-- Free the `atom_type` ENUM type identifier. Renamed (not dropped) so any
-- ad-hoc rollback (down.sql) can flip the rename without recreating the
-- enum from scratch.
ALTER TYPE atom_type RENAME TO learning_atom_question_type;

-- -----------------------------------------------------------------------------
-- Decision #1: relax title NOT NULL.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ALTER COLUMN title DROP NOT NULL;

-- -----------------------------------------------------------------------------
-- Promote stem TEXT NOT NULL to top-level. Backfill from questions.prompt
-- (latest non-deleted question per atom). Atoms with no question land on
-- the empty-string default; handler enforces non-empty on write.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN stem TEXT;

UPDATE learning_atoms a
SET stem = COALESCE(
    (SELECT q.prompt
       FROM questions q
      WHERE q.atom_id = a.atom_id
        AND q.deleted_at IS NULL
      ORDER BY q.created_at DESC
      LIMIT 1),
    ''
)
WHERE stem IS NULL;

ALTER TABLE learning_atoms ALTER COLUMN stem SET NOT NULL;

-- -----------------------------------------------------------------------------
-- Decision #3: cognitive_level VARCHAR(32) with Bloom enum check.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN cognitive_level VARCHAR(32);

ALTER TABLE learning_atoms ADD CONSTRAINT learning_atoms_cognitive_level_check
    CHECK (
        cognitive_level IS NULL
        OR cognitive_level IN (
            'knowledge',
            'comprehension',
            'application',
            'analysis',
            'synthesis',
            'evaluation'
        )
    );

-- -----------------------------------------------------------------------------
-- Decision #4: subject VARCHAR(64).
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN subject VARCHAR(64);

-- -----------------------------------------------------------------------------
-- Decision #5: media_assets JSONB (≤1 image entry; cap enforced at handler).
--
-- Shape per AtomMediaAsset (chora-contracts/openapi/creation-admin.yaml):
--   [{type:'image', url:string, alt_text:string, mime:string, size_bytes:int}]
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN media_assets JSONB;

-- -----------------------------------------------------------------------------
-- Decision #6: imda_dimension_tags JSONB enum array.
-- Values per ImdaDimensionTag (ADR-141 canonical labels):
--   ['accountability', 'transparency', 'safety_robustness',
--    'fairness_human_oversight']
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN imda_dimension_tags JSONB;

-- -----------------------------------------------------------------------------
-- NEW: author_note TEXT NULL — author's private free-text note. Distinct
-- semantics from `title`; an author scratchpad / editor context.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_atoms ADD COLUMN author_note TEXT;

COMMENT ON COLUMN learning_atoms.question_type IS
'ADR-156 Phase 1 (renamed from atom_type). VARCHAR(32) holding the wire-level
QuestionType (e.g. mcq/flashcard/video/essay/outline). Handler validates the
enum; DB no longer constrains so the contract can evolve without a migration.';

COMMENT ON COLUMN learning_atoms.stem IS
'ADR-156 Phase 1 NEW. Canonical question prompt. Backfilled from
questions.prompt where present. FE auto-derives display_label from this
when title is empty.';

COMMENT ON COLUMN learning_atoms.cognitive_level IS
'ADR-156 Decision #3. Bloom 6-level taxonomy (knowledge/comprehension/
application/analysis/synthesis/evaluation). Optional initially; mandatory
post-curriculum onboarding.';

COMMENT ON COLUMN learning_atoms.subject IS
'ADR-156 Decision #4. Free-text subject (≤64). Vocabulary curation deferred.';

COMMENT ON COLUMN learning_atoms.media_assets IS
'ADR-156 Decision #5. JSONB array; Phase 1 ≤1 image entry. Shape per
AtomMediaAsset (chora-contracts/openapi/creation-admin.yaml).';

COMMENT ON COLUMN learning_atoms.imda_dimension_tags IS
'ADR-156 Decision #6 + ADR-141 canonical labels. JSONB string array of
IMDA Model AI Governance Framework dimension tags. Optional initially; gate
before public catalog later.';

COMMENT ON COLUMN learning_atoms.author_note IS
'ADR-156 Phase 1. Author''s private free-text note — distinct from title;
authoring scratchpad / context-for-editors.';

COMMIT;
