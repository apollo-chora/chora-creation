// topic_tags.go — the LearningAtom's topic classification (CHO-2142).
//
// An atom's Tags are its topic axis. chora-consumption projects them into
// atom_index.topic_tags (creation encodes payload["tags"] into wire field 7
// topic_node_ids; consumption's protodecode maps field 7 back to topic_tags),
// and that axis drives the fog catalogue bias, the dose topic pick, and the
// topic-accuracy projector. An atom that reaches PUBLISHED with no tags is
// projected tagless and acked unclassified — the CHO-2142 defect.
//
// Tags are atom-level classification METADATA, not revision content: SetTopicTags
// deliberately does NOT bump Revision (the append-only AtomRevision chain records
// content edits, and atom.published.v1 pins a revision id — a classification pass
// must not desync either).
package atom

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MaxTopicTags caps a single atom's topic axis. A classifier that fans out
	// past this is over-labelling; the surplus is dropped rather than failing the
	// whole backfill (the leading tags are the highest-signal ones).
	MaxTopicTags = 8

	// MaxTopicTagLen bounds one tag. Tags are short topic slugs
	// ("fractions", "number-sense"), never sentences — an overlong tag means the
	// classifier returned prose, which is a fail-loud parse error, not a tag.
	MaxTopicTagLen = 64
)

// ErrNoTopicTags is returned when a tag set normalises to nothing. A classifier
// that yields no usable tag MUST fail loud: writing an empty set back would
// silently reproduce the very defect the backfill exists to fix.
var ErrNoTopicTags = errors.New("no usable topic tags after normalisation")

// NormalizeTopicTags canonicalises a raw tag set: trim, lowercase, drop blanks,
// dedupe (first occurrence wins), cap at MaxTopicTags. Order is preserved so the
// classifier's ranking survives. Returns ErrNoTopicTags when nothing usable
// remains, and a loud error when a tag exceeds MaxTopicTagLen.
func NormalizeTopicTags(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, r := range raw {
		t := strings.ToLower(strings.TrimSpace(r))
		if t == "" {
			continue
		}
		if len(t) > MaxTopicTagLen {
			return nil, fmt.Errorf("topic tag too long: %d > %d (%q)", len(t), MaxTopicTagLen, t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) == MaxTopicTags {
			break
		}
	}

	if len(out) == 0 {
		return nil, ErrNoTopicTags
	}
	return out, nil
}

// NeedsTopicTags reports whether the atom carries no usable topic tag — i.e. it
// is a classification candidate. Blank-only tags count as untagged (that is what
// the source rows in CHO-2142 actually look like).
func (a *LearningAtom) NeedsTopicTags() bool {
	for _, t := range a.Tags {
		if strings.TrimSpace(t) != "" {
			return false
		}
	}
	return true
}

// SetTopicTags replaces the atom's topic axis with a normalised tag set.
//
// Refuses a frozen orphan edition (ADR-229 A1.2 — orphans are immutable) and a
// soft-deleted atom. Rejects an empty/unusable set (ErrNoTopicTags) WITHOUT
// mutating the atom, so a failed classification never blanks existing tags.
//
// Does NOT bump Revision: classification is metadata enrichment, not a content
// edit (see the package doc).
func (a *LearningAtom) SetTopicTags(tags []string) error {
	if a.IsOrphan() {
		return ErrOrphanFrozen
	}
	if a.DeletedAt != nil {
		return errors.New("cannot set topic tags on a soft-deleted atom")
	}

	normalised, err := NormalizeTopicTags(tags)
	if err != nil {
		return err // atom left untouched
	}

	a.Tags = normalised
	a.UpdatedAt = time.Now().UTC()
	return nil
}
