// atom_updated_event_test.go - chora.creation.atom.updated.v1 constructor
// contract (ADR-244 D5 standing trigger).
//
// The metadata PATCH path mutates PUBLISHED atoms and, until this event,
// announced nothing: the topic, its BINARY schema binding, its DLQ and the
// chora-consumption KG-invalidation consumer all existed with no producer
// behind them. The constructor under test is the producer's payload builder.
//
// Idempotency key contract, which is the reason this file exists:
//   - DETERMINISTIC for one update, so a crash-redelivery of the SAME update
//     dedupes on the outbox UNIQUE(idempotency_key) index.
//   - DISTINCT across successive updates of the same atom, so a second real
//     edit is never swallowed as a duplicate of the first.
//
// The key is stamped from the atom's UpdatedAt (the instant ApplyUpdate wrote)
// and NOT from Revision: phyllis.go re-assigns Revision from a revision number
// (a.Revision = r.RevisionNumber), so the counter is not monotonic per edit.
package atom_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// newUpdatedFixture returns a PUBLISHED atom carrying every metadata field the
// PATCH path can touch.
func newUpdatedFixture(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: "01970000-0000-7000-8000-000000000001",
		Gcid:     "01970000-0000-7000-9000-000000000001",
		Title:    "Comparing fractions",
		Body:     "body",
		Tags:     []string{"fractions"},
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.Status = atom.StatusPublished
	a.QuestionType = atom.AtomType("mcq")
	a.Stem = "Which fraction is larger?"
	a.Subject = "math"
	a.CognitiveLevel = atom.CognitiveLevelComprehension
	a.AuthorNote = "keep the denominators small"
	a.Difficulty = 3
	a.ImdaDimensionTags = []atom.ImdaDimTag{atom.ImdaDimTransparency}
	a.MediaAssets = []atom.MediaAsset{{
		Type: "image", URL: "gs://chora-atom-media-dev/f.png",
		AltText: "two fraction bars", MIME: "image/png", SizeBytes: 2048,
	}}
	a.UpdatedAt = time.Date(2026, 8, 16, 9, 30, 0, 123456789, time.UTC)
	return a
}

