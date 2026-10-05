// atom_repository.go — pgx-backed implementation of atom.Repository.
//
// SQL contract:
//
//   - Save: UPSERT on conflict(atom_id). Idempotent under retry; matches
//     the in-memory adapter's Save semantics.
//   - Get / List / ListByCourse: filter `deleted_at IS NULL` per
//     ddd-enforcement #6 (default queries always filter soft-deleted).
//
// RLS contract (#27 — atom RLS fix): `learning_atoms` is tenant-scoped via
// the `tenant_isolation` RLS policy and chora_creation_app_rw is NOBYPASSRLS,
// so a bare QueryRow against the pool silently returns 0 rows. The
// repository now opens a `RunInTenantTx` transaction internally whenever a
// TxQuerier was supplied at construction time. SET LOCAL chora.tenant_id
// runs in the SAME transaction as the user query so PgBouncer transaction-
// pooling does not leak the GUC.
//
// Construction seams:
//
//   - NewAtomRepository(*PgxPoolQuerier) — production; both Querier + TxQuerier
//     are wired so every SQL call is RLS-tx wrapped.
//   - NewAtomRepositoryFromTxQuerier(TxQuerier) — used by tests injecting a
//     stub TxQuerier that records RunInTenantTx invocations.
//   - NewAtomRepositoryWithQuerier(Querier) — bare-querier path; preserved
//     for unit tests that stub the SQL surface without an RLS-aware seam.
//   - NewAtomRepositoryFromTx(pgx.Tx) — caller has already opened a
//     tenant-bound tx (e.g. integration tests); SQL runs against that tx.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// AtomRepository is the pgx-backed implementation of atom.Repository.
//
// When `tx` is non-nil, every method wraps its SQL in tx.RunInTenantTx so
// SET LOCAL chora.tenant_id applies before the user query — required because
// chora_creation_app_rw is NOBYPASSRLS. When `tx` is nil (legacy / stub
// path), the bare `q` is used directly.
type AtomRepository struct {
	q  Querier
	tx TxQuerier
}

// NewAtomRepository wraps a *PgxPoolQuerier. *PgxPoolQuerier satisfies both
// Querier and TxQuerier, so the returned repo wraps every call in a
// tenant-scoped tx (RLS path).
func NewAtomRepository(querier *PgxPoolQuerier) *AtomRepository {
	return &AtomRepository{q: querier, tx: querier}
}

// NewAtomRepositoryFromTxQuerier wraps an explicit TxQuerier. Used by tests
// that want to capture the RunInTenantTx calls.
func NewAtomRepositoryFromTxQuerier(tq TxQuerier) *AtomRepository {
	return &AtomRepository{tx: tq}
}

// NewAtomRepositoryWithQuerier accepts the lower-level Querier; used by
// unit tests that stub the SQL surface without an RLS-aware tx seam. The
// bare-querier path skips tenant-tx wrapping (the test is asserting SQL
// shape, not RLS).
func NewAtomRepositoryWithQuerier(q Querier) *AtomRepository {
	return &AtomRepository{q: q}
}

// NewAtomRepositoryFromTx wraps a pgx.Tx so callers that have already
// opened a tenant-bound transaction (e.g. via RunTenantTx) can issue
// repository calls within that tx so SET LOCAL chora.tenant_id applies.
// Skips RunInTenantTx wrapping since the caller owns the tx.
func NewAtomRepositoryFromTx(tx pgx.Tx) *AtomRepository {
	return &AtomRepository{q: NewPgxTxQuerier(tx)}
}

// atomColumns is the SELECT projection for LearningAtom. ADR-156 Phase 1
// renames atom_type → question_type and adds stem, subject, cognitive_level,
// imda_dimension_tags, media_assets, author_note. The NULLABLE Phase 1
// columns use COALESCE to deliver scan-friendly defaults (empty string /
// empty JSON literal '[]').
const atomColumns = `atom_id, tenant_id, gcid,
    COALESCE(course_id::text, ''),
    COALESCE(title, ''), body,
    COALESCE(tags, '[]'::jsonb)::text,
    mode::text, status::text,
    question_type,
    COALESCE(difficulty, 0),
    revision, created_at, updated_at, deleted_at,
    COALESCE(stem, ''),
    COALESCE(subject, ''),
    COALESCE(cognitive_level, ''),
    COALESCE(imda_dimension_tags, '[]'::jsonb)::text,
    COALESCE(media_assets, '[]'::jsonb)::text,
    COALESCE(author_note, ''),
    COALESCE(cloned_from_atom_id::text, ''),
    COALESCE(reuse_visibility, 'private'),
    COALESCE(orphaned_from_atom_id::text, ''),
    COALESCE(orphaned_source_revision_id::text, ''),
    orphaned_at`

