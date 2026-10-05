// question_repository.go — pgx-backed implementation of
// ports.QuestionRepository for the Question sub-entity of LearningAtom.
//
// Per /Users/daleleung/.claude/plans/golden-hopping-owl.md P3-A:
//
//   - Save(ctx, q, rev) — creates the parent Question + the first
//     QuestionRevision in a single tenant-scoped transaction. The unique
//     partial index uq_questions_atom_alive enforces D2 (1 atom = 1 non-
//     deleted question); on the 23505 violation the adapter returns
//     question.ErrAtomHasQuestion so the handler can map to 409.
//
//   - GetByAtomID / GetByID — joins questions + question_revisions to
//     return the live question plus its LATEST revision (ORDER BY
//     revision_number DESC LIMIT 1). JSONB payload columns are decoded
//     into *question.MCQPayload or *question.OEPayload based on the
//     parent question_type.
//
//   - AppendRevision — SELECTs MAX(revision_number) for the question
//     inside the same tenant tx, then INSERTs the next row. The migration
//     0007 trigger reuses enforce_atom_revisions_append_only(), so any
//     attempt to UPDATE/DELETE this table is rejected by Postgres
//     (defence-in-depth — the adapter only ever INSERTs).
//
//   - SoftDelete — UPDATE deleted_at = now() inside the tenant tx.
//
// RLS contract — chora_creation_app_rw is NOBYPASSRLS, so every method
// wraps its SQL in RunInTenantTx so SET LOCAL chora.tenant_id runs in the
// SAME transaction as the user query (#27 pattern from atom_repository.go).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// QuestionRepository is the pgx-backed implementation of ports.QuestionRepository.
//
// When `tx` is non-nil, every method wraps its SQL in tx.RunInTenantTx so
// SET LOCAL chora.tenant_id runs before the user query.
type QuestionRepository struct {
	tx TxQuerier
}

// NewQuestionRepository wraps a *PgxPoolQuerier (production wiring) — the
// *PgxPoolQuerier satisfies TxQuerier so every call goes through a tenant-
// scoped tx.
func NewQuestionRepository(querier *PgxPoolQuerier) *QuestionRepository {
	return &QuestionRepository{tx: querier}
}

// NewQuestionRepositoryFromTxQuerier wraps an explicit TxQuerier — used by
// tests injecting a stub TxQuerier that records RunInTenantTx invocations.
func NewQuestionRepositoryFromTxQuerier(tq TxQuerier) *QuestionRepository {
	return &QuestionRepository{tx: tq}
}

// -----------------------------------------------------------------------------
// Save — atomic create of questions row + first question_revisions row
// -----------------------------------------------------------------------------

