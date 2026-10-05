package atom_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// The atom's content tags are what chora-consumption projects into
// atom_index.topic_tags: the creation encoder writes payload["tags"] to wire
// field 7 (topic_node_ids) when no explicit topic_node_ids is present
// (protomarshal.go:198-210), and consumption's protodecode maps field 7 back to
// topic_tags (protodecode.go:633).
//
// Before CHO-2142 the typed atom.Event carried NO tags at all, so
// atom.published.v1 — the event the publish handler AND the backfill re-emit —
// reached the wire tagless every time. A source-side tag fix could therefore
// never project. These tests pin the tags onto the typed event.
func TestNewAtomPublishedEvent_CarriesTopicTags(t *testing.T) {
	a := newDraft(t, []string{"fractions", "number-sense"})
	ev := atom.NewAtomPublishedEvent(a, "rev-1", 1, "opt-1", 4, false, "", "tp", "ts")

	if len(ev.Tags) != 2 || ev.Tags[0] != "fractions" || ev.Tags[1] != "number-sense" {
		t.Fatalf("atom.published.v1 must carry the atom's tags, got %v", ev.Tags)
	}
}

func TestNewAtomCreatedEvent_CarriesTopicTags(t *testing.T) {
	a := newDraft(t, []string{"fractions"})
	ev := atom.NewAtomCreatedEvent(a, "tp", "ts")

	if len(ev.Tags) != 1 || ev.Tags[0] != "fractions" {
		t.Fatalf("atom.created.v1 must carry the atom's tags, got %v", ev.Tags)
	}
}

func TestNewAtomPublishedReemitEvent_CarriesTopicTags(t *testing.T) {
	a := newDraft(t, []string{"fractions"})
	ev := atom.NewAtomPublishedReemitEvent(a, "rev-1", 1, "opt-1", 4, false, "", "tp", "ts", "salt1")

	if len(ev.Tags) != 1 || ev.Tags[0] != "fractions" {
		t.Fatalf("re-emit must carry tags (it is the backfill's vehicle), got %v", ev.Tags)
	}
	if ev.IdempotencyKey == "" {
		t.Fatal("re-emit must keep its salted deterministic idempotency key")
	}
}

func TestNewAtomPublishedEvent_UntaggedAtomEmitsNoTags(t *testing.T) {
	// Proto3-elided: an untagged atom must not invent tags.
	a := newDraft(t, nil)
	ev := atom.NewAtomPublishedEvent(a, "rev-1", 1, "", 0, true, "", "tp", "ts")
	if len(ev.Tags) != 0 {
		t.Fatalf("untagged atom must emit no tags, got %v", ev.Tags)
	}
}
