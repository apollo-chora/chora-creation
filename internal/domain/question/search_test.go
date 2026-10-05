// search_test.go — unit tests for the SearchFilter value object that powers
// the cross-atom Question picker (FE A+ X.2 test-set editor; ADR-155 D1).
//
// Per .claude/rules/development-execution.md TDD — these tests are RED first
// (no Validate / Normalize implementation yet), GREEN after the impl lands.
package question_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	gcid1 = "01970000-0000-7000-9000-000000000001"
	atom1 = "01970000-0000-7000-8000-0000000000a1"
	atom2 = "01970000-0000-7000-8000-0000000000a2"
)

func TestSearchFilter_Normalize_DefaultsPaginationToPage1Per20(t *testing.T) {
	f := question.SearchFilter{}
	n := f.Normalize()
	if n.Page != 1 {
		t.Errorf("default Page = %d; want 1", n.Page)
	}
	if n.Per != 20 {
		t.Errorf("default Per = %d; want 20", n.Per)
	}
}

func TestSearchFilter_Normalize_CapsPerAt100(t *testing.T) {
	f := question.SearchFilter{Per: 250}
	n := f.Normalize()
	if n.Per != 100 {
		t.Errorf("Per cap = %d; want 100", n.Per)
	}
}

func TestSearchFilter_Normalize_NegativePageBecomes1(t *testing.T) {
	f := question.SearchFilter{Page: -5}
	n := f.Normalize()
	if n.Page != 1 {
		t.Errorf("negative Page coerced to %d; want 1", n.Page)
	}
}

func TestSearchFilter_Normalize_TrimsQuery(t *testing.T) {
	f := question.SearchFilter{Q: "  math  "}
	n := f.Normalize()
	if n.Q != "math" {
		t.Errorf("Q = %q; want %q", n.Q, "math")
	}
}

func TestSearchFilter_Normalize_LowercasesAndDeduplicatesTypes(t *testing.T) {
	f := question.SearchFilter{Types: []string{"MCQ", "mcq", "OE", "outline", "OUTLINE"}}
	n := f.Normalize()
	got := strings.Join(n.Types, ",")
	// Order doesn't matter for the contract — only the dedup'd lowercase set.
	for _, want := range []string{"mcq", "oe", "outline"} {
		if !strings.Contains(got, want) {
			t.Errorf("Types missing %q (got %q)", want, got)
		}
	}
	if len(n.Types) != 3 {
		t.Errorf("Types len = %d; want 3 (deduplicated)", len(n.Types))
	}
}

func TestSearchFilter_Validate_RejectsLongQ(t *testing.T) {
	long := strings.Repeat("x", 4097)
	f := question.SearchFilter{Q: long}
	if err := f.Validate(); err == nil {
		t.Errorf("expected error for q longer than 4096; got nil")
	}
}

func TestSearchFilter_Validate_AcceptsEmptyFilter(t *testing.T) {
	f := question.SearchFilter{}
	if err := f.Validate(); err != nil {
		t.Errorf("empty filter should validate; got %v", err)
	}
}

func TestSearchFilter_Offset_ReturnsZeroBasedOffsetFromPagePer(t *testing.T) {
	f := question.SearchFilter{Page: 3, Per: 20}.Normalize()
	if f.Offset() != 40 {
		t.Errorf("Offset(page=3, per=20) = %d; want 40", f.Offset())
	}
	// Page 1 = offset 0.
	f1 := question.SearchFilter{Page: 1, Per: 20}.Normalize()
	if f1.Offset() != 0 {
		t.Errorf("Offset(page=1) = %d; want 0", f1.Offset())
	}
}

func TestSearchFilter_MatchesType(t *testing.T) {
	f := question.SearchFilter{Types: []string{"mcq", "oe"}}.Normalize()
	if !f.MatchesType("mcq") {
		t.Errorf("expected mcq to match")
	}
	if !f.MatchesType("oe") {
		t.Errorf("expected oe to match")
	}
	if f.MatchesType("outline") {
		t.Errorf("outline should not match a [mcq,oe] filter")
	}
	// Empty Types == "match all".
	empty := question.SearchFilter{}.Normalize()
	if !empty.MatchesType("anything") {
		t.Errorf("empty Types filter should match anything")
	}
}

// -----------------------------------------------------------------------------
// CHO-1899 atom-sharing-redesign — extended filters (author_gcid / tag / state
// / atom_id) + sort. RED first per .claude/rules/development-execution.md.
// -----------------------------------------------------------------------------