const (
	sqlInsertQuestion = `
        INSERT INTO questions (
            question_id, atom_id, tenant_id, author_gcid,
            question_type, prompt,
            source_type, latest_revision_id,
            mcq_payload, oe_payload,
            created_at, updated_at
        ) VALUES (
            $1, $2, $3, $4,
            $5::question_type, $6,
            $7::revision_source_type, $8,
            NULLIF($9, '')::jsonb, NULLIF($10, '')::jsonb,
            $11, $12
        )
    `

	sqlInsertQuestionRevision = `
        INSERT INTO question_revisions (
            revision_id, question_id, atom_id, tenant_id,
            revision_number, prompt,
            mcq_payload, oe_payload,
            source_type, source_metadata,
            authored_by_gcid, authored_at
        ) VALUES (
            $1, $2, $3, $4,
            $5, $6,
            NULLIF($7, '')::jsonb, NULLIF($8, '')::jsonb,
            $9::revision_source_type, COALESCE(NULLIF($10, '')::jsonb, '{}'::jsonb),
            $11, $12
        )
    `

	sqlSoftDeleteQuestion = `
        UPDATE questions
           SET deleted_at = now(),
               updated_at = now()
         WHERE question_id = $1
           AND tenant_id   = $2
           AND deleted_at IS NULL
    `

	sqlSelectMaxRevisionNumber = `
        SELECT COALESCE(MAX(revision_number), 0)
          FROM question_revisions
         WHERE question_id = $1
    `

	// Joined select — left of FROM is the question, JOIN brings in the
	// latest QuestionRevision (subquery picks the highest revision_number
	// for each question). Filters: deleted_at IS NULL on the parent.
	sqlSelectQuestionWithLatestRevision = `
        SELECT q.question_id, q.atom_id, q.tenant_id, q.author_gcid,
               q.question_type::text, q.prompt,
               q.source_type::text, COALESCE(q.latest_revision_id::text, ''),
               COALESCE((
                   SELECT MAX(qr.revision_number)
                     FROM question_revisions qr
                    WHERE qr.question_id = q.question_id
               ), 0)                                          AS revision,
               q.deleted_at,
               q.created_at, q.updated_at,
               COALESCE(q.mcq_payload::text, ''),
               COALESCE(q.oe_payload::text, ''),
               r.revision_id, r.revision_number,
               r.source_type::text,
               r.prompt,
               r.authored_at
          FROM questions q
          JOIN question_revisions r
            ON r.question_id = q.question_id
           AND r.revision_number = (
               SELECT MAX(rr.revision_number)
                 FROM question_revisions rr
                WHERE rr.question_id = q.question_id
           )
         WHERE q.tenant_id = $1
           AND q.deleted_at IS NULL
           AND %s
         ORDER BY r.revision_number DESC
         LIMIT 1
    `

	// sqlSelectQuestionWithLatestRevisionAnyState — same joined projection
	// WITHOUT the parent deleted_at filter. Reserved for the ADR-229 A1
	// orphan mint (the archive cascade soft-deletes the question BEFORE the
	// orphan_required event arrives; the mint still clones its content).
	sqlSelectQuestionWithLatestRevisionAnyState = `
        SELECT q.question_id, q.atom_id, q.tenant_id, q.author_gcid,
               q.question_type::text, q.prompt,
               q.source_type::text, COALESCE(q.latest_revision_id::text, ''),
               COALESCE((
                   SELECT MAX(qr.revision_number)
                     FROM question_revisions qr
                    WHERE qr.question_id = q.question_id
               ), 0)                                          AS revision,
               q.deleted_at,
               q.created_at, q.updated_at,
               COALESCE(q.mcq_payload::text, ''),
               COALESCE(q.oe_payload::text, ''),
               r.revision_id, r.revision_number,
               r.source_type::text,
               r.prompt,
               r.authored_at
          FROM questions q
          JOIN question_revisions r
            ON r.question_id = q.question_id
           AND r.revision_number = (
               SELECT MAX(rr.revision_number)
                 FROM question_revisions rr
                WHERE rr.question_id = q.question_id
           )
         WHERE q.tenant_id = $1
           AND %s
         ORDER BY r.revision_number DESC
         LIMIT 1
    `
)

