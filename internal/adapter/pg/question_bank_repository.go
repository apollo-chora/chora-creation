// question_bank_repository.go — pgx-backed implementation of questionbank.Repository
// (W3.B.1, 2026-06-28). Mirrors collection_repository.go.
//
// SQL contract:
//
//   - Save: UPSERT question_banks row + reconcile question_bank_items child rows via
//     SOFT-DELETE-then-UPSERT (per ddd-enforcement Aggregate Invariant #4 —
//     never hard-delete). The reconciliation marks all existing child rows for
//     this bank as deleted_at = now() inside the tx, then UPSERT-resurrects the
//     currently-active subset by clearing deleted_at and updating position.
//     When the parent is soft-deleted (DeletedAt != nil), only the soft-delete
//     UPDATE runs (cascade per Aggregate Invariant #5).
//
//   - Get / GetVisible / List: filter `deleted_at IS NULL` on BOTH parent +
//     child tables.
//
// RLS contract: chora_creation_app_rw is NOBYPASSRLS, so every method wraps its
// SQL in tx.RunInTenantTx so SET LOCAL chora.tenant_id runs in the SAME
// transaction as the user query (#27 pattern from atom_repository.go).
//
// GetVisible: QuestionBank has NO PUBLIC visibility (exam-security), so there is no
// cross-tenant read path — GetVisible delegates to Get (rows owned by
// readerTenantID only). The seam is preserved for API symmetry with the
// Collection aggregate.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// QuestionBankRepository is the pgx-backed implementation of questionbank.Repository.
type QuestionBankRepository struct {
	tx TxQuerier
}

// NewQuestionBankRepository wraps a *PgxPoolQuerier (production wiring).
func NewQuestionBankRepository(querier *PgxPoolQuerier) *QuestionBankRepository {
	return &QuestionBankRepository{tx: querier}
}

// NewQuestionBankRepositoryFromTxQuerier wraps an explicit TxQuerier — used by
// tests injecting a stub TxQuerier that records RunInTenantTx calls.
func NewQuestionBankRepositoryFromTxQuerier(tq TxQuerier) *QuestionBankRepository {
	return &QuestionBankRepository{tx: tq}
}

// -----------------------------------------------------------------------------
// SQL constants
// -----------------------------------------------------------------------------

const sqlUpsertQuestionBank = `
    INSERT INTO question_banks (
        question_bank_id, tenant_id, owner_gcid, name, description, visibility,
        tags, created_at, updated_at, deleted_at
    ) VALUES (
        $1, $2, $3, $4, $5, $6::question_bank_visibility,
        $7, $8, $9, $10
    )
    ON CONFLICT (question_bank_id) DO UPDATE SET
        name        = EXCLUDED.name,
        description = EXCLUDED.description,
        visibility  = EXCLUDED.visibility,
        tags        = EXCLUDED.tags,
        updated_at  = EXCLUDED.updated_at,
        deleted_at  = EXCLUDED.deleted_at
`

// sqlSoftDeleteQuestionBankItems marks ALL child rows for the supplied bank as
// deleted_at = now() (idempotent). Canonical cascade-soft-delete +
// reconciliation primitive per Aggregate Invariant #4 + #5.
const sqlSoftDeleteQuestionBankItems = `
    UPDATE question_bank_items
    SET deleted_at = now()
    WHERE question_bank_id = $1 AND deleted_at IS NULL
`

// sqlUpsertQuestionBankItem resurrects (or inserts) a child row, clearing
// deleted_at and setting the current position. Conflicts on
// (question_bank_id, question_id) PK update the row in place. Position uniqueness
// among active rows is enforced by idx_question_bank_items_position_active.
const sqlUpsertQuestionBankItem = `
    INSERT INTO question_bank_items (
        question_bank_id, question_id, tenant_id, position, added_at, deleted_at
    ) VALUES (
        $1, $2, $3, $4, $5, NULL
    )
    ON CONFLICT (question_bank_id, question_id) DO UPDATE SET
        position   = EXCLUDED.position,
        deleted_at = NULL
`

