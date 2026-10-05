// source_chunk_repository_test.go — RED→GREEN coverage for the
// SourceChunkRepository pgx adapter (Lane 1c, migration 0017).
//
// Mirrors the stubTxQuerier pattern (atom_repository_test.go) so SQL
// emission is observable without a live DB. Every path runs inside
// RunInTenantTx so SET LOCAL chora.tenant_id applies before the SQL
// (chora_creation_app_rw is NOBYPASSRLS — the 0015 lesson).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

const (
	chunkTen = "22222222-2222-7222-8222-222222222222"
	chunkJob = "01970000-0000-7000-8000-00000000c001"
)

func chunkFixture(idx int, pageNo *int) sourcechunk.Chunk {
	return sourcechunk.Chunk{
		ChunkID:    "01970000-0000-7000-8000-00000000d00" + string(rune('0'+idx)),
		JobID:      chunkJob,
		TenantID:   chunkTen,
		FileURI:    "gs://bucket/tenants/t/jobs/j/source-1",
		FileRole:   sourcechunk.RoleSource,
		ChunkIndex: idx,
		PageNo:     pageNo,
		Text:       "Photosynthesis converts light energy.",
		CreatedAt:  time.Now().UTC(),
	}
}

func intPtr(v int) *int { return &v }

func TestSourceChunkRepository_InsertChunks_RunsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)

	chunks := []sourcechunk.Chunk{chunkFixture(0, intPtr(1)), chunkFixture(1, nil)}
	if err := r.InsertChunks(context.Background(), chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("RunInTenantTx calls = %d; want 1 (single tx for the batch)", len(tq.calls))
	}
	if tq.calls[0].tenantID != chunkTen {
		t.Errorf("tenantID = %q; want %q", tq.calls[0].tenantID, chunkTen)
	}
	if tq.execCount != 2 {
		t.Errorf("Exec count = %d; want 2 (one INSERT per chunk)", tq.execCount)
	}
	if !strings.Contains(tq.execSQL, "INSERT INTO source_material_chunks") {
		t.Errorf("expected INSERT INTO source_material_chunks; got %q", tq.execSQL)
	}
}

func TestSourceChunkRepository_InsertChunks_EmptyNoOp(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)
	if err := r.InsertChunks(context.Background(), nil); err != nil {
		t.Fatalf("InsertChunks(nil): %v", err)
	}
	if len(tq.calls) != 0 {
		t.Errorf("RunInTenantTx calls = %d; want 0 for empty input", len(tq.calls))
	}
}

func TestSourceChunkRepository_InsertChunks_RejectsMixedTenants(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)
	a := chunkFixture(0, nil)
	b := chunkFixture(1, nil)
	b.TenantID = "33333333-3333-7333-8333-333333333333"
	if err := r.InsertChunks(context.Background(), []sourcechunk.Chunk{a, b}); err == nil {
		t.Fatal("expected error for mixed-tenant batch (single RLS context per tx)")
	}
	if len(tq.calls) != 0 {
		t.Errorf("RunInTenantTx must not run on validation failure")
	}
}

func TestSourceChunkRepository_InsertChunks_PropagatesExecError(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.execErr = errors.New("boom")
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)
	if err := r.InsertChunks(context.Background(), []sourcechunk.Chunk{chunkFixture(0, nil)}); err == nil {
		t.Fatal("expected exec error to propagate")
	}
}

func TestSourceChunkRepository_ListByJob_ScansRows(t *testing.T) {
	created := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{rows: [][]any{
		{
			"01970000-0000-7000-8000-00000000d001", chunkJob, chunkTen,
			"gs://bucket/tenants/t/jobs/j/source-1", "source",
			int32(0), int32(2), "Page two text.", created,
		},
		{
			"01970000-0000-7000-8000-00000000d002", chunkJob, chunkTen,
			"gs://bucket/tenants/t/jobs/j/rubric", "rubric",
			int32(0), int32(0), "Rubric text.", created,
		},
	}}
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)

	got, err := r.ListByJob(context.Background(), chunkTen, chunkJob)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != chunkTen {
		t.Fatalf("RunInTenantTx calls = %+v; want 1 call with tenant", tq.calls)
	}
	if !strings.Contains(tq.querySQL, "FROM source_material_chunks") {
		t.Errorf("expected SELECT FROM source_material_chunks; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "ORDER BY") {
		t.Errorf("expected deterministic ORDER BY; got %q", tq.querySQL)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].ChunkID != "01970000-0000-7000-8000-00000000d001" || got[0].FileRole != "source" {
		t.Errorf("row0 = %+v", got[0])
	}
	// page_no scanned via COALESCE(page_no, 0): 2 → *int(2); 0 → nil.
	if got[0].PageNo == nil || *got[0].PageNo != 2 {
		t.Errorf("row0.PageNo = %v; want 2", got[0].PageNo)
	}
	if got[1].PageNo != nil {
		t.Errorf("row1.PageNo = %v; want nil (COALESCE 0 ⇒ unpaged)", got[1].PageNo)
	}
	if got[1].FileRole != "rubric" || got[1].Text != "Rubric text." {
		t.Errorf("row1 = %+v", got[1])
	}
}

func TestSourceChunkRepository_ListByJob_RequiresTenantAndJob(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewSourceChunkRepositoryFromTxQuerier(tq)
	if _, err := r.ListByJob(context.Background(), "", chunkJob); err == nil {
		t.Error("expected error for empty tenantID")
	}
	if _, err := r.ListByJob(context.Background(), chunkTen, ""); err == nil {
		t.Error("expected error for empty jobID")
	}
}
