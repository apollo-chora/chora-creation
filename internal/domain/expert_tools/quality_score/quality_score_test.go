// Package quality_score_test exercises the 4-dimensional atom quality score.
//
// Use case (per docs/design/ux_expert_tooling.md): instructors get a
// machine-computed score across four dimensions before publishing — clarity,
// pedagogical-soundness, fairness, accessibility — each in [0,1]. Aggregate
// score is the weighted mean (defaults: 0.30 / 0.30 / 0.20 / 0.20). The
// score is appended (append-only history) to the atom for audit replay
// per ddd-enforcement aggregate invariant #4.
package quality_score_test

import (
	"testing"

	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
)

const (
	tenantA   = "01970000-0000-7000-8000-000000000001"
	gcidA     = "01970000-0000-7000-9000-000000000001"
	atomA     = "01970000-0000-7000-aaaa-000000000001"
	revisionA = "01970000-0000-7000-bbbb-000000000001"
)

// -----------------------------------------------------------------------------
// Constructor invariants
// -----------------------------------------------------------------------------

func TestNew_ComputesAggregateScore(t *testing.T) {
	t.Parallel()
	s, err := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.8, PedagogicalSoundness: 0.9, Fairness: 0.7, Accessibility: 0.6,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := 0.30*0.8 + 0.30*0.9 + 0.20*0.7 + 0.20*0.6
	if !approxEqual(s.AggregateScore, want, 1e-9) {
		t.Errorf("aggregate = %f; want %f", s.AggregateScore, want)
	}
}

func TestNew_AssignsUUIDv7ScoreID(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5,
	})
	if s.ScoreID == "" || len(s.ScoreID) != 36 || s.ScoreID[14] != '7' {
		t.Errorf("ScoreID = %q; want UUIDv7", s.ScoreID)
	}
}

func TestNew_RejectsClarityOutOfRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    qs.NewParams
	}{
		{"clarity > 1", qs.NewParams{TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 1.1, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5}},
		{"pedagogical < 0", qs.NewParams{TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: -0.1, Fairness: 0.5, Accessibility: 0.5}},
		{"fairness > 1", qs.NewParams{TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 1.5, Accessibility: 0.5}},
		{"accessibility < 0", qs.NewParams{TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: -0.5}},
	}
	for _, c := range cases {
		_, err := qs.New(c.p)
		if err == nil {
			t.Errorf("%s: expected error; got nil", c.name)
		}
	}
}

func TestNew_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	cases := map[string]qs.NewParams{
		"missing tenant_id": {AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5},
		"missing atom_id": {TenantID: tenantA, RevisionID: revisionA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5},
		"missing revision_id": {TenantID: tenantA, AtomID: atomA, ComputedBy: gcidA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5},
		"missing computed_by": {TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
			Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5},
	}
	for name, p := range cases {
		_, err := qs.New(p)
		if err == nil {
			t.Errorf("%s: expected error; got nil", name)
		}
	}
}

func TestNew_AcceptsCustomWeights(t *testing.T) {
	t.Parallel()
	s, err := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 1.0, PedagogicalSoundness: 0.0, Fairness: 0.0, Accessibility: 0.0,
		Weights: &qs.Weights{Clarity: 0.5, PedagogicalSoundness: 0.2, Fairness: 0.2, Accessibility: 0.1},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !approxEqual(s.AggregateScore, 0.5, 1e-9) {
		t.Errorf("aggregate = %f; want 0.5", s.AggregateScore)
	}
}

func TestNew_RejectsWeightsNotSummingToOne(t *testing.T) {
	t.Parallel()
	_, err := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.5, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5,
		Weights: &qs.Weights{Clarity: 0.1, PedagogicalSoundness: 0.1, Fairness: 0.1, Accessibility: 0.1},
	})
	if err == nil {
		t.Errorf("expected error for weights not summing to 1; got nil")
	}
}

// -----------------------------------------------------------------------------
// Verdict bucket
// -----------------------------------------------------------------------------

func TestVerdict_Excellent(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.95, PedagogicalSoundness: 0.95, Fairness: 0.95, Accessibility: 0.95,
	})
	if s.Verdict != qs.VerdictExcellent {
		t.Errorf("verdict = %q; want excellent for high score", s.Verdict)
	}
}

func TestVerdict_NeedsWork(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.2, PedagogicalSoundness: 0.2, Fairness: 0.2, Accessibility: 0.2,
	})
	if s.Verdict != qs.VerdictNeedsWork {
		t.Errorf("verdict = %q; want needs_work for low score", s.Verdict)
	}
}

func TestVerdict_Acceptable(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.6, PedagogicalSoundness: 0.6, Fairness: 0.6, Accessibility: 0.6,
	})
	if s.Verdict != qs.VerdictAcceptable {
		t.Errorf("verdict = %q; want acceptable for mid score", s.Verdict)
	}
}

// -----------------------------------------------------------------------------
// Find weakest dimension
// -----------------------------------------------------------------------------

func TestWeakestDimension_FindsLowest(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.9, PedagogicalSoundness: 0.5, Fairness: 0.3, Accessibility: 0.7,
	})
	if s.WeakestDimension() != qs.DimensionFairness {
		t.Errorf("weakest = %q; want fairness", s.WeakestDimension())
	}
}

// -----------------------------------------------------------------------------
// Apply normalisation (clamp + round)
// -----------------------------------------------------------------------------

func TestNew_RoundsToFourDecimalPlaces(t *testing.T) {
	t.Parallel()
	s, _ := qs.New(qs.NewParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, ComputedBy: gcidA,
		Clarity: 0.123456789, PedagogicalSoundness: 0.5, Fairness: 0.5, Accessibility: 0.5,
	})
	if s.Clarity != 0.1235 { // banker's-style 4dp round expected
		t.Errorf("clarity not rounded to 4dp: %v", s.Clarity)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func approxEqual(a, b, eps float64) bool {
	if a > b {
		return (a - b) <= eps
	}
	return (b - a) <= eps
}
