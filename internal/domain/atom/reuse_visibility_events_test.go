// reuse_visibility_events_test.go — ADR-229 WS-1 remainder (CHO-2127): the
// reuse-consent flag rides the event spine.
//
// RED before the Event struct gains ReuseVisibility/PreviousReuseVisibility
// and NewAtomReuseVisibilityChangedEvent lands:
//   - AtomCreated / AtomPublished builders carry the aggregate's audience so
//     the sharing atom_projections read-model hydrates the flag on publish.
//   - The dedicated changed-event carries new + previous audience so
//     consumers detect narrowing vs widening statelessly.
package atom_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func newReuseFixtureAtom(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: "11111111-1111-7111-8111-111111111111",
		Gcid:     "01970000-0000-7000-9000-0000000000aa",
		Title:    "reuse fixture",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	return a
}

// TestNewAtomCreatedEvent_CarriesReuseVisibility — created events surface the
// aggregate's audience (PRIVATE by construction on New).
func TestNewAtomCreatedEvent_CarriesReuseVisibility(t *testing.T) {
	t.Parallel()
	a := newReuseFixtureAtom(t)

	ev := atom.NewAtomCreatedEvent(a, "tp", "ts")
	if ev.ReuseVisibility != string(atom.ReusePrivate) {
		t.Errorf("created event reuse_visibility = %q; want %q (consent-first default)",
			ev.ReuseVisibility, atom.ReusePrivate)
	}
}

// TestNewAtomPublishedEvent_CarriesReuseVisibility — the publish snapshot
// carries the CURRENT audience (the projection maps it onto its column).
func TestNewAtomPublishedEvent_CarriesReuseVisibility(t *testing.T) {
	t.Parallel()
	a := newReuseFixtureAtom(t)
	if _, err := a.ChangeReuseVisibility(a.Gcid, atom.ReuseTenant); err != nil {
		t.Fatalf("ChangeReuseVisibility: %v", err)
	}

	ev := atom.NewAtomPublishedEvent(a, "rev-1", 1, "", 0, true, "", "tp", "ts")
	if ev.ReuseVisibility != string(atom.ReuseTenant) {
		t.Errorf("published event reuse_visibility = %q; want %q (current audience)",
			ev.ReuseVisibility, atom.ReuseTenant)
	}
}

// TestNewAtomReuseVisibilityChangedEvent — the dedicated audience-change
// event: envelope basics + new/previous audience + author attribution.
func TestNewAtomReuseVisibilityChangedEvent(t *testing.T) {
	t.Parallel()
	a := newReuseFixtureAtom(t)
	prev := a.ReuseVisibility
	if _, err := a.ChangeReuseVisibility(a.Gcid, atom.ReuseTenant); err != nil {
		t.Fatalf("ChangeReuseVisibility: %v", err)
	}

	ev := atom.NewAtomReuseVisibilityChangedEvent(a, prev, "tp-1", "ts-1")

	if ev.Type != atom.EventTypeAtomReuseVisibilityChanged {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomReuseVisibilityChanged)
	}
	if string(ev.Type) != "chora.creation.atom.reuse_visibility_changed.v1" {
		t.Errorf("event type literal = %q; want the canonical v1 topic", ev.Type)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != a.TenantID {
		t.Errorf("tenant_id = %q; want %q", ev.TenantID, a.TenantID)
	}
	if ev.Gcid != a.Gcid {
		t.Errorf("gcid = %q; want author %q (change is author-only)", ev.Gcid, a.Gcid)
	}
	if ev.ReuseVisibility != string(atom.ReuseTenant) {
		t.Errorf("reuse_visibility = %q; want %q", ev.ReuseVisibility, atom.ReuseTenant)
	}
	if ev.PreviousReuseVisibility != string(prev) {
		t.Errorf("previous_visibility = %q; want %q", ev.PreviousReuseVisibility, prev)
	}
	if ev.EventID == "" || ev.IdempotencyKey == "" {
		t.Errorf("event_id/idempotency_key must be minted (got %q / %q)", ev.EventID, ev.IdempotencyKey)
	}
	if ev.TraceParent != "tp-1" || ev.TraceState != "ts-1" {
		t.Errorf("trace context = %q/%q; want tp-1/ts-1", ev.TraceParent, ev.TraceState)
	}
	// OccurredAt is the change instant (UpdatedAt stamped by the mutation).
	occurred, err := time.Parse(time.RFC3339Nano, ev.OccurredAt)
	if err != nil {
		t.Fatalf("occurred_at parse: %v", err)
	}
	if !occurred.Equal(a.UpdatedAt.UTC()) {
		t.Errorf("occurred_at = %v; want the mutation UpdatedAt %v", occurred, a.UpdatedAt.UTC())
	}
}
