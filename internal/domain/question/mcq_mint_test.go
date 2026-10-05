// mcq_mint_test.go — CHO-2255 (SECURITY): the stored option_id must carry no
// answer signal, whatever the caller supplied.
//
// Live evidence (2026-07-17, chora_creation): 7 of 187 MCQs carry ids that name
// the key — `opt_correct` with `opt_distractor_1..3` beside it — and a live
// probe of GET /api/atoms/{id} confirmed the learner receives them verbatim.
// The learner-safe projection worked perfectly (only option_id + label reached
// the wire, no is_correct, no explainer) and the answer walked out inside the id
// regardless: an allow-list on field NAMES cannot save you when the leak is a
// field VALUE.
package question_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// seqIDs returns a deterministic id source for assertions.
func seqIDs() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("minted-%d", n) }
}

func TestMintOptionIDs_ReplacesAnswerNamingIDs(t *testing.T) {
	// The exact live shape from atom 019ee545-…
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "opt_correct", Label: "Generation, Storage, Ingestion", IsCorrect: true, Explainer: "yes"},
		{OptionID: "opt_distractor_3", Label: "Generation, Extraction", IsCorrect: false},
		{OptionID: "opt_distractor_2", Label: "Ingestion, Transformation", IsCorrect: false},
		{OptionID: "opt_distractor_1", Label: "Security, Data management", IsCorrect: false},
	}}
	p.MintOptionIDs(seqIDs())

	for _, o := range p.Options {
		if strings.Contains(strings.ToLower(o.OptionID), "correct") ||
			strings.Contains(strings.ToLower(o.OptionID), "distractor") {
			t.Errorf("stored option id %q still names the answer key", o.OptionID)
		}
	}
	// The answer key itself must survive the re-key — fields move with their id.
	if got := p.CorrectOptionID(); got != "minted-1" {
		t.Errorf("CorrectOptionID = %q; want the minted id of the is_correct option", got)
	}
	// Display content untouched.
	if p.Options[0].Label != "Generation, Storage, Ingestion" {
		t.Errorf("minting disturbed the label: %q", p.Options[0].Label)
	}
}

// Unconditional: a "neutral-looking" id is re-keyed too. CHO-2244 proved the
// ids that look neutral (opt_1a / opt_1) encode POSITION, which correlates with
// the answer — so there is no shape worth trusting, and no inspection to do.
func TestMintOptionIDs_IsUnconditional(t *testing.T) {
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "opt_1a", Label: "A", IsCorrect: true},
		{OptionID: "opt_1b", Label: "B"},
	}}
	p.MintOptionIDs(seqIDs())
	for _, o := range p.Options {
		if strings.HasPrefix(o.OptionID, "opt_1") {
			t.Errorf("a caller-supplied id survived minting: %q — the naming is exactly what cannot be trusted", o.OptionID)
		}
	}
}

func TestMintOptionIDs_UniquePerOption(t *testing.T) {
	p := &question.MCQPayload{Options: []question.MCQOption{
		{OptionID: "a"}, {OptionID: "b"}, {OptionID: "c"}, {OptionID: "d"},
	}}
	p.MintOptionIDs(seqIDs())
	seen := map[string]bool{}
	for _, o := range p.Options {
		if o.OptionID == "" {
			t.Fatalf("minted a blank option id — an option with no identity cannot be graded")
		}
		if seen[o.OptionID] {
			t.Fatalf("minted a duplicate option id %q", o.OptionID)
		}
		seen[o.OptionID] = true
	}
}

func TestMintOptionIDs_NilSafe(t *testing.T) {
	var p *question.MCQPayload
	p.MintOptionIDs(seqIDs()) // must not panic
	q := &question.MCQPayload{Options: []question.MCQOption{{OptionID: "x"}}}
	q.MintOptionIDs(nil) // nil source = no-op, never blanks the id
	if q.Options[0].OptionID != "x" {
		t.Errorf("a nil id source must be a no-op, not a blanking: %q", q.Options[0].OptionID)
	}
}
