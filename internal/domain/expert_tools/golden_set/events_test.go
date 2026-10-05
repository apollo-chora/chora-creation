// Tests for golden-set event payload builders.
package golden_set_test

import (
	"strings"
	"testing"

	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
)

func TestNewAnchorCreatedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.9,
	})
	p := gs.NewAnchorCreatedPayload(a)
	if p.AnchorID != a.AnchorID || p.QualityScore != 0.9 {
		t.Errorf("payload mismatch: %+v", p)
	}
	if !strings.HasPrefix(p.OccurredAt, "20") {
		t.Errorf("occurred_at not RFC3339: %q", p.OccurredAt)
	}
}

func TestNewComparedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "alpha beta", QualityScore: 0.9,
	})
	c, _ := gs.Compare(gs.CompareParams{
		TenantID: tenantA, ComparedAtomID: atomB, ComparedRevisionID: revisionA,
		ComparedBody: "alpha gamma", Anchor: a,
	})
	p := gs.NewComparedPayload(tenantA, c)
	if p.ComparedAtomID != atomB || p.AnchorID != a.AnchorID {
		t.Errorf("payload mismatch: %+v", p)
	}
	if p.SimilarityScore != c.SimilarityScore {
		t.Errorf("similarity not roundtripped")
	}
}