// Save persists the atom (UPSERT on atom_id). When the repo holds a
// TxQuerier (production wiring), the UPSERT runs inside a SET LOCAL
// chora.tenant_id transaction so the RLS WITH CHECK on learning_atoms
// passes.
//
// ADR-156 Phase 1: replaces the old atom_type column with question_type
// (VARCHAR(32)) and adds 6 new columns — stem, subject, cognitive_level,
// imda_dimension_tags (JSONB), media_assets (JSONB), author_note.
func (r *AtomRepository) Save(ctx context.Context, a *atom.LearningAtom) error {
	stmt, args, err := atomUpsert(a)
	if err != nil {
		return err
	}
	if r.tx != nil {
		return r.tx.RunInTenantTx(ctx, a.TenantID, func(ctx context.Context, tx Tx) error {
			if err := tx.Exec(ctx, stmt, args...); err != nil {
				return fmt.Errorf("pg.AtomRepository.Save: %w", err)
			}
			return nil
		})
	}
	return r.q.Exec(ctx, stmt, args...)
}

// atomUpsert builds the learning_atoms UPSERT statement and its bind
// arguments. Shared by AtomRepository.Save (which opens its own tenant tx)
// and AtomTxWriter (which writes the atom and its outbox row inside ONE
// caller-owned tx), so the two paths persist byte-identical rows.
func atomUpsert(a *atom.LearningAtom) (string, []any, error) {
	if a == nil {
		return "", nil, errors.New("pg: atomUpsert: nil atom")
	}
	tagsJSON, err := json.Marshal(a.Tags)
	if err != nil {
		return "", nil, fmt.Errorf("pg: atomUpsert: marshal tags: %w", err)
	}
	// JSONB serialization for the 2 new array columns. Marshal nil to '[]'
	// (not 'null') so the JSONB column stays useful for downstream
	// projection / FE consumers.
	imdaJSON, err := marshalJSONOrEmptyArr(a.ImdaDimensionTags)
	if err != nil {
		return "", nil, fmt.Errorf("pg: atomUpsert: marshal imda_dimension_tags: %w", err)
	}
	mediaJSON, err := marshalJSONOrEmptyArr(a.MediaAssets)
	if err != nil {
		return "", nil, fmt.Errorf("pg: atomUpsert: marshal media_assets: %w", err)
	}
	const sql = `
        INSERT INTO learning_atoms (
            atom_id, tenant_id, gcid, course_id, title, body, tags,
            mode, status, question_type, difficulty,
            revision, created_at, updated_at, deleted_at,
            stem, subject, cognitive_level,
            imda_dimension_tags, media_assets, author_note,
            cloned_from_atom_id, reuse_visibility,
            orphaned_from_atom_id, orphaned_source_revision_id, orphaned_at
        ) VALUES (
            $1, $2, $3, NULLIF($4,'')::uuid, NULLIF($5,''), $6, $7::jsonb,
            $8::atom_mode, $9::atom_status, $10, NULLIF($11,0),
            $12, $13, $14, $15,
            $16, NULLIF($17,''), NULLIF($18,''),
            $19::jsonb, $20::jsonb, NULLIF($21,''),
            NULLIF($22,'')::uuid, $23,
            NULLIF($24,'')::uuid, NULLIF($25,'')::uuid, $26
        )
        ON CONFLICT (atom_id) DO UPDATE SET
            title               = EXCLUDED.title,
            body                = EXCLUDED.body,
            tags                = EXCLUDED.tags,
            mode                = EXCLUDED.mode,
            status              = EXCLUDED.status,
            question_type       = EXCLUDED.question_type,
            difficulty          = EXCLUDED.difficulty,
            revision            = EXCLUDED.revision,
            updated_at          = EXCLUDED.updated_at,
            deleted_at          = EXCLUDED.deleted_at,
            stem                = EXCLUDED.stem,
            subject             = EXCLUDED.subject,
            cognitive_level     = EXCLUDED.cognitive_level,
            imda_dimension_tags = EXCLUDED.imda_dimension_tags,
            media_assets        = EXCLUDED.media_assets,
            author_note         = EXCLUDED.author_note,
            cloned_from_atom_id = EXCLUDED.cloned_from_atom_id,
            reuse_visibility    = EXCLUDED.reuse_visibility,
            orphaned_from_atom_id       = EXCLUDED.orphaned_from_atom_id,
            orphaned_source_revision_id = EXCLUDED.orphaned_source_revision_id,
            orphaned_at                 = EXCLUDED.orphaned_at
    `
	args := []any{
		a.AtomID,
		a.TenantID,
		a.Gcid,
		a.CourseID,
		a.Title,
		a.Body,
		string(tagsJSON),
		string(a.Mode),
		string(a.Status),
		string(a.QuestionType),
		a.Difficulty,
		a.Revision,
		a.CreatedAt,
		a.UpdatedAt,
		a.DeletedAt,
		a.Stem,
		a.Subject,
		string(a.CognitiveLevel),
		imdaJSON,
		mediaJSON,
		a.AuthorNote,
		a.ClonedFromAtomID,
		persistReuseVisibility(a.ReuseVisibility),
		a.OrphanedFromAtomID,
		a.OrphanedSourceRevisionID,
		a.OrphanedAt,
	}
	return sql, args, nil
}

