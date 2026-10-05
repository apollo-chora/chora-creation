// Package golden_set_test exercises the golden-anchor + comparison domain.
//
// Use case: an instructor curates a small set of "golden" atoms that
// represent the platform's reference quality bar. New atoms are compared
// against the closest anchor (by tag overlap + body lexical similarity)
// to produce a similarity score + verdict (above|near|below the bar).
package golden_set_test

import (
	"testing"

	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
)

const (
	tenantA   = "01970000-0000-7000-8000-000000000001"
	gcidA     = "01970000-0000-7000-9000-000000000001"
	atomA     = "01970000-0000-7000-aaaa-000000000001"
	atomB     = "01970000-0000-7000-aaaa-000000000002"
	atomC     = "01970000-0000-7000-aaaa-000000000003"
	revisionA = "01970000-0000-7000-bbbb-000000000001"
)

// -----------------------------------------------------------------------------
// Anchor constructor
// -----------------------------------------------------------------------------

func TestNewAnchor_AssignsUUIDv7(t *testing.T) {
	t.Parallel()
	a, err := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA,
		AtomID: atomA, Title: "ref atom", Body: "photosynthesis is the conversion of light into chemical energy",
		Tags: []string{"biology", "photosynthesis"}, QualityScore: 0.92,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.AnchorID == "" || len(a.AnchorID) != 36 || a.AnchorID[14] != '7' {
		t.Errorf("AnchorID = %q; want UUIDv7", a.AnchorID)
	}
}

func TestNewAnchor_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	cases := map[string]gs.NewAnchorParams{
		"missing tenant_id": {AtomID: atomA, Title: "t", Body: "b", QualityScore: 0.5, CreatedBy: gcidA},
		"missing atom_id":   {TenantID: tenantA, Title: "t", Body: "b", QualityScore: 0.5, CreatedBy: gcidA},
		"missing title":     {TenantID: tenantA, AtomID: atomA, Body: "b", QualityScore: 0.5, CreatedBy: gcidA},
		"missing body":      {TenantID: tenantA, AtomID: atomA, Title: "t", QualityScore: 0.5, CreatedBy: gcidA},
		"missing creator":   {TenantID: tenantA, AtomID: atomA, Title: "t", Body: "b", QualityScore: 0.5},
	}
	for name, p := range cases {
		_, err := gs.NewAnchor(p)
		if err == nil {
			t.Errorf("%s: expected error; got nil", name)
		}
	}
}

func TestNewAnchor_RejectsQualityOutOfRange(t *testing.T) {
	t.Parallel()
	_, err := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA,
		AtomID: atomA, Title: "t", Body: "b", QualityScore: 1.1,
	})
	if err == nil {
		t.Errorf("expected error for quality > 1; got nil")
	}
	_, err = gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA,
		AtomID: atomA, Title: "t", Body: "b", QualityScore: -0.1,
	})
	if err == nil {
		t.Errorf("expected error for quality < 0; got nil")
	}
}

// -----------------------------------------------------------------------------
// Compare — happy path
// -----------------------------------------------------------------------------

func TestCompare_HappyPathReturnsScore(t *testing.T) {
	t.Parallel()
	anchor, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "Photosynthesis basics", Body: "plants convert light into chemical energy",
		Tags: []string{"biology", "plant"}, QualityScore: 0.9,
	})
	cmp, err := gs.Compare(gs.CompareParams{
		TenantID:           tenantA,
		ComparedAtomID:     atomB,
		ComparedRevisionID: revisionA,
		ComparedBody:       "plants convert light energy into chemical energy through chlorophyll",
		ComparedTags:       []string{"biology", "chlorophyll"},
		Anchor:             anchor,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cmp.SimilarityScore <= 0 || cmp.SimilarityScore > 1 {
		t.Errorf("similarity = %f; want (0,1]", cmp.SimilarityScore)
	}
	if cmp.AnchorID != anchor.AnchorID {
		t.Errorf("anchor_id mismatch")
	}
}

func TestCompare_VerdictAboveBar(t *testing.T) {
	t.Parallel()
	anchor, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "ref", Body: "alpha beta gamma delta",
		Tags: []string{"x"}, QualityScore: 0.5,
	})
	// Identical body + tags -> highest possible similarity
	cmp, err := gs.Compare(gs.CompareParams{
		TenantID: tenantA, ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "alpha beta gamma delta", ComparedTags: []string{"x"},
		Anchor: anchor,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cmp.Verdict != gs.VerdictAboveBar {
		t.Errorf("verdict = %q; want above_bar (identical content); score=%f", cmp.Verdict, cmp.SimilarityScore)
	}
}

func TestCompare_VerdictBelowBar(t *testing.T) {
	t.Parallel()
	anchor, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "ref", Body: "alpha beta gamma delta epsilon zeta eta theta iota kappa",
		Tags: []string{"biology"}, QualityScore: 0.9,
	})
	cmp, _ := gs.Compare(gs.CompareParams{
		TenantID: tenantA, ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "totally unrelated content about pizza and cooking",
		ComparedTags: []string{"food"},
		Anchor:       anchor,
	})
	if cmp.Verdict != gs.VerdictBelowBar {
		t.Errorf("verdict = %q; want below_bar; score=%f", cmp.Verdict, cmp.SimilarityScore)
	}
}

func TestCompare_RejectsNilAnchor(t *testing.T) {
	t.Parallel()
	_, err := gs.Compare(gs.CompareParams{
		TenantID: tenantA, ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "x",
	})
	if err == nil {
		t.Errorf("expected error for nil anchor; got nil")
	}
}

func TestCompare_RejectsCrossTenant(t *testing.T) {
	t.Parallel()
	anchor, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.5,
	})
	_, err := gs.Compare(gs.CompareParams{
		TenantID:       "01970000-0000-7000-8000-000000000099",
		ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "b",
		Anchor:       anchor,
	})
	if err == nil {
		t.Errorf("expected error for cross-tenant compare; got nil")
	}
}

func TestCompare_RejectsMissingComparedBody(t *testing.T) {
	t.Parallel()
	anchor, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.5,
	})
	_, err := gs.Compare(gs.CompareParams{
		TenantID: tenantA, ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "  ",
		Anchor:       anchor,
	})
	if err == nil {
		t.Errorf("expected error for missing body; got nil")
	}
}

// -----------------------------------------------------------------------------
// Anchor soft-delete
// -----------------------------------------------------------------------------

func TestAnchor_SoftDelete(t *testing.T) {
	t.Parallel()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.5,
	})
	if !a.IsActive() {
		t.Errorf("IsActive() = false on fresh anchor")
	}
	a.SoftDelete()
	if a.IsActive() {
		t.Errorf("IsActive() = true after SoftDelete")
	}
	if a.DeletedAt == nil {
		t.Errorf("DeletedAt = nil after SoftDelete")
	}
}

func TestAnchor_SoftDeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.5,
	})
	a.SoftDelete()
	first := *a.DeletedAt
	a.SoftDelete()
	if !a.DeletedAt.Equal(first) {
		t.Errorf("idempotency violation: first=%v second=%v", first, *a.DeletedAt)
	}
}