func TestSearchFilter_Normalize_TrimsAndDedupesNewSliceFields(t *testing.T) {
	f := question.SearchFilter{
		AuthorGCID: "  01970000-0000-7000-9000-000000000001  ",
		Tags:       []string{" exam-2026 ", "exam-2026", "", "review"},
		States:     []string{"DRAFT", "draft", "PUBLISHED"},
		AtomIDs:    []string{" 01970000-0000-7000-8000-0000000000a1 ", "01970000-0000-7000-8000-0000000000a1"},
	}.Normalize()

	if f.AuthorGCID != "01970000-0000-7000-9000-000000000001" {
		t.Errorf("AuthorGCID = %q; want trimmed", f.AuthorGCID)
	}
	if len(f.Tags) != 2 {
		t.Errorf("Tags = %v; want 2 (trimmed + deduped, case PRESERVED)", f.Tags)
	}
	// State is closed-vocab → lowercased + deduped.
	if len(f.States) != 2 {
		t.Errorf("States = %v; want 2 (draft,published deduped+lowercased)", f.States)
	}
	if len(f.AtomIDs) != 1 {
		t.Errorf("AtomIDs = %v; want 1 (deduped)", f.AtomIDs)
	}
}

func TestSearchFilter_Normalize_PreservesTagCase(t *testing.T) {
	// Tags map to the JSONB `?|` operator which is CASE-SENSITIVE; Normalize
	// must NOT lowercase free-form tags or the GIN match silently misses.
	f := question.SearchFilter{Tags: []string{"Algebra", "Geometry"}}.Normalize()
	joined := strings.Join(f.Tags, ",")
	if !strings.Contains(joined, "Algebra") || !strings.Contains(joined, "Geometry") {
		t.Errorf("Tags = %v; want original case preserved", f.Tags)
	}
}

func TestSearchFilter_MatchesAuthor(t *testing.T) {
	f := question.SearchFilter{AuthorGCID: gcid1}.Normalize()
	if !f.MatchesAuthor(gcid1) {
		t.Errorf("expected author %q to match", gcid1)
	}
	if f.MatchesAuthor("01970000-0000-7000-9000-000000000099") {
		t.Errorf("a different author should not match")
	}
	if !(question.SearchFilter{}).MatchesAuthor("anyone") {
		t.Errorf("empty AuthorGCID should match anyone")
	}
}

func TestSearchFilter_MatchesState(t *testing.T) {
	f := question.SearchFilter{States: []string{"draft", "published"}}.Normalize()
	if !f.MatchesState("draft") || !f.MatchesState("PUBLISHED") {
		t.Errorf("draft + published (case-insensitive) should match")
	}
	if f.MatchesState("archived") {
		t.Errorf("archived should not match a [draft,published] filter")
	}
	if !(question.SearchFilter{}).MatchesState("archived") {
		t.Errorf("empty States should match all")
	}
}

func TestSearchFilter_MatchesAtomID(t *testing.T) {
	f := question.SearchFilter{AtomIDs: []string{atom1, atom2}}.Normalize()
	if !f.MatchesAtomID(atom1) || !f.MatchesAtomID(atom2) {
		t.Errorf("both atom ids should match")
	}
	if f.MatchesAtomID("01970000-0000-7000-8000-0000000000ff") {
		t.Errorf("a third atom id should not match")
	}
	if !(question.SearchFilter{}).MatchesAtomID("anything") {
		t.Errorf("empty AtomIDs should match all")
	}
}

func TestSearchFilter_MatchesTags_ORWithinFamily_CaseSensitive(t *testing.T) {
	f := question.SearchFilter{Tags: []string{"exam-2026", "review"}}.Normalize()
	// OR within family: an atom carrying ANY of the filter tags matches.
	if !f.MatchesTags([]string{"review", "warmup"}) {
		t.Errorf("atom with 'review' should match an [exam-2026,review] filter")
	}
	if f.MatchesTags([]string{"warmup", "cooldown"}) {
		t.Errorf("atom with none of the filter tags should not match")
	}
	// Case-sensitive (mirrors the JSONB `?|` operator).
	if f.MatchesTags([]string{"Review"}) {
		t.Errorf("'Review' must NOT match 'review' — tag match is case-sensitive")
	}
	if !(question.SearchFilter{}).MatchesTags([]string{"x"}) {
		t.Errorf("empty Tags should match all")
	}
}

func TestParseSorts_DefaultAndSingleKey(t *testing.T) {
	if s, err := question.ParseSorts(""); err != nil || s != nil {
		t.Errorf("empty sort = (%v,%v); want (nil,nil) so caller applies default", s, err)
	}
	s, err := question.ParseSorts("created_at:desc")
	if err != nil {
		t.Fatalf("ParseSorts: %v", err)
	}
	if len(s) != 1 || s[0].Field != "created_at" || s[0].Direction != "desc" {
		t.Errorf("ParseSorts(created_at:desc) = %v; want one {created_at,desc}", s)
	}
}

func TestParseSorts_MultiKeyOrdered(t *testing.T) {
	s, err := question.ParseSorts("prompt:asc,created_at:desc")
	if err != nil {
		t.Fatalf("ParseSorts: %v", err)
	}
	if len(s) != 2 || s[0].Field != "prompt" || s[0].Direction != "asc" || s[1].Field != "created_at" {
		t.Errorf("multi-key sort = %v; want [{prompt,asc},{created_at,desc}]", s)
	}
}

