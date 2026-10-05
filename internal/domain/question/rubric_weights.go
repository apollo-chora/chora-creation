package question

import (
	"math"
	"sort"
)

// NormalizeWeightPercents converts fractional rubric weights (each in [0,1],
// summing to ~1.0 on the wire) into integer percents that sum to EXACTLY 100,
// using the largest-remainder (Hamilton) method.
//
// Why this exists
// ---------------
// OEPayload.Validate() (oe.go) enforces sum(WeightPercent) == 100 for
// deterministic float-free equality. Rounding each weight independently with
// int(w*100 + 0.5) does NOT preserve that sum: e.g. {0.333, 0.333, 0.334} →
// {33, 33, 33} = 99, so an otherwise-valid OE question bounced with HTTP 400 at
// accept. Largest-remainder floors every weight, then distributes the leftover
// (100 − Σfloor) one point at a time to the criteria with the biggest discarded
// fractional part — guaranteeing Σ == 100 while staying as close as possible to
// the author's intended proportions.
//
// Used by every wire→domain rubric mapping (the AI-Assist POST/PATCH save path
// in questions_handler.go AND the batch candidate normalizer) so all paths agree.
//
// Hexagonal: pure domain helper, stdlib only.
func NormalizeWeightPercents(weights []float64) []int {
	n := len(weights)
	if n == 0 {
		return nil
	}

	out := make([]int, n)
	remainders := make([]float64, n)
	sum := 0
	for i, w := range weights {
		scaled := w * 100
		f := int(math.Floor(scaled))
		if f < 0 {
			f = 0
			scaled = 0
		}
		out[i] = f
		remainders[i] = scaled - float64(f)
		sum += f
	}

	deficit := 100 - sum
	// Order indices by descending remainder (ties: lower index first) so the
	// distribution is deterministic.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return remainders[order[a]] > remainders[order[b]]
	})

	if deficit > 0 {
		// Hand out the leftover points to the largest remainders.
		for k := 0; k < deficit; k++ {
			out[order[k%n]]++
		}
	} else if deficit < 0 {
		// Weights summed to > 1.0 (wire slack): trim from the SMALLEST
		// remainders first, never below 0.
		need := -deficit
		for i := n - 1; i >= 0 && need > 0; i-- {
			idx := order[i]
			if out[idx] > 0 {
				out[idx]--
				need--
			}
		}
	}
	return out
}
