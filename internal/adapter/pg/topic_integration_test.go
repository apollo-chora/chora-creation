//go:build integration

// topic_integration_test.go — live Cloud SQL integration tests for the topic
// tree (CHO-2275). Gated behind the `integration` build tag; connects to the
// live chora_creation DB via the app_rw DSN (NOBYPASSRLS) exactly like
// integration_test.go. Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-local.json
//	export CHORA_TEST_DSN='postgres://.../chora_creation?sslmode=disable'  (proxy on the host)
//	go test -tags integration -run TestTopic ./internal/adapter/pg/...
//
// Each test uses fresh per-run tenant UUIDs and hard-deletes its rows in
// t.Cleanup so it leaves prod clean and is safely re-runnable.
package pg_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

func newTopicTenant(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("mint tenant: %v", err)
	}
	return id.String()
}

func cleanupTopics(t *testing.T, pool *pgxpool.Pool, tenants ...string) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range tenants {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Logf("cleanup begin (%s): %v", tenant, err)
			continue
		}
		_, _ = tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant))
		_, _ = tx.Exec(ctx, "DELETE FROM topic_node_atoms WHERE tenant_id = $1", tenant)
		_, _ = tx.Exec(ctx, "DELETE FROM topic_nodes WHERE tenant_id = $1", tenant)
		if err := tx.Commit(ctx); err != nil {
			t.Logf("cleanup commit (%s): %v", tenant, err)
		}
	}
}

func mkNode(t *testing.T, repo *pg.TopicRepository, tenant, name string, parent *string) *topic.TopicNode {
	t.Helper()
	n, err := topic.NewTopicNode(tenant, name, parent, 0)
	if err != nil {
		t.Fatalf("NewTopicNode(%s): %v", name, err)
	}
	if err := repo.Create(context.Background(), n); err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return n
}

// TestTopic_Integration_RoundTripAndRLS is the required cross-tenant isolation
// guard: tenant B must see zero of tenant A's nodes, live under FORCE RLS.
func TestTopic_Integration_RoundTripAndRLS(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewTopicRepository(pg.NewPgxPoolQuerier(pool))
	ctx := context.Background()

	tenantA := newTopicTenant(t)
	tenantB := newTopicTenant(t)
	t.Cleanup(func() { cleanupTopics(t, pool, tenantA, tenantB) })

	// Round-trip under tenant A.
	root := mkNode(t, repo, tenantA, "Fractions", nil)
	child := mkNode(t, repo, tenantA, "Numerators", &root.TopicID)

	got, err := repo.GetByID(ctx, tenantA, root.TopicID)
	if err != nil {
		t.Fatalf("GetByID(A, root): %v", err)
	}
	if got.Name != "Fractions" {
		t.Errorf("name = %q, want Fractions", got.Name)
	}

	tree, err := repo.GetTree(ctx, tenantA, nil)
	if err != nil {
		t.Fatalf("GetTree(A): %v", err)
	}
	if len(tree) != 2 {
		t.Errorf("tenant A tree = %d nodes, want 2", len(tree))
	}
	kids, err := repo.GetTree(ctx, tenantA, &root.TopicID)
	if err != nil {
		t.Fatalf("GetTree(A, root): %v", err)
	}
	if len(kids) != 1 || kids[0].TopicID != child.TopicID {
		t.Errorf("children of root = %+v, want [%s]", kids, child.TopicID)
	}

	// RLS isolation: tenant B sees NONE of tenant A's nodes.
	treeB, err := repo.GetTree(ctx, tenantB, nil)
	if err != nil {
		t.Fatalf("GetTree(B): %v", err)
	}
	if len(treeB) != 0 {
		t.Errorf("tenant B sees %d of tenant A's nodes, want 0 (RLS breach!)", len(treeB))
	}
	if _, err := repo.GetByID(ctx, tenantB, root.TopicID); err != topic.ErrNotFound {
		t.Errorf("GetByID(B, A's node) = %v, want ErrNotFound (RLS must hide it)", err)
	}
}

