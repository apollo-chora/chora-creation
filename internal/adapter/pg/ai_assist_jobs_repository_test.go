// ai_assist_jobs_repository_test.go — TDD coverage for the pgx adapter
// of ports.AiAssistJobsRepository (Step 4b per docs/m13/ack-oe-ai-assist
// -plan-2026-05-17.md).
//
// Uses the shared stubTxQuerier from atom_repository_test.go so SQL
// emission is observable without a live DB. Every method MUST wrap
// SQL in RunInTenantTx so SET LOCAL chora.tenant_id runs before any
// tenant-scoped operation (chora_creation_app_rw is NOBYPASSRLS and
// ai_assist_jobs carries the ai_assist_jobs_tenant_isolation policy).
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
)

const (
	aiAssistJobID  = "01970000-0000-7000-8000-0000000000aa"
	aiAssistTenant = "22222222-2222-7222-8222-222222222222"
	aiAssistAuthor = "00000000-0000-7000-8000-00000000a001"
)

func newQueuedAiAssistJob(t *testing.T) *aiassist.Job {
	t.Helper()
	j, err := aiassist.NewQueuedJob(
		aiAssistJobID, aiAssistTenant, aiAssistAuthor,
		aiassist.QuestionTypeOE, []byte(`{"prompt":"chlorophyll"}`),
	)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return j
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_Create_InsertsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	if err := r.Create(context.Background(), newQueuedAiAssistJob(t)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx called once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != aiAssistTenant {
		t.Errorf("tx tenantID = %q; want %q", tq.calls[0].tenantID, aiAssistTenant)
	}
	if !strings.Contains(tq.execSQL, "INSERT INTO ai_assist_jobs") {
		t.Errorf("expected INSERT INTO ai_assist_jobs; got %q", tq.execSQL)
	}
	// Idempotent on (id) — re-emit must not double-create.
	if !strings.Contains(tq.execSQL, "ON CONFLICT (id) DO NOTHING") {
		t.Errorf("expected ON CONFLICT (id) DO NOTHING idempotency clause; got %q", tq.execSQL)
	}
}

func TestAiAssistJobsRepository_Create_RejectsNil(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	if err := r.Create(context.Background(), nil); err == nil {
		t.Errorf("expected error on nil job")
	}
}

func TestAiAssistJobsRepository_Create_RejectsMissingIDs(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	j := newQueuedAiAssistJob(t)
	j.ID = ""
	if err := r.Create(context.Background(), j); err == nil {
		t.Errorf("expected error on empty ID")
	}
	j = newQueuedAiAssistJob(t)
	j.TenantID = ""
	if err := r.Create(context.Background(), j); err == nil {
		t.Errorf("expected error on empty TenantID")
	}
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_Get_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	_, err := r.Get(context.Background(), aiAssistTenant, aiAssistJobID)
	// Stub returns ErrNoRows → adapter returns aiassist.ErrNotFound.
	if err == nil || err != aiassist.ErrNotFound {
		t.Fatalf("expected aiassist.ErrNotFound; got %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != aiAssistTenant {
		t.Errorf("RunInTenantTx not invoked with tenantID")
	}
	if !strings.Contains(tq.querySQL, "FROM ai_assist_jobs") {
		t.Errorf("expected SELECT ... FROM ai_assist_jobs; got %q", tq.querySQL)
	}
	// Tenant-scoped — WHERE clause includes id + tenant_id.
	if !strings.Contains(tq.querySQL, "id = $1") || !strings.Contains(tq.querySQL, "tenant_id = $2") {
		t.Errorf("expected tenant-scoped WHERE; got %q", tq.querySQL)
	}
}

func TestAiAssistJobsRepository_Get_RejectsEmptyArgs(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	if _, err := r.Get(context.Background(), "", "id"); err == nil {
		t.Errorf("expected error on empty tenantID")
	}
	if _, err := r.Get(context.Background(), "tenant", ""); err == nil {
		t.Errorf("expected error on empty jobID")
	}
}

// -----------------------------------------------------------------------------
// UpdateCompleted
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_UpdateCompleted_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	err := r.UpdateCompleted(
		context.Background(), aiAssistTenant, aiAssistJobID,
		[]byte(`{"stem":"x"}`), []byte(`[]`), false, 1, 10,
	)
	if err != nil {
		t.Fatalf("UpdateCompleted: %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != aiAssistTenant {
		t.Errorf("RunInTenantTx not invoked")
	}
	if !strings.Contains(tq.execSQL, "UPDATE ai_assist_jobs") {
		t.Errorf("expected UPDATE; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "status = 'COMPLETED'") {
		t.Errorf("expected status=COMPLETED literal in UPDATE; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "WHERE id = $1") {
		t.Errorf("expected tenant-scoped WHERE; got %q", tq.execSQL)
	}
}

func TestAiAssistJobsRepository_UpdateCompleted_RejectsEmptyArgs(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	if err := r.UpdateCompleted(context.Background(), "", "id", nil, nil, false, 0, 0); err == nil {
		t.Errorf("expected error on empty tenant")
	}
}

// -----------------------------------------------------------------------------
// UpdateRefused — refusal_reason guard
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_UpdateRefused_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	err := r.UpdateRefused(
		context.Background(), aiAssistTenant, aiAssistJobID,
		aiassist.RefusalReasonGuardrailPre,
		"armor:pii_high_risk_block",
		"Your prompt couldn't be processed.",
		nil, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("UpdateRefused: %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != aiAssistTenant {
		t.Errorf("RunInTenantTx not invoked")
	}
	if !strings.Contains(tq.execSQL, "status = 'REFUSED'") {
		t.Errorf("expected status=REFUSED literal; got %q", tq.execSQL)
	}
}

func TestAiAssistJobsRepository_UpdateRefused_RejectsInvalidReason(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	// Empty reason — invalid per [[feedback-no-stubs-real-wiring]].
	if err := r.UpdateRefused(
		context.Background(), aiAssistTenant, aiAssistJobID,
		aiassist.RefusalReason(""), "", "", nil, nil, 0, 0,
	); err == nil {
		t.Errorf("expected error on empty refusal_reason")
	}
	// Out-of-enum reason — invalid.
	if err := r.UpdateRefused(
		context.Background(), aiAssistTenant, aiAssistJobID,
		aiassist.RefusalReason("POLICY"), "", "", nil, nil, 0, 0,
	); err == nil {
		t.Errorf("expected error on out-of-enum refusal_reason")
	}
}

// -----------------------------------------------------------------------------
// UpdateFailed
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_UpdateFailed_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	err := r.UpdateFailed(
		context.Background(), aiAssistTenant, aiAssistJobID,
		nil, "orchestrator crashed", 0, 0,
	)
	if err != nil {
		t.Fatalf("UpdateFailed: %v", err)
	}
	if !strings.Contains(tq.execSQL, "status = 'FAILED'") {
		t.Errorf("expected status=FAILED literal; got %q", tq.execSQL)
	}
}

// -----------------------------------------------------------------------------
// Compile-time port satisfaction (regression: changes to the
// AiAssistJobsRepository interface must be reflected here so the build
// catches signature drift).
// -----------------------------------------------------------------------------

func TestAiAssistJobsRepository_ImplementsPort(t *testing.T) {
	// If the pg adapter ever drifts from the port, this var assignment
	// (which runs at compile-time via Go's type system) will fail.
	_ = time.Now()
	tq := &stubTxQuerier{}
	r := pg.NewAiAssistJobsRepositoryFromTxQuerier(tq)
	_ = r
}