const sqlGetQuestionBank = `
    SELECT question_bank_id, tenant_id, owner_gcid, name, description,
           visibility::text, tags, created_at, updated_at, deleted_at
    FROM question_banks
    WHERE question_bank_id = $1 AND tenant_id = $2 AND deleted_at IS NULL
`

const sqlListQuestionBankItems = `
    SELECT question_bank_id, question_id, tenant_id, position, added_at
    FROM question_bank_items
    WHERE question_bank_id = $1 AND deleted_at IS NULL
    ORDER BY position ASC
`

// -----------------------------------------------------------------------------
// Save — atomic UPSERT question_banks + reconcile question_bank_items
// -----------------------------------------------------------------------------

func (r *QuestionBankRepository) Save(ctx context.Context, b *questionbank.QuestionBank) error {
	if b == nil {
		return errors.New("pg.QuestionBankRepository.Save: nil question bank")
	}
	if r.tx == nil {
		return errors.New("pg.QuestionBankRepository.Save: tx querier not wired")
	}
	return r.tx.RunInTenantTx(ctx, b.TenantID, func(ctx context.Context, tx Tx) error {
		tags := b.Tags
		if tags == nil {
			tags = []string{} // column is NOT NULL DEFAULT '{}'
		}
		args := []any{
			b.QuestionBankID, b.TenantID, b.OwnerGCID,
			b.Name, b.Description, string(b.Visibility),
			tags, b.CreatedAt, b.UpdatedAt, b.DeletedAt,
		}
		if err := tx.Exec(ctx, sqlUpsertQuestionBank, args...); err != nil {
			return fmt.Errorf("pg.QuestionBankRepository.Save: upsert question bank: %w", err)
		}
		// Cascade-soft-delete every existing child row. When the parent is
		// soft-deleted, this is the entire reconciliation. Otherwise the UPSERT
		// below resurrects the currently-active subset.
		if err := tx.Exec(ctx, sqlSoftDeleteQuestionBankItems, b.QuestionBankID); err != nil {
			return fmt.Errorf("pg.QuestionBankRepository.Save: soft-delete child rows: %w", err)
		}
		if b.DeletedAt != nil {
			return nil
		}
		for _, it := range b.Items {
			addedAt := it.AddedAt
			if addedAt.IsZero() {
				addedAt = time.Now().UTC()
			}
			if err := tx.Exec(ctx, sqlUpsertQuestionBankItem,
				b.QuestionBankID, it.QuestionID, b.TenantID, it.Position, addedAt,
			); err != nil {
				return fmt.Errorf("pg.QuestionBankRepository.Save: upsert child item %s: %w", it.QuestionID, err)
			}
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Get — tenant-scoped, soft-delete-filtered
// -----------------------------------------------------------------------------

func (r *QuestionBankRepository) Get(ctx context.Context, tenantID, id string) (*questionbank.QuestionBank, error) {
	if r.tx == nil {
		return nil, errors.New("pg.QuestionBankRepository.Get: tx querier not wired")
	}
	var result *questionbank.QuestionBank
	notFound := false
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlGetQuestionBank, id, tenantID)
		b, err := scanQuestionBank(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				notFound = true
				return nil
			}
			return err
		}
		items, err := loadQuestionBankItems(ctx, tx, b.QuestionBankID)
		if err != nil {
			return err
		}
		b.Items = items
		result = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg.QuestionBankRepository.Get: %w", err)
	}
	if notFound {
		return nil, questionbank.ErrNotFound
	}
	return result, nil
}

// GetVisible — QuestionBank has no cross-tenant PUBLIC path, so this returns Get
// behaviour (rows owned by readerTenantID only).
func (r *QuestionBankRepository) GetVisible(ctx context.Context, readerTenantID, id string) (*questionbank.QuestionBank, error) {
	return r.Get(ctx, readerTenantID, id)
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func (r *QuestionBankRepository) List(ctx context.Context, tenantID string, f questionbank.ListFilter) ([]*questionbank.QuestionBank, error) {
	if r.tx == nil {
		return nil, errors.New("pg.QuestionBankRepository.List: tx querier not wired")
	}
	var sb strings.Builder
	sb.WriteString(`
        SELECT question_bank_id, tenant_id, owner_gcid, name, description,
               visibility::text, tags, created_at, updated_at, deleted_at
        FROM question_banks
        WHERE tenant_id = $1 AND deleted_at IS NULL`)
	args := []any{tenantID}
	if f.OwnerGCID != "" {
		args = append(args, f.OwnerGCID)
		fmt.Fprintf(&sb, " AND owner_gcid = $%d", len(args))
	}
	if f.Visibility != "" {
		args = append(args, string(f.Visibility))
		fmt.Fprintf(&sb, " AND visibility = $%d::question_bank_visibility", len(args))
	}
	// name-search — case-insensitive substring on the bank Name.
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+q+"%")
		fmt.Fprintf(&sb, " AND name ILIKE $%d", len(args))
	}
	// tag-filter — OR within the family: overlap ANY of the supplied tags via
	// the GIN-indexable TEXT[] `&&` operator (idx_question_banks_tags,
	// array_ops). pgx encodes []string as a Postgres text[].
	if len(f.Tags) > 0 {
		args = append(args, f.Tags)
		fmt.Fprintf(&sb, " AND tags && $%d::text[]", len(args))
	}
	sb.WriteString(" " + buildQuestionBankOrderBy(f.Sorts))
	if f.Limit > 0 {
		fmt.Fprintf(&sb, " LIMIT %d", f.Limit)
	}
	if f.Offset > 0 {
		fmt.Fprintf(&sb, " OFFSET %d", f.Offset)
	}

	out := []*questionbank.QuestionBank{}
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, sb.String(), args...)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		var collected []*questionbank.QuestionBank
		for rows.Next() {
			b, err := scanQuestionBank(rows)
			if err != nil {
				return err
			}
			collected = append(collected, b)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, b := range collected {
			items, err := loadQuestionBankItems(ctx, tx, b.QuestionBankID)
			if err != nil {
				return err
			}
			b.Items = items
		}
		out = collected
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg.QuestionBankRepository.List: %w", err)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func loadQuestionBankItems(ctx context.Context, tx Tx, questionBankID string) ([]*questionbank.QuestionBankItem, error) {
	rows, err := tx.Query(ctx, sqlListQuestionBankItems, questionBankID)
	if err != nil {
		return nil, fmt.Errorf("loadQuestionBankItems query: %w", err)
	}
	defer rows.Close()
	var out []*questionbank.QuestionBankItem
	for rows.Next() {
		it := &questionbank.QuestionBankItem{}
		var addedAt time.Time
		if err := rows.Scan(&it.QuestionBankID, &it.QuestionID, &it.TenantID, &it.Position, &addedAt); err != nil {
			return nil, fmt.Errorf("loadQuestionBankItems scan: %w", err)
		}
		it.AddedAt = addedAt
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadQuestionBankItems iter: %w", err)
	}
	return out, nil
}

