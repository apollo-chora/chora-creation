// atom_event_constructors_test.go — coverage for the small event-constructor
// functions and the AtomType accessor that the domain tests had not reached.
package atom_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestAtomType_Accessor(t *testing.T) {
	a := newDraft(t, nil)
	// Set a type explicitly and assert the accessor returns it.
	a.QuestionType = atom.QuestionTypeFlashcard
	if got := a.AtomType(); got != atom.QuestionTypeFlashcard {
		t.Errorf("AtomType() = %q, want %q", got, atom.QuestionTypeFlashcard)
	}
}

func TestNewAtomArchivedEvent(t *testing.T) {
	a := newDraft(t, []string{"fractions"})
	ev := atom.NewAtomArchivedEvent(a, "00000000-0000-7000-8000-000000000042", "tp", "ts")
	if ev.Type != atom.EventTypeAtomArchived {
		t.Errorf("Type = %q, want archived", ev.Type)
	}
	if ev.Gcid != "00000000-0000-7000-8000-000000000042" {
		t.Errorf("Gcid = %q, want the archiving actor", ev.Gcid)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("AtomID = %q, want %q", ev.AtomID, a.AtomID)
	}
	// OccurredAt falls back to UpdatedAt when DeletedAt is nil.
	if ev.OccurredAt == "" {
		t.Error("OccurredAt empty")
	}
}

func TestNewAtomRevisedEvent(t *testing.T) {
	a := newDraft(t, nil)
	rev := &atom.AppendOnlyRevision{
		RevisionID:     "rev-7",
		AtomID:         a.AtomID,
		RevisionNumber: 7,
		Body:           "v2",
		AuthoredBy:     "00000000-0000-7000-8000-000000000077",
		AuthoredAt:     time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
		SourceType:     atom.SourceManual,
	}
	ev := atom.NewAtomRevisedEvent(a, rev, "tp", "ts")
	if ev.Type != atom.EventTypeAtomRevised {
		t.Errorf("Type = %q, want revised", ev.Type)
	}
	if ev.RevisionID != "rev-7" || ev.RevisionNumber != 7 {
		t.Errorf("Revision = %q/%d; want rev-7/7", ev.RevisionID, ev.RevisionNumber)
	}
	if ev.Gcid != rev.AuthoredBy {
		t.Errorf("Gcid = %q, want AuthoredBy", ev.Gcid)
	}
	if ev.SourceType != atom.SourceManual {
		t.Errorf("SourceType = %q, want manual", ev.SourceType)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("AtomID = %q, want %q", ev.AtomID, a.AtomID)
	}
}