// TestTopic_Integration_CyclePrevention exercises the Move cycle guard live via
// the recursive-CTE ancestry walk.
func TestTopic_Integration_CyclePrevention(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewTopicRepository(pg.NewPgxPoolQuerier(pool))
	ctx := context.Background()

	tenant := newTopicTenant(t)
	t.Cleanup(func() { cleanupTopics(t, pool, tenant) })

	a := mkNode(t, repo, tenant, "A", nil)
	b := mkNode(t, repo, tenant, "B", &a.TopicID)
	c := mkNode(t, repo, tenant, "C", &b.TopicID)

	// Move A under C: A is an ancestor of C → cycle.
	if err := repo.Move(ctx, tenant, a.TopicID, &c.TopicID); err != topic.ErrCycle {
		t.Errorf("Move(A under C) = %v, want ErrCycle", err)
	}
	// A must be unchanged (still a root).
	gotA, _ := repo.GetByID(ctx, tenant, a.TopicID)
	if gotA.ParentID != nil {
		t.Errorf("A parent = %v after refused move, want nil", *gotA.ParentID)
	}

	// Valid move: C under A directly (no cycle).
	if err := repo.Move(ctx, tenant, c.TopicID, &a.TopicID); err != nil {
		t.Errorf("Move(C under A) = %v, want nil", err)
	}
	gotC, _ := repo.GetByID(ctx, tenant, c.TopicID)
	if gotC.ParentID == nil || *gotC.ParentID != a.TopicID {
		t.Errorf("C parent = %v, want %s", gotC.ParentID, a.TopicID)
	}

	// Move a node to root.
	if err := repo.Move(ctx, tenant, b.TopicID, nil); err != nil {
		t.Errorf("Move(B to root) = %v, want nil", err)
	}
	gotB, _ := repo.GetByID(ctx, tenant, b.TopicID)
	if gotB.ParentID != nil {
		t.Errorf("B parent = %v after move-to-root, want nil", *gotB.ParentID)
	}
}

// TestTopic_Integration_DeleteNonEmpty exercises the SoftDelete children guard.
func TestTopic_Integration_DeleteNonEmpty(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewTopicRepository(pg.NewPgxPoolQuerier(pool))
	ctx := context.Background()

	tenant := newTopicTenant(t)
	t.Cleanup(func() { cleanupTopics(t, pool, tenant) })

	parent := mkNode(t, repo, tenant, "Parent", nil)
	child := mkNode(t, repo, tenant, "Child", &parent.TopicID)

	if err := repo.SoftDelete(ctx, tenant, parent.TopicID); err != topic.ErrHasChildren {
		t.Errorf("SoftDelete(parent w/ child) = %v, want ErrHasChildren", err)
	}
	// The child deletes fine, then the parent can be removed.
	if err := repo.SoftDelete(ctx, tenant, child.TopicID); err != nil {
		t.Errorf("SoftDelete(child) = %v, want nil", err)
	}
	if err := repo.SoftDelete(ctx, tenant, parent.TopicID); err != nil {
		t.Errorf("SoftDelete(now-empty parent) = %v, want nil", err)
	}
	// Soft-deleted nodes drop out of the tree.
	tree, _ := repo.GetTree(ctx, tenant, nil)
	if len(tree) != 0 {
		t.Errorf("tree after deletes = %d, want 0", len(tree))
	}
}

// TestTopic_Integration_AttachAtom verifies the atom-centric attach + its
// idempotency (ON CONFLICT), and the topic-missing guard.
func TestTopic_Integration_AttachAtom(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewTopicRepository(pg.NewPgxPoolQuerier(pool))
	ctx := context.Background()

	tenant := newTopicTenant(t)
	t.Cleanup(func() { cleanupTopics(t, pool, tenant) })

	node := mkNode(t, repo, tenant, "Topic", nil)
	atomID, _ := uuid.NewV7()

	if err := repo.AttachAtom(ctx, tenant, node.TopicID, atomID.String()); err != nil {
		t.Fatalf("AttachAtom: %v", err)
	}
	// Idempotent: attaching the same atom again must not error.
	if err := repo.AttachAtom(ctx, tenant, node.TopicID, atomID.String()); err != nil {
		t.Errorf("AttachAtom (repeat) = %v, want nil (idempotent)", err)
	}
	// Attach to a non-existent topic → ErrNotFound.
	ghost, _ := uuid.NewV7()
	if err := repo.AttachAtom(ctx, tenant, ghost.String(), atomID.String()); err != topic.ErrNotFound {
		t.Errorf("AttachAtom(ghost topic) = %v, want ErrNotFound", err)
	}
}
