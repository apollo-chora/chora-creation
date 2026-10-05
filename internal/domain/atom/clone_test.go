// clone_test.go — RED-first unit tests for atom.Clone (ADR-199 clone-as-variant,
// Wave 2). Pure-domain: the clone copies the source's content into a NEW draft
// atom owned by the caller, stamping cloned_from_atom_id provenance.
package atom_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func srcAtom() *atom.LearningAtom {
	return &atom.LearningAtom{
		AtomID:         "01970000-0000-7000-8000-0000000000a1",
		TenantID:       "01970000-0000-7000-8000-0000000000t1",
		Gcid:           "01970000-0000-7000-9000-0000000000au",
		Title:          "Original",
		Body:           "body text",
		Tags:           []string{"algebra", "exam-2026"},
		Mode:           atom.ModeStraightUp,
		Status:         atom.StatusPublished,
		QuestionType:   "mcq",
		Difficulty:     3,
		Stem:           "What is 2+2?",
		Subject:        "math",
		CognitiveLevel: atom.CognitiveLevel("application"),
	}
}

func TestClone_CopiesContentNewDraftOwnedByCallerWithProvenance(t *testing.T) {
	src := srcAtom()
	out, err := atom.Clone(atom.CloneParams{
		Source:   src,
		TenantID: "01970000-0000-7000-8000-0000000000t2",
		Gcid:     "01970000-0000-7000-9000-0000000000ca",
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if out.AtomID == "" || out.AtomID == src.AtomID {
		t.Errorf("clone AtomID = %q; want a fresh non-source id", out.AtomID)
	}
	if out.Status != atom.StatusDraft {
		t.Errorf("clone Status = %q; want draft", out.Status)
	}
	if out.Gcid != "01970000-0000-7000-9000-0000000000ca" || out.TenantID != "01970000-0000-7000-8000-0000000000t2" {
		t.Errorf("clone owner = (%s,%s); want caller (ca,t2)", out.Gcid, out.TenantID)
	}
	if out.ClonedFromAtomID != src.AtomID {
		t.Errorf("clone ClonedFromAtomID = %q; want source %q", out.ClonedFromAtomID, src.AtomID)
	}
	if out.Title != "Copy of Original" {
		t.Errorf("clone Title = %q; want %q", out.Title, "Copy of Original")
	}
	// Content fields copied verbatim.
	if out.Stem != src.Stem || out.Body != src.Body || out.QuestionType != src.QuestionType ||
		out.Difficulty != src.Difficulty || out.Subject != src.Subject || out.CognitiveLevel != src.CognitiveLevel {
		t.Errorf("clone content not copied: %+v", out)
	}
	if len(out.Tags) != len(src.Tags) {
		t.Errorf("clone Tags = %v; want %v", out.Tags, src.Tags)
	}
	// Source MUST be untouched (append-only respected).
	if src.Status != atom.StatusPublished || src.Title != "Original" {
		t.Errorf("source mutated by Clone: %+v", src)
	}
	// Tags must be a defensive copy (not aliased to the source slice).
	out.Tags[0] = "MUTATED"
	if src.Tags[0] == "MUTATED" {
		t.Errorf("clone Tags alias the source slice")
	}
}

func TestClone_TitleOverride(t *testing.T) {
	out, err := atom.Clone(atom.CloneParams{
		Source: srcAtom(), TenantID: "t2", Gcid: "ca", TitleOverride: "My Variant",
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if out.Title != "My Variant" {
		t.Errorf("Title = %q; want override 'My Variant'", out.Title)
	}
}

func TestClone_NilSource(t *testing.T) {
	if _, err := atom.Clone(atom.CloneParams{TenantID: "t2", Gcid: "ca"}); err == nil {
		t.Errorf("expected error for nil source")
	}
}

func TestClone_RequiresCaller(t *testing.T) {
	if _, err := atom.Clone(atom.CloneParams{Source: srcAtom(), TenantID: "", Gcid: ""}); err == nil {
		t.Errorf("expected error for missing tenant/gcid")
	}
}
