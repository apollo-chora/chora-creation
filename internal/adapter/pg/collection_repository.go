// collection_repository.go — pgx-backed implementation of
// collection.Repository (WS-6a, 2026-05-26).
//
// SQL contract:
//
//   - Save: UPSERT collections row + reconcile collection_atoms child rows
//     via SOFT-DELETE-then-UPSERT (per ddd-enforcement Aggregate Invariant
//     #5 — never hard-delete). The reconciliation marks all existing child
//     rows for this collection as deleted_at = now() inside the tx, then
//     UPSERT-resurrects the currently-active subset by clearing
//     deleted_at and updating position. When the parent is soft-deleted
//     (DeletedAt != nil), only the soft-delete UPDATE runs (cascade per
//     Aggregate Invariant #8).
//
//   - Get / List: filter `deleted_at IS NULL` per Aggregate Invariant #6
//     (default queries always filter soft-deleted) on BOTH parent + child
//     tables.
//
//   - ReuseFacts (ADR-233): ONE batch read over learning_atoms supplying the
//     locally-owned half of the ADR-229 disjunct (author gcid + audience +
//     publish state). learning_atoms lives in the SAME chora_creation database,
//     so this is an intra-DB cross-aggregate read, NOT a cross-DB query.
//
// RLS contract: chora_creation_app_rw is NOBYPASSRLS, so every method
// wraps its SQL in tx.RunInTenantTx so SET LOCAL chora.tenant_id runs in
// the SAME transaction as the user query (#27 pattern from
// atom_repository.go).
//
// ADR-233 D7: `collections.visibility` is TEXT (private|friends|tenant), NOT the
// retired PG enum `collection_visibility` — migration 0031 converts it. Every
// ::collection_visibility cast is therefore gone; the column scans straight into
// the audience value object.
//
// ADR-233 D8: GetVisible is GONE. It was a pure passthrough to Get — no owner
// check, no visibility check — which is how the horizontal-authz defect shipped.
// The read predicate now lives in collection.Collection.VisibleTo, composed by
// collection.Service.GetVisible. The repository enforces the TENANT boundary
// only.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// CollectionRepository is the pgx-backed implementation of
// collection.Repository.
type CollectionRepository struct {
	tx TxQuerier
}

// NewCollectionRepository wraps a *PgxPoolQuerier (production wiring).
func NewCollectionRepository(querier *PgxPoolQuerier) *CollectionRepository {
	return &CollectionRepository{tx: querier}
}

// NewCollectionRepositoryFromTxQuerier wraps an explicit TxQuerier — used
// by tests injecting a stub TxQuerier that records RunInTenantTx calls.
func NewCollectionRepositoryFromTxQuerier(tq TxQuerier) *CollectionRepository {
	return &CollectionRepository{tx: tq}
}

// -----------------------------------------------------------------------------
// SQL constants
// -----------------------------------------------------------------------------

const sqlUpsertCollection = `
    INSERT INTO collections (
        collection_id, tenant_id, owner_gcid, title, description, visibility,
        created_at, updated_at, deleted_at
    ) VALUES (
        $1, $2, $3, $4, $5, $6,
        $7, $8, $9
    )
    ON CONFLICT (collection_id) DO UPDATE SET
        title       = EXCLUDED.title,
        description = EXCLUDED.description,
        visibility  = EXCLUDED.visibility,
        updated_at  = EXCLUDED.updated_at,
        deleted_at  = EXCLUDED.deleted_at
`

// sqlSoftDeleteCollectionAtoms marks ALL child rows for the supplied
// collection as deleted_at = now() (idempotent — repeating the UPDATE
// keeps deleted_at set). This is the canonical cascade-soft-delete +
// reconciliation primitive per Aggregate Invariant #5 + #8.
const sqlSoftDeleteCollectionAtoms = `
    UPDATE collection_atoms
    SET deleted_at = now()
    WHERE collection_id = $1 AND deleted_at IS NULL
`

// sqlUpsertCollectionAtom resurrects (or inserts) a child row, clearing
// deleted_at and setting the current position. Conflicts on (collection_id,
// atom_id) PK update the row in place. Position uniqueness among active
// rows is enforced by the partial unique index idx_collection_atoms_position_active
// (migration 0016).
const sqlUpsertCollectionAtom = `
    INSERT INTO collection_atoms (
        collection_id, atom_id, tenant_id, position, added_at, deleted_at
    ) VALUES (
        $1, $2, $3, $4, $5, NULL
    )
    ON CONFLICT (collection_id, atom_id) DO UPDATE SET
        position   = EXCLUDED.position,
        deleted_at = NULL
`

const sqlGetCollection = `
    SELECT collection_id, tenant_id, owner_gcid, title, description,
           visibility, created_at, updated_at, deleted_at
    FROM collections
    WHERE collection_id = $1 AND tenant_id = $2 AND deleted_at IS NULL
`

