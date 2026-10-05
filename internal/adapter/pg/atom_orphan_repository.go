// atom_orphan_repository.go — ADR-229 Amendment A1 (CHO-2132): the pg surface
// behind the singleton orphan-edition mint.
//
//   - GetAnyState        — atom loader WITHOUT the deleted_at filter: the
//     archive trigger fires after the original was soft-deleted, and the mint
//     still needs its content.
//   - MintOrphan         — idempotent singleton INSERT: ON CONFLICT against
//     the partial unique index learning_atoms_orphan_singleton_idx
//     (orphaned_from_atom_id, orphaned_source_revision_id) WHERE
//     orphaned_from_atom_id IS NOT NULL. A conflict returns the EXISTING
//     orphan (inserted=false) — repeat withdrawals and event redeliveries
//     converge on one edition.
//   - RepointCollections — same-DB repoint of creation's OWN collection
//     entries held by non-author owners (the author's entries keep the live
//     atom). Rows whose collection already holds the orphan (repeat
//     withdrawal after a re-add) are soft-deleted as a dedupe-merge — the
//     entry's continuity is already covered by the orphan row; NEVER a
//     hard-delete.
//
// Every method runs inside RunInTenantTx (SET LOCAL chora.tenant_id) so RLS
// scopes all reads/writes to the event's tenant.
package pg

import (
	"errors"
	"fmt"

	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// SQLMintOrphanInsert is the idempotent singleton INSERT. RETURNING atom_id
// distinguishes inserted (row) from conflict (no row) without needing
// RowsAffected on the Tx seam.
const SQLMintOrphanInsert = `
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
        ON CONFLICT (orphaned_from_atom_id, orphaned_source_revision_id)
            WHERE orphaned_from_atom_id IS NOT NULL
            DO NOTHING
        RETURNING atom_id`

// GetAnyState returns the atom for (tenantID, atomID) INCLUDING soft-deleted
// rows. Reserved for the orphan-mint path: the archive trigger legitimately
// targets an archived original. Returns atom.ErrNotFound when the row is
// missing entirely.
func (r *AtomRepository) GetAnyState(ctx context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	if r.tx == nil {
		return nil, errors.New("pg.AtomRepository.GetAnyState: no TxQuerier wired")
	}
	const sql = `
        SELECT ` + atomColumns + `
        FROM learning_atoms
        WHERE atom_id = $1 AND tenant_id = $2
    `
	var (
		result   *atom.LearningAtom
		notFound bool
	)
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
		return nil, fmt.Errorf("pg.AtomRepository.GetAnyState: %w", txErr)
	}
	if notFound {
		return nil, atom.ErrNotFound
	}
	return result, nil
}

// MintOrphan idempotently persists the orphan edition. Returns
// (orphan, true, nil) when this call inserted the row, or
// (existing, false, nil) when the singleton already existed for
// (orphaned_from_atom_id, orphaned_source_revision_id) — the caller emits the
// orphan_created event ONLY on inserted=true (plus the outbox
// UNIQUE(idempotency_key) backstop).
func (r *AtomRepository) MintOrphan(ctx context.Context, o *atom.LearningAtom) (*atom.LearningAtom, bool, error) {
	if r.tx == nil {
		return nil, false, errors.New("pg.AtomRepository.MintOrphan: no TxQuerier wired")
	}
	if o == nil || !o.IsOrphan() || o.OrphanedSourceRevisionID == "" {
		return nil, false, errors.New("pg.AtomRepository.MintOrphan: atom is not an orphan edition (markers required)")
	}
	tagsJSON, err := jsonMarshalOrEmptyArr(o.Tags)
	if err != nil {
		return nil, false, fmt.Errorf("pg.AtomRepository.MintOrphan: marshal tags: %w", err)
	}
	imdaJSON, err := marshalJSONOrEmptyArr(o.ImdaDimensionTags)
	if err != nil {
		return nil, false, fmt.Errorf("pg.AtomRepository.MintOrphan: marshal imda tags: %w", err)
	}
	mediaJSON, err := marshalJSONOrEmptyArr(o.MediaAssets)
	if err != nil {
		return nil, false, fmt.Errorf("pg.AtomRepository.MintOrphan: marshal media: %w", err)
	}

	const sqlSelectExisting = `
        SELECT ` + atomColumns + `
        FROM learning_atoms
        WHERE orphaned_from_atom_id = $1 AND orphaned_source_revision_id = $2
    `

	var (
		out      *atom.LearningAtom
		inserted bool
	)
	txErr := r.tx.RunInTenantTx(ctx, o.TenantID, func(ctx context.Context, tx Tx) error {
		var returnedID string
		err := tx.QueryRow(ctx, SQLMintOrphanInsert,
			o.AtomID, o.TenantID, o.Gcid, o.CourseID, o.Title, o.Body, tagsJSON,
			string(o.Mode), string(o.Status), string(o.QuestionType), o.Difficulty,
			o.Revision, o.CreatedAt, o.UpdatedAt, o.DeletedAt,
			o.Stem, o.Subject, string(o.CognitiveLevel),
			imdaJSON, mediaJSON, o.AuthorNote,
			o.ClonedFromAtomID, persistReuseVisibility(o.ReuseVisibility),
			o.OrphanedFromAtomID, o.OrphanedSourceRevisionID, o.OrphanedAt,
		).Scan(&returnedID)
		switch {
		case err == nil:
			inserted = true
			out = o
			return nil
		case errors.Is(err, ErrNoRows):
			// Singleton conflict — load + return the existing orphan.
			row := tx.QueryRow(ctx, sqlSelectExisting, o.OrphanedFromAtomID, o.OrphanedSourceRevisionID)
			existing, scanErr := scanAtom(row)
			if scanErr != nil {
				return fmt.Errorf("load existing orphan for (%s, %s): %w",
					o.OrphanedFromAtomID, o.OrphanedSourceRevisionID, scanErr)
			}
			out = existing
			return nil
		default:
			return fmt.Errorf("insert orphan: %w", err)
		}
	})
	if txErr != nil {
		return nil, false, fmt.Errorf("pg.AtomRepository.MintOrphan: %w", txErr)
	}
	return out, inserted, nil
}

