// Package version_diff_test exercises the atom-revision diff service.
//
// Use case: instructors review what changed between two revisions of an
// atom (for code review, peer review, audit trail). Diff is line-level,
// returning insertions / deletions / unchanged hunks.
package version_diff_test

import (
	"testing"

	vd "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/version_diff"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	atomA   = "01970000-0000-7000-aaaa-000000000001"
	revA    = "01970000-0000-7000-bbbb-000000000001"
	revB    = "01970000-0000-7000-bbbb-000000000002"
)

// -----------------------------------------------------------------------------
// Compute
// -----------------------------------------------------------------------------

func TestCompute_IdenticalRevisionsAreAllUnchanged(t *testing.T) {
	t.Parallel()
	d, err := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "alpha\nbeta\ngamma",
		HeadRevisionID: revB, HeadBody: "alpha\nbeta\ngamma",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if d.AdditionsCount != 0 || d.DeletionsCount != 0 {
		t.Errorf("identical bodies produced changes: +%d -%d", d.AdditionsCount, d.DeletionsCount)
	}
	for _, h := range d.Hunks {
		if h.Op != vd.OpUnchanged {
			t.Errorf("unexpected hunk op for identical bodies: %q", h.Op)
		}
	}
}

func TestCompute_PureInsertionAtEnd(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "alpha\nbeta",
		HeadRevisionID: revB, HeadBody: "alpha\nbeta\ngamma",
	})
	if d.AdditionsCount != 1 {
		t.Errorf("additions = %d; want 1", d.AdditionsCount)
	}
	if d.DeletionsCount != 0 {
		t.Errorf("deletions = %d; want 0", d.DeletionsCount)
	}
}

func TestCompute_PureDeletionAtEnd(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "alpha\nbeta\ngamma",
		HeadRevisionID: revB, HeadBody: "alpha\nbeta",
	})
	if d.DeletionsCount != 1 {
		t.Errorf("deletions = %d; want 1", d.DeletionsCount)
	}
	if d.AdditionsCount != 0 {
		t.Errorf("additions = %d; want 0", d.AdditionsCount)
	}
}

func TestCompute_ReplacementMixed(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "alpha\nbeta\ngamma",
		HeadRevisionID: revB, HeadBody: "alpha\ndelta\ngamma",
	})
	// LCS-based diff sees beta as deletion and delta as insertion (line replace).
	if d.AdditionsCount != 1 || d.DeletionsCount != 1 {
		t.Errorf("replace: +%d -%d; want +1 -1", d.AdditionsCount, d.DeletionsCount)
	}
}

func TestCompute_RejectsSameRevisionIDs(t *testing.T) {
	t.Parallel()
	_, err := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "alpha",
		HeadRevisionID: revA, HeadBody: "alpha",
	})
	if err == nil {
		t.Errorf("expected error for same base+head revision; got nil")
	}
}

func TestCompute_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	_, err := vd.Compute(vd.ComputeParams{
		AtomID:         atomA,
		BaseRevisionID: revA, BaseBody: "alpha",
		HeadRevisionID: revB, HeadBody: "beta",
	})
	if err == nil {
		t.Errorf("expected error for missing tenant; got nil")
	}
}

func TestCompute_RejectsMissingAtomID(t *testing.T) {
	t.Parallel()
	_, err := vd.Compute(vd.ComputeParams{
		TenantID:       tenantA,
		BaseRevisionID: revA, BaseBody: "alpha",
		HeadRevisionID: revB, HeadBody: "beta",
	})
	if err == nil {
		t.Errorf("expected error for missing atom_id; got nil")
	}
}

func TestCompute_RejectsBodyTooLarge(t *testing.T) {
	t.Parallel()
	huge := make([]byte, vd.MaxBodyBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	_, err := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: string(huge),
		HeadRevisionID: revB, HeadBody: "x",
	})
	if err == nil {
		t.Errorf("expected error for oversized body; got nil")
	}
}

// -----------------------------------------------------------------------------
// Hunk shape + ordering
// -----------------------------------------------------------------------------

func TestCompute_HunkOpsAreValid(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "a\nb\nc",
		HeadRevisionID: revB, HeadBody: "a\nx\nc",
	})
	for _, h := range d.Hunks {
		switch h.Op {
		case vd.OpUnchanged, vd.OpInsert, vd.OpDelete:
			// ok
		default:
			t.Errorf("unknown op %q", h.Op)
		}
	}
}

func TestCompute_RevisionIDsRoundTrip(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "a",
		HeadRevisionID: revB, HeadBody: "b",
	})
	if d.BaseRevisionID != revA || d.HeadRevisionID != revB {
		t.Errorf("revision ids did not roundtrip: %+v", d)
	}
}

func TestCompute_AssignsDiffID(t *testing.T) {
	t.Parallel()
	d, _ := vd.Compute(vd.ComputeParams{
		TenantID: tenantA, AtomID: atomA,
		BaseRevisionID: revA, BaseBody: "a",
		HeadRevisionID: revB, HeadBody: "b",
	})
	if d.DiffID == "" || len(d.DiffID) != 36 || d.DiffID[14] != '7' {
		t.Errorf("DiffID = %q; want UUIDv7", d.DiffID)
	}
}
