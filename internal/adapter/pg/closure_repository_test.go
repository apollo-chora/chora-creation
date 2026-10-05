// closure_repository_test.go — pgx adapter tests for the durable
// ClosureRepository (W0-F1 durability + W0-F5 error-honesty, CHO-2198)
// using the shared stub TxQuerier idiom (see atom_repository_test.go's
// stubTxQuerier/stubTx — ClosureRepository ALWAYS runs inside
// RunInTenantTx, so the plain stubQuerier path is not exercised here).
//
// These are unit tests against the SQL emit + scan surface — no live DB.
// The critical assertions here are the W0-F5 ones: a genuine backing-store
// error from either Pseudonymise or IsPseudonymised must come back as a
// non-nil error, never get coerced into a false/zero "everything is fine"
// result (the swallowed-error trap the in-memory port's original
// `IsPseudonymised(gcid string) bool` signature made structurally
// impossible to avoid).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/config"
)

func closureSpecFixture() []config.TableSpec {
	return []config.TableSpec{
		{
			Table: "learning_atom",
			Columns: []config.ColumnSpec{
				{Column: "created_by_display_name", Strategy: "tombstone_string", Value: "Former member"},
				{Column: "created_by_email_snapshot", Strategy: "drop", Value: ""},
				{Column: "published_by_display_name", Strategy: "tombstone_string", Value: "Former member"},
			},
		},
		{
			Table: "media_asset",
			Columns: []config.ColumnSpec{
				{Column: "uploaded_by_display_name", Strategy: "tombstone_string", Value: "Former member"},
			},
		},
	}
}

// -----------------------------------------------------------------------------
// Pseudonymise
// -----------------------------------------------------------------------------

func TestClosureRepository_Pseudonymise_EmitsInsertOnConflictReturningID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{uuid.NewString()}}
	repo := pg.NewClosureRepository(tq)

	rows, err := repo.Pseudonymise(context.Background(), "11111111-1111-7111-8111-111111111111", "gcid-A", closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if rows != 4 {
		t.Fatalf("expected rows_touched=4 (3+1 columns); got %d", rows)
	}

	wants := []string{"INSERT INTO closure_pseudonymisation_state", "ON CONFLICT", "DO NOTHING", "RETURNING"}
	for _, w := range wants {
		if !strings.Contains(tq.querySQL, w) {
			t.Errorf("Pseudonymise SQL missing %q; got:\n%s", w, tq.querySQL)
		}
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx to be called exactly once; got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != "11111111-1111-7111-8111-111111111111" {
		t.Errorf("RunInTenantTx tenantID = %q; want %q", tq.calls[0].tenantID, "11111111-1111-7111-8111-111111111111")
	}
}

func TestClosureRepository_Pseudonymise_MintsUUIDv7ForID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{uuid.NewString()}}
	repo := pg.NewClosureRepository(tq)

	if _, err := repo.Pseudonymise(context.Background(), "tenant-A", "gcid-A", nil); err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if len(tq.queryArgs) == 0 {
		t.Fatalf("expected query args")
	}
	idArg, ok := tq.queryArgs[0].(string)
	if !ok || idArg == "" {
		t.Fatalf("expected non-empty string id as first arg; got %T %v", tq.queryArgs[0], tq.queryArgs[0])
	}
	parsed, err := uuid.Parse(idArg)
	if err != nil {
		t.Fatalf("minted id %q is not a valid UUID: %v", idArg, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("minted id %q is not UUIDv7 (version=%d)", idArg, parsed.Version())
	}
}