// sqlReuseFacts is the ADR-229 disjunct's locally-owned half, read in ONE batch
// query (never N+1) for the add-time + convert-time collection gates.
//
// Column names are the DEPLOYED ones, not the obvious ones:
//   - the author column on learning_atoms is `gcid` ("author / owner;
//     cross-domain ref", 0001_initial.sql:57) — NOT author_gcid / owner_gcid.
//   - `reuse_visibility` is TEXT + CHECK (0029_atom_reuse_visibility.up.sql),
//     COALESCEd because rows predating that migration can carry NULL.
//   - `status` is the atom_status enum; reuse rides the PUBLISHED revision.
//
// Atoms absent from the result (soft-deleted, or never existed) are simply
// absent from the map — reuseconsent.EvaluateAll surfaces them as
// ATOM_NOT_FOUND rather than crashing the conversion.
const sqlReuseFacts = `
    SELECT la.atom_id::text,
           la.gcid::text,
           COALESCE(la.reuse_visibility, 'private'),
           (la.status = 'published') AS published
    FROM learning_atoms la
    WHERE la.tenant_id = $1
      AND la.atom_id = ANY($2)
      AND la.deleted_at IS NULL
`

const sqlListCollectionAtoms = `
    SELECT collection_id, atom_id, position, added_at
    FROM collection_atoms
    WHERE collection_id = $1 AND deleted_at IS NULL
    ORDER BY position ASC
`

// -----------------------------------------------------------------------------
// Save — atomic UPSERT collections + reconcile collection_atoms
// -----------------------------------------------------------------------------

