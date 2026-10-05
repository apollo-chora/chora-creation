package pg_test

// CHO-2398 (ADR-251 D5): ApplyChunkCandidates - slot-keyed, RLS-scoped,
// idempotent chunk persistence. The SQL owns the guards: status='running'
// (a late chunk after the terminal never clobbers) and slot-absent (a
// duplicate or reordered delivery contributes nothing); RETURNING-as-CAS
// reports whether THIS call applied.

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
)

func TestQuestionJobRepository_ApplyChunkCandidates_AppliesInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{jobID}} // RETURNING a row => applied
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	applied, err := r.ApplyChunkCandidates(
		context.Background(), jobTen, jobID, 1, 3, []byte(`[{"stem":"a"}]`),
	)
	if err != nil {
		t.Fatalf("ApplyChunkCandidates: %v", err)
	}
	if !applied {
		t.Error("applied = false; want true (RETURNING row)")
	}
	if !strings.Contains(tq.querySQL, "UPDATE question_generation_jobs") {
		t.Errorf("expected UPDATE question_generation_jobs; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "chunk_candidates_jsonb") {
		t.Errorf("expected chunk_candidates_jsonb slot write; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "status = 'running'") {
		t.Errorf("expected status='running' guard; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "chunk_candidates_jsonb ? $2") {
		t.Errorf("expected slot-absent idempotence guard; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "GREATEST") {
		t.Errorf("expected chunk_count_total to never regress; got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "SET status") {
		t.Errorf("must NOT transition status; got %q", tq.querySQL)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != jobTen {
		t.Errorf("expected one RunInTenantTx on tenant %q; got %+v", jobTen, tq.calls)
	}
}

func TestQuestionJobRepository_ApplyChunkCandidates_GuardReject_NotApplied(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows} // terminal job OR slot filled
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	applied, err := r.ApplyChunkCandidates(
		context.Background(), jobTen, jobID, 0, 2, []byte(`[{"stem":"a"}]`),
	)
	if err != nil {
		t.Fatalf("guard reject must be a no-op, not an error: %v", err)
	}
	if applied {
		t.Error("applied = true; want false (ErrNoRows => guard rejected)")
	}
}

func TestQuestionJobRepository_ApplyChunkCandidates_InputGuards(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if _, err := r.ApplyChunkCandidates(context.Background(), "", jobID, 0, 1, []byte(`[]`)); err == nil {
		t.Error("expected error on empty tenantID (RLS no-op guard)")
	}
	if _, err := r.ApplyChunkCandidates(context.Background(), jobTen, jobID, -1, 1, []byte(`[]`)); err == nil {
		t.Error("expected error on negative chunk_index")
	}
	if _, err := r.ApplyChunkCandidates(context.Background(), jobTen, jobID, 0, 1, nil); err == nil {
		t.Error("expected error on empty candidates JSON (never a blank slot)")
	}
	if len(tq.calls) != 0 {
		t.Errorf("input guards must reject before RunInTenantTx; got %d calls", len(tq.calls))
	}
}
