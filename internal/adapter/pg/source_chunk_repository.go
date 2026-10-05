// source_chunk_repository.go — pgx-backed implementation of
// ports.SourceChunkRepository for chora_creation.source_material_chunks
// (migration 0017, Lane 1c CHO-1703 / ADR-180 D9).
//
// Every method wraps SQL in RunInTenantTx so SET LOCAL chora.tenant_id runs
// BEFORE any tenant-scoped read/write (chora_creation_app_rw is NOBYPASSRLS
// — the 0015 lesson). Chunks are immutable extraction artifacts: INSERT +
// SELECT only, no UPDATE path.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// SourceChunkRepository is the pgx-backed implementation.
type SourceChunkRepository struct {
	tx TxQuerier
}

// NewSourceChunkRepository wraps a *PgxPoolQuerier for production wiring.
func NewSourceChunkRepository(querier *PgxPoolQuerier) *SourceChunkRepository {
	return &SourceChunkRepository{tx: querier}
}

// NewSourceChunkRepositoryFromTxQuerier wraps an explicit TxQuerier for tests.
func NewSourceChunkRepositoryFromTxQuerier(tq TxQuerier) *SourceChunkRepository {
	return &SourceChunkRepository{tx: tq}
}

// Compile-time check.
var _ ports.SourceChunkRepository = (*SourceChunkRepository)(nil)

const (
	sqlInsertSourceChunk = `
        INSERT INTO source_material_chunks (
            chunk_id, job_id, tenant_id,
            file_uri, file_role,
            chunk_index, page_no,
            text, created_at
        ) VALUES (
            $1, $2, $3,
            $4, $5,
            $6, NULLIF($7, 0),
            $8, $9
        )
    `

	// page_no comes back COALESCEd to 0 so the scan target stays a plain
	// int32 (0 ⇒ unpaged ⇒ nil on the domain struct) — the CHECK constraint
	// guarantees stored values are NULL or ≥1, so 0 is unambiguous.
	sqlSelectSourceChunksByJob = `
        SELECT
            chunk_id::text,
            job_id::text,
            tenant_id::text,
            file_uri,
            file_role,
            chunk_index,
            COALESCE(page_no, 0),
            text,
            created_at
        FROM source_material_chunks
        WHERE tenant_id = $1 AND job_id = $2
        ORDER BY file_uri, chunk_index
    `
)

// InsertChunks implements ports.SourceChunkRepository.InsertChunks.
// One tenant-scoped transaction for the whole batch; all chunks must share
// one tenant (the RLS context for the tx).
func (r *SourceChunkRepository) InsertChunks(ctx context.Context, chunks []sourcechunk.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if r.tx == nil {
		return errors.New("pg.SourceChunkRepository.InsertChunks: no TxQuerier wired")
	}
	tenantID := chunks[0].TenantID
	if tenantID == "" {
		return errors.New("pg.SourceChunkRepository.InsertChunks: empty tenant_id")
	}
	for i := range chunks {
		if chunks[i].TenantID != tenantID {
			return fmt.Errorf("pg.SourceChunkRepository.InsertChunks: mixed tenants in batch (chunk %d)", i)
		}
	}

	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		for i := range chunks {
			c := &chunks[i]
			pageNo := 0
			if c.PageNo != nil {
				pageNo = *c.PageNo
			}
			createdAt := c.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			if err := tx.Exec(ctx, sqlInsertSourceChunk,
				c.ChunkID, c.JobID, c.TenantID,
				c.FileURI, c.FileRole,
				int32(c.ChunkIndex), int32(pageNo),
				c.Text, createdAt,
			); err != nil {
				return fmt.Errorf("pg.SourceChunkRepository.InsertChunks: insert chunk %d: %w", i, err)
			}
		}
		return nil
	})
}

// ListByJob implements ports.SourceChunkRepository.ListByJob.
func (r *SourceChunkRepository) ListByJob(ctx context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error) {
	if r.tx == nil {
		return nil, errors.New("pg.SourceChunkRepository.ListByJob: no TxQuerier wired")
	}
	if tenantID == "" {
		return nil, errors.New("pg.SourceChunkRepository.ListByJob: tenantID required")
	}
	if jobID == "" {
		return nil, errors.New("pg.SourceChunkRepository.ListByJob: jobID required")
	}

	var out []sourcechunk.Chunk
	if err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, err := tx.Query(ctx, sqlSelectSourceChunksByJob, tenantID, jobID)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c          sourcechunk.Chunk
				chunkIndex int32
				pageNo     int32
				createdAt  time.Time
			)
			if err := rows.Scan(
				&c.ChunkID, &c.JobID, &c.TenantID,
				&c.FileURI, &c.FileRole,
				&chunkIndex, &pageNo,
				&c.Text, &createdAt,
			); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			c.ChunkIndex = int(chunkIndex)
			if pageNo > 0 {
				p := int(pageNo)
				c.PageNo = &p
			}
			c.CreatedAt = createdAt
			out = append(out, c)
		}
		return rows.Err()
	}); err != nil {
		return nil, fmt.Errorf("pg.SourceChunkRepository.ListByJob: %w", err)
	}
	return out, nil
}
