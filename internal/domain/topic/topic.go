// Package topic is the TopicNode aggregate for chora-creation's content
// topic-tree feature (CHO-2275, Sub-phase A).
//
// Domain model:
//   - Each TopicNode is an aggregate ROOT. The tree is a projection over the flat
//     set of nodes (parent_id references another node of the same type — a UUID
//     reference, no FK), NOT one loaded aggregate. Repositories return a flat
//     slice; the FE builds the tree client-side.
//   - Atom-centric (SP-02): a topic REFERENCES atoms by UUID via TopicAtomLink;
//     it never OWNS an atom. Topics/collections query atoms; atoms stay unowned.
//   - Soft-delete only (ddd-enforcement #4): DeletedAt, default queries filter
//     WHERE deleted_at IS NULL. Delete of a node with active children is REFUSED
//     — children are separate aggregate roots, and a cascade across aggregates is
//     forbidden; the admin reparents/deletes children first.
//
// Pure domain — no infrastructure imports (hexagonal). google/uuid is a value
// primitive used exactly as the atom + collection aggregates mint UUIDv7 ids.
package topic

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxNameLen bounds a topic node label. Topic names are short labels
// ("Fractions", "Number Sense"), never prose — an overlong name is a fail-loud
// validation error (mirrors atom.MaxTopicTagLen's intent).
const MaxNameLen = 128

// Sentinel errors (hexagonal: domain owns them; adapters map to HTTP/gRPC).
var (
	// ErrNotFound is the canonical missing-or-soft-deleted sentinel.
	ErrNotFound = errors.New("topic: node not found")
	// ErrTenantRequired — a node MUST be tenant-scoped.
	ErrTenantRequired = errors.New("topic: tenant_id required")
	// ErrNameRequired — the label normalises to empty.
	ErrNameRequired = errors.New("topic: name required")
	// ErrNameTooLong — the label exceeds MaxNameLen.
	ErrNameTooLong = errors.New("topic: name too long")
	// ErrInvalidParent — parent id provided but blank/whitespace.
	ErrInvalidParent = errors.New("topic: invalid parent id")
	// ErrInvalidSortOrder — negative sort order.
	ErrInvalidSortOrder = errors.New("topic: sort order must be >= 0")
	// ErrCycle — a Move would make a node its own ancestor/descendant.
	ErrCycle = errors.New("topic: move would create a cycle")
	// ErrHasChildren — refuse to soft-delete a node with active children.
	ErrHasChildren = errors.New("topic: node has active children")
	// ErrDeleted — mutation attempted on a soft-deleted node.
	ErrDeleted = errors.New("topic: node is soft-deleted")
	// ErrInvalidAtomID — attach called with an empty/blank atom id.
	ErrInvalidAtomID = errors.New("topic: invalid atom id")
)

// TopicNode is the aggregate root: one node in a tenant's topic taxonomy.
type TopicNode struct {
	TopicID   string
	TenantID  string
	Name      string
	ParentID  *string // nil = root
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewTopicNode mints a new root/child node with a fresh UUIDv7 id.
func NewTopicNode(tenantID, name string, parentID *string, sortOrder int) (*TopicNode, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	cleanName, err := normalizeName(name)
	if err != nil {
		return nil, err
	}
	if sortOrder < 0 {
		return nil, ErrInvalidSortOrder
	}
	cleanParent, err := normalizeParent(parentID)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("topic: mint id: %w", err)
	}
	now := time.Now().UTC()
	return &TopicNode{
		TopicID:   id.String(),
		TenantID:  tenantID,
		Name:      cleanName,
		ParentID:  cleanParent,
		SortOrder: sortOrder,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Rename replaces the label. Refuses a soft-deleted node; leaves the node
// untouched on a validation failure.
func (n *TopicNode) Rename(name string) error {
	if n.DeletedAt != nil {
		return ErrDeleted
	}
	cleanName, err := normalizeName(name)
	if err != nil {
		return err
	}
	n.Name = cleanName
	n.touch()
	return nil
}

// SetSortOrder updates the sibling ordering key.
func (n *TopicNode) SetSortOrder(order int) error {
	if n.DeletedAt != nil {
		return ErrDeleted
	}
	if order < 0 {
		return ErrInvalidSortOrder
	}
	n.SortOrder = order
	n.touch()
	return nil
}

// Move reparents the node. newParentChain is the ancestry path of the NEW parent
// INCLUDING the new parent itself (newParent, newParent.parent, … up to a root).
// The move is a cycle iff this node's own id appears in that chain — the node
// would become its own ancestor (i.e. the target is one of the node's own
// descendants). A nil newParentID promotes the node to a root.
//
// The chain is supplied by the repository (which walks the tree); the cycle rule
// itself is pure + unit-tested here.
func (n *TopicNode) Move(newParentID *string, newParentChain []string) error {
	if n.DeletedAt != nil {
		return ErrDeleted
	}
	cleanParent, err := normalizeParent(newParentID)
	if err != nil {
		return err
	}
	if cleanParent == nil {
		n.ParentID = nil
		n.touch()
		return nil
	}
	if *cleanParent == n.TopicID {
		return ErrCycle
	}
	for _, ancestor := range newParentChain {
		if ancestor == n.TopicID {
			return ErrCycle
		}
	}
	n.ParentID = cleanParent
	n.touch()
	return nil
}

// SoftDelete marks the node deleted. Refuses when the node still has active
// children (hasActiveChildren) — children are separate aggregate roots and a
// cascade across aggregates is forbidden; the admin reparents/deletes them
// first. Idempotent: re-deleting an already-deleted node is a no-op.
func (n *TopicNode) SoftDelete(hasActiveChildren bool) error {
	if n.DeletedAt != nil {
		return nil
	}
	if hasActiveChildren {
		return ErrHasChildren
	}
	now := time.Now().UTC()
	n.DeletedAt = &now
	n.UpdatedAt = now
	return nil
}

// IsDeleted reports whether the node is soft-deleted.
func (n *TopicNode) IsDeleted() bool { return n.DeletedAt != nil }

func (n *TopicNode) touch() { n.UpdatedAt = time.Now().UTC() }

// TopicAtomLink is the atom-centric attach record: the topic REFERENCES an atom
// by UUID. No FK to learning_atoms — cross-aggregate refs are validated by the
// domain, never enforced with a database FK (ddd-enforcement #3).
type TopicAtomLink struct {
	TenantID  string
	TopicID   string
	AtomID    string
	CreatedAt time.Time
}

// NewTopicAtomLink validates + builds an attach record.
func NewTopicAtomLink(tenantID, topicID, atomID string) (*TopicAtomLink, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	if strings.TrimSpace(topicID) == "" {
		return nil, ErrNotFound
	}
	if strings.TrimSpace(atomID) == "" {
		return nil, ErrInvalidAtomID
	}
	return &TopicAtomLink{
		TenantID:  tenantID,
		TopicID:   topicID,
		AtomID:    atomID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// normalizeName trims + validates a topic label.
func normalizeName(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", ErrNameRequired
	}
	if len(t) > MaxNameLen {
		return "", ErrNameTooLong
	}
	return t, nil
}

// normalizeParent trims a provided parent id; nil stays nil (root). A provided
// but blank parent is a fail-loud error rather than a silent promotion to root.
func normalizeParent(parentID *string) (*string, error) {
	if parentID == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*parentID)
	if t == "" {
		return nil, ErrInvalidParent
	}
	return &t, nil
}
