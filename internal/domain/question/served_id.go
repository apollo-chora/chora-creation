// served_id.go — CHO-2272 (SECURITY): the LEARNER-facing atom MCQ option id and
// option ORDER are DERIVED from display content at serve, never taken from stored
// data. The stored option_id and stored index both correlate with the answer
// (live 2026-07-17: `opt_1` is the correct option 67 of 187 times; the published
// set is 62% correct-first), and CHO-2255's persist-time mint+shuffle cannot fix
// the rows already at rest. Deriving at serve fixes every legacy row with no
// mcq_payload mutation, and closes the door for good — the serve trusts only
// bytes already on screen.
//
// This mirrors the campaign lane's served_id.go (CHO-2244). The only structural
// difference: an atom MCQ option carries a single display field (Label), and an
// atom serves ONE question, so there is no question-index domain separator — the
// id is a function of the trimmed label alone.
//
// # Why creation also derives the grading KEY (not just the serve)
//
// The campaign lane derives on BOTH doors because it grades in-domain (it holds
// the labels). The atom lane grades CROSS-DOMAIN: chora_consumption.atom_index
// compares the learner's pick against a stored `correct_option_id` string and
// holds no labels, so it cannot re-derive a display-derived id. Therefore
// creation — which holds the labels — computes ServedCorrectOptionID() and ships
// THAT on chora.creation.atom.created.v1, keeping served == emitted == graded
// with zero change to the consumer. (This reverses the mcq.go rationale that
// once chose persist-time minting precisely because the key crosses a domain
// boundary — see ADR-241.)
package question

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// servedOptionIDLen is the hex width of a served option id. 16 hex chars = 64
// bits — collision-free in practice for a handful of options per question, and a
// genuine collision is caught and failed loud rather than silently served.
const servedOptionIDLen = 16

// servedOptionID derives the opaque id served for one atom MCQ option and graded
// against. Pure + deterministic: same trimmed label -> same id, forever. It
// hashes ONLY the label (the option's sole display field), so an attacker who
// "breaks" it learns only text already on screen; it does NOT depend on the
// stored option_id or the stored index, the two things that correlate with the
// answer.
func servedOptionID(label string) string {
	h := sha256.New()
	writeLenPrefixed(h, strings.TrimSpace(label))
	return hex.EncodeToString(h.Sum(nil))[:servedOptionIDLen]
}

// writeLenPrefixed writes a length prefix then the bytes, so distinct fields can
// never be concatenated ambiguously.
func writeLenPrefixed(h hash.Hash, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	h.Write(b[:])
	h.Write([]byte(s))
}

// servedOptionIDs returns the served id for each option IN STORED ORDER, failing
// loud when an option has no display content (it cannot be keyed from what the
// learner sees) or two options share display text (the served id-space would be
// ambiguous and grade two options identically). Both the serve door and the key
// derivation go through here, so they can never disagree about an option's id.
func (p *MCQPayload) servedOptionIDs() ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("mcq: nil payload")
	}
	ids := make([]string, len(p.Options))
	seen := make(map[string]int, len(p.Options))
	for i, o := range p.Options {
		if strings.TrimSpace(o.Label) == "" {
			return nil, fmt.Errorf("mcq: option[%d] has no display content — it cannot be keyed from what the learner sees", i)
		}
		id := servedOptionID(o.Label)
		if j, dup := seen[id]; dup {
			return nil, fmt.Errorf("mcq: option[%d] and option[%d] have identical display text — the served id-space would be ambiguous", j, i)
		}
		seen[id] = i
		ids[i] = id
	}
	return ids, nil
}

// ServedLearnerOptions returns the learner-safe served projection: IsCorrect +
// Explainer stripped, every option re-keyed to its servedOptionID, and options
// SORTED by that id. Sorting by the opaque id is a free deterministic shuffle —
// the hash is uniform over display content, which is independent of correctness,
// so the served position carries no signal. The original payload is NOT mutated.
//
// Fails loud (error) on an underivable / ambiguous id-space so the serve door can
// refuse rather than ship something it could not key from what the learner sees.
func (p MCQPayload) ServedLearnerOptions() ([]MCQOption, error) {
	ids, err := p.servedOptionIDs()
	if err != nil {
		return nil, err
	}
	out := make([]MCQOption, len(p.Options))
	for i, o := range p.Options {
		out[i] = MCQOption{
			OptionID: ids[i],
			Label:    o.Label,
			// IsCorrect + Explainer intentionally zero — learner-safe.
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OptionID < out[j].OptionID })
	return out, nil
}

// ServedCorrectOptionID returns the servedOptionID of the is_correct option — the
// cross-domain grading key carried on chora.creation.atom.created.v1. It goes
// through the SAME servedOptionIDs() derivation as the serve door, so the key can
// never disagree with what the learner is handed. Fails loud on the same
// ambiguity as the serve door (so a key is never shipped that the serve could not
// reproduce), or when no option is marked correct.
func (p *MCQPayload) ServedCorrectOptionID() (string, error) {
	ids, err := p.servedOptionIDs()
	if err != nil {
		return "", err
	}
	for i, o := range p.Options {
		if o.IsCorrect {
			return ids[i], nil
		}
	}
	return "", fmt.Errorf("mcq: no option marked is_correct")
}