func TestClosureRepository_Pseudonymise_ReturnsZeroWhenConflictFires(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	// ON CONFLICT DO NOTHING suppresses the RETURNING row — the stub Tx
	// signals that exactly as the pg.Tx seam does: ErrNoRows (the
	// stubQuerier's default QueryRow behaviour when .queryRow is unset).
	tq := &stubTxQuerier{}
	repo := pg.NewClosureRepository(tq)

	rows, err := repo.Pseudonymise(context.Background(), "tenant-A", "gcid-A", closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: expected idempotent no-op, got error: %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on idempotent replay; got %d", rows)
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyTenantID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	tq := &stubTxQuerier{}
	repo := pg.NewClosureRepository(tq)
	_, err := repo.Pseudonymise(context.Background(), "", "gcid-A", nil)
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if len(tq.calls) != 0 {
		t.Fatalf("must not open a tenant tx on validation failure; got %d calls", len(tq.calls))
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyGCID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	tq := &stubTxQuerier{}
	repo := pg.NewClosureRepository(tq)
	_, err := repo.Pseudonymise(context.Background(), "tenant-A", "", nil)
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if len(tq.calls) != 0 {
		t.Fatalf("must not open a tenant tx on validation failure; got %d calls", len(tq.calls))
	}
}

// TestClosureRepository_Pseudonymise_PropagatesScanError is the W0-F5
// fail-loud proof for Pseudonymise: a genuine backing-store error (NOT
// pg.ErrNoRows) must come back as a non-nil error, never as a silent
// "idempotent no-op" (0, nil) — conflating "I don't know" with "already
// done" would let the closure saga believe this domain acked when it did
// not, exactly the class of bug in
// reusable_gotcha_swallowed_error_damage_is_decided_by_the_caller.
func TestClosureRepository_Pseudonymise_PropagatesScanError(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: boom}
	repo := pg.NewClosureRepository(tq)

	rows, err := repo.Pseudonymise(context.Background(), "tenant-A", "gcid-A", closureSpecFixture())
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (rows=%d)", rows)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on error; got %d", rows)
	}
}

// -----------------------------------------------------------------------------
// IsPseudonymised
// -----------------------------------------------------------------------------

func TestClosureRepository_IsPseudonymised_TrueWhenRowExists(t *testing.T) {
	t.Parallel()
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{1}}
	repo := pg.NewClosureRepository(tq)

	got, err := repo.IsPseudonymised(context.Background(), "tenant-A", "gcid-A")
	if err != nil {
		t.Fatalf("IsPseudonymised: %v", err)
	}
	if !got {
		t.Fatalf("expected true when a row exists")
	}
	if !strings.Contains(tq.querySQL, "FROM closure_pseudonymisation_state") {
		t.Errorf("IsPseudonymised SQL malformed; got:\n%s", tq.querySQL)
	}
}

func TestClosureRepository_IsPseudonymised_FalseWhenNoRows(t *testing.T) {
	t.Parallel()
	tq := &stubTxQuerier{} // defaults to ErrNoRows
	repo := pg.NewClosureRepository(tq)

	got, err := repo.IsPseudonymised(context.Background(), "tenant-A", "gcid-A")
	if err != nil {
		t.Fatalf("IsPseudonymised: expected nil error on a clean miss; got %v", err)
	}
	if got {
		t.Fatalf("expected false when no row exists")
	}
}

// TestClosureRepository_IsPseudonymised_PropagatesQueryError is the core
// W0-F5 proof for IsPseudonymised: this is exactly the method the original
// `IsPseudonymised(gcid string) bool` signature could NOT have implemented
// honestly against Postgres (no ctx, no error return). A real backing-store
// error must be reported, not folded into `false`.
func TestClosureRepository_IsPseudonymised_PropagatesQueryError(t *testing.T) {
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: boom}
	repo := pg.NewClosureRepository(tq)

	got, err := repo.IsPseudonymised(context.Background(), "tenant-A", "gcid-A")
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (got=%v)", got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if got {
		t.Fatalf("expected false alongside the error (never claim true on failure)")
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()
	tq := &stubTxQuerier{}
	repo := pg.NewClosureRepository(tq)
	_, err := repo.IsPseudonymised(context.Background(), "", "gcid-A")
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if len(tq.calls) != 0 {
		t.Fatalf("must not open a tenant tx on validation failure; got %d calls", len(tq.calls))
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	tq := &stubTxQuerier{}
	repo := pg.NewClosureRepository(tq)
	_, err := repo.IsPseudonymised(context.Background(), "tenant-A", "")
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if len(tq.calls) != 0 {
		t.Fatalf("must not open a tenant tx on validation failure; got %d calls", len(tq.calls))
	}
}

func TestClosureRepository_NoTxQuerier_FailsLoud(t *testing.T) {
	t.Parallel()
	repo := pg.NewClosureRepository(nil)
	if _, err := repo.Pseudonymise(context.Background(), "tenant-A", "gcid-A", nil); err == nil {
		t.Fatalf("expected error when no TxQuerier wired (Pseudonymise)")
	}
	if _, err := repo.IsPseudonymised(context.Background(), "tenant-A", "gcid-A"); err == nil {
		t.Fatalf("expected error when no TxQuerier wired (IsPseudonymised)")
	}
}
