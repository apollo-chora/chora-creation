// atom_embedding_repository.go — pgx-backed repo for the atom_embeddings
// pgvector table (Epic-1b W4 — the table existed since 0001 but had no
// writer/reader until this seam).
//
// SQL contract:
//
//   - Upsert: INSERT ... ON CONFLICT (atom_id) DO UPDATE. Idempotent; a
//     re-published atom refreshes its vector in place.
//   - Search: cosine nearest (`<=>`, ivfflat-indexed) JOINed against
//     learning_atoms so only PUBLISHED, non-deleted atoms ever match.
//   - ListPublishedMissingEmbedding: backfill scan (published atoms with no
//     embedding row yet).
//
// RLS contract: atom_embeddings + learning_atoms are tenant-scoped and
// chora_creation_app_rw is NOBYPASSRLS — every call wraps in RunInTenantTx so
// SET LOCAL chora.tenant_id applies in the same tx (mirrors atom_repository).
package pg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// EmbeddingMatch is one semantic search hit.
type EmbeddingMatch struct {
	AtomID         string
	CosineDistance float32
	Title          string
}

// AtomMissingEmbedding is one backfill candidate (text fields to embed +
// the author gcid the embedding is attributed to on the gateway ledger).
type AtomMissingEmbedding struct {
	AtomID     string
	Title      string
	Body       string
	Stem       string
	AuthorGCID string
}

// Search bounds: <=0 defaults, hard cap (mirrors the proto contract).
const (
	defaultEmbeddingSearchLimit = 5
	maxEmbeddingSearchLimit     = 20
)

// AtomEmbeddingRepository is the pgx-backed atom_embeddings adapter.
type AtomEmbeddingRepository struct {
	tx TxQuerier
}

// NewAtomEmbeddingRepository wraps a *PgxPoolQuerier (production RLS path).
func NewAtomEmbeddingRepository(querier *PgxPoolQuerier) *AtomEmbeddingRepository {
	return &AtomEmbeddingRepository{tx: querier}
}

// NewAtomEmbeddingRepositoryFromTxQuerier wraps an explicit TxQuerier (tests).
func NewAtomEmbeddingRepositoryFromTxQuerier(tq TxQuerier) *AtomEmbeddingRepository {
	return &AtomEmbeddingRepository{tx: tq}
}

// Upsert writes/refreshes one atom's embedding.
func (r *AtomEmbeddingRepository) Upsert(ctx context.Context, tenantID, atomID string, embedding []float32, modelID string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(atomID) == "" {
		return fmt.Errorf("pg: atom_embeddings upsert: tenant_id + atom_id required")
	}
	if len(embedding) == 0 {
		return fmt.Errorf("pg: atom_embeddings upsert: embedding required")
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		return tx.Exec(ctx, upsertAtomEmbeddingSQL, atomID, tenantID, embeddingVectorLiteral(embedding), modelID)
	})
}

// Search returns the nearest PUBLISHED atoms by cosine distance (best first).
func (r *AtomEmbeddingRepository) Search(ctx context.Context, tenantID string, query []float32, limit int) ([]EmbeddingMatch, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("pg: atom_embeddings search: tenant_id required")
	}
	if len(query) == 0 {
		return nil, fmt.Errorf("pg: atom_embeddings search: query embedding required")
	}
	if limit <= 0 {
		limit = defaultEmbeddingSearchLimit
	}
	if limit > maxEmbeddingSearchLimit {
		limit = maxEmbeddingSearchLimit
	}
	var out []EmbeddingMatch
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, err := tx.Query(ctx, searchAtomEmbeddingsSQL, embeddingVectorLiteral(query), limit)
		if err != nil {
			return fmt.Errorf("pg: search atom_embeddings: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var m EmbeddingMatch
			var distance float64
			if err := rows.Scan(&m.AtomID, &distance, &m.Title); err != nil {
				return fmt.Errorf("pg: scan embedding match: %w", err)
			}
			m.CosineDistance = float32(distance)
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListPublishedMissingEmbedding returns up to limit published atoms with no
// embedding row yet (the env-gated startup backfill's work-list).
func (r *AtomEmbeddingRepository) ListPublishedMissingEmbedding(ctx context.Context, tenantID string, limit int) ([]AtomMissingEmbedding, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("pg: atom_embeddings backfill scan: tenant_id required")
	}
	if limit <= 0 {
		limit = 100
	}
	var out []AtomMissingEmbedding
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, err := tx.Query(ctx, listAtomsMissingEmbeddingSQL, limit)
		if err != nil {
			return fmt.Errorf("pg: list atoms missing embedding: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var a AtomMissingEmbedding
			if err := rows.Scan(&a.AtomID, &a.Title, &a.Body, &a.Stem, &a.AuthorGCID); err != nil {
				return fmt.Errorf("pg: scan missing-embedding atom: %w", err)
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// embeddingVectorLiteral renders a pgvector literal ("[f1,f2,...]") bound as
// $N::vector — same no-pgvector-go approach as chora-consumption's
// learner_weakness / familiar_memory paths.
func embeddingVectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

const upsertAtomEmbeddingSQL = `
INSERT INTO atom_embeddings (atom_id, tenant_id, embedding, model_id, embedding_version, created_at, updated_at)
VALUES ($1, $2, $3::vector, $4, 1, now(), now())
ON CONFLICT (atom_id) DO UPDATE
   SET embedding = EXCLUDED.embedding,
       model_id  = EXCLUDED.model_id,
       updated_at = now()`

const searchAtomEmbeddingsSQL = `
SELECT e.atom_id, (e.embedding <=> $1::vector) AS distance, COALESCE(a.title, '')
  FROM atom_embeddings e
  JOIN learning_atoms a
    ON a.atom_id = e.atom_id
   AND a.deleted_at IS NULL
   AND a.status = 'published'
 ORDER BY e.embedding <=> $1::vector
 LIMIT $2`

const listAtomsMissingEmbeddingSQL = `
SELECT a.atom_id, COALESCE(a.title, ''), a.body, COALESCE(a.stem, ''), a.gcid
  FROM learning_atoms a
  LEFT JOIN atom_embeddings e ON e.atom_id = a.atom_id
 WHERE a.deleted_at IS NULL
   AND a.status = 'published'
   AND a.orphaned_from_atom_id IS NULL
   AND e.atom_id IS NULL
 LIMIT $1`
