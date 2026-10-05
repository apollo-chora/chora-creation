package question

import "testing"

// TestNormalizeWeightPercents locks the largest-remainder (Hamilton) conversion
// of fractional rubric weights (each in [0,1], summing to ~1.0 on the wire) to
// integer percents that ALWAYS sum to exactly 100 — the OEPayload.Validate()
// invariant (oe.go). Naive per-criterion round-to-nearest broke this for
// repeating decimals (e.g. {0.333,0.333,0.334} → {33,33,33}=99), which made an
// otherwise-valid OE question bounce with HTTP 400 at accept.
func TestNormalizeWeightPercents(t *testing.T) {
	cases := []struct {
		name    string
		weights []float64
		want    []int
	}{
		{"clean 40/30/30", []float64{0.4, 0.3, 0.3}, []int{40, 30, 30}},
		{"two halves", []float64{0.5, 0.5}, []int{50, 50}},
		{"repeating thirds", []float64{0.333, 0.333, 0.334}, nil},   // sum==100 asserted below
		{"exact thirds", []float64{1.0 / 3, 1.0 / 3, 1.0 / 3}, nil}, // sum==100
		{"four quarters", []float64{0.25, 0.25, 0.25, 0.25}, []int{25, 25, 25, 25}},
		{"ten tenths", []float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}, nil}, // sum==100
		{"single", []float64{1.0}, []int{100}},
		{"slightly over (rounding slack)", []float64{0.34, 0.33, 0.34}, nil}, // sum==100
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeWeightPercents(tc.weights)
			if len(got) != len(tc.weights) {
				t.Fatalf("len(got)=%d; want %d", len(got), len(tc.weights))
			}
			sum := 0
			for _, p := range got {
				if p < 0 {
					t.Errorf("negative percent %d in %v", p, got)
				}
				sum += p
			}
			if sum != 100 {
				t.Errorf("sum(%v) = %d; want 100", got, sum)
			}
			if tc.want != nil {
				for i := range tc.want {
					if got[i] != tc.want[i] {
						t.Errorf("got %v; want %v", got, tc.want)
						break
					}
				}
			}
		})
	}
}

func TestNormalizeWeightPercents_Empty(t *testing.T) {
	if got := NormalizeWeightPercents(nil); got != nil {
		t.Errorf("nil weights → %v; want nil", got)
	}
	if got := NormalizeWeightPercents([]float64{}); len(got) != 0 {
		t.Errorf("empty weights → %v; want empty", got)
	}
}

// TestNormalizeWeightPercents_RepeatingThirdsExact pins the headline regression:
// {0.333,0.333,0.334} must sum to 100 (largest remainder puts the +1 on 0.334).
func TestNormalizeWeightPercents_RepeatingThirdsExact(t *testing.T) {
	got := NormalizeWeightPercents([]float64{0.333, 0.333, 0.334})
	sum := got[0] + got[1] + got[2]
	if sum != 100 {
		t.Fatalf("sum %v = %d; want 100", got, sum)
	}
	// The largest fractional remainder (0.334*100=33.4 → .4 beats .3) gets the +1.
	if got[2] != 34 {
		t.Errorf("got %v; want the +1 on the largest remainder (index 2 = 34)", got)
	}
}
