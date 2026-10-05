package media

import "testing"

func TestComputeQualityGrade(t *testing.T) {
	tests := []struct {
		name      string
		blur      float64
		resOK     bool
		formatOK  bool
		wantGrade QualityGrade
	}{
		// Grade A: sharp + adequate res + valid format.
		{"sharp high-res valid", 0.9, true, true, QualityGradeA},
		{"exactly sharp threshold", 0.7, true, true, QualityGradeA},

		// Grade B: acceptable blur + adequate res + valid format.
		{"good blur", 0.6, true, true, QualityGradeB},
		{"exactly acceptable threshold", 0.5, true, true, QualityGradeB},

		// Grade C: passable blur + adequate res + valid format.
		{"passable blur", 0.4, true, true, QualityGradeC},
		{"exactly blurry threshold", 0.3, true, true, QualityGradeC},

		// Grade D: blurry or low resolution.
		{"blurry high-res", 0.1, true, true, QualityGradeD},
		{"zero blur high-res", 0.0, true, true, QualityGradeD},
		{"passable blur low-res", 0.4, false, true, QualityGradeD},

		// Grade F: invalid format.
		{"invalid format sharp", 0.9, true, false, QualityGradeF},
		{"invalid format blurry", 0.1, false, false, QualityGradeF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeQualityGrade(tt.blur, tt.resOK, tt.formatOK)
			if got != tt.wantGrade {
				t.Errorf("ComputeQualityGrade(%.2f, %v, %v) = %s, want %s",
					tt.blur, tt.resOK, tt.formatOK, got, tt.wantGrade)
			}
		})
	}
}

func TestComputeLLMReady(t *testing.T) {
	tests := []struct {
		name      string
		blur      float64
		resOK     bool
		formatOK  bool
		wantReady bool
	}{
		{"all good", 0.8, true, true, true},
		{"exactly at blur threshold", 0.3, true, true, true},
		{"just below blur threshold", 0.29, true, true, false},
		{"blurry", 0.1, true, true, false},
		{"low res", 0.8, false, true, false},
		{"invalid format", 0.8, true, false, false},
		{"all bad", 0.0, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeLLMReady(tt.blur, tt.resOK, tt.formatOK)
			if got != tt.wantReady {
				t.Errorf("ComputeLLMReady(%.2f, %v, %v) = %v, want %v",
					tt.blur, tt.resOK, tt.formatOK, got, tt.wantReady)
			}
		})
	}
}

func TestQualityGradeConstants(t *testing.T) {
	// Ensure grade constants are the expected string values.
	grades := map[QualityGrade]string{
		QualityGradeA: "A",
		QualityGradeB: "B",
		QualityGradeC: "C",
		QualityGradeD: "D",
		QualityGradeF: "F",
	}
	for grade, expected := range grades {
		if string(grade) != expected {
			t.Errorf("QualityGrade constant %q != expected %q", grade, expected)
		}
	}
}

func TestBlurThresholdOrdering(t *testing.T) {
	// Thresholds must be ordered: Blurry < Acceptable < Sharp.
	if BlurThresholdBlurry >= BlurThresholdAcceptable {
		t.Errorf("BlurThresholdBlurry (%.2f) >= BlurThresholdAcceptable (%.2f)",
			BlurThresholdBlurry, BlurThresholdAcceptable)
	}
	if BlurThresholdAcceptable >= BlurThresholdSharp {
		t.Errorf("BlurThresholdAcceptable (%.2f) >= BlurThresholdSharp (%.2f)",
			BlurThresholdAcceptable, BlurThresholdSharp)
	}
}
