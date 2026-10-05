package atom_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

var errSeqBoom = errors.New("outbox: Insert: connection reset by peer")

// seqFailRepo fails every Save.
type seqFailRepo struct{ fakeRepo }

func (r *seqFailRepo) Save(_ context.Context, _ *atom.LearningAtom) error { return errSeqBoom }

// seqFailPub fails every Publish and counts attempts.
type seqFailPub struct{ calls int }

func (p *seqFailPub) Publish(_ context.Context, _ atom.Event) error {
	p.calls++
	return errSeqBoom
}

func seqAtom(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   "01970000-0000-7000-8000-000000000001",
		Gcid:       "01970000-0000-7000-9000-000000000001",
		CourseID:   "01970000-0000-7000-7000-000000000001",
		Title:      "sequential writer atom",
		Body:       "body",
		AtomType:   atom.AtomType("mcq"),
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("NewBound: %v", err)
	}
	return a
}

func TestSequentialPublisher_SavesThenPublishes(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	pub := &fakePublisher{}
	w := atom.NewSequentialPublisher(repo, pub)

	a := seqAtom(t)
	if err := w.SaveAndPublish(context.Background(), a, atom.Event{Type: atom.EventTypeAtomCreated}); err != nil {
		t.Fatalf("SaveAndPublish: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Errorf("saved atoms = %d; want 1", len(repo.saved))
	}
	if len(pub.events) != 1 {
		t.Errorf("published events = %d; want 1", len(pub.events))
	}
}

// TestSequentialPublisher_SurfacesPublishFailure is the whole reason this
// non-atomic fallback is allowed to exist: it cannot roll the save back, but
// it must never report success for an event that was not queued.
func TestSequentialPublisher_SurfacesPublishFailure(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	pub := &seqFailPub{}
	w := atom.NewSequentialPublisher(repo, pub)

	err := w.SaveAndPublish(context.Background(), seqAtom(t), atom.Event{Type: atom.EventTypeAtomCreated})
	if err == nil {
		t.Fatal("SaveAndPublish returned nil after a failed publish; want the error surfaced")
	}
	if pub.calls != 1 {
		t.Errorf("publish attempts = %d; want 1 (this test cannot see the failure otherwise)", pub.calls)
	}
	if !errors.Is(err, errSeqBoom) {
		t.Errorf("error = %v; want the publish error unwrapped to the caller", err)
	}
}

func TestSequentialPublisher_SaveFailureSkipsPublish(t *testing.T) {
	t.Parallel()

	pub := &seqFailPub{}
	w := atom.NewSequentialPublisher(&seqFailRepo{}, pub)

	if err := w.SaveAndPublish(context.Background(), seqAtom(t), atom.Event{}); err == nil {
		t.Fatal("SaveAndPublish returned nil after a failed save; want the error surfaced")
	}
	if pub.calls != 0 {
		t.Errorf("publish attempts after a failed save = %d; want 0", pub.calls)
	}
}

// TestSequentialPublisher_NilPublisherIsAWiredDeployment: event emission not
// being wired is a legitimate shape (the legacy NewRouter shim). The save
// still runs and no event is claimed.
func TestSequentialPublisher_NilPublisherIsAWiredDeployment(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	w := atom.NewSequentialPublisher(repo, nil)

	if err := w.SaveAndPublish(context.Background(), seqAtom(t), atom.Event{}); err != nil {
		t.Fatalf("SaveAndPublish with no publisher wired: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Errorf("saved atoms = %d; want 1", len(repo.saved))
	}
}

func TestSequentialPublisher_RefusesIncompleteWiring(t *testing.T) {
	t.Parallel()

	if err := atom.NewSequentialPublisher(nil, nil).SaveAndPublish(context.Background(), seqAtom(t), atom.Event{}); err == nil {
		t.Error("nil repository: got nil error; want a loud refusal")
	}
	if err := atom.NewSequentialPublisher(&fakeRepo{}, nil).SaveAndPublish(context.Background(), nil, atom.Event{}); err == nil {
		t.Error("nil atom: got nil error; want a loud refusal")
	}
}
