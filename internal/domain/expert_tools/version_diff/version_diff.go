// Package version_diff models a line-level diff between two LearningAtom
// revisions for the expert-tooling diff viewer.
//
// Use case (per docs/design/ux_expert_tooling.md): instructors compare two
// revisions side-by-side to see what changed (peer review, audit, undo
// guidance). Diff is computed via classic LCS over newline-split lines.
//
// Hexagonal: dependency-free w.r.t. infrastructure. The diff is a pure
// function — no persistence is mandatory; the chora-creation HTTP handler
// can serve the result directly without storing it (caching may be added
// downstream via Memorystore for hot pairs).
package version_diff

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxBodyBytes caps the per-side body size to keep the LCS matrix bounded.
// 256 KiB matches the per-Pub/Sub-message ceiling and is well above the
// expected atom-body distribution (LearningAtom bodies are typically short
// MCQ stems / flashcard backs).
const MaxBodyBytes = 256 * 1024

// -----------------------------------------------------------------------------
// Op + Hunk
// -----------------------------------------------------------------------------

// Op labels the kind of change for a Hunk line.
type Op string

const (
	OpUnchanged Op = "unchanged"
	OpInsert    Op = "insert"
	OpDelete    Op = "delete"
)

// Hunk is one line of the diff with its position in the base / head body.
//
// Positions are 1-indexed for human display. -1 indicates "not present in
// that side" (e.g. an Insert has BaseLineNumber = -1).
type Hunk struct {
	Op             Op     `json:"op"`
	BaseLineNumber int    `json:"base_line_number"`
	HeadLineNumber int    `json:"head_line_number"`
	Text           string `json:"text"`
}

// -----------------------------------------------------------------------------
// Diff aggregate
// -----------------------------------------------------------------------------

// Diff is the result of comparing two atom revisions.
type Diff struct {
	DiffID         string    `json:"diff_id"`
	TenantID       string    `json:"tenant_id"`
	AtomID         string    `json:"atom_id"`
	BaseRevisionID string    `json:"base_revision_id"`
	HeadRevisionID string    `json:"head_revision_id"`
	Hunks          []Hunk    `json:"hunks"`
	AdditionsCount int       `json:"additions_count"`
	DeletionsCount int       `json:"deletions_count"`
	UnchangedCount int       `json:"unchanged_count"`
	ComputedAt     time.Time `json:"computed_at"`
}

// ComputeParams is the input to Compute.
type ComputeParams struct {
	TenantID       string
	AtomID         string
	BaseRevisionID string
	BaseBody       string
	HeadRevisionID string
	HeadBody       string
}

// Compute produces a line-level diff between BaseBody and HeadBody using
// classic LCS. The output is deterministic.
func Compute(p ComputeParams) (*Diff, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("atom_id is required")
	}
	if strings.TrimSpace(p.BaseRevisionID) == "" {
		return nil, errors.New("base_revision_id is required")
	}
	if strings.TrimSpace(p.HeadRevisionID) == "" {
		return nil, errors.New("head_revision_id is required")
	}
	if p.BaseRevisionID == p.HeadRevisionID {
		return nil, errors.New("base_revision_id and head_revision_id must differ")
	}
	if len(p.BaseBody) > MaxBodyBytes {
		return nil, fmt.Errorf("base body too large: %d > %d", len(p.BaseBody), MaxBodyBytes)
	}
	if len(p.HeadBody) > MaxBodyBytes {
		return nil, fmt.Errorf("head body too large: %d > %d", len(p.HeadBody), MaxBodyBytes)
	}

	base := splitLines(p.BaseBody)
	head := splitLines(p.HeadBody)
	hunks := lcsDiff(base, head)

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}

	d := &Diff{
		DiffID:         id.String(),
		TenantID:       p.TenantID,
		AtomID:         p.AtomID,
		BaseRevisionID: p.BaseRevisionID,
		HeadRevisionID: p.HeadRevisionID,
		Hunks:          hunks,
		ComputedAt:     time.Now().UTC(),
	}
	for _, h := range hunks {
		switch h.Op {
		case OpInsert:
			d.AdditionsCount++
		case OpDelete:
			d.DeletionsCount++
		case OpUnchanged:
			d.UnchangedCount++
		}
	}
	return d, nil
}

// splitLines splits s on '\n'; an empty input yields an empty slice.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// -----------------------------------------------------------------------------
// LCS-based line diff (classic dynamic programming)
// -----------------------------------------------------------------------------

// lcsDiff produces a hunk list using the standard LCS edit-script algorithm.
//
// O(N*M) time + space. With MaxBodyBytes = 256 KiB and average line length
// ~ 80 bytes the worst case is ~3300 x 3300 = 11M — acceptable for an
// instructor-tooling endpoint that does not run on the learner-facing hot
// path.
func lcsDiff(a, b []string) []Hunk {
	n, m := len(a), len(b)
	// Build LCS DP table.
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				if dp[i-1][j] >= dp[i][j-1] {
					dp[i][j] = dp[i-1][j]
				} else {
					dp[i][j] = dp[i][j-1]
				}
			}
		}
	}

	// Backtrack to produce the edit script.
	hunks := make([]Hunk, 0, n+m)
	i, j := n, m
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			hunks = append(hunks, Hunk{
				Op: OpUnchanged, BaseLineNumber: i, HeadLineNumber: j, Text: a[i-1],
			})
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			hunks = append(hunks, Hunk{
				Op: OpDelete, BaseLineNumber: i, HeadLineNumber: -1, Text: a[i-1],
			})
			i--
		} else {
			hunks = append(hunks, Hunk{
				Op: OpInsert, BaseLineNumber: -1, HeadLineNumber: j, Text: b[j-1],
			})
			j--
		}
	}
	for i > 0 {
		hunks = append(hunks, Hunk{
			Op: OpDelete, BaseLineNumber: i, HeadLineNumber: -1, Text: a[i-1],
		})
		i--
	}
	for j > 0 {
		hunks = append(hunks, Hunk{
			Op: OpInsert, BaseLineNumber: -1, HeadLineNumber: j, Text: b[j-1],
		})
		j--
	}

	// Backtracking produced reverse order; reverse in place.
	for l, r := 0, len(hunks)-1; l < r; l, r = l+1, r-1 {
		hunks[l], hunks[r] = hunks[r], hunks[l]
	}
	return hunks
}