// TestNewAtomUpdatedEvent_CarriesSnapshotAndDeterministicKey - the event names
// the atom, its post-patch metadata snapshot and exactly the fields that moved.
func TestNewAtomUpdatedEvent_CarriesSnapshotAndDeterministicKey(t *testing.T) {
	t.Parallel()
	a := newUpdatedFixture(t)

	ev := atom.NewAtomUpdatedEvent(a, []string{"stem", "difficulty"},
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "")

	if ev.Type != atom.EventTypeAtomUpdated {
		t.Errorf("type = %q; want %q", ev.Type, atom.EventTypeAtomUpdated)
	}
	if string(atom.EventTypeAtomUpdated) != "chora.creation.atom.updated.v1" {
		t.Errorf("topic = %q; want chora.creation.atom.updated.v1 (the live schema-bound topic)",
			atom.EventTypeAtomUpdated)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != a.TenantID {
		t.Errorf("tenant_id = %q; want %q", ev.TenantID, a.TenantID)
	}
	if ev.Gcid != a.Gcid {
		t.Errorf("gcid = %q; want the author %q", ev.Gcid, a.Gcid)
	}
	if ev.Status != string(atom.StatusPublished) {
		t.Errorf("status = %q; want published", ev.Status)
	}
	wantOccurred := a.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if ev.OccurredAt != wantOccurred {
		t.Errorf("occurred_at = %q; want the update instant %q", ev.OccurredAt, wantOccurred)
	}
	wantKey := a.AtomID + ":updated:" + wantOccurred
	if ev.IdempotencyKey != wantKey {
		t.Errorf("idempotency_key = %q; want %q", ev.IdempotencyKey, wantKey)
	}
	if ev.EventID == "" || ev.EventID == ev.IdempotencyKey {
		t.Errorf("event_id = %q; want a fresh UUIDv7 distinct from the dedupe key", ev.EventID)
	}
	if len(ev.ChangedFields) != 2 || ev.ChangedFields[0] != "stem" || ev.ChangedFields[1] != "difficulty" {
		t.Errorf("changed_fields = %v; want [stem difficulty] in caller order", ev.ChangedFields)
	}

	// The metadata snapshot the consumer reads alongside changed_fields.
	if ev.Title != a.Title || ev.Stem != a.Stem || ev.Subject != a.Subject {
		t.Errorf("title/stem/subject = %q/%q/%q; want %q/%q/%q",
			ev.Title, ev.Stem, ev.Subject, a.Title, a.Stem, a.Subject)
	}
	if ev.AuthorNote != a.AuthorNote {
		t.Errorf("author_note = %q; want %q", ev.AuthorNote, a.AuthorNote)
	}
	if ev.CognitiveLevel != string(a.CognitiveLevel) {
		t.Errorf("cognitive_level = %q; want %q", ev.CognitiveLevel, a.CognitiveLevel)
	}
	if ev.Difficulty != a.Difficulty {
		t.Errorf("difficulty = %d; want %d", ev.Difficulty, a.Difficulty)
	}
	if string(ev.AtomType) != string(a.QuestionType) {
		t.Errorf("atom_type = %q; want %q", ev.AtomType, a.QuestionType)
	}
	if len(ev.Tags) != 1 || ev.Tags[0] != "fractions" {
		t.Errorf("tags = %v; want [fractions]", ev.Tags)
	}
	if len(ev.ImdaDimensionTags) != 1 || ev.ImdaDimensionTags[0] != string(atom.ImdaDimTransparency) {
		t.Errorf("imda_dimension_tags = %v; want [transparency]", ev.ImdaDimensionTags)
	}
	if len(ev.MediaAssets) != 1 || ev.MediaAssets[0].URL != a.MediaAssets[0].URL {
		t.Errorf("media_assets = %+v; want the atom's single image asset", ev.MediaAssets)
	}
}

// TestNewAtomUpdatedEvent_DefensiveCopies - a later mutation of the aggregate
// (or of the caller's slice) must not retro-edit an already-queued event.
func TestNewAtomUpdatedEvent_DefensiveCopies(t *testing.T) {
	t.Parallel()
	a := newUpdatedFixture(t)
	changed := []string{"stem"}

	ev := atom.NewAtomUpdatedEvent(a, changed, "tp", "")

	changed[0] = "title"
	a.Tags[0] = "decimals"
	a.ImdaDimensionTags[0] = atom.ImdaDimAccountability
	a.MediaAssets[0].URL = "gs://elsewhere/x.png"

	if ev.ChangedFields[0] != "stem" {
		t.Errorf("changed_fields aliased the caller slice: %v", ev.ChangedFields)
	}
	if ev.Tags[0] != "fractions" {
		t.Errorf("tags aliased the aggregate: %v", ev.Tags)
	}
	if ev.ImdaDimensionTags[0] != string(atom.ImdaDimTransparency) {
		t.Errorf("imda_dimension_tags aliased the aggregate: %v", ev.ImdaDimensionTags)
	}
	if ev.MediaAssets[0].URL != "gs://chora-atom-media-dev/f.png" {
		t.Errorf("media_assets aliased the aggregate: %+v", ev.MediaAssets)
	}
}

// TestNewAtomUpdatedEvent_BareAtomElidesEmptySets - an atom with no tags, no
// IMDA labels and no media must carry nil rather than empty slices, so the
// encoder elides those fields instead of writing an empty list the consumer
// would read as "the author cleared them". A zero UpdatedAt falls back to
// CreatedAt so the dedupe key and occurred_at are never blank.
func TestNewAtomUpdatedEvent_BareAtomElidesEmptySets(t *testing.T) {
	t.Parallel()
	a, err := atom.New(atom.NewParams{
		TenantID: "01970000-0000-7000-8000-000000000001",
		Gcid:     "01970000-0000-7000-9000-000000000001",
		Title:    "Bare atom",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.Status = atom.StatusPublished
	a.UpdatedAt = time.Time{}

	ev := atom.NewAtomUpdatedEvent(a, []string{"title"}, "tp", "")

	if ev.Tags != nil || ev.ImdaDimensionTags != nil || ev.MediaAssets != nil {
		t.Errorf("empty sets must be nil, got tags=%v imda=%v media=%v",
			ev.Tags, ev.ImdaDimensionTags, ev.MediaAssets)
	}
	wantOccurred := a.CreatedAt.UTC().Format(time.RFC3339Nano)
	if ev.OccurredAt != wantOccurred {
		t.Errorf("occurred_at = %q; want the CreatedAt fallback %q", ev.OccurredAt, wantOccurred)
	}
	if ev.IdempotencyKey != a.AtomID+":updated:"+wantOccurred {
		t.Errorf("idempotency_key = %q; want the CreatedAt fallback in the key", ev.IdempotencyKey)
	}
}

// TestNewAtomUpdatedEvent_KeyIsDistinctPerUpdate - two successive edits of the
// same atom MUST NOT collide on the outbox UNIQUE index, or the second edit
// would be silently dropped.
func TestNewAtomUpdatedEvent_KeyIsDistinctPerUpdate(t *testing.T) {
	t.Parallel()
	a := newUpdatedFixture(t)

	first := atom.NewAtomUpdatedEvent(a, []string{"stem"}, "tp", "")
	a.UpdatedAt = a.UpdatedAt.Add(time.Millisecond)
	second := atom.NewAtomUpdatedEvent(a, []string{"title"}, "tp", "")

	if first.IdempotencyKey == second.IdempotencyKey {
		t.Fatalf("successive updates share idempotency_key %q; the second edit would dedupe away",
			first.IdempotencyKey)
	}
	if !strings.HasPrefix(second.IdempotencyKey, a.AtomID+":updated:") {
		t.Errorf("idempotency_key = %q; want the %s:updated:<instant> shape", second.IdempotencyKey, a.AtomID)
	}
}

// TestNewAtomUpdatedEvent_SameUpdateIsStable - rebuilding the event for the
// SAME update (a retry after a failed enqueue) yields the same dedupe key.
func TestNewAtomUpdatedEvent_SameUpdateIsStable(t *testing.T) {
	t.Parallel()
	a := newUpdatedFixture(t)

	first := atom.NewAtomUpdatedEvent(a, []string{"stem"}, "tp", "")
	retry := atom.NewAtomUpdatedEvent(a, []string{"stem"}, "tp", "")

	if first.IdempotencyKey != retry.IdempotencyKey {
		t.Fatalf("retry key %q != first %q; a redelivery would double-emit",
			retry.IdempotencyKey, first.IdempotencyKey)
	}
	if first.EventID == retry.EventID {
		t.Error("event_id must be a fresh UUIDv7 per build, only the dedupe key repeats")
	}
}
