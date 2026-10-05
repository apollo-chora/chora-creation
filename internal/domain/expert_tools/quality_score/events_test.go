// Tests for quality-score event payload builders.
package quality_score_test

import (
	"strings"
	"testing"

	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
)

func TestNewComputedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.8, PedagogicalSoundness: 0.8, Fairness: 0.8, Accessibility: 0.8,
	})
	p := qs.NewComputedPayload(s)
	if p.ScoreID != s.ScoreID || p.AggregateScore != s.AggregateScore {
		t.Errorf("payload mismatch: %+v", p)
	}
	if p.Verdict != string(s.Verdict) {
		t.Errorf("verdict mismatch")
	}
	if !strings.HasPrefix(p.OccurredAt, "20") {
		t.Errorf("occurred_at not RFC3339: %q", p.OccurredAt)
	}
}
