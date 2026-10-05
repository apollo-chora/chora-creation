-- =============================================================================
-- chora-creation : 0005_question_type_enum.up.sql
--
-- CR              : Question Authoring (MCQ + Open-Ended)
-- Design doc      : docs/m14/cr-question-authoring-design-2026-05-15.md §2.1 + §8
-- Plan            : ~/.claude/plans/golden-hopping-owl.md (D3 split)
-- Domain          : Content Creation (5 core)
-- Database        : chora_creation
-- Author          : agent — P0+P1 question-authoring contracts/migrations
-- Date            : 2026-05-15
-- Architecture    : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Purpose:
--   Introduces the `question_type` ENUM — the payload-shape discriminator for
--   the new Question sub-entity of LearningAtom. Distinct from the existing
--   `atom_type` enum (which is the container flavour — mcq/flashcard/video/
--   essay/outline). Per design §2.1 (and D3 in golden-hopping-owl.md), the
--   16-value enum encodes the 2 in-scope question types (`mcq` + `oe`) and 14
--   `reserved_*` sentinels so the contract stays stable as future types onboard
--   without an API break.
--
--   Alphabetical order across the reserved sentinels per design §2.1.
--   DO NOT reorder — ordinal stability is part of the contract.
--
-- Cross-DB JOINs FORBIDDEN — see ddd-enforcement.md. Cross-domain identifiers
-- (course_id, gcid, ...) stay as UUIDs without FK constraint.
-- =============================================================================

BEGIN;

CREATE TYPE question_type AS ENUM (
    'mcq',
    'oe',
    'reserved_code_execution',
    'reserved_completion',
    'reserved_drag_drop',
    'reserved_fill_blank',
    'reserved_matching',
    'reserved_multi_select',
    'reserved_multimedia',
    'reserved_oral',
    'reserved_ordering',
    'reserved_peer_graded',
    'reserved_short_answer',
    'reserved_simulation',
    'reserved_table_completion',
    'reserved_true_false'
);

COMMENT ON TYPE question_type IS
'Payload-shape discriminator for the Question sub-entity of LearningAtom.
mcq + oe are in-scope per the 2026-05-15 Question Authoring CR; the 14
reserved_* values are sentinels — present so future question types can
onboard without breaking the OpenAPI/Protobuf enum contract.';

COMMIT;