func scanQuestionBank(s interface{ Scan(...any) error }) (*questionbank.QuestionBank, error) {
	var (
		b          questionbank.QuestionBank
		visibility string
		tags       []string
		createdAt  time.Time
		updatedAt  time.Time
		deletedAt  *time.Time
	)
	if err := s.Scan(
		&b.QuestionBankID,
		&b.TenantID,
		&b.OwnerGCID,
		&b.Name,
		&b.Description,
		&visibility,
		&tags,
		&createdAt,
		&updatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}
	b.Visibility = questionbank.Visibility(visibility)
	b.Tags = tags
	b.CreatedAt = createdAt
	b.UpdatedAt = updatedAt
	b.DeletedAt = deletedAt
	return &b, nil
}

// buildQuestionBankOrderBy renders a safe ORDER BY from the validated sort
// keys. Column names come ONLY from the domain whitelist
// (questionbank.Sort.Column) and the direction is constrained to ASC/DESC — the
// raw user field string never reaches the SQL, so there is no injection
// surface. A stable `question_bank_id DESC` tiebreak is always appended; absent
// sorts default to `created_at DESC` (preserving the pre-CHO-1899 behaviour).
func buildQuestionBankOrderBy(sorts []questionbank.Sort) string {
	cols := make([]string, 0, len(sorts)+1)
	for _, s := range sorts {
		col, ok := s.Column()
		if !ok {
			continue // defence-in-depth: the parser already rejected unknowns
		}
		cols = append(cols, col+" "+s.SQLDirection())
	}
	if len(cols) == 0 {
		cols = append(cols, "created_at DESC")
	}
	cols = append(cols, "question_bank_id DESC")
	return "ORDER BY " + strings.Join(cols, ", ")
}

