package atom_variants

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	codeStarterMaxLen  = 65536
	codeSolutionMaxLen = 65536
	codeMaxTestCases   = 32
)

// codeLanguageWhitelist is the set of accepted lowercase language tags.
// Adding a language requires updating both this map AND the executor
// service's sandbox image (out-of-scope for this MVP).
var codeLanguageWhitelist = map[string]struct{}{
	"go":         {},
	"python":     {},
	"javascript": {},
	"typescript": {},
	"java":       {},
	"rust":       {},
	"sql":        {},
}

// CodeTestCase is a single (input -> expected_output) pair for the executor.
type CodeTestCase struct {
	Name           string `json:"name"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
}

// CodeEditor payload — interactive code-execution atom.
type CodeEditor struct {
	VariantID   string         `json:"variant_id"`
	AtomID      string         `json:"atom_id"`
	VariantType VariantType    `json:"type"`
	Language    string         `json:"language"`
	StarterCode string         `json:"starter_code"`
	TestCases   []CodeTestCase `json:"test_cases"`
	Solution    string         `json:"solution"`
	PublishedAt time.Time      `json:"published_at"`
}

// CodeEditorParams is the constructor input.
type CodeEditorParams struct {
	AtomID      string
	Language    string
	StarterCode string
	TestCases   []CodeTestCase
	Solution    string
}

// NewCodeEditor constructs a validated CodeEditor with a UUIDv7 id.
func NewCodeEditor(p CodeEditorParams) (*CodeEditor, error) {
	if err := requireAtom(p.AtomID); err != nil {
		return nil, err
	}
	if p.Language == "" {
		return nil, errors.New("language is required")
	}
	if _, ok := codeLanguageWhitelist[p.Language]; !ok {
		return nil, fmt.Errorf("language %q not in whitelist (must be lowercase)", p.Language)
	}
	if len(p.StarterCode) > codeStarterMaxLen {
		return nil, fmt.Errorf("starter_code too long: %d > %d", len(p.StarterCode), codeStarterMaxLen)
	}
	if strings.TrimSpace(p.Solution) == "" {
		return nil, errors.New("solution is required")
	}
	if len(p.Solution) > codeSolutionMaxLen {
		return nil, fmt.Errorf("solution too long: %d > %d", len(p.Solution), codeSolutionMaxLen)
	}
	if len(p.TestCases) == 0 {
		return nil, errors.New("at least one test_case is required")
	}
	if len(p.TestCases) > codeMaxTestCases {
		return nil, fmt.Errorf("too many test_cases: %d > %d", len(p.TestCases), codeMaxTestCases)
	}
	cases := make([]CodeTestCase, 0, len(p.TestCases))
	seen := make(map[string]struct{}, len(p.TestCases))
	for i, tc := range p.TestCases {
		if strings.TrimSpace(tc.Name) == "" {
			return nil, fmt.Errorf("test_case[%d]: name is required", i)
		}
		if _, dup := seen[tc.Name]; dup {
			return nil, fmt.Errorf("test_case[%d]: duplicate name %q", i, tc.Name)
		}
		seen[tc.Name] = struct{}{}
		if strings.TrimSpace(tc.ExpectedOutput) == "" {
			return nil, fmt.Errorf("test_case[%d]: expected_output is required", i)
		}
		cases = append(cases, tc)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	return &CodeEditor{
		VariantID:   id.String(),
		AtomID:      p.AtomID,
		VariantType: VariantTypeCodeEditor,
		Language:    p.Language,
		StarterCode: p.StarterCode,
		TestCases:   cases,
		Solution:    p.Solution,
		PublishedAt: time.Now().UTC(),
	}, nil
}

// Type implements Variant.
func (c *CodeEditor) Type() VariantType { return VariantTypeCodeEditor }

// GetAtomID implements Variant.
func (c *CodeEditor) GetAtomID() string { return c.AtomID }

// GetPublishedAt implements Variant.
func (c *CodeEditor) GetPublishedAt() time.Time { return c.PublishedAt }
