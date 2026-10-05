//go:build integration

// exam_embargo_integration_test.go — ADR-191 rollout §2 LEAD adversarial test,
// layer-1 (DB-RLS) half, against the LIVE chora_creation database.
//
// Proves the RESTRICTIVE `exam_content_embargo` policy (mig 0028) end-to-end
// through the REAL repository + the O3 role-GUC propagation seam: a
// PROCTOR-claimed caller gets ZERO atom-content rows; an INSTRUCTOR caller (and
// a no-roles caller) reads content unaffected. Also asserts the atom_revisions
// policy is installed (the append-only trigger forbids writing/cleaning a test
// revision, so that table is verified via the pg_policies catalog).
//
// Run (see integration_test.go header for env):
//
//	go test -tags integration ./services/chora-creation/internal/adapter/pg/...
package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestIntegration_ExamContentEmbargo_RLS(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantID, _ := uuid.NewV7()
	atomID, _ := uuid.NewV7()
	gcid, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM learning_atoms WHERE atom_id = $1`, atomID.String())
	})

	// Seed one atom under tenantID (tenant-scoped insert so tenant_isolation
	// admits it).
	{
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin seed: %v", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantID.String()+"'"); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO learning_atoms
                (atom_id, tenant_id, gcid, title, body, mode, status, revision)
            VALUES ($1, $2, $3, 'embargo test', 'secret paper', 'straight-up'::atom_mode, 'published'::atom_status, 1)`,
			atomID.String(), tenantID.String(), gcid.String()); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("insert atom: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit seed: %v", err)
		}
	}

	repo := pg.NewAtomRepository(pg.NewPgxPoolQuerier(pool))

	// (1) PROCTOR claim on ctx → O3 seam emits SET LOCAL chora.user_roles=
	// 'proctor' → restrictive policy excludes the row → ErrNotFound (ZERO rows).
	if _, err := repo.Get(tracing.WithUserRoles(ctx, "proctor"), tenantID.String(), atomID.String()); !errors.Is(err, atom.ErrNotFound) {
		t.Errorf("proctor Get err = %v; want atom.ErrNotFound (embargoed → zero rows)", err)
	}

	// (2) INSTRUCTOR+PROCTOR → still embargoed (presence of proctor token).
	if _, err := repo.Get(tracing.WithUserRoles(ctx, "instructor,proctor"), tenantID.String(), atomID.String()); !errors.Is(err, atom.ErrNotFound) {
		t.Errorf("instructor+proctor Get err = %v; want atom.ErrNotFound", err)
	}

	// (3) INSTRUCTOR-only → content visible (unaffected).
	if got, err := repo.Get(tracing.WithUserRoles(ctx, "instructor"), tenantID.String(), atomID.String()); err != nil || got == nil {
		t.Errorf("instructor Get = (%v, %v); want the atom, nil err", got, err)
	}

	// (4) No roles on ctx (legacy path) → visible (COALESCE unset → non-proctor).
	if got, err := repo.Get(ctx, tenantID.String(), atomID.String()); err != nil || got == nil {
		t.Errorf("no-roles Get = (%v, %v); want the atom, nil err (COALESCE unset)", got, err)
	}

	// (5) atom_revisions coverage — the append-only trigger forbids writing a
	// test revision, so assert the RESTRICTIVE SELECT policy is installed via
	// the catalog.
	for _, tbl := range []string{"learning_atoms", "atom_revisions"} {
		var present bool
		if err := pool.QueryRow(ctx, `
            SELECT EXISTS (
                SELECT 1 FROM pg_policies
                WHERE schemaname = 'public' AND tablename = $1
                  AND policyname = 'exam_content_embargo'
                  AND permissive = 'RESTRICTIVE' AND cmd = 'SELECT'
            )`, tbl).Scan(&present); err != nil {
			t.Fatalf("pg_policies check for %s: %v", tbl, err)
		}
		if !present {
			t.Errorf("exam_content_embargo RESTRICTIVE SELECT policy missing on %s (mig 0028 not applied?)", tbl)
		}
	}
}
