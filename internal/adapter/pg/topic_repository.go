// topic_repository.go — pgx-backed implementation of topic.Repository +
// topic.SeedSink (CHO-2275 Sub-phase A).
//
// RLS contract: topic_nodes + topic_node_atoms are tenant-scoped (RLS keyed on
// chora.tenant_id, FORCE) and chora_creation_app_rw is NOBYPASSRLS — a bare
// query against the pool silently returns 0 rows. So every method wraps its SQL
// in RunInTenantTx (SET LOCAL chora.tenant_id runs in the SAME transaction),
// exactly like AtomRepository. Move + SoftDelete run their read-modify-write in
// ONE transaction so the cycle/children reads and the write stay consistent.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

// TopicRepository is the pgx-backed topic.Repository + topic.SeedSink.
type TopicRepository struct {
	tx TxQuerier
}

// NewTopicRepository wraps a *PgxPoolQuerier (production wiring). *PgxPoolQuerier
// satisfies TxQuerier, so every call runs inside a tenant-scoped tx (RLS path).
func NewTopicRepository(querier *PgxPoolQuerier) *TopicRepository {
	return &TopicRepository{tx: querier}
}

// NewTopicRepositoryFromTxQuerier wraps an explicit TxQuerier (test seam).
func NewTopicRepositoryFromTxQuerier(tq TxQuerier) *TopicRepository {
	return &TopicRepository{tx: tq}
}

const topicColumns = `topic_id, tenant_id, name,
    COALESCE(parent_id::text, ''), sort_order,
    created_at, updated_at, deleted_at`

// Create inserts a new node inside a tenant-scoped tx.
func (r *TopicRepository) Create(ctx context.Context, n *topic.TopicNode) error {
	if n == nil {
		return errors.New("pg.TopicRepository.Create: nil node")
	}
	const sql = `
        INSERT INTO topic_nodes (
            topic_id, tenant_id, name, parent_id, sort_order,
            created_at, updated_at, deleted_at
        ) VALUES (
            $1, $2, $3, NULLIF($4,'')::uuid, $5, $6, $7, $8
        )`
	return r.tx.RunInTenantTx(ctx, n.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sql,
			n.TopicID, n.TenantID, n.Name, parentArg(n.ParentID), n.SortOrder,
			n.CreatedAt, n.UpdatedAt, n.DeletedAt,
		); err != nil {
			return fmt.Errorf("pg.TopicRepository.Create: %w", err)
		}
		return nil
	})
}

// Update persists a node's mutable fields (the DB trigger also bumps updated_at).
func (r *TopicRepository) Update(ctx context.Context, n *topic.TopicNode) error {
	if n == nil {
		return errors.New("pg.TopicRepository.Update: nil node")
	}
	const sql = `
        UPDATE topic_nodes
        SET name = $3, parent_id = NULLIF($4,'')::uuid, sort_order = $5,
            deleted_at = $6
        WHERE topic_id = $1 AND tenant_id = $2`
	return r.tx.RunInTenantTx(ctx, n.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, sql,
			n.TopicID, n.TenantID, n.Name, parentArg(n.ParentID), n.SortOrder, n.DeletedAt,
		); err != nil {
			return fmt.Errorf("pg.TopicRepository.Update: %w", err)
		}
		return nil
	})
}

// GetByID returns the active node for (tenant, id) or topic.ErrNotFound.
func (r *TopicRepository) GetByID(ctx context.Context, tenantID, topicID string) (*topic.TopicNode, error) {
	const sql = `SELECT ` + topicColumns + `
        FROM topic_nodes
        WHERE topic_id = $1 AND tenant_id = $2 AND deleted_at IS NULL`
	var out *topic.TopicNode
	notFound := false
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		n, err := scanTopicNode(tx.QueryRow(ctx, sql, topicID, tenantID))
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				notFound = true
				return nil
			}
			return err
		}
		out = n
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg.TopicRepository.GetByID: %w", err)
	}
	if notFound {
		return nil, topic.ErrNotFound
	}
	return out, nil
}

