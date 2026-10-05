package atom_variants_test

import (
	"strings"
	"testing"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

func TestCodeEditor_LanguageWhitelist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		lang    string
		wantErr bool
	}{
		{"go ok", "go", false},
		{"python ok", "python", false},
		{"javascript ok", "javascript", false},
		{"typescript ok", "typescript", false},
		{"java ok", "java", false},
		{"rust ok", "rust", false},
		{"sql ok", "sql", false},
		{"empty rejected", "", true},
		{"perl rejected", "perl", true},
		{"upper rejected (must be lowercase)", "GO", true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := av.NewCodeEditor(av.CodeEditorParams{
				AtomID:      atomID,
				Language:    tc.lang,
				StarterCode: "// stub",
				TestCases:   []av.CodeTestCase{{Name: "case-1", Input: "1", ExpectedOutput: "1"}},
				Solution:    "// solution",
			})
			if tc.wantErr && err == nil {
				t.Errorf("language %q expected error; got nil", tc.lang)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("language %q unexpected error: %v", tc.lang, err)
			}
		})
	}
}

func TestCodeEditor_RequiresAtomAndSolutionAndTestCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params av.CodeEditorParams
		want   string
	}{
		{
			name:   "missing atom",
			params: av.CodeEditorParams{Language: "go", StarterCode: "x", Solution: "x", TestCases: []av.CodeTestCase{{Name: "t", Input: "1", ExpectedOutput: "1"}}},
			want:   "atom",
		},
		{
			name:   "missing solution",
			params: av.CodeEditorParams{AtomID: atomID, Language: "go", StarterCode: "x", TestCases: []av.CodeTestCase{{Name: "t", Input: "1", ExpectedOutput: "1"}}},
			want:   "solution",
		},
		{
			name:   "no test cases",
			params: av.CodeEditorParams{AtomID: atomID, Language: "go", StarterCode: "x", Solution: "x", TestCases: []av.CodeTestCase{}},
			want:   "test_case",
		},
		{
			name: "test case missing name",
			params: av.CodeEditorParams{
				AtomID: atomID, Language: "go", StarterCode: "x", Solution: "x",
				TestCases: []av.CodeTestCase{{Input: "1", ExpectedOutput: "1"}},
			},
			want: "name",
		},
		{
			name: "test case missing expected output",
			params: av.CodeEditorParams{
				AtomID: atomID, Language: "go", StarterCode: "x", Solution: "x",
				TestCases: []av.CodeTestCase{{Name: "t", Input: "1"}},
			},
			want: "expected_output",
		},
		{
			name: "starter code too long",
			params: av.CodeEditorParams{
				AtomID: atomID, Language: "go", StarterCode: strings.Repeat("a", 65537),
				Solution:  "x",
				TestCases: []av.CodeTestCase{{Name: "t", Input: "1", ExpectedOutput: "1"}},
			},
			want: "starter",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := av.NewCodeEditor(tc.params)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want contains %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCodeEditor_HappyPath(t *testing.T) {
	t.Parallel()

	c, err := av.NewCodeEditor(av.CodeEditorParams{
		AtomID:      atomID,
		Language:    "go",
		StarterCode: "package main\n",
		TestCases:   []av.CodeTestCase{{Name: "smoke", Input: "1", ExpectedOutput: "2"}},
		Solution:    "package main // solution",
	})
	if err != nil {
		t.Fatalf("NewCodeEditor unexpected: %v", err)
	}
	if c.Type() != av.VariantTypeCodeEditor {
		t.Errorf("Type = %s; want code-editor", c.Type())
	}
	if len(c.TestCases) != 1 {
		t.Errorf("TestCases len = %d; want 1", len(c.TestCases))
	}
}
