// orphan_test.go — ADR-229 Amendment A1 (CHO-2132) singleton orphan edition,
// domain slice. RED-first per strict TDD.
//
// The orphan edition is an immutable clone of a withdrawn-while-consumed
// atom's last-published revision:
//   - owned by the ORIGINAL author (attribution R1; pseudonymised at closure),
//   - status=published (consumers can still read/snapshot it),
//   - reuse_visibility=private FOREVER (the cross-lane picker contract),
//   - FROZEN: revision appends, reuse-visibility changes, publish and
//     soft-delete all refuse with ErrOrphanFrozen (consumed-continuity is
//     absolute — even the author cannot break consumers),
//   - evolving an orphan = fork via Clone() (which must NOT propagate the
//     orphan markers).
package atom

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// newPublishedSource builds a published source atom the way the authoring
// flow would have left it.
func newPublishedSource(t *testing.T) *LearningAtom {
	t.Helper()
	a, err := New(NewParams{
		TenantID: "0197aaaa-0000-7111-8111-000000000001",
		Gcid:     "0197bbbb-0000-7000-8000-0000000000aa",
		Title:    "Fractions: halves and quarters",
		Body:     "body",
		Tags:     []string{"math", "fractions"},
		Mode:     ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Stem = "What is 1/2 + 1/4?"
	a.QuestionType = QuestionTypeMCQ
	a.Difficulty = 2
	a.Subject = "math"
	a.ReuseVisibility = ReuseTenant
	if err := a.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return a
}

const sourceRevisionID = "0197cccc-0000-7000-8000-0000000000f1"

func mintOrphanForTest(t *testing.T, src *LearningAtom) *LearningAtom {
	t.Helper()
	o, err := CloneOrphan(OrphanCloneParams{Source: src, SourceRevisionID: sourceRevisionID})
	if err != nil {
		t.Fatalf("CloneOrphan: %v", err)
	}
	return o
}

// -----------------------------------------------------------------------------
// CloneOrphan
// -----------------------------------------------------------------------------

func TestCloneOrphan_BuildsImmutablePrivatePublishedEdition(t *testing.T) {
	src := newPublishedSource(t)
	o := mintOrphanForTest(t, src)

	if o.AtomID == "" || o.AtomID == src.AtomID {
		t.Fatalf("orphan must be a NEW aggregate (got atom_id %q, source %q)", o.AtomID, src.AtomID)
	}
	if o.TenantID != src.TenantID {
		t.Fatalf("orphan tenant = %q, want source tenant %q", o.TenantID, src.TenantID)
	}
	if o.Gcid != src.Gcid {
		t.Fatalf("orphan owner = %q, want ORIGINAL author %q (attribution R1)", o.Gcid, src.Gcid)
	}
	if o.Title != src.Title {
		t.Fatalf("orphan title = %q, want verbatim source title %q (edition, not a copy)", o.Title, src.Title)
	}
	if o.Status != StatusPublished {
		t.Fatalf("orphan status = %q, want published (consumers still read/snapshot)", o.Status)
	}
	if o.ReuseVisibility != ReusePrivate {
		t.Fatalf("orphan reuse_visibility = %q, want private FOREVER (cross-lane contract)", o.ReuseVisibility)
	}
	if o.OrphanedFromAtomID != src.AtomID {
		t.Fatalf("orphaned_from_atom_id = %q, want %q", o.OrphanedFromAtomID, src.AtomID)
	}
	if o.OrphanedSourceRevisionID != sourceRevisionID {
		t.Fatalf("orphaned_source_revision_id = %q, want %q", o.OrphanedSourceRevisionID, sourceRevisionID)
	}
	if o.OrphanedAt == nil || o.OrphanedAt.IsZero() {
		t.Fatalf("orphaned_at must be stamped")
	}
	if o.ClonedFromAtomID != src.AtomID {
		t.Fatalf("cloned_from_atom_id = %q, want %q (clone-substrate provenance)", o.ClonedFromAtomID, src.AtomID)
	}
	if !o.IsOrphan() {
		t.Fatalf("IsOrphan() = false on a minted orphan")
	}
	if src.IsOrphan() {
		t.Fatalf("source must NOT be mutated into an orphan")
	}
	// Content copied.
	if o.Stem != src.Stem || o.Body != src.Body || o.QuestionType != src.QuestionType || o.Subject != src.Subject {
		t.Fatalf("orphan content not copied verbatim")
	}
	// Deep-copied slices (mutating the orphan's tags must not touch source).
	if len(o.Tags) != len(src.Tags) {
		t.Fatalf("tags not copied")
	}
	if len(o.Tags) > 0 {
		o.Tags[0] = "mutated"
		if src.Tags[0] == "mutated" {
			t.Fatalf("tags alias the source slice (need defensive copy)")
		}
	}
	// CourseID intentionally NOT inherited (independent edition).
	if o.CourseID != "" {
		t.Fatalf("orphan course_id = %q, want empty", o.CourseID)
	}
}

func TestCloneOrphan_Guards(t *testing.T) {
	src := newPublishedSource(t)

	if _, err := CloneOrphan(OrphanCloneParams{Source: nil, SourceRevisionID: sourceRevisionID}); err == nil {
		t.Fatalf("nil source must refuse")
	}
	if _, err := CloneOrphan(OrphanCloneParams{Source: src, SourceRevisionID: "  "}); err == nil {
		t.Fatalf("blank source revision must refuse (singleton key half)")
	}
	// orphan-of-orphan is a can't-happen state — refuse loud.
	o := mintOrphanForTest(t, src)
	if _, err := CloneOrphan(OrphanCloneParams{Source: o, SourceRevisionID: sourceRevisionID}); err == nil {
		t.Fatalf("orphan-of-orphan must refuse")
	}
}

// -----------------------------------------------------------------------------
// FROZEN — every mutation refuses with ErrOrphanFrozen
// -----------------------------------------------------------------------------

func TestOrphan_FrozenMutationsRefuse(t *testing.T) {
	src := newPublishedSource(t)
	o := mintOrphanForTest(t, src)

	if _, err := o.ChangeReuseVisibility(o.Gcid, ReuseTenant); !errors.Is(err, ErrOrphanFrozen) {
		t.Fatalf("ChangeReuseVisibility on orphan: err = %v, want ErrOrphanFrozen", err)
	}
	title := "new title"
	if err := o.ApplyUpdate(UpdateParams{Title: &title}); !errors.Is(err, ErrOrphanFrozen) {
		t.Fatalf("ApplyUpdate on orphan: err = %v, want ErrOrphanFrozen", err)
	}
	if err := o.Publish(); !errors.Is(err, ErrOrphanFrozen) {
		t.Fatalf("Publish on orphan: err = %v, want ErrOrphanFrozen (no silent no-op)", err)
	}
	if err := o.SoftDelete(); !errors.Is(err, ErrOrphanFrozen) {
		t.Fatalf("SoftDelete on orphan: err = %v, want ErrOrphanFrozen (continuity survives archive)", err)
	}
	if o.DeletedAt != nil {
		t.Fatalf("orphan must not be soft-deleted by the refused call")
	}
	if o.ReuseVisibility != ReusePrivate {
		t.Fatalf("orphan reuse_visibility mutated despite refusal")
	}
}

// The original atom stays fully mutable — the freeze applies to the orphan
// edition only (cascade never crosses aggregates).
func TestOriginal_StaysMutableAfterOrphanMint(t *testing.T) {
	src := newPublishedSource(t)
	_ = mintOrphanForTest(t, src)

	if _, err := src.ChangeReuseVisibility(src.Gcid, ReusePrivate); err != nil {
		t.Fatalf("original ChangeReuseVisibility: %v", err)
	}
	if err := src.SoftDelete(); err != nil {
		t.Fatalf("original SoftDelete: %v", err)
	}
	if src.DeletedAt == nil {
		t.Fatalf("original soft-delete did not land")
	}
}

// -----------------------------------------------------------------------------
// Fork stays open — Clone() of an orphan is the evolution path, and the fork
// must NOT inherit the orphan markers (it is a fresh draft owned by the caller).
// -----------------------------------------------------------------------------

func TestClone_OfOrphanIsAllowedAndClearsOrphanMarkers(t *testing.T) {
	src := newPublishedSource(t)
	o := mintOrphanForTest(t, src)

	fork, err := Clone(CloneParams{
		Source:   o,
		TenantID: o.TenantID,
		Gcid:     "0197dddd-0000-7000-8000-0000000000bb", // the consumer forking
	})
	if err != nil {
		t.Fatalf("Clone of orphan must stay allowed (fork path): %v", err)
	}
	if fork.IsOrphan() {
		t.Fatalf("fork must not inherit orphan markers")
	}
	if fork.OrphanedFromAtomID != "" || fork.OrphanedSourceRevisionID != "" || fork.OrphanedAt != nil {
		t.Fatalf("fork carries orphan fields: %+v", fork)
	}
	if fork.ClonedFromAtomID != o.AtomID {
		t.Fatalf("fork cloned_from = %q, want the orphan %q", fork.ClonedFromAtomID, o.AtomID)
	}
	if fork.Status != StatusDraft {
		t.Fatalf("fork status = %q, want draft", fork.Status)
	}
}

// -----------------------------------------------------------------------------
// NewAtomOrphanCreatedEvent
// -----------------------------------------------------------------------------

func TestNewAtomOrphanCreatedEvent_ShapeAndDeterministicKey(t *testing.T) {
	src := newPublishedSource(t)
	o := mintOrphanForTest(t, src)

	ev := NewAtomOrphanCreatedEvent(o, OrphanTriggerNarrowed, "00-abc-def-01", "vendor=x")

	if ev.Type != EventTypeAtomOrphanCreated {
		t.Fatalf("event type = %q, want %q", ev.Type, EventTypeAtomOrphanCreated)
	}
	if string(ev.Type) != "chora.creation.atom.orphan_created.v1" {
		t.Fatalf("topic literal = %q", ev.Type)
	}
	if ev.AtomID != o.AtomID {
		t.Fatalf("event atom_id (aggregate) = %q, want the ORPHAN %q", ev.AtomID, o.AtomID)
	}
	if ev.OrphanedFromAtomID != src.AtomID {
		t.Fatalf("orphaned_from = %q, want %q", ev.OrphanedFromAtomID, src.AtomID)
	}
	if ev.RevisionID != sourceRevisionID {
		t.Fatalf("source revision on event = %q, want %q", ev.RevisionID, sourceRevisionID)
	}
	if ev.Trigger != OrphanTriggerNarrowed {
		t.Fatalf("trigger = %q", ev.Trigger)
	}
	if ev.TenantID != o.TenantID || ev.Gcid != o.Gcid {
		t.Fatalf("envelope tenant/gcid = %q/%q", ev.TenantID, ev.Gcid)
	}
	if ev.TraceParent != "00-abc-def-01" || ev.TraceState != "vendor=x" {
		t.Fatalf("trace context not threaded")
	}
	// DETERMINISTIC idempotency key = the singleton key. The creation outbox
	// UNIQUE(idempotency_key) is the DB-level "no duplicate orphan_created
	// event" enforcement: re-mint attempts and crash-redelivery re-publishes
	// dedupe on it.
	wantKey := src.AtomID + ":orphan_created:" + sourceRevisionID
	if ev.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want deterministic %q", ev.IdempotencyKey, wantKey)
	}
	if ev.EventID == "" || ev.EventID == ev.IdempotencyKey {
		t.Fatalf("event id must be a fresh UUID, got %q", ev.EventID)
	}
	if ev.OccurredAt == "" {
		t.Fatalf("occurred_at must be set")
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.OccurredAt); err != nil {
		t.Fatalf("occurred_at not RFC3339: %v", err)
	}
}

func TestOrphanTriggers_Canonical(t *testing.T) {
	for _, tr := range []string{OrphanTriggerNarrowed, OrphanTriggerUnshared, OrphanTriggerArchived} {
		if !ValidOrphanTrigger(tr) {
			t.Fatalf("trigger %q must be valid", tr)
		}
	}
	for _, tr := range []string{"", "widened", "NARROWED", " archived"} {
		if ValidOrphanTrigger(tr) {
			t.Fatalf("trigger %q must be invalid", tr)
		}
	}
	if OrphanTriggerNarrowed != "narrowed" || OrphanTriggerUnshared != "unshared" || OrphanTriggerArchived != "archived" {
		t.Fatalf("trigger literals drifted from the proto contract")
	}
}

// Guard: the orphan struct must not accidentally satisfy strings the picker
// exclusion depends on. reuse_visibility private is load-bearing.
func TestOrphan_PrivateForeverPin(t *testing.T) {
	src := newPublishedSource(t)
	src.ReuseVisibility = ReuseFriends // whatever the source was
	o := mintOrphanForTest(t, src)
	if o.ReuseVisibility != ReusePrivate {
		t.Fatalf("orphan must be born private regardless of source audience, got %q", o.ReuseVisibility)
	}
	if strings.TrimSpace(string(o.ReuseVisibility)) != "private" {
		t.Fatalf("private literal drifted")
	}
}
