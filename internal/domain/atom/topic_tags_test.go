package atom_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// newDraft builds a minimal valid DRAFT atom for the topic-tag tests.
func newDraft(t *testing.T, tags []string) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: "11111111-1111-7111-8111-111111111111",
		Gcid:     "00000000-0000-7000-8000-000000001999",
		Title:    "Comparing Fractions",
		Body:     "Which fraction is larger?",
		Tags:     tags,
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	return a
}

func TestNormalizeTopicTags_TrimsLowercasesDedupes(t *testing.T) {
	got, err := atom.NormalizeTopicTags([]string{"  Fractions ", "NUMBER-SENSE", "fractions", "", "  "})
	if err != nil {
		t.Fatalf("NormalizeTopicTags: %v", err)
	}
	want := []string{"fractions", "number-sense"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNormalizeTopicTags_EmptyResultIsError(t *testing.T) {
	// A classifier that returns nothing usable must FAIL LOUD, never write an
	// empty tag set that silently reproduces the very bug we are fixing.
	if _, err := atom.NormalizeTopicTags([]string{"", "   "}); !errors.Is(err, atom.ErrNoTopicTags) {
		t.Fatalf("want ErrNoTopicTags, got %v", err)
	}
	if _, err := atom.NormalizeTopicTags(nil); !errors.Is(err, atom.ErrNoTopicTags) {
		t.Fatalf("want ErrNoTopicTags for nil, got %v", err)
	}
}

func TestNormalizeTopicTags_RejectsOverlongTag(t *testing.T) {
	long := strings.Repeat("x", atom.MaxTopicTagLen+1)
	if _, err := atom.NormalizeTopicTags([]string{long}); err == nil {
		t.Fatal("want error for overlong tag, got nil")
	}
}

func TestNormalizeTopicTags_CapsTagCount(t *testing.T) {
	in := make([]string, 0, atom.MaxTopicTags+3)
	for i := 0; i < atom.MaxTopicTags+3; i++ {
		in = append(in, string(rune('a'+i))+"-topic")
	}
	got, err := atom.NormalizeTopicTags(in)
	if err != nil {
		t.Fatalf("NormalizeTopicTags: %v", err)
	}
	if len(got) != atom.MaxTopicTags {
		t.Fatalf("want capped at %d, got %d", atom.MaxTopicTags, len(got))
	}
}

func TestSetTopicTags_SetsNormalisedTagsAndDoesNotBumpRevision(t *testing.T) {
	a := newDraft(t, nil)
	beforeRev := a.Revision
	beforeUpdated := a.UpdatedAt

	if err := a.SetTopicTags([]string{"Fractions", "number-sense"}); err != nil {
		t.Fatalf("SetTopicTags: %v", err)
	}

	if len(a.Tags) != 2 || a.Tags[0] != "fractions" || a.Tags[1] != "number-sense" {
		t.Fatalf("tags not normalised: %v", a.Tags)
	}
	// A classification backfill enriches metadata; it is NOT a content edit.
	// Bumping Revision would misreport the atom's edit history and desync the
	// revision pinned on atom.published.v1.
	if a.Revision != beforeRev {
		t.Fatalf("Revision must not bump: %d -> %d", beforeRev, a.Revision)
	}
	if !a.UpdatedAt.After(beforeUpdated) && !a.UpdatedAt.Equal(beforeUpdated) {
		t.Fatal("UpdatedAt must be stamped")
	}
}

func TestSetTopicTags_RefusesSoftDeleted(t *testing.T) {
	a := newDraft(t, nil)
	if err := a.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := a.SetTopicTags([]string{"fractions"}); err == nil {
		t.Fatal("want error on soft-deleted atom, got nil")
	}
}

func TestSetTopicTags_RefusesOrphanEdition(t *testing.T) {
	a := newDraft(t, nil)
	a.OrphanedFromAtomID = "019f278f-5acb-7415-a526-eb852b6409c7" // frozen orphan (ADR-229 A1.2)
	if err := a.SetTopicTags([]string{"fractions"}); !errors.Is(err, atom.ErrOrphanFrozen) {
		t.Fatalf("want ErrOrphanFrozen, got %v", err)
	}
}

func TestSetTopicTags_EmptyIsRejected(t *testing.T) {
	a := newDraft(t, []string{"existing"})
	if err := a.SetTopicTags(nil); !errors.Is(err, atom.ErrNoTopicTags) {
		t.Fatalf("want ErrNoTopicTags, got %v", err)
	}
	// The existing tags must survive a rejected write.
	if len(a.Tags) != 1 || a.Tags[0] != "existing" {
		t.Fatalf("tags mutated on rejected write: %v", a.Tags)
	}
}

func TestNeedsTopicTags(t *testing.T) {
	if !newDraft(t, nil).NeedsTopicTags() {
		t.Fatal("nil tags must need classification")
	}
	if !newDraft(t, []string{"  "}).NeedsTopicTags() {
		t.Fatal("blank-only tags must need classification")
	}
	if newDraft(t, []string{"fractions"}).NeedsTopicTags() {
		t.Fatal("tagged atom must NOT need classification")
	}
}
