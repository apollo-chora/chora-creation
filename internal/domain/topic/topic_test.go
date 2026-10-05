package topic

import (
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// NewTopicNode
// -----------------------------------------------------------------------------

func TestNewTopicNode_Valid(t *testing.T) {
	tenant := "00000000-0000-7000-8000-000000000001"
	n, err := NewTopicNode(tenant, "  Fractions  ", nil, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.TenantID != tenant {
		t.Errorf("tenant = %q, want %q", n.TenantID, tenant)
	}
	if n.Name != "Fractions" {
		t.Errorf("name = %q, want trimmed %q", n.Name, "Fractions")
	}
	if n.ParentID != nil {
		t.Errorf("parent = %v, want nil (root)", *n.ParentID)
	}
	if n.SortOrder != 3 {
		t.Errorf("sortOrder = %d, want 3", n.SortOrder)
	}
	if n.TopicID == "" {
		t.Error("TopicID must be minted (UUIDv7), got empty")
	}
	// UUIDv7 shape: 36 chars, version nibble '7'.
	if len(n.TopicID) != 36 || n.TopicID[14] != '7' {
		t.Errorf("TopicID %q is not a UUIDv7 (want len 36, version nibble 7)", n.TopicID)
	}
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		t.Error("timestamps must be set")
	}
	if n.DeletedAt != nil {
		t.Error("new node must not be soft-deleted")
	}
}

func TestNewTopicNode_WithParent(t *testing.T) {
	tenant := "00000000-0000-7000-8000-000000000001"
	parent := "00000000-0000-7000-8000-0000000000aa"
	n, err := NewTopicNode(tenant, "Numerators", &parent, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.ParentID == nil || *n.ParentID != parent {
		t.Errorf("parent = %v, want %q", n.ParentID, parent)
	}
}

func TestNewTopicNode_Rejects(t *testing.T) {
	tenant := "00000000-0000-7000-8000-000000000001"
	cases := []struct {
		name    string
		tenant  string
		topic   string
		parent  *string
		wantErr error
	}{
		{"empty tenant", "", "Fractions", nil, ErrTenantRequired},
		{"empty name", tenant, "", nil, ErrNameRequired},
		{"blank name", tenant, "    ", nil, ErrNameRequired},
		{"name too long", tenant, strings.Repeat("x", MaxNameLen+1), nil, ErrNameTooLong},
		{"blank parent", tenant, "Fractions", strptr("   "), ErrInvalidParent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTopicNode(tc.tenant, tc.topic, tc.parent, 0)
			if err != tc.wantErr {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Rename
// -----------------------------------------------------------------------------

func TestTopicNode_Rename(t *testing.T) {
	n := mustNode(t, "Old")
	before := n.UpdatedAt
	time.Sleep(time.Millisecond)
	if err := n.Rename("  New Name  "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Name != "New Name" {
		t.Errorf("name = %q, want %q", n.Name, "New Name")
	}
	if !n.UpdatedAt.After(before) {
		t.Error("Rename must bump UpdatedAt")
	}
}

func TestTopicNode_Rename_Rejects(t *testing.T) {
	n := mustNode(t, "Old")
	if err := n.Rename(""); err != ErrNameRequired {
		t.Errorf("empty: err = %v, want %v", err, ErrNameRequired)
	}
	if err := n.Rename(strings.Repeat("y", MaxNameLen+1)); err != ErrNameTooLong {
		t.Errorf("too long: err = %v, want %v", err, ErrNameTooLong)
	}
	if n.Name != "Old" {
		t.Errorf("name mutated on failed rename: %q", n.Name)
	}
}

func TestTopicNode_Rename_RefusesDeleted(t *testing.T) {
	n := mustNode(t, "Old")
	_ = n.SoftDelete(false)
	if err := n.Rename("New"); err != ErrDeleted {
		t.Errorf("err = %v, want %v", err, ErrDeleted)
	}
}

// -----------------------------------------------------------------------------
// SetSortOrder
// -----------------------------------------------------------------------------

func TestTopicNode_SetSortOrder(t *testing.T) {
	n := mustNode(t, "T")
	before := n.UpdatedAt
	time.Sleep(time.Millisecond)
	if err := n.SetSortOrder(9); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.SortOrder != 9 {
		t.Errorf("sortOrder = %d, want 9", n.SortOrder)
	}
	if !n.UpdatedAt.After(before) {
		t.Error("SetSortOrder must bump UpdatedAt")
	}
}

func TestTopicNode_SetSortOrder_Rejects(t *testing.T) {
	n := mustNode(t, "T")
	if err := n.SetSortOrder(-1); err != ErrInvalidSortOrder {
		t.Errorf("negative: err = %v, want %v", err, ErrInvalidSortOrder)
	}
	n2 := mustNode(t, "T2")
	_ = n2.SoftDelete(false)
	if err := n2.SetSortOrder(1); err != ErrDeleted {
		t.Errorf("deleted: err = %v, want %v", err, ErrDeleted)
	}
}

// -----------------------------------------------------------------------------
// Move — cycle prevention (the guard)
// -----------------------------------------------------------------------------

func TestTopicNode_Move_ToRoot(t *testing.T) {
	n := mustNode(t, "Child")
	p := "00000000-0000-7000-8000-0000000000aa"
	n.ParentID = &p
	if err := n.Move(nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.ParentID != nil {
		t.Errorf("parent = %v, want nil after move to root", *n.ParentID)
	}
}

func TestTopicNode_Move_Valid(t *testing.T) {
	n := mustNode(t, "Node")
	newParent := "00000000-0000-7000-8000-0000000000bb"
	// chain = [newParent, grandparent, root]; node id NOT present → no cycle.
	chain := []string{newParent, "00000000-0000-7000-8000-0000000000cc"}
	before := n.UpdatedAt
	time.Sleep(time.Millisecond)
	if err := n.Move(&newParent, chain); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.ParentID == nil || *n.ParentID != newParent {
		t.Errorf("parent = %v, want %q", n.ParentID, newParent)
	}
	if !n.UpdatedAt.After(before) {
		t.Error("Move must bump UpdatedAt")
	}
}

func TestTopicNode_Move_CycleToSelf(t *testing.T) {
	n := mustNode(t, "Node")
	// Moving under itself: new parent == node id.
	if err := n.Move(&n.TopicID, []string{n.TopicID}); err != ErrCycle {
		t.Errorf("err = %v, want %v", err, ErrCycle)
	}
}

func TestTopicNode_Move_CycleToDescendant(t *testing.T) {
	n := mustNode(t, "A") // A is the node being moved
	descendant := "00000000-0000-7000-8000-0000000000dd"
	// The new parent's ancestry chain contains A itself (A is an ancestor of the
	// target) → A would become its own descendant → cycle.
	chain := []string{descendant, "00000000-0000-7000-8000-0000000000ee", n.TopicID}
	if err := n.Move(&descendant, chain); err != ErrCycle {
		t.Errorf("err = %v, want %v", err, ErrCycle)
	}
	if n.ParentID != nil {
		t.Error("parent must not mutate on a rejected cyclic move")
	}
}

func TestTopicNode_Move_RefusesDeleted(t *testing.T) {
	n := mustNode(t, "Node")
	_ = n.SoftDelete(false)
	p := "00000000-0000-7000-8000-0000000000bb"
	if err := n.Move(&p, []string{p}); err != ErrDeleted {
		t.Errorf("err = %v, want %v", err, ErrDeleted)
	}
}

func TestTopicNode_Move_RejectsBlankParent(t *testing.T) {
	n := mustNode(t, "Node")
	blank := "   "
	if err := n.Move(&blank, nil); err != ErrInvalidParent {
		t.Errorf("err = %v, want %v", err, ErrInvalidParent)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete — non-empty guard
// -----------------------------------------------------------------------------

func TestTopicNode_SoftDelete_Empty(t *testing.T) {
	n := mustNode(t, "Leaf")
	if err := n.SoftDelete(false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.DeletedAt == nil {
		t.Error("DeletedAt must be set")
	}
	if !n.IsDeleted() {
		t.Error("IsDeleted must be true")
	}
}

func TestTopicNode_SoftDelete_ForbidsNonEmpty(t *testing.T) {
	n := mustNode(t, "Parent")
	if err := n.SoftDelete(true); err != ErrHasChildren {
		t.Errorf("err = %v, want %v", err, ErrHasChildren)
	}
	if n.DeletedAt != nil {
		t.Error("must not soft-delete a node with active children")
	}
}

func TestTopicNode_SoftDelete_Idempotent(t *testing.T) {
	n := mustNode(t, "Leaf")
	_ = n.SoftDelete(false)
	first := *n.DeletedAt
	if err := n.SoftDelete(false); err != nil {
		t.Fatalf("re-delete must be a no-op, got %v", err)
	}
	if !n.DeletedAt.Equal(first) {
		t.Error("re-delete must not change DeletedAt")
	}
}

// -----------------------------------------------------------------------------
// TopicAtomLink — atom-centric attach (topic references atom by UUID)
// -----------------------------------------------------------------------------

func TestNewTopicAtomLink_Valid(t *testing.T) {
	tenant := "00000000-0000-7000-8000-000000000001"
	topicID := "00000000-0000-7000-8000-0000000000aa"
	atomID := "00000000-0000-7000-8000-0000000000bb"
	l, err := NewTopicAtomLink(tenant, topicID, atomID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.TenantID != tenant || l.TopicID != topicID || l.AtomID != atomID {
		t.Errorf("link fields = %+v", l)
	}
	if l.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}
}

func TestNewTopicAtomLink_Rejects(t *testing.T) {
	tenant := "00000000-0000-7000-8000-000000000001"
	topicID := "00000000-0000-7000-8000-0000000000aa"
	atomID := "00000000-0000-7000-8000-0000000000bb"
	cases := []struct {
		name                string
		tenant, topic, atom string
		wantErr             error
	}{
		{"empty tenant", "", topicID, atomID, ErrTenantRequired},
		{"empty topic", tenant, "", atomID, ErrNotFound},
		{"empty atom", tenant, topicID, "", ErrInvalidAtomID},
		{"blank atom", tenant, topicID, "   ", ErrInvalidAtomID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTopicAtomLink(tc.tenant, tc.topic, tc.atom)
			if err != tc.wantErr {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func strptr(s string) *string { return &s }

func mustNode(t *testing.T, name string) *TopicNode {
	t.Helper()
	n, err := NewTopicNode("00000000-0000-7000-8000-000000000001", name, nil, 0)
	if err != nil {
		t.Fatalf("mustNode: %v", err)
	}
	return n
}