// Save persists the aggregate. Child collection_atoms rows are reconciled
// via SOFT-DELETE-then-UPSERT inside the same tenant-scoped transaction.
// Soft-delete cascades to child rows per Aggregate Invariant #8.
func (r *CollectionRepository) Save(ctx context.Context, c *collection.Collection) error {
	if c == nil {
		return errors.New("pg.CollectionRepository.Save: nil collection")
	}
	if r.tx == nil {
		return errors.New("pg.CollectionRepository.Save: tx querier not wired")
	}
	return r.tx.RunInTenantTx(ctx, c.TenantID, func(ctx context.Context, tx Tx) error {
		args := []any{
			c.CollectionID, c.TenantID, c.OwnerGcid,
			c.Title, c.Description, string(c.Visibility),
			c.CreatedAt, c.UpdatedAt, c.DeletedAt,
		}
		if err := tx.Exec(ctx, sqlUpsertCollection, args...); err != nil {
			return fmt.Errorf("pg.CollectionRepository.Save: upsert collection: %w", err)
		}
		// Cascade-soft-delete every existing child row. When the parent is
		// soft-deleted, this is the entire reconciliation. Otherwise the
		// UPSERT below resurrects the currently-active subset.
		if err := tx.Exec(ctx, sqlSoftDeleteCollectionAtoms, c.CollectionID); err != nil {
			return fmt.Errorf("pg.CollectionRepository.Save: soft-delete child rows: %w", err)
		}
		if c.DeletedAt != nil {
			return nil
		}
		for _, a := range c.Atoms {
			addedAt := a.AddedAt
			if addedAt.IsZero() {
				addedAt = time.Now().UTC()
			}
			if err := tx.Exec(ctx, sqlUpsertCollectionAtom,
				c.CollectionID, a.AtomID, c.TenantID, a.Position, addedAt,
			); err != nil {
				return fmt.Errorf("pg.CollectionRepository.Save: upsert child atom %s: %w", a.AtomID, err)
			}
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Get — tenant-scoped, soft-delete-filtered
// -----------------------------------------------------------------------------

// Get returns the collection iff it belongs to tenantID + is not soft-
// deleted. Atoms are loaded in a follow-up SELECT inside the same tx so
// RLS applies uniformly.
func (r *CollectionRepository) Get(ctx context.Context, tenantID, id string) (*collection.Collection, error) {
	if r.tx == nil {
		return nil, errors.New("pg.CollectionRepository.Get: tx querier not wired")
	}
	var result *collection.Collection
	notFound := false
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlGetCollection, id, tenantID)
		c, err := scanCollection(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				notFound = true
				return nil
			}
			return err
		}
		atoms, err := loadCollectionAtoms(ctx, tx, c.CollectionID)
		if err != nil {
			return err
		}
		c.Atoms = atoms
		result = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg.CollectionRepository.Get: %w", err)
	}
	if notFound {
		return nil, collection.ErrNotFound
	}
	return result, nil
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func (r *CollectionRepository) List(ctx context.Context, tenantID string, f collection.ListFilter) ([]*collection.Collection, error) {
	if r.tx == nil {
		return nil, errors.New("pg.CollectionRepository.List: tx querier not wired")
	}
	var sb strings.Builder
	sb.WriteString(`
        SELECT collection_id, tenant_id, owner_gcid, title, description,
               visibility, created_at, updated_at, deleted_at
        FROM collections
        WHERE tenant_id = $1 AND deleted_at IS NULL`)
	args := []any{tenantID}
	if f.OwnerGcid != "" {
		args = append(args, f.OwnerGcid)
		fmt.Fprintf(&sb, " AND owner_gcid = $%d", len(args))
	}
	if f.Visibility != "" {
		args = append(args, string(f.Visibility))
		fmt.Fprintf(&sb, " AND visibility = $%d", len(args))
	}
	sb.WriteString(" ORDER BY created_at DESC")
	if f.Limit > 0 {
		fmt.Fprintf(&sb, " LIMIT %d", f.Limit)
	}
	if f.Offset > 0 {
		fmt.Fprintf(&sb, " OFFSET %d", f.Offset)
	}

	out := []*collection.Collection{}
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, sb.String(), args...)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		var collected []*collection.Collection
		for rows.Next() {
			c, err := scanCollection(rows)
			if err != nil {
				return err
			}
			collected = append(collected, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range collected {
			atoms, err := loadCollectionAtoms(ctx, tx, c.CollectionID)
			if err != nil {
				return err
			}
			c.Atoms = atoms
		}
		out = collected
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg.CollectionRepository.List: %w", err)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func loadCollectionAtoms(ctx context.Context, tx Tx, collectionID string) ([]*collection.CollectionAtom, error) {
	rows, err := tx.Query(ctx, sqlListCollectionAtoms, collectionID)
	if err != nil {
		return nil, fmt.Errorf("loadCollectionAtoms query: %w", err)
	}
	defer rows.Close()
	var out []*collection.CollectionAtom
	for rows.Next() {
		a := &collection.CollectionAtom{}
		var addedAt time.Time
		if err := rows.Scan(&a.CollectionID, &a.AtomID, &a.Position, &addedAt); err != nil {
			return nil, fmt.Errorf("loadCollectionAtoms scan: %w", err)
		}
		a.AddedAt = addedAt
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadCollectionAtoms iter: %w", err)
	}
	return out, nil
}

func scanCollection(s interface{ Scan(...any) error }) (*collection.Collection, error) {
	var (
		c           collection.Collection
		visibility  string
		createdAt   time.Time
		updatedAt   time.Time
		deletedAt   *time.Time
		description string
		title       string
	)
	if err := s.Scan(
		&c.CollectionID,
		&c.TenantID,
		&c.OwnerGcid,
		&title,
		&description,
		&visibility,
		&createdAt,
		&updatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}
	c.Title = title
	c.Description = description
	c.Visibility = audience.Audience(visibility)
	c.CreatedAt = createdAt
	c.UpdatedAt = updatedAt
	c.DeletedAt = deletedAt
	return &c, nil
}

// -----------------------------------------------------------------------------
// ReuseFacts — ADR-233 / ADR-229 gate input (collection.AtomReuseFactLookup)
// -----------------------------------------------------------------------------

// ReuseFacts returns the reuse facts for the supplied atoms, keyed by atom_id,
// in ONE batch query inside the tenant RLS transaction.
//
// An atom absent from the map is an atom that no longer resolves — a collection
// holds FK-less cross-aggregate refs (ddd-enforcement #3), so a curated atom can
// be soft-deleted out from under it. That is a normal, expected outcome, not an
// error: reuseconsent.EvaluateAll excludes it as ATOM_NOT_FOUND.
func (r *CollectionRepository) ReuseFacts(
	ctx context.Context, tenantID string, atomIDs []string,
) (map[string]reuseconsent.AtomFact, error) {
	out := make(map[string]reuseconsent.AtomFact, len(atomIDs))
	if len(atomIDs) == 0 {
		return out, nil
	}
	if r.tx == nil {
		return nil, errors.New("pg.CollectionRepository.ReuseFacts: tx querier not wired")
	}

	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, sqlReuseFacts, tenantID, atomIDs)
		if qerr != nil {
			return fmt.Errorf("query: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				atomID     string
				authorGCID string
				visibility string
				published  bool
			)
			if err := rows.Scan(&atomID, &authorGCID, &visibility, &published); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			out[atomID] = reuseconsent.AtomFact{
				AtomID:     atomID,
				AuthorGCID: authorGCID,
				// NOT coerced: an unrecognised value must fail CLOSED at the
				// predicate (reuseconsent.Evaluate's default leg), never be
				// silently widened into a valid audience here.
				Audience:  audience.Audience(visibility),
				Published: published,
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("pg.CollectionRepository.ReuseFacts: %w", err)
	}
	return out, nil
}

// Compile-time checks.
var (
	_ collection.Repository          = (*CollectionRepository)(nil)
	_ collection.AtomReuseFactLookup = (*CollectionRepository)(nil)
)