// SQLRepointCollectionAtoms repoints active collection entries referencing
// the withdrawn original onto the orphan, EXCLUDING (a) collections owned by
// the author (their entries keep the live reference) and (b) collections that
// already hold the orphan (the PK (collection_id, atom_id) would collide —
// those rows are merged by SQLMergeCollectionAtoms instead).
const SQLRepointCollectionAtoms = `
        UPDATE collection_atoms ca
           SET atom_id = $2
          FROM collections c
         WHERE c.collection_id = ca.collection_id
           AND ca.atom_id = $1
           AND ca.deleted_at IS NULL
           AND c.deleted_at IS NULL
           AND c.owner_gcid <> $3
           AND NOT EXISTS (
               SELECT 1 FROM collection_atoms dup
                WHERE dup.collection_id = ca.collection_id
                  AND dup.atom_id = $2
           )`

// SQLMergeCollectionAtoms soft-deletes the leftover non-author entries whose
// collection ALREADY holds the orphan (repeat withdrawal after a re-add) — a
// dedupe-merge, never a revoke and never a hard-delete: the collection keeps
// the orphan edition via its existing row.
const SQLMergeCollectionAtoms = `
        UPDATE collection_atoms ca
           SET deleted_at = now()
          FROM collections c
         WHERE c.collection_id = ca.collection_id
           AND ca.atom_id = $1
           AND ca.deleted_at IS NULL
           AND c.deleted_at IS NULL
           AND c.owner_gcid <> $2`

// RepointCollections repoints creation's own collection entries from the
// withdrawn original onto the orphan edition (idempotent — a re-run matches
// zero rows). Returns the number of statements executed (2) errors loud.
func (r *AtomRepository) RepointCollections(ctx context.Context, tenantID, originalAtomID, orphanAtomID, authorGCID string) (int, error) {
	if r.tx == nil {
		return 0, errors.New("pg.AtomRepository.RepointCollections: no TxQuerier wired")
	}
	if originalAtomID == "" || orphanAtomID == "" || authorGCID == "" {
		return 0, errors.New("pg.AtomRepository.RepointCollections: original, orphan and author ids required")
	}
	statements := 0
	txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, SQLRepointCollectionAtoms, originalAtomID, orphanAtomID, authorGCID); err != nil {
			return fmt.Errorf("repoint collection_atoms: %w", err)
		}
		statements++
		if err := tx.Exec(ctx, SQLMergeCollectionAtoms, originalAtomID, authorGCID); err != nil {
			return fmt.Errorf("merge duplicate collection_atoms: %w", err)
		}
		statements++
		return nil
	})
	if txErr != nil {
		return statements, fmt.Errorf("pg.AtomRepository.RepointCollections: %w", txErr)
	}
	return statements, nil
}

// jsonMarshalOrEmptyArr mirrors marshalJSONOrEmptyArr for []string tags.
func jsonMarshalOrEmptyArr(tags []string) (string, error) {
	return marshalJSONOrEmptyArr(tags)
}
