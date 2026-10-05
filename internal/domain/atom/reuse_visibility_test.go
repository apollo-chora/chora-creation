// reuse_visibility_test.go — RED specs for the ADR-229 WS-1 reuse-consent
// flag (CHO-2127).
//
// reuse_visibility is the CREATION-owned consent bit (decoupled from
// sharing's license/promotion/grants): who may REUSE this atom — private
// (author only, the default), friends (author's explicit friend set, hidden
// in A+ until the friends audience un-hides), or tenant. Playback is
// untouched. Only the AUTHOR may change it — the first author-authz guard in
// the creation domain.
package atom_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func newReuseFixture(t *testing.T) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: "11111111-1111-7111-8111-111111111111",
		Gcid:     "00000000-0000-7000-8000-0000000000aa",
		Title:    "Photosynthesis",
		Body:     "What does chlorophyll absorb?",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	return a
}

func TestNewAtom_DefaultsReuseVisibilityPrivate(t *testing.T) {
	t.Parallel()
	a := newReuseFixture(t)
	if a.ReuseVisibility != atom.ReusePrivate {
		t.Fatalf("ReuseVisibility = %q; want default %q (consent-first, ADR-229)",
			a.ReuseVisibility, atom.ReusePrivate)
	}
}

func TestParseReuseVisibility(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"private", "friends", "tenant"} {
		if _, err := atom.ParseReuseVisibility(ok); err != nil {
			t.Errorf("ParseReuseVisibility(%q) unexpected error: %v", ok, err)
		}
	}
	if _, err := atom.ParseReuseVisibility("everyone"); err == nil {
		t.Errorf("ParseReuseVisibility must reject unknown labels")
	}
	if _, err := atom.ParseReuseVisibility(""); err == nil {
		t.Errorf("ParseReuseVisibility must reject empty")
	}
}

func TestChangeReuseVisibility_AuthorOnly(t *testing.T) {
	t.Parallel()
	a := newReuseFixture(t)

	// Non-author refused — the flag is the author's consent, nobody else's.
	if _, err := a.ChangeReuseVisibility("00000000-0000-7000-8000-0000000000bb", atom.ReuseTenant); !errors.Is(err, atom.ErrNotAuthor) {
		t.Fatalf("non-author change: err = %v; want ErrNotAuthor", err)
	}
	if a.ReuseVisibility != atom.ReusePrivate {
		t.Fatalf("refused change must not mutate; got %q", a.ReuseVisibility)
	}

	// Author widens: changed=true.
	changed, err := a.ChangeReuseVisibility(a.Gcid, atom.ReuseTenant)
	if err != nil {
		t.Fatalf("author change: %v", err)
	}
	if !changed || a.ReuseVisibility != atom.ReuseTenant {
		t.Fatalf("changed=%v vis=%q; want true/tenant", changed, a.ReuseVisibility)
	}

	// Same value: changed=false (no event fires on a no-op).
	changed, err = a.ChangeReuseVisibility(a.Gcid, atom.ReuseTenant)
	if err != nil {
		t.Fatalf("no-op change: %v", err)
	}
	if changed {
		t.Fatalf("same-value change must report changed=false")
	}

	// Soft-deleted atom refuses mutation.
	a.SoftDelete()
	if _, err := a.ChangeReuseVisibility(a.Gcid, atom.ReusePrivate); err == nil {
		t.Fatalf("soft-deleted atom must refuse reuse-visibility change")
	}
}