// GetTree returns active nodes for a tenant as a flat slice. parentID nil ⇒ ALL
// of the tenant's nodes; parentID set ⇒ direct children of parentID.
func (r *TopicRepository) GetTree(ctx context.Context, tenantID string, parentID *string) ([]*topic.TopicNode, error) {
	sql := `SELECT ` + topicColumns + `
        FROM topic_nodes
        WHERE tenant_id = $1 AND deleted_at IS NULL`
	args := []any{tenantID}
	if parentID != nil {
		args = append(args, *parentID)
		sql += ` AND parent_id = $2::uuid`
	}
	sql += ` ORDER BY sort_order ASC, created_at ASC`

	out := []*topic.TopicNode{}
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, sql, args...)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			n, serr := scanTopicNode(rows)
			if serr != nil {
				return serr
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("pg.TopicRepository.GetTree: %w", err)
	}
	return out, nil
}

// Move reparents topicID. Loads the node + the new parent's ancestry chain and
// calls topic.TopicNode.Move (the cycle guard), then persists — all in one tx.
func (r *TopicRepository) Move(ctx context.Context, tenantID, topicID string, newParentID *string) error {
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		node, err := scanTopicNode(tx.QueryRow(ctx, `SELECT `+topicColumns+`
            FROM topic_nodes WHERE topic_id = $1 AND tenant_id = $2 AND deleted_at IS NULL`,
			topicID, tenantID))
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return topic.ErrNotFound
			}
			return fmt.Errorf("pg.TopicRepository.Move: load node: %w", err)
		}

		var chain []string
		if newParentID != nil && strings.TrimSpace(*newParentID) != "" {
			chain, err = ancestorChain(ctx, tx, tenantID, *newParentID)
			if err != nil {
				return fmt.Errorf("pg.TopicRepository.Move: ancestry: %w", err)
			}
			if len(chain) == 0 {
				// The target parent does not exist (or is deleted) for this tenant.
				return topic.ErrNotFound
			}
		}

		if err := node.Move(newParentID, chain); err != nil {
			return err // topic.ErrCycle / ErrInvalidParent — mapped by the handler
		}

		if err := tx.Exec(ctx, `
            UPDATE topic_nodes SET parent_id = NULLIF($3,'')::uuid
            WHERE topic_id = $1 AND tenant_id = $2`,
			topicID, tenantID, parentArg(node.ParentID)); err != nil {
			return fmt.Errorf("pg.TopicRepository.Move: persist: %w", err)
		}
		return nil
	})
}

// SoftDelete soft-deletes topicID. Counts active children and calls
// topic.TopicNode.SoftDelete (ErrHasChildren for a non-empty node); on success
// soft-deletes the node AND its own atom-attachment link rows (a bounded cascade
// within the topic-attachment concern, never across aggregates). One tx.
func (r *TopicRepository) SoftDelete(ctx context.Context, tenantID, topicID string) error {
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		node, err := scanTopicNode(tx.QueryRow(ctx, `SELECT `+topicColumns+`
            FROM topic_nodes WHERE topic_id = $1 AND tenant_id = $2 AND deleted_at IS NULL`,
			topicID, tenantID))
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return topic.ErrNotFound
			}
			return fmt.Errorf("pg.TopicRepository.SoftDelete: load node: %w", err)
		}

		var childCount int
		if err := tx.QueryRow(ctx, `
            SELECT count(*) FROM topic_nodes
            WHERE tenant_id = $1 AND parent_id = $2::uuid AND deleted_at IS NULL`,
			tenantID, topicID).Scan(&childCount); err != nil {
			return fmt.Errorf("pg.TopicRepository.SoftDelete: count children: %w", err)
		}

		if err := node.SoftDelete(childCount > 0); err != nil {
			return err // topic.ErrHasChildren — mapped by the handler
		}

		if err := tx.Exec(ctx, `
            UPDATE topic_nodes SET deleted_at = $3
            WHERE topic_id = $1 AND tenant_id = $2`,
			topicID, tenantID, node.DeletedAt); err != nil {
			return fmt.Errorf("pg.TopicRepository.SoftDelete: persist: %w", err)
		}
		// Bounded cascade: soft-delete the topic's own atom-attachment links.
		if err := tx.Exec(ctx, `
            UPDATE topic_node_atoms SET deleted_at = now()
            WHERE topic_id = $1 AND tenant_id = $2 AND deleted_at IS NULL`,
			topicID, tenantID); err != nil {
			return fmt.Errorf("pg.TopicRepository.SoftDelete: detach atoms: %w", err)
		}
		return nil
	})
}

