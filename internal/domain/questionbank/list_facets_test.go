// list_facets_test.go — RED-first unit tests for the listMyQuestionBanks facets
// (CHO-1899 BE gap #2, 2026-06-28): name-search (Q) + tag-filter (Tags, OR-any)
// + whitelisted sort. Pure-domain — no infrastructure imports.
package questionbank_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

func TestListFilter_MatchesQ_CaseInsensitiveSubstring(t *testing.T) {
	f := questionbank.ListFilter{Q: "alg"}
	if !f.MatchesQ("Algebra Basics") {
		t.Errorf("expected 'alg' to match 'Algebra Basics' (case-insensitive)")
	}
	if f.MatchesQ("Geometry") {
		t.Errorf("'alg' should not match 'Geometry'")
	}
	if !(questionbank.ListFilter{}).MatchesQ("anything") {
		t.Errorf("empty Q should match all")
	}
}

func TestListFilter_MatchesTags_ORAnyCaseSensitive(t *testing.T) {
	f := questionbank.ListFilter{Tags: []string{"exam-2026", "review"}}
	if !f.MatchesTags([]string{"review", "warmup"}) {
		t.Errorf("bank with 'review' should match an [exam-2026,review] filter (OR-any)")
	}
	if f.MatchesTags([]string{"warmup"}) {
		t.Errorf("bank with no overlapping tag should not match")
	}
	// Case-sensitive — mirrors the TEXT[] && overlap operator.
	if f.MatchesTags([]string{"Review"}) {
		t.Errorf("'Review' must NOT match 'review' — tag match is case-sensitive")
	}
	if !(questionbank.ListFilter{}).MatchesTags([]string{"x"}) {
		t.Errorf("empty Tags should match all")
	}
}

func TestParseSorts_BankFieldsAndMultiKey(t *testing.T) {
	if s, err := questionbank.ParseSorts(""); err != nil || s != nil {
		t.Errorf("empty sort = (%v,%v); want (nil,nil)", s, err)
	}
	s, err := questionbank.ParseSorts("name:asc,created_at:desc")
	if err != nil {
		t.Fatalf("ParseSorts: %v", err)
	}
	if len(s) != 2 || s[0].Field != "name" || s[0].Direction != "asc" || s[1].Field != "created_at" {
		t.Errorf("multi-key sort = %v; want [{name,asc},{created_at,desc}]", s)
	}
}

func TestParseSorts_RejectsUnknownFieldOrDirOrShape(t *testing.T) {
	for _, bad := range []string{"bogus:asc", "name:sideways", "name", "owner_gcid:asc", "visibility:desc"} {
		if _, err := questionbank.ParseSorts(bad); err == nil {
			t.Errorf("ParseSorts(%q) = nil err; want rejection", bad)
		}
	}
}

func TestSort_ColumnWhitelist(t *testing.T) {
	for field, want := range map[string]string{"name": "name", "created_at": "created_at", "updated_at": "updated_at"} {
		col, ok := questionbank.Sort{Field: field, Direction: "asc"}.Column()
		if !ok || col != want {
			t.Errorf("Column(%q) = (%q,%v); want (%q,true)", field, col, ok, want)
		}
	}
	if _, ok := (questionbank.Sort{Field: "tenant_id); DROP"}).Column(); ok {
		t.Errorf("non-whitelisted field returned ok=true — injection surface")
	}
}

func TestSortBanks_DefaultCreatedAtDescWithIDTiebreak(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	banks := []*questionbank.QuestionBank{
		{QuestionBankID: "b", Name: "old", CreatedAt: base},
		{QuestionBankID: "a", Name: "new", CreatedAt: base.Add(2 * time.Hour)},
		{QuestionBankID: "c", Name: "mid", CreatedAt: base.Add(time.Hour)},
	}
	questionbank.SortBanks(banks, nil) // nil = default created_at DESC
	if banks[0].Name != "new" || banks[1].Name != "mid" || banks[2].Name != "old" {
		t.Errorf("default sort order = %s,%s,%s; want new,mid,old (created_at DESC)",
			banks[0].Name, banks[1].Name, banks[2].Name)
	}

	// Equal created_at → question_bank_id DESC tiebreak.
	tied := []*questionbank.QuestionBank{
		{QuestionBankID: "a", CreatedAt: base},
		{QuestionBankID: "c", CreatedAt: base},
		{QuestionBankID: "b", CreatedAt: base},
	}
	questionbank.SortBanks(tied, nil)
	if tied[0].QuestionBankID != "c" || tied[1].QuestionBankID != "b" || tied[2].QuestionBankID != "a" {
		t.Errorf("tiebreak order = %s,%s,%s; want c,b,a (question_bank_id DESC)",
			tied[0].QuestionBankID, tied[1].QuestionBankID, tied[2].QuestionBankID)
	}
}

func TestSort_SQLDirection(t *testing.T) {
	if d := (questionbank.Sort{Direction: "asc"}).SQLDirection(); d != "ASC" {
		t.Errorf("asc → %q; want ASC", d)
	}
	if d := (questionbank.Sort{Direction: "desc"}).SQLDirection(); d != "DESC" {
		t.Errorf("desc → %q; want DESC", d)
	}
	if d := (questionbank.Sort{Direction: "garbage"}).SQLDirection(); d != "DESC" {
		t.Errorf("unknown direction → %q; want DESC fallback", d)
	}
}

func TestSortBanks_UpdatedAtAsc(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	banks := []*questionbank.QuestionBank{
		{QuestionBankID: "1", Name: "late", UpdatedAt: base.Add(2 * time.Hour)},
		{QuestionBankID: "2", Name: "early", UpdatedAt: base},
		{QuestionBankID: "3", Name: "mid", UpdatedAt: base.Add(time.Hour)},
	}
	questionbank.SortBanks(banks, []questionbank.Sort{{Field: "updated_at", Direction: "asc"}})
	if banks[0].Name != "early" || banks[1].Name != "mid" || banks[2].Name != "late" {
		t.Errorf("updated_at asc order = %s,%s,%s; want early,mid,late",
			banks[0].Name, banks[1].Name, banks[2].Name)
	}
}

func TestSortBanks_NameAsc(t *testing.T) {
	banks := []*questionbank.QuestionBank{
		{QuestionBankID: "1", Name: "Charlie"},
		{QuestionBankID: "2", Name: "Alpha"},
		{QuestionBankID: "3", Name: "Bravo"},
	}
	questionbank.SortBanks(banks, []questionbank.Sort{{Field: "name", Direction: "asc"}})
	if banks[0].Name != "Alpha" || banks[1].Name != "Bravo" || banks[2].Name != "Charlie" {
		t.Errorf("name asc order = %s,%s,%s; want Alpha,Bravo,Charlie",
			banks[0].Name, banks[1].Name, banks[2].Name)
	}
}
