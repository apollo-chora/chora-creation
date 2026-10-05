// SourceChunkRepository — port for the source_material_chunks table
// (migration 0017, Lane 1c CHO-1703 / ADR-180 D9).
//
// Write side: the batch-upload handler extracts deterministic page-
// referenced chunks at job creation and bulk-inserts them. Read side: the
// ai_assist completed-event subscriber loads the job's chunks for the D15
// citation-verification pass. The store is reuse-ready for KG / growth-edge
// / daily-dose grounding (embedding column lives in the schema; v1 writes
// no embeddings) — future consumers arrive via the chora-creation
// ContentRetrieval.SearchEmbeddings gRPC seam, never via cross-DB SQL.
package ports

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

// SourceChunkRepository is the hexagonal port for source-material chunks.
type SourceChunkRepository interface {
	// InsertChunks persists the extraction artifacts for one job in a single
	// tenant-scoped transaction. All chunks MUST share the same tenant_id
	// (the RLS context). A nil/empty slice is a no-op.
	InsertChunks(ctx context.Context, chunks []sourcechunk.Chunk) error

	// ListByJob returns the job's chunks ordered by (file_uri, chunk_index)
	// for deterministic verification passes. Empty result is NOT an error —
	// image-only batches legitimately have zero chunks.
	ListByJob(ctx context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error)
}