func TestParseSorts_RejectsUnknownFieldOrDirectionOrShape(t *testing.T) {
	for _, bad := range []string{"bogus:asc", "created_at:sideways", "created_at", "nonsuch:asc", ":asc", "created_at:"} {
		if _, err := question.ParseSorts(bad); err == nil {
			t.Errorf("ParseSorts(%q) = nil err; want rejection (no injection / unknown field)", bad)
		}
	}
}

func TestSort_ColumnWhitelist_MapsToQualifiedColumns(t *testing.T) {
	// ADR-206: atom sort columns are qualified `la.` (the search LEFT JOINs the
	// `questions q` row); `prompt` sorts by the display-label expression (live
	// prompt, stem/title fallback for a draft atom with no question yet).
	col, ok := question.Sort{Field: "prompt", Direction: "asc"}.Column()
	if !ok || col != "COALESCE(q.prompt, la.stem, la.title)" {
		t.Errorf("prompt Column() = (%q,%v); want (COALESCE(q.prompt, la.stem, la.title),true)", col, ok)
	}
	col, ok = question.Sort{Field: "created_at"}.Column()
	if !ok || col != "la.created_at" {
		t.Errorf("created_at Column() = (%q,%v); want (la.created_at,true)", col, ok)
	}
	col, ok = question.Sort{Field: "title"}.Column()
	if !ok || col != "la.title" {
		t.Errorf("title Column() = (%q,%v); want (la.title,true)", col, ok)
	}
	col, ok = question.Sort{Field: "question_type"}.Column()
	if !ok || col != "la.question_type" {
		t.Errorf("question_type Column() = (%q,%v); want (la.question_type,true)", col, ok)
	}
	if _, ok := (question.Sort{Field: "evil); DROP TABLE"}).Column(); ok {
		t.Errorf("non-whitelisted field returned ok=true — injection surface")
	}
}

// TestParseSorts_AcceptsPickerSortOptions guards the live atom-question-picker
// sort dropdown: title:asc + question_type:asc were a 400 regression before the
// question-search whitelist was widened to match the picker's real options.
func TestParseSorts_AcceptsPickerSortOptions(t *testing.T) {
	for _, opt := range []string{"created_at:desc", "created_at:asc", "updated_at:desc", "title:asc", "question_type:asc"} {
		if _, err := question.ParseSorts(opt); err != nil {
			t.Errorf("ParseSorts(%q) = %v; want accepted (live picker sort option)", opt, err)
		}
	}
}

func TestSort_SQLDirection(t *testing.T) {
	if d := (question.Sort{Direction: "asc"}).SQLDirection(); d != "ASC" {
		t.Errorf("asc → %q; want ASC", d)
	}
	if d := (question.Sort{Direction: "desc"}).SQLDirection(); d != "DESC" {
		t.Errorf("desc → %q; want DESC", d)
	}
	if d := (question.Sort{Direction: "garbage"}).SQLDirection(); d != "DESC" {
		t.Errorf("unknown direction → %q; want DESC fallback", d)
	}
}

func TestSearchFilter_Validate_RejectsBadSort(t *testing.T) {
	f := question.SearchFilter{Sorts: []question.Sort{{Field: "bogus", Direction: "asc"}}}
	if err := f.Validate(); err == nil {
		t.Errorf("expected Validate to reject a non-whitelisted sort field")
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 — GrantedAtomIDs (the granted disjunct's hydrated id list).
// -----------------------------------------------------------------------------

func TestSearchFilter_Normalize_GrantedAtomIDs_LowercasesAndDedupes(t *testing.T) {
	f := question.SearchFilter{
		GrantedAtomIDs: []string{
			" 0197AAAA-0000-7000-8000-000000000001 ",
			"0197aaaa-0000-7000-8000-000000000001",
			"",
			"0197bbbb-0000-7000-8000-000000000002",
		},
	}.Normalize()
	want := []string{
		"0197aaaa-0000-7000-8000-000000000001",
		"0197bbbb-0000-7000-8000-000000000002",
	}
	if len(f.GrantedAtomIDs) != len(want) {
		t.Fatalf("GrantedAtomIDs = %v; want %v", f.GrantedAtomIDs, want)
	}
	for i := range want {
		if f.GrantedAtomIDs[i] != want[i] {
			t.Errorf("GrantedAtomIDs[%d] = %q; want %q", i, f.GrantedAtomIDs[i], want[i])
		}
	}
}

func TestSearchFilter_Normalize_GrantedAtomIDs_EmptyYieldsNil(t *testing.T) {
	f := question.SearchFilter{GrantedAtomIDs: []string{"", "  "}}.Normalize()
	if f.GrantedAtomIDs != nil {
		t.Errorf("GrantedAtomIDs = %v; want nil", f.GrantedAtomIDs)
	}
}