// ListItemsPage resolves a filtered/sorted/paginated page of the bank's
// questions — enriched via an intra-DB JOIN (question_bank_items ⋈ questions,
// both in chora_creation) — plus the total match count. Tenant-scoped
// (RunInTenantTx + RLS), so a non-existent / cross-tenant bank yields an empty
// page. Keyword is a case-insensitive prompt substring; types filters
// question_type; sort is column-whitelisted (token → fixed column — the raw
// user value never reaches SQL). Huge-bank-safe: only one page is materialised.
func (r *QuestionBankRepository) ListItemsPage(
	ctx context.Context, tenantID, questionBankID string, f questionbank.ItemPageFilter,
) ([]questionbank.EnrichedQuestionBankItem, int, error) {
	// Shared WHERE + positional args (count + page run the same predicate).
	args := []any{questionBankID}
	where := "qbi.question_bank_id = $1 AND qbi.deleted_at IS NULL AND q.deleted_at IS NULL"
	if kw := strings.TrimSpace(f.Query); kw != "" {
		args = append(args, "%"+kw+"%")
		where += fmt.Sprintf(" AND q.prompt ILIKE $%d", len(args))
	}
	if len(f.Types) > 0 {
		args = append(args, f.Types)
		where += fmt.Sprintf(" AND q.question_type::text = ANY($%d::text[])", len(args))
	}

	// Column whitelist — token → fixed column; unknown/empty ⇒ position.
	orderCol := "qbi.position"
	switch f.SortKey {
	case "added_at":
		orderCol = "qbi.added_at"
	case "prompt":
		orderCol = "q.prompt"
	}
	dir := "ASC"
	if f.SortDesc {
		dir = "DESC"
	}
	orderBy := fmt.Sprintf("ORDER BY %s %s, qbi.position ASC, qbi.question_id ASC", orderCol, dir)

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	const joinFrom = "FROM question_bank_items qbi " +
		"JOIN questions q ON q.question_id = qbi.question_id AND q.tenant_id = qbi.tenant_id"

	var (
		items []questionbank.EnrichedQuestionBankItem
		total int
	)
	txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) "+joinFrom+" WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count: %w", err)
		}
		pageArgs := append(append([]any{}, args...), limit, offset)
		pageSQL := fmt.Sprintf(
			"SELECT qbi.question_id, q.atom_id, q.question_type::text, q.prompt, qbi.position, qbi.added_at "+
				"%s WHERE %s %s LIMIT $%d OFFSET $%d",
			joinFrom, where, orderBy, len(pageArgs)-1, len(pageArgs),
		)
		rows, err := tx.Query(ctx, pageSQL, pageArgs...)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var it questionbank.EnrichedQuestionBankItem
			if err := rows.Scan(&it.QuestionID, &it.AtomID, &it.QuestionType, &it.Prompt, &it.Position, &it.AddedAt); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	if txErr != nil {
		return nil, 0, fmt.Errorf("pg.QuestionBankRepository.ListItemsPage: %w", txErr)
	}
	if items == nil {
		items = []questionbank.EnrichedQuestionBankItem{}
	}
	return items, total, nil
}

// Compile-time checks.
var (
	_ questionbank.Repository = (*QuestionBankRepository)(nil)
	_ questionbank.ItemPager  = (*QuestionBankRepository)(nil)
)
