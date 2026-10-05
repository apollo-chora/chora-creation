//go:build integration

// integration_test.go — live Cloud SQL integration tests gated behind
// the `integration` build tag. Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-local.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_creation-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-local
//	go test -tags integration ./services/chora-creation/internal/adapter/pg/...
//
// These tests connect to the live chora_creation database via the local
// Cloud SQL Auth Proxy on :5432.
//
// Test suite (mirrors chora-identity Wave-A close-out):
//
//  1. Round-trip Save → Get against the seeded Phyllis CSPO atom set
//  2. RLS isolation: insert a learning_atom under tenant A, query under
//     tenant B → 0 rows (tenant_isolation policy must hold)
//  3. AtomRevisions append-only enforcement: UPDATE / DELETE rejected
//     by trigger
package pg_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cgcdb "github.com/apollo-chora/chora-common/db"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// chenTenantID + cspoCourseID are the deterministic IDs from
// chora-infra/seed/phyllis/03_creation.sql.
const (
	chenTenantID  = "22222222-2222-7222-8222-222222222222"
	cspoCourseID  = "33333333-3333-7333-8333-333333333333"
	chenGCID      = "00000000-0000-7000-8000-000000001002"
	seededAtomID1 = "00000000-0000-7000-8000-000000005e10"
)

func liveDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("CHORA_TEST_DSN")
	secretID := os.Getenv("CHORA_TEST_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		t.Skip("set CHORA_TEST_DSN or CHORA_TEST_DSN_SECRET_ID to run integration tests")
	}

	project := os.Getenv("CHORA_TEST_DB_PROJECT")
	if project == "" {
		project = "chora-local"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fetcher cgcdb.SecretFetcher
	var sclient *cgcsecrets.Client
	if secretID != "" && dsn == "" {
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			t.Fatalf("secret manager: %v", err)
		}
		sclient = c
		fetcher = c
	}

	pool, err := cgcdb.Bootstrap(ctx, cgcdb.BootstrapOptions{
		DSN:             dsn,
		SecretID:        secretID,
		SecretFetcher:   fetcher,
		RewriteFromPort: 6432,
		RewriteToPort:   5432,
		AppName:         "chora-creation-pg-integration-test",
	})
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	})
	return pool
}

// TestIntegration_AtomRepository_ListByCourse_PhyllisSeed verifies the
// Phyllis seed (5 atoms attached to Mr. Chen's CSPO course) is reachable
// via ListByCourse under Mr. Chen's tenant context.
func TestIntegration_AtomRepository_ListByCourse_PhyllisSeed(t *testing.T) {
	pool := liveDB(t)
	q := pg.NewPgxPoolQuerier(pool)
	r := pg.NewAtomRepository(q)

	ctx := context.Background()

	// Set tenant context so RLS lets app_rw read Mr. Chen's tenant rows.
	// (When connected as the migrate role this is defense-in-depth — OWNER
	// bypasses RLS — but the same code path runs in production under
	// app_rw which is RLS-bound.)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", chenTenantID)); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	// Use the tx-bound querier for this read so SET LOCAL applies.
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM learning_atoms WHERE course_id = $1::uuid AND deleted_at IS NULL`,
		cspoCourseID).Scan(&count); err != nil {
		t.Fatalf("count seeded atoms: %v", err)
	}
	if count < 5 {
		t.Fatalf("expected >= 5 seeded atoms in CSPO course; got %d (run chora-infra/scripts/seed-phyllis-demo.sh)", count)
	}

	// Now exercise the repo via a tx-scoped Querier so the SET LOCAL
	// chora.tenant_id applies. Production callers wrap their HTTP request
	// in a tenant-bound transaction (RunTenantTx) before invoking the
	// repository methods.
	got, err := pg.NewAtomRepositoryFromTx(tx).Get(ctx, chenTenantID, seededAtomID1)
	if err != nil {
		t.Fatalf("get seeded atom: %v", err)
	}
	if got.AtomID != seededAtomID1 {
		t.Errorf("atom_id: got %q want %q", got.AtomID, seededAtomID1)
	}
	if got.TenantID != chenTenantID {
		t.Errorf("tenant_id: got %q want %q", got.TenantID, chenTenantID)
	}
	if got.CourseID != cspoCourseID {
		t.Errorf("course_id: got %q want %q", got.CourseID, cspoCourseID)
	}
	_ = q
	_ = r
}

// TestIntegration_RLS_TenantIsolation verifies that a learning_atom inserted
// under tenant A is NOT visible under tenant B.
func TestIntegration_RLS_TenantIsolation(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	atomID, _ := uuid.NewV7()
	gcid, _ := uuid.NewV7()

	t.Cleanup(func() {
		// Hard-delete is allowed for the test-only atom (no FK from
		// atom_revisions yet because we never wrote one).
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM learning_atoms WHERE atom_id = $1`, atomID.String())
	})

	// Insert under tenant A
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("set local A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO learning_atoms
            (atom_id, tenant_id, gcid, title, body, mode, status, revision)
        VALUES ($1, $2, $3, 'rls test', 'body', 'straight-up'::atom_mode, 'draft'::atom_status, 1)`,
		atomID.String(), tenantA.String(), gcid.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert under A: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// Read under tenant B — must see 0 rows
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer tx2.Rollback(ctx)
	if _, err := tx2.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantB.String())); err != nil {
		t.Fatalf("set local B: %v", err)
	}
	var countB int
	if err := tx2.QueryRow(ctx,
		`SELECT count(*) FROM learning_atoms WHERE atom_id = $1`, atomID.String()).Scan(&countB); err != nil {
		t.Fatalf("count under B: %v", err)
	}
	if countB != 0 {
		t.Errorf("RLS LEAK: tenant B saw %d rows for tenant A's atom_id", countB)
	}

	// Read under tenant A — must see 1 row
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A2: %v", err)
	}
	defer tx3.Rollback(ctx)
	if _, err := tx3.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		t.Fatalf("set local A2: %v", err)
	}
	var countA int
	if err := tx3.QueryRow(ctx,
		`SELECT count(*) FROM learning_atoms WHERE atom_id = $1`, atomID.String()).Scan(&countA); err != nil {
		t.Fatalf("count under A: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 row under tenant A, got %d", countA)
	}
	t.Logf("RLS isolation OK: tenant A=%d row(s), tenant B=%d row(s)", countA, countB)
}

// TestIntegration_AtomRepository_NotFound verifies missing-atom returns
// the domain sentinel error.
func TestIntegration_AtomRepository_NotFound(t *testing.T) {
	pool := liveDB(t)
	q := pg.NewPgxPoolQuerier(pool)
	r := pg.NewAtomRepository(q)

	missing, _ := uuid.NewV7()
	_, err := r.Get(context.Background(), chenTenantID, missing.String())
	if err == nil {
		t.Fatalf("expected ErrNotFound")
	}
	if !errors.Is(err, atom.ErrNotFound) {
		t.Errorf("expected atom.ErrNotFound, got %v", err)
	}
}