// persistReuseVisibility hardens the write: an unset domain value persists
// as the consent-first default rather than an empty string that would trip
// the CHECK constraint (fail-loud is the CHECK's job for junk values).
func persistReuseVisibility(v atom.ReuseVisibility) string {
	if v == "" {
		return string(atom.ReusePrivate)
	}
	return string(v)
}

// marshalJSONOrEmptyArr marshals v to JSON; returns '[]' when v is a nil
// slice. The empty-array literal keeps the JSONB column shape sensible for
// downstream projection.
func marshalJSONOrEmptyArr(v any) (string, error) {
	// json.Marshal of a nil slice emits "null"; we want "[]" so the JSONB
	// column has an iterable shape. The reflection-free path: marshal,
	// then swap "null" → "[]".
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	s := string(out)
	if s == "null" {
		return "[]", nil
	}
	return s, nil
}

// Get returns the atom for (tenantID, atomID) iff it exists, belongs to
// tenantID, and is not soft-deleted. When the repo holds a TxQuerier
// (production wiring), the SELECT runs inside a SET LOCAL chora.tenant_id
// transaction so the RLS policy resolves rows for this tenant.
func (r *AtomRepository) Get(ctx context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	const sql = `
        SELECT ` + atomColumns + `
        FROM learning_atoms
        WHERE atom_id = $1 AND tenant_id = $2 AND deleted_at IS NULL
    `
	if r.tx != nil {
		var result *atom.LearningAtom
		notFound := false
		txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
			row := tx.QueryRow(ctx, sql, atomID, tenantID)
			a, err := scanAtom(row)
			if err != nil {
				if errors.Is(err, ErrNoRows) {
					notFound = true
					return nil
				}
				return err
			}
			result = a
			return nil
		})
		if txErr != nil {
			return nil, fmt.Errorf("pg.AtomRepository.Get: %w", txErr)
		}
		if notFound {
			return nil, atom.ErrNotFound
		}
		return result, nil
	}
	row := r.q.QueryRow(ctx, sql, atomID, tenantID)
	a, err := scanAtom(row)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return nil, atom.ErrNotFound
		}
		return nil, fmt.Errorf("pg.AtomRepository.Get: %w", err)
	}
	return a, nil
}

// List returns active atoms for a tenant, optionally filtered by status.
// When the repo holds a TxQuerier (production wiring), the SELECT runs
// inside a SET LOCAL chora.tenant_id transaction so the RLS policy
// resolves the tenant's rows.
func (r *AtomRepository) List(ctx context.Context, tenantID string, f atom.ListFilter) ([]*atom.LearningAtom, error) {
	sql := `
        SELECT ` + atomColumns + `
        FROM learning_atoms
        WHERE tenant_id = $1 AND deleted_at IS NULL
    `
	args := []any{tenantID}
	if f.Status != "" {
		args = append(args, string(f.Status))
		sql += fmt.Sprintf(` AND status = $%d::atom_status`, len(args))
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		sql += fmt.Sprintf(` AND title ILIKE $%d`, len(args))
	}
	sql += ` ORDER BY created_at ASC`
	if f.Limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	if f.Offset > 0 {
		sql += fmt.Sprintf(` OFFSET %d`, f.Offset)
	}

	if r.tx != nil {
		out := []*atom.LearningAtom{}
		txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
			rows, qerr := tx.Query(ctx, sql, args...)
			if qerr != nil {
				return qerr
			}
			defer rows.Close()
			for rows.Next() {
				a, scanErr := scanAtom(rows)
				if scanErr != nil {
					return scanErr
				}
				out = append(out, a)
			}
			return rows.Err()
		})
		if txErr != nil {
			return nil, fmt.Errorf("pg.AtomRepository.List: %w", txErr)
		}
		return out, nil
	}

	rows, err := r.q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pg.AtomRepository.List: %w", err)
	}
	defer rows.Close()
	out := []*atom.LearningAtom{}
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, fmt.Errorf("pg.AtomRepository.List scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.AtomRepository.List iter: %w", err)
	}
	return out, nil
}