// Save persists the parent question + its first revision in a single tenant
// tx. On unique-constraint violation against `uq_questions_atom_alive` the
// adapter returns question.ErrAtomHasQuestion (D2).
func (r *QuestionRepository) Save(ctx context.Context, q *question.Question, rev *question.QuestionRevision) error {
	if q == nil || rev == nil {
		return errors.New("pg.QuestionRepository.Save: nil question or revision")
	}
	if r.tx == nil {
		return errors.New("pg.QuestionRepository.Save: no TxQuerier wired (production must wire NewQuestionRepository)")
	}

	mcqJSON, oeJSON, err := encodePayloads(q.MCQ, q.OE)
	if err != nil {
		return fmt.Errorf("pg.QuestionRepository.Save: encode payload: %w", err)
	}

	return r.tx.RunInTenantTx(ctx, q.TenantID, func(ctx context.Context, tx Tx) error {
		// 1. INSERT INTO questions
		if err := tx.Exec(ctx, sqlInsertQuestion,
			q.QuestionID, q.AtomID, q.TenantID, q.AuthorGcid,
			string(q.Type), q.Prompt,
			string(q.SourceType), nullableUUID(q.LatestRevisionID),
			mcqJSON, oeJSON,
			q.CreatedAt, q.UpdatedAt,
		); err != nil {
			if isUniqueViolationOnAtomAlive(err) {
				return question.ErrAtomHasQuestion
			}
			return fmt.Errorf("pg.QuestionRepository.Save: insert questions: %w", err)
		}

		// 2. INSERT INTO question_revisions
		revMCQ, revOE, err := encodePayloads(rev.MCQPayload, rev.OEPayload)
		if err != nil {
			return fmt.Errorf("pg.QuestionRepository.Save: encode revision payload: %w", err)
		}
		var metaJSON string
		if len(rev.SourceMetadata) > 0 {
			b, mErr := json.Marshal(rev.SourceMetadata)
			if mErr != nil {
				return fmt.Errorf("pg.QuestionRepository.Save: encode metadata: %w", mErr)
			}
			metaJSON = string(b)
		}
		if err := tx.Exec(ctx, sqlInsertQuestionRevision,
			rev.RevisionID, rev.QuestionID, rev.AtomID, rev.TenantID,
			rev.RevisionNumber, rev.Prompt,
			revMCQ, revOE,
			string(rev.SourceType), metaJSON,
			rev.AuthoredByGCID, rev.AuthoredAt,
		); err != nil {
			return fmt.Errorf("pg.QuestionRepository.Save: insert question_revisions: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Get — by atom_id (returns the live, non-deleted question on that atom)
// -----------------------------------------------------------------------------

func (r *QuestionRepository) GetByAtomID(ctx context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error) {
	const where = "q.atom_id = $2"
	return r.fetchOne(ctx, tenantID, where, atomID)
}

func (r *QuestionRepository) GetByID(ctx context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	const where = "q.question_id = $2"
	return r.fetchOne(ctx, tenantID, where, questionID)
}

// GetByAtomIDAnyState mirrors GetByAtomID but INCLUDES soft-deleted
// questions. Reserved for the ADR-229 A1 orphan mint (CHO-2132): the archive
// trigger fires AFTER deleteAtom's cascade soft-deleted the question, and the
// mint still needs the last-published revision's content to clone. Never use
// it on a read path — default queries filter deleted_at IS NULL.
func (r *QuestionRepository) GetByAtomIDAnyState(ctx context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error) {
	if r.tx == nil {
		return nil, nil, errors.New("pg.QuestionRepository.GetByAtomIDAnyState: no TxQuerier wired")
	}
	// Same joined projection as sqlSelectQuestionWithLatestRevision minus the
	// parent deleted_at filter.
	sql := fmt.Sprintf(sqlSelectQuestionWithLatestRevisionAnyState, "q.atom_id = $2")
	var (
		q   *question.Question
		rev *question.QuestionRevision
		nf  bool
	)
	txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sql, tenantID, atomID)
		got, gotRev, err := scanQuestionWithRevision(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				nf = true
				return nil
			}
			return err
		}
		q = got
		rev = gotRev
		return nil
	})
	if txErr != nil {
		return nil, nil, fmt.Errorf("pg.QuestionRepository.GetByAtomIDAnyState: %w", txErr)
	}
	if nf {
		return nil, nil, question.ErrNotFound
	}
	return q, rev, nil
}

func (r *QuestionRepository) fetchOne(ctx context.Context, tenantID, where string, arg string) (*question.Question, *question.QuestionRevision, error) {
	if r.tx == nil {
		return nil, nil, errors.New("pg.QuestionRepository.fetchOne: no TxQuerier wired")
	}
	sql := fmt.Sprintf(sqlSelectQuestionWithLatestRevision, where)
	var (
		q   *question.Question
		rev *question.QuestionRevision
		nf  bool
	)
	txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sql, tenantID, arg)
		got, gotRev, err := scanQuestionWithRevision(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				nf = true
				return nil
			}
			return err
		}
		q = got
		rev = gotRev
		return nil
	})
	if txErr != nil {
		return nil, nil, fmt.Errorf("pg.QuestionRepository.fetchOne: %w", txErr)
	}
	if nf {
		return nil, nil, question.ErrNotFound
	}
	return q, rev, nil
}

