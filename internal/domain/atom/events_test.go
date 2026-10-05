// events_test.go — atom.published.v1 idempotency-key contract.
//
// The publish path stamps a DETERMINISTIC idempotency_key
// (atom_id + ":published:" + revision_id) so re-publishing the same live
// revision dedupes on the outbox UNIQUE index. That same determinism means the
// backfill endpoint can NEVER re-emit an event the outbox has already seen
// (CHO-2128 F1: 101/116 live atoms pre-date the sharing projection
// subscriber). NewAtomPublishedReemitEvent salts the key
// (":reemit:" + salt) so an operator-driven backfill re-enqueues while staying
// deterministic PER SALT (re-running the same salt still dedupes).
package atom_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func newPublishedFixture(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "salted atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	return a
}

// TestNewAtomPublishedReemitEvent_SaltsIdempotencyKey — the salted constructor
// appends ":reemit:" + salt to the deterministic key and changes NOTHING else
// about the payload (same event shape, fresh event_id).
func TestNewAtomPublishedReemitEvent_SaltsIdempotencyKey(t *testing.T) {
	t.Parallel()
	a := newPublishedFixture(t)

	plain := atom.NewAtomPublishedEvent(a, "rev-1", 1, "opt_1", 2, false, "", "tp", "ts")
	salted := atom.NewAtomPublishedReemitEvent(a, "rev-1", 1, "opt_1", 2, false, "", "tp", "ts", "seed-2026-07-11")

	wantPlain := a.AtomID + ":published:rev-1"
	if plain.IdempotencyKey != wantPlain {
		t.Fatalf("plain idempotency_key = %q; want %q", plain.IdempotencyKey, wantPlain)
	}
	wantSalted := wantPlain + ":reemit:seed-2026-07-11"
	if salted.IdempotencyKey != wantSalted {
		t.Errorf("salted idempotency_key = %q; want %q", salted.IdempotencyKey, wantSalted)
	}

	// Payload identical apart from the key + fresh event_id.
	if salted.Type != plain.Type || salted.TenantID != plain.TenantID ||
		salted.AtomID != plain.AtomID || salted.RevisionID != plain.RevisionID ||
		salted.CorrectOptionID != plain.CorrectOptionID || salted.AnswerCount != plain.AnswerCount ||
		salted.HasOpenEndedQuestion != plain.HasOpenEndedQuestion {
		t.Errorf("salted payload diverged from plain: %+v vs %+v", salted, plain)
	}
	if salted.EventID == "" || salted.EventID == plain.EventID {
		t.Errorf("salted event_id = %q (plain %q); want a fresh UUIDv7", salted.EventID, plain.EventID)
	}
}

// TestNewAtomPublishedReemitEvent_EmptySaltFallsBackToDeterministicKey — a
// blank salt (empty or whitespace) yields the UNSALTED deterministic key; the
// HTTP handler is responsible for rejecting blank salts fail-loud.
func TestNewAtomPublishedReemitEvent_EmptySaltFallsBackToDeterministicKey(t *testing.T) {
	t.Parallel()
	a := newPublishedFixture(t)
	want := a.AtomID + ":published:rev-9"

	for _, salt := range []string{"", "   "} {
		ev := atom.NewAtomPublishedReemitEvent(a, "rev-9", 9, "", 0, true, "", "tp", "ts", salt)
		if ev.IdempotencyKey != want {
			t.Errorf("salt %q: idempotency_key = %q; want %q", salt, ev.IdempotencyKey, want)
		}
		if strings.Contains(ev.IdempotencyKey, ":reemit:") {
			t.Errorf("salt %q: key %q must not carry a :reemit: segment", salt, ev.IdempotencyKey)
		}
	}
}
