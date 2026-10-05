// Package quality_score models the 4-dimensional atom quality score.
//
// Dimensions (per docs/design/ux_expert_tooling.md):
//   - Clarity                — readability + structure
//   - PedagogicalSoundness   — learning-objective alignment
//   - Fairness               — bias / inclusion screen
//   - Accessibility          — WCAG-friendly content (alt text, plain language)
//
// Each dimension is a float in [0, 1]. Aggregate is a weighted mean; default
// weights (0.30 / 0.30 / 0.20 / 0.20) are tunable per QualityScore record.
//
// Append-only per .claude/rules/ddd-enforcement.md aggregate invariant #4 —
// each scoring run produces a new QualityScore record; never edit in place.
//
// Hexagonal: dependency-free w.r.t. infrastructure.
package quality_score

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Dimension enum
// -----------------------------------------------------------------------------

// Dimension labels the four scoring axes.
type Dimension string

const (
	DimensionClarity              Dimension = "clarity"
	DimensionPedagogicalSoundness Dimension = "pedagogical_soundness"
	DimensionFairness             Dimension = "fairness"
	DimensionAccessibility        Dimension = "accessibility"
)

// -----------------------------------------------------------------------------
// Verdict bucket
// -----------------------------------------------------------------------------

// Verdict is the qualitative bucket the aggregate score falls into.
type Verdict string

const (
	VerdictExcellent  Verdict = "excellent"
	VerdictAcceptable Verdict = "acceptable"
	VerdictNeedsWork  Verdict = "needs_work"
)

const (
	thresholdExcellent  = 0.85
	thresholdAcceptable = 0.55
)

// classifyVerdict maps aggregate score to Verdict.
func classifyVerdict(s float64) Verdict {
	switch {
	case s >= thresholdExcellent:
		return VerdictExcellent
	case s >= thresholdAcceptable:
		return VerdictAcceptable
	default:
		return VerdictNeedsWork
	}
}

// -----------------------------------------------------------------------------
// Weights
// -----------------------------------------------------------------------------

// Weights apply to each Dimension when computing the aggregate score. Must
// sum to ~1.0 (epsilon 1e-6 to absorb float drift).
type Weights struct {
	Clarity              float64 `json:"clarity"`
	PedagogicalSoundness float64 `json:"pedagogical_soundness"`
	Fairness             float64 `json:"fairness"`
	Accessibility        float64 `json:"accessibility"`
}

// DefaultWeights are the platform default weights (0.30/0.30/0.20/0.20).
func DefaultWeights() Weights {
	return Weights{
		Clarity:              0.30,
		PedagogicalSoundness: 0.30,
		Fairness:             0.20,
		Accessibility:        0.20,
	}
}

// Sum returns Clarity + PedagogicalSoundness + Fairness + Accessibility.
func (w Weights) Sum() float64 {
	return w.Clarity + w.PedagogicalSoundness + w.Fairness + w.Accessibility
}

// -----------------------------------------------------------------------------
// QualityScore aggregate
// -----------------------------------------------------------------------------

// QualityScore is one scoring snapshot for a single atom revision. Append-only:
// re-scoring an atom produces a new QualityScore record (new ScoreID).
type QualityScore struct {
	ScoreID              string    `json:"score_id"`
	TenantID             string    `json:"tenant_id"`
	AtomID               string    `json:"atom_id"`
	RevisionID           string    `json:"revision_id"`
	ComputedBy           string    `json:"computed_by_gcid"`
	Clarity              float64   `json:"clarity"`
	PedagogicalSoundness float64   `json:"pedagogical_soundness"`
	Fairness             float64   `json:"fairness"`
	Accessibility        float64   `json:"accessibility"`
	Weights              Weights   `json:"weights"`
	AggregateScore       float64   `json:"aggregate_score"`
	Verdict              Verdict   `json:"verdict"`
	ComputedAt           time.Time `json:"computed_at"`
}

// NewParams is the input to New.
type NewParams struct {
	TenantID             string
	AtomID               string
	RevisionID           string
	ComputedBy           string
	Clarity              float64
	PedagogicalSoundness float64
	Fairness             float64
	Accessibility        float64
	// Weights is optional — when nil, DefaultWeights() is used.
	Weights *Weights
}

// New computes a QualityScore record from the four dimension scores. Each
// dimension must be in [0, 1]; weights (when supplied) must sum to ~1.0.
// Dimension scores are rounded to 4 decimal places for stable storage.
func New(p NewParams) (*QualityScore, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("atom_id is required")
	}
	if strings.TrimSpace(p.RevisionID) == "" {
		return nil, errors.New("revision_id is required")
	}
	if strings.TrimSpace(p.ComputedBy) == "" {
		return nil, errors.New("computed_by is required")
	}
	if err := requireUnit(p.Clarity, "clarity"); err != nil {
		return nil, err
	}
	if err := requireUnit(p.PedagogicalSoundness, "pedagogical_soundness"); err != nil {
		return nil, err
	}
	if err := requireUnit(p.Fairness, "fairness"); err != nil {
		return nil, err
	}
	if err := requireUnit(p.Accessibility, "accessibility"); err != nil {
		return nil, err
	}

	w := DefaultWeights()
	if p.Weights != nil {
		w = *p.Weights
		if math.Abs(w.Sum()-1.0) > 1e-6 {
			return nil, fmt.Errorf("weights must sum to 1.0; got %f", w.Sum())
		}
		if w.Clarity < 0 || w.PedagogicalSoundness < 0 || w.Fairness < 0 || w.Accessibility < 0 {
			return nil, errors.New("weights must be non-negative")
		}
	}

	clarity := round4(p.Clarity)
	ped := round4(p.PedagogicalSoundness)
	fair := round4(p.Fairness)
	acc := round4(p.Accessibility)
	agg := round4(w.Clarity*clarity + w.PedagogicalSoundness*ped +
		w.Fairness*fair + w.Accessibility*acc)

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	return &QualityScore{
		ScoreID:              id.String(),
		TenantID:             p.TenantID,
		AtomID:               p.AtomID,
		RevisionID:           p.RevisionID,
		ComputedBy:           p.ComputedBy,
		Clarity:              clarity,
		PedagogicalSoundness: ped,
		Fairness:             fair,
		Accessibility:        acc,
		Weights:              w,
		AggregateScore:       agg,
		Verdict:              classifyVerdict(agg),
		ComputedAt:           time.Now().UTC(),
	}, nil
}

// WeakestDimension returns the Dimension with the lowest dimension score.
// Ties favour the dimension order: clarity > pedagogical > fairness > accessibility.
func (q *QualityScore) WeakestDimension() Dimension {
	type pair struct {
		d Dimension
		v float64
	}
	pairs := []pair{
		{DimensionClarity, q.Clarity},
		{DimensionPedagogicalSoundness, q.PedagogicalSoundness},
		{DimensionFairness, q.Fairness},
		{DimensionAccessibility, q.Accessibility},
	}
	weakest := pairs[0]
	for _, p := range pairs[1:] {
		if p.v < weakest.v {
			weakest = p
		}
	}
	return weakest.d
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// requireUnit verifies x is in [0, 1].
func requireUnit(x float64, field string) error {
	if math.IsNaN(x) {
		return fmt.Errorf("%s must not be NaN", field)
	}
	if x < 0 || x > 1 {
		return fmt.Errorf("%s out of range [0,1]: %f", field, x)
	}
	return nil
}

// round4 rounds x to 4 decimal places using IEEE 754 round-half-away-from-zero.
func round4(x float64) float64 {
	return math.Round(x*1e4) / 1e4
}