// ListByCourse returns active atoms for a tenant scoped to a course. When
// the repo holds a TxQuerier (production wiring), the SELECT runs inside a
// SET LOCAL chora.tenant_id transaction so the RLS policy resolves the
// tenant's rows.
func (r *AtomRepository) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*atom.LearningAtom, error) {
	const sql = `
        SELECT ` + atomColumns + `
        FROM learning_atoms
        WHERE tenant_id = $1 AND course_id = $2::uuid AND deleted_at IS NULL
        ORDER BY created_at ASC
    `
	if r.tx != nil {
		out := []*atom.LearningAtom{}
		txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
			rows, qerr := tx.Query(ctx, sql, tenantID, courseID)
			if qerr != nil {
				return qerr
			}
			defer rows.Close()
			for rows.Next() {
				a, scanErr := scanAtom(rows)
				if scanErr != nil {
					return scanErr
				}
				out = append(out, a)
			}
			return rows.Err()
		})
		if txErr != nil {
			return nil, fmt.Errorf("pg.AtomRepository.ListByCourse: %w", txErr)
		}
		return out, nil
	}

	rows, err := r.q.Query(ctx, sql, tenantID, courseID)
	if err != nil {
		return nil, fmt.Errorf("pg.AtomRepository.ListByCourse: %w", err)
	}
	defer rows.Close()
	out := []*atom.LearningAtom{}
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, fmt.Errorf("pg.AtomRepository.ListByCourse scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.AtomRepository.ListByCourse iter: %w", err)
	}
	return out, nil
}

// scanAtom maps a row to *atom.LearningAtom. Accepts both Row and Rows
// (both expose Scan via the type assertion).
//
// ADR-156 Phase 1: scans the renamed question_type column + 6 new columns
// (stem, subject, cognitive_level, imda_dimension_tags, media_assets,
// author_note). JSONB columns deserialize into typed slices on the
// aggregate. Empty/NULL columns map to zero-value Go types — handler
// enforces invariants on write, not read.
func scanAtom(s interface{ Scan(...any) error }) (*atom.LearningAtom, error) {
	var (
		a              atom.LearningAtom
		courseID       string
		tagsJSON       string
		mode           string
		status         string
		questionType   string
		difficulty     int
		deletedAt      *time.Time
		stem           string
		subject        string
		cognitiveLevel string
		imdaJSON       string
		mediaJSON      string
		authorNote     string
		clonedFrom     string
		reuseVis       string
		orphanedFrom   string
		orphanedRev    string
		orphanedAt     *time.Time
	)
	err := s.Scan(
		&a.AtomID,
		&a.TenantID,
		&a.Gcid,
		&courseID,
		&a.Title,
		&a.Body,
		&tagsJSON,
		&mode,
		&status,
		&questionType,
		&difficulty,
		&a.Revision,
		&a.CreatedAt,
		&a.UpdatedAt,
		&deletedAt,
		&stem,
		&subject,
		&cognitiveLevel,
		&imdaJSON,
		&mediaJSON,
		&authorNote,
		&clonedFrom,
		&reuseVis,
		&orphanedFrom,
		&orphanedRev,
		&orphanedAt,
	)
	if err != nil {
		return nil, err
	}
	a.OrphanedFromAtomID = orphanedFrom
	a.OrphanedSourceRevisionID = orphanedRev
	if orphanedAt != nil {
		t := orphanedAt.UTC()
		a.OrphanedAt = &t
	}
	a.ReuseVisibility = atom.ReuseVisibility(reuseVis)
	a.CourseID = courseID
	a.Mode = atom.Mode(mode)
	a.Status = atom.Status(status)
	if questionType != "" {
		a.QuestionType = atom.QuestionType(questionType)
	}
	a.Difficulty = difficulty
	if deletedAt != nil {
		t := deletedAt.UTC()
		a.DeletedAt = &t
	}
	if tagsJSON != "" {
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err == nil {
			a.Tags = tags
		}
	}
	a.Stem = stem
	a.Subject = subject
	if cognitiveLevel != "" {
		a.CognitiveLevel = atom.CognitiveLevel(cognitiveLevel)
	}
	if imdaJSON != "" && imdaJSON != "[]" {
		var tags []atom.ImdaDimTag
		if err := json.Unmarshal([]byte(imdaJSON), &tags); err == nil {
			a.ImdaDimensionTags = tags
		}
	}
	if mediaJSON != "" && mediaJSON != "[]" {
		var media []atom.MediaAsset
		if err := json.Unmarshal([]byte(mediaJSON), &media); err == nil {
			a.MediaAssets = media
		}
	}
	a.AuthorNote = authorNote
	a.ClonedFromAtomID = clonedFrom
	return &a, nil
}

// Compile-time check.
var _ atom.Repository = (*AtomRepository)(nil)
