// topic_seed_source.go — pgx-backed topic.AtomTagSource (CHO-2275 Sub-phase A).
//
// Reads the distinct topic-tag slugs a tenant's atoms carry from
// learning_atoms.tags (the JSONB array the CHO-2142 classifier writes). This is
// the READ side of the seed-from-atom-tags backfill; the topic side (topic_nodes)
// is the write side (TopicRepository). Both are chora_creation-local — an
// in-domain read, never a cross-DB query.
//
// learning_atoms is RLS-protected (chora.tenant_id), so the scan runs inside a
// RunInTenantTx like every other read of that table.
package pg

import (
	"context"
	"fmt"
)

// AtomTagSource reads distinct atom tags for the topic backfill.
type AtomTagSource struct {
	tx TxQuerier
}

// NewAtomTagSource wraps a *PgxPoolQuerier (production wiring).
func NewAtomTagSource(querier *PgxPoolQuerier) *AtomTagSource {
	return &AtomTagSource{tx: querier}
}

// NewAtomTagSourceFromTxQuerier wraps an explicit TxQuerier (test seam).
func NewAtomTagSourceFromTxQuerier(tq TxQuerier) *AtomTagSource {
	return &AtomTagSource{tx: tq}
}

// DistinctAtomTags returns the distinct, lowercased, trimmed, non-blank tag
// slugs across the tenant's active atoms. A non-array tags value (should never
// happen — the column defaults to '[]') is treated as empty rather than erroring
// the whole scan.
func (s *AtomTagSource) DistinctAtomTags(ctx context.Context, tenantID string) ([]string, error) {
	const sql = `
        SELECT DISTINCT lower(btrim(tag.value))
        FROM learning_atoms la
        CROSS JOIN LATERAL jsonb_array_elements_text(
            CASE WHEN jsonb_typeof(la.tags) = 'array' THEN la.tags ELSE '[]'::jsonb END
        ) AS tag(value)
        WHERE la.tenant_id = $1 AND la.deleted_at IS NULL
          AND btrim(tag.value) <> ''
        ORDER BY 1`
	var out []string
	err := s.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, sql, tenantID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var tag string
			if serr := rows.Scan(&tag); serr != nil {
				return serr
			}
			out = append(out, tag)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("pg.AtomTagSource.DistinctAtomTags: %w", err)
	}
	return out, nil
}
