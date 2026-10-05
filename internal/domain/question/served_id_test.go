// served_id_test.go — CHO-2272 (SECURITY): the LEARNER-facing atom MCQ option id
// and order are DERIVED from display content at serve, so no stored id (opt_1 /
// opt_correct) and no stored order (correct-first) can reach a learner. Because
// atom grading is cross-domain and label-free downstream, creation also derives
// the correct option's key — served == emitted == graded. Mirrors the campaign
// lane's served_id.go (CHO-2244); atom options carry only a label, so no
// question-index separator is needed.
package question_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func biasedMCQ() *question.MCQPayload {
	// The live legacy shape: answer typed FIRST, ids name/position the key.
	return &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "opt_correct", Label: "Paris", IsCorrect: true, Explainer: "yes"},
		{OptionID: "opt_1", Label: "London", IsCorrect: false, Explainer: "no"},
		{OptionID: "opt_2", Label: "Rome", IsCorrect: false},
		{OptionID: "opt_3", Label: "Berlin", IsCorrect: false},
	}}
}

func TestServedLearnerOptions_StripsRekeysAndDecorrelatesOrder(t *testing.T) {
	p := biasedMCQ()
	got, err := p.ServedLearnerOptions()
	if err != nil {
		t.Fatalf("ServedLearnerOptions: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d options; want 4", len(got))
	}
	for _, o := range got {
		// No answer signal survives in the served id.
		low := strings.ToLower(o.OptionID)
		if strings.Contains(low, "correct") || strings.Contains(low, "distractor") || strings.HasPrefix(low, "opt_") {
			t.Errorf("served id %q still carries stored-id signal", o.OptionID)
		}
		// Learner-safe: no IsCorrect, no Explainer leaks through the copy.
		if o.IsCorrect {
			t.Errorf("served option %q leaked is_correct=true", o.OptionID)
		}
		if o.Explainer != "" {
			t.Errorf("served option %q leaked explainer %q", o.OptionID, o.Explainer)
		}
	}
	// Order is by served id (deterministic), NOT stored order — so "Paris"
	// (stored first, the answer) must not be guaranteed first. With these four
	// fixed labels the hash order is deterministic; assert it is not the stored
	// order (a real decorrelation), and that the labels are all preserved.
	servedLabels := []string{got[0].Label, got[1].Label, got[2].Label, got[3].Label}
	storedLabels := []string{"Paris", "London", "Rome", "Berlin"}
	same := true
	for i := range servedLabels {
		if servedLabels[i] != storedLabels[i] {
			same = false
			break
		}
	}
	if same {
		t.Errorf("served order equals stored order %v — the answer-first bias is not decorrelated", storedLabels)
	}
	// Every label survives (set equality).
	seen := map[string]bool{}
	for _, o := range got {
		seen[o.Label] = true
	}
	for _, l := range storedLabels {
		if !seen[l] {
			t.Errorf("label %q was dropped by the projection", l)
		}
	}
}

func TestServedLearnerOptions_IsDeterministic(t *testing.T) {
	a, err1 := biasedMCQ().ServedLearnerOptions()
	b, err2 := biasedMCQ().ServedLearnerOptions()
	if err1 != nil || err2 != nil {
		t.Fatalf("errs: %v %v", err1, err2)
	}
	for i := range a {
		if a[i].OptionID != b[i].OptionID || a[i].Label != b[i].Label {
			t.Errorf("non-deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

// The round-trip invariant: the key creation ships on atom.created.v1 MUST equal
// the served id the learner is handed for the correct option. If these ever
// disagree, every honest submission grades WRONG.
func TestServedCorrectOptionID_EqualsTheServedIDOfTheCorrectOption(t *testing.T) {
	p := biasedMCQ()
	served, err := p.ServedLearnerOptions()
	if err != nil {
		t.Fatalf("ServedLearnerOptions: %v", err)
	}
	key, err := p.ServedCorrectOptionID()
	if err != nil {
		t.Fatalf("ServedCorrectOptionID: %v", err)
	}
	// Find the served id whose slot is the correct label ("Paris").
	var wantID string
	for _, o := range served {
		if o.Label == "Paris" {
			wantID = o.OptionID
			break
		}
	}
	if wantID == "" {
		t.Fatalf("the correct label was not served")
	}
	if key != wantID {
		t.Errorf("ServedCorrectOptionID = %q; want the served id of the correct option %q — grading would break", key, wantID)
	}
	// And it must NOT be the stored id.
	if key == "opt_correct" {
		t.Errorf("ServedCorrectOptionID leaked the stored id")
	}
}

func TestServedOptions_FailLoudOnIdenticalDisplayText(t *testing.T) {
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "a", Label: "Same", IsCorrect: true},
		{OptionID: "b", Label: "Same", IsCorrect: false},
	}}
	if _, err := p.ServedLearnerOptions(); err == nil {
		t.Error("ServedLearnerOptions: want error on identical display text (ambiguous served id-space); got nil")
	}
	if _, err := p.ServedCorrectOptionID(); err == nil {
		t.Error("ServedCorrectOptionID: want error on identical display text; got nil")
	}
}

func TestServedOptions_FailLoudOnEmptyLabel(t *testing.T) {
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "a", Label: "  ", IsCorrect: true},
		{OptionID: "b", Label: "Real", IsCorrect: false},
	}}
	if _, err := p.ServedLearnerOptions(); err == nil {
		t.Error("ServedLearnerOptions: want error on an option with no display content; got nil")
	}
}

func TestServedCorrectOptionID_FailLoudWhenNoCorrect(t *testing.T) {
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "a", Label: "X", IsCorrect: false},
		{OptionID: "b", Label: "Y", IsCorrect: false},
	}}
	if _, err := p.ServedCorrectOptionID(); err == nil {
		t.Error("want error when no option is is_correct; got nil")
	}
}