// AttachAtom idempotently links an atom to a topic. Verifies the topic exists
// for the tenant first (topic.ErrNotFound otherwise). Atom-centric: the atom is
// referenced by UUID, never owned.
func (r *TopicRepository) AttachAtom(ctx context.Context, tenantID, topicID, atomID string) error {
	link, err := topic.NewTopicAtomLink(tenantID, topicID, atomID)
	if err != nil {
		return err
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `
            SELECT 1 FROM topic_nodes
            WHERE topic_id = $1 AND tenant_id = $2 AND deleted_at IS NULL`,
			topicID, tenantID).Scan(&one); err != nil {
			if errors.Is(err, ErrNoRows) {
				return topic.ErrNotFound
			}
			return fmt.Errorf("pg.TopicRepository.AttachAtom: verify topic: %w", err)
		}
		if err := tx.Exec(ctx, `
            INSERT INTO topic_node_atoms (topic_id, atom_id, tenant_id, created_at, deleted_at)
            VALUES ($1, $2::uuid, $3, now(), NULL)
            ON CONFLICT (topic_id, atom_id) DO UPDATE SET deleted_at = NULL`,
			link.TopicID, link.AtomID, link.TenantID); err != nil {
			return fmt.Errorf("pg.TopicRepository.AttachAtom: %w", err)
		}
		return nil
	})
}

// ExistingRootNames returns the lowercased names of the tenant's active ROOT
// topic nodes (topic.SeedSink) so the backfill never re-creates one.
func (r *TopicRepository) ExistingRootNames(ctx context.Context, tenantID string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, qerr := tx.Query(ctx, `
            SELECT DISTINCT lower(name) FROM topic_nodes
            WHERE tenant_id = $1 AND parent_id IS NULL AND deleted_at IS NULL`, tenantID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if serr := rows.Scan(&name); serr != nil {
				return serr
			}
			out[name] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("pg.TopicRepository.ExistingRootNames: %w", err)
	}
	return out, nil
}

// ancestorChain returns startID + its ancestors up to a root (inclusive), active
// nodes only, via a recursive CTE. Empty when startID does not exist for the
// tenant. Bounded by a depth guard against a pre-existing cycle.
func ancestorChain(ctx context.Context, tx Tx, tenantID, startID string) ([]string, error) {
	const sql = `
        WITH RECURSIVE chain AS (
            SELECT topic_id, parent_id, 1 AS depth
            FROM topic_nodes
            WHERE topic_id = $1::uuid AND tenant_id = $2 AND deleted_at IS NULL
          UNION ALL
            SELECT t.topic_id, t.parent_id, c.depth + 1
            FROM topic_nodes t
            JOIN chain c ON t.topic_id = c.parent_id
            WHERE t.tenant_id = $2 AND t.deleted_at IS NULL AND c.depth < 10000
        )
        SELECT topic_id FROM chain`
	rows, err := tx.Query(ctx, sql, startID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chain []string
	for rows.Next() {
		var id string
		if serr := rows.Scan(&id); serr != nil {
			return nil, serr
		}
		chain = append(chain, id)
	}
	return chain, rows.Err()
}

// scanTopicNode maps a Row/Rows to *topic.TopicNode.
func scanTopicNode(s interface{ Scan(...any) error }) (*topic.TopicNode, error) {
	var (
		n         topic.TopicNode
		parent    string
		deletedAt *time.Time
	)
	if err := s.Scan(
		&n.TopicID, &n.TenantID, &n.Name, &parent, &n.SortOrder,
		&n.CreatedAt, &n.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if parent != "" {
		p := parent
		n.ParentID = &p
	}
	if deletedAt != nil {
		t := deletedAt.UTC()
		n.DeletedAt = &t
	}
	return &n, nil
}

// parentArg maps a *string parent id to the SQL text arg ("" ⇒ NULL via NULLIF).
func parentArg(parentID *string) string {
	if parentID == nil {
		return ""
	}
	return *parentID
}

// Compile-time checks: the repo satisfies both ports it is wired against.
var (
	_ topic.Repository = (*TopicRepository)(nil)
	_ topic.SeedSink   = (*TopicRepository)(nil)
)