// -----------------------------------------------------------------------------
// AppendRevision — append-only, monotonic revision_number
// -----------------------------------------------------------------------------

func (r *QuestionRepository) AppendRevision(ctx context.Context, rev *question.QuestionRevision) error {
	if rev == nil {
		return errors.New("pg.QuestionRepository.AppendRevision: nil revision")
	}
	if r.tx == nil {
		return errors.New("pg.QuestionRepository.AppendRevision: no TxQuerier wired")
	}

	mcqJSON, oeJSON, err := encodePayloads(rev.MCQPayload, rev.OEPayload)
	if err != nil {
		return fmt.Errorf("pg.QuestionRepository.AppendRevision: encode payload: %w", err)
	}
	var metaJSON string
	if len(rev.SourceMetadata) > 0 {
		b, mErr := json.Marshal(rev.SourceMetadata)
		if mErr != nil {
			return fmt.Errorf("pg.QuestionRepository.AppendRevision: encode metadata: %w", mErr)
		}
		metaJSON = string(b)
	}

	return r.tx.RunInTenantTx(ctx, rev.TenantID, func(ctx context.Context, tx Tx) error {
		// Pessimistic monotonic-number computation. SELECT MAX(revision_number)
		// inside the same tx — Postgres locks the matching rows under the
		// shared snapshot so concurrent appends get serialised; the UNIQUE
		// (question_id, revision_number) constraint is the ultimate guarantor.
		var maxRev int32
		if err := tx.QueryRow(ctx, sqlSelectMaxRevisionNumber, rev.QuestionID).Scan(&maxRev); err != nil {
			if !errors.Is(err, ErrNoRows) {
				return fmt.Errorf("pg.QuestionRepository.AppendRevision: select max: %w", err)
			}
		}
		rev.RevisionNumber = int(maxRev) + 1

		if err := tx.Exec(ctx, sqlInsertQuestionRevision,
			rev.RevisionID, rev.QuestionID, rev.AtomID, rev.TenantID,
			rev.RevisionNumber, rev.Prompt,
			mcqJSON, oeJSON,
			string(rev.SourceType), metaJSON,
			rev.AuthoredByGCID, rev.AuthoredAt,
		); err != nil {
			return fmt.Errorf("pg.QuestionRepository.AppendRevision: insert: %w", err)
		}

		// Update the parent's latest_revision_id pointer AND sync the
		// current-state projection columns (prompt + payloads) to the new
		// revision. fetchOne / SnapshotQuestionByID read the payload from
		// questions.mcq_payload (NOT question_revisions), so without this sync
		// an appended revision (PATCH edit OR W8 image re-home) is invisible
		// to every reader — the row keeps the prior payload. The append-only
		// invariant applies to question_revisions (history), never to this
		// mutable current-state projection. Mirrors the in-mem fake repo
		// (which reflects rev → parent) so pg + fake agree.
		const updLatest = `
            UPDATE questions
               SET latest_revision_id = $1,
                   prompt             = $4,
                   mcq_payload        = NULLIF($5, '')::jsonb,
                   oe_payload         = NULLIF($6, '')::jsonb,
                   updated_at         = now()
             WHERE question_id = $2 AND tenant_id = $3
        `
		if err := tx.Exec(ctx, updLatest,
			rev.RevisionID, rev.QuestionID, rev.TenantID,
			rev.Prompt, mcqJSON, oeJSON,
		); err != nil {
			return fmt.Errorf("pg.QuestionRepository.AppendRevision: update latest: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// SoftDelete — UPDATE deleted_at = now(). Idempotent (the WHERE filters out
// already-deleted rows so re-calling is a no-op).
// -----------------------------------------------------------------------------

func (r *QuestionRepository) SoftDelete(ctx context.Context, tenantID, questionID string) error {
	if r.tx == nil {
		return errors.New("pg.QuestionRepository.SoftDelete: no TxQuerier wired")
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sqlSoftDeleteQuestion, questionID, tenantID); err != nil {
			return fmt.Errorf("pg.QuestionRepository.SoftDelete: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// encodePayloads returns the JSON string forms for the two discriminated
// payloads. Exactly one of MCQ / OE may be non-nil per the parent question's
// type (the migration CHECK enforces this at the DB layer; the domain
// constructor enforces it at the boundary).
func encodePayloads(mcq *question.MCQPayload, oe *question.OEPayload) (string, string, error) {
	var mcqStr, oeStr string
	if mcq != nil {
		b, err := json.Marshal(mcq)
		if err != nil {
			return "", "", fmt.Errorf("marshal mcq: %w", err)
		}
		mcqStr = string(b)
	}
	if oe != nil {
		b, err := json.Marshal(oe)
		if err != nil {
			return "", "", fmt.Errorf("marshal oe: %w", err)
		}
		oeStr = string(b)
	}
	return mcqStr, oeStr, nil
}

// nullableUUID returns an empty string for "" so the INSERT's NULLIF guard
// produces NULL when no value was supplied.
func nullableUUID(s string) string { return s }

// isUniqueViolationOnAtomAlive detects the 23505 unique-constraint violation
// against the partial UNIQUE index uq_questions_atom_alive declared by
// migration 0006 (the D2 enforcement). pgx wraps the error with a code-bearing
// message; we match on the constraint name to keep the predicate stable across
// pgx versions.
func isUniqueViolationOnAtomAlive(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") && strings.Contains(msg, "uq_questions_atom_alive")
}

// scanQuestionWithRevision maps the joined row into the domain pair.
//
// JSONB columns are scanned via `::text` casts in the SELECT so pgx can hand
// us a Go string; we then unmarshal into the discriminated payload struct
// based on the parent question_type.
func scanQuestionWithRevision(row interface{ Scan(...any) error }) (*question.Question, *question.QuestionRevision, error) {
	var (
		q               question.Question
		qType           string
		sourceType      string
		latestRevID     string
		revision        int32
		deletedAt       *time.Time
		mcqJSON, oeJSON string
		revID           string
		revNumber       int32
		revSourceType   string
		revPrompt       string
		revAuthoredAt   time.Time
		createdAt       time.Time
		updatedAt       time.Time
	)
	if err := row.Scan(
		&q.QuestionID, &q.AtomID, &q.TenantID, &q.AuthorGcid,
		&qType, &q.Prompt,
		&sourceType, &latestRevID,
		&revision,
		&deletedAt,
		&createdAt, &updatedAt,
		&mcqJSON, &oeJSON,
		&revID, &revNumber, &revSourceType, &revPrompt, &revAuthoredAt,
	); err != nil {
		return nil, nil, err
	}
	q.Type = question.QuestionType(qType)
	q.SourceType = atom.SourceType(sourceType)
	q.LatestRevisionID = latestRevID
	q.Revision = int(revision)
	q.CreatedAt = createdAt
	q.UpdatedAt = updatedAt
	if deletedAt != nil {
		t := deletedAt.UTC()
		q.DeletedAt = &t
	}
	if mcqJSON != "" {
		var p question.MCQPayload
		if err := json.Unmarshal([]byte(mcqJSON), &p); err != nil {
			return nil, nil, fmt.Errorf("decode mcq_payload: %w", err)
		}
		q.MCQ = &p
	}
	if oeJSON != "" {
		var p question.OEPayload
		if err := json.Unmarshal([]byte(oeJSON), &p); err != nil {
			return nil, nil, fmt.Errorf("decode oe_payload: %w", err)
		}
		q.OE = &p
	}

	rev := &question.QuestionRevision{
		RevisionID:     revID,
		QuestionID:     q.QuestionID,
		AtomID:         q.AtomID,
		TenantID:       q.TenantID,
		RevisionNumber: int(revNumber),
		Prompt:         revPrompt,
		SourceType:     atom.SourceType(revSourceType),
		MCQPayload:     q.MCQ,
		OEPayload:      q.OE,
		AuthoredByGCID: q.AuthorGcid,
		AuthoredAt:     revAuthoredAt,
	}
	return &q, rev, nil
}

// Compile-time check.
var _ ports.QuestionRepository = (*QuestionRepository)(nil)
