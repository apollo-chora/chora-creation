// repository.go — TopicNode persistence port (hexagonal: domain owns the
// interface; adapters in internal/adapter/pg implement it against Cloud SQL).
package topic

import "context"

// Repository is the TopicNode persistence port.
//
// RLS contract: topic_nodes is tenant-scoped (RLS keyed on chora.tenant_id,
// FORCE) and chora_creation_app_rw is NOBYPASSRLS, so every method runs inside a
// SET LOCAL chora.tenant_id transaction in the pg adapter (mirrors AtomRepository).
//
// Move + SoftDelete embed their read-modify-write in a single tenant-scoped
// transaction in the adapter — the multi-row cycle/children reads and the write
// stay consistent — while the cycle rule (TopicNode.Move) and the non-empty rule
// (TopicNode.SoftDelete) remain pure domain logic.
type Repository interface {
	// Create inserts a new node.
	Create(ctx context.Context, n *TopicNode) error

	// Update persists a node's mutable fields (name, parent_id, sort_order,
	// updated_at, deleted_at) after the aggregate has been mutated in-memory.
	Update(ctx context.Context, n *TopicNode) error

	// GetByID returns the active node for (tenant, id) or ErrNotFound.
	GetByID(ctx context.Context, tenantID, topicID string) (*TopicNode, error)

	// GetTree returns active nodes for a tenant as a flat slice. parentID nil ⇒
	// ALL of the tenant's nodes (the FE builds the tree); parentID set ⇒ only the
	// direct children of parentID.
	GetTree(ctx context.Context, tenantID string, parentID *string) ([]*TopicNode, error)

	// Move reparents topicID under newParentID (nil ⇒ root). The adapter loads
	// the node + the new parent's ancestry chain and calls TopicNode.Move so the
	// cycle guard fires; returns ErrCycle on a cyclic move, ErrNotFound if the
	// node or the target parent is missing. Atomic within one tenant tx.
	Move(ctx context.Context, tenantID, topicID string, newParentID *string) error

	// SoftDelete soft-deletes topicID. The adapter counts active children and
	// calls TopicNode.SoftDelete; returns ErrHasChildren for a non-empty node,
	// ErrNotFound if missing. Atomic within one tenant tx.
	SoftDelete(ctx context.Context, tenantID, topicID string) error

	// AttachAtom idempotently links an atom (by UUID) to a topic. The topic must
	// exist for the tenant (ErrNotFound otherwise). Atom-centric: the atom is
	// referenced, never owned (no cross-aggregate FK).
	AttachAtom(ctx context.Context, tenantID, topicID, atomID string) error
}
