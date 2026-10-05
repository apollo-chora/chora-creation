// Package atom_test exercises the Phyllis MVP extension of the LearningAtom
// aggregate (Comic Ch6 P12, docs/m13/phyllis-mvp-2026-05-08.md §5.3).
//
// TDD RED phase — these tests describe the new course-bound atom + revision
// behaviour that must be added without breaking the existing M10 skeleton
// surface (atom_test.go).
package atom_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const (
	courseA = "01970000-0000-7000-7000-000000000001"
)

// -----------------------------------------------------------------------------
// NewBound: Phyllis-MVP atom with course_id + atom_type + difficulty + first
// revision auto-attached. Per spec §5.3: POST /atoms creates LearningAtom +
// first AtomRevision (append-only) in one go.
// -----------------------------------------------------------------------------

func TestNewBound_AssignsCourseAndType(t *testing.T) {
	t.Parallel()

	a, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   courseA,
		Title:      "What is Agile?",
		Body:       "Agile is...",
		AtomType:   atom.TypeMCQ,
		Difficulty: 3,
		Tags:       []string{"agile", "scrum"},
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("NewBound unexpected error: %v", err)
	}
	if a.CourseID != courseA {
		t.Errorf("CourseID = %q; want %q", a.CourseID, courseA)
	}
	// ADR-156 Decision #2: struct field renamed AtomType → QuestionType.
	// Type alias `type QuestionType = AtomType` keeps atom.TypeMCQ comparable.
	if a.QuestionType != atom.TypeMCQ {
		t.Errorf("QuestionType = %q; want %q", a.QuestionType, atom.TypeMCQ)
	}
	if a.Difficulty != 3 {
		t.Errorf("Difficulty = %d; want 3", a.Difficulty)
	}
	if a.AtomID == "" || a.AtomID[14] != '7' {
		t.Errorf("AtomID is not UUIDv7: %q", a.AtomID)
	}
}

func TestNewBound_AutoCreatesFirstRevision(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   courseA,
		Title:      "x",
		Body:       "first body content",
		AtomType:   atom.TypeFlashcard,
		Difficulty: 1,
		SourceType: atom.SourceManual,
	})

	cur := a.CurrentRevision()
	if cur == nil {
		t.Fatalf("CurrentRevision = nil; want first revision auto-attached")
	}
	if cur.Body != "first body content" {
		t.Errorf("revision body = %q; want %q", cur.Body, "first body content")
	}
	if cur.SourceType != atom.SourceManual {
		t.Errorf("revision SourceType = %q; want %q", cur.SourceType, atom.SourceManual)
	}
	if cur.RevisionNumber != 1 {
		t.Errorf("revision number = %d; want 1", cur.RevisionNumber)
	}
	if cur.AuthoredBy != gcidA {
		t.Errorf("revision AuthoredBy = %q; want %q", cur.AuthoredBy, gcidA)
	}
	if cur.RevisionID == "" || cur.RevisionID[14] != '7' {
		t.Errorf("RevisionID is not UUIDv7: %q", cur.RevisionID)
	}
}

func TestNewBound_RejectsInvalidAtomType(t *testing.T) {
	t.Parallel()

	_, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   courseA,
		Title:      "x",
		Body:       "y",
		AtomType:   atom.AtomType("freeform"),
		Difficulty: 3,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for invalid atom type; got nil")
	}
}

func TestNewBound_RejectsMissingCourse(t *testing.T) {
	t.Parallel()

	_, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   "",
		Title:      "x",
		Body:       "y",
		AtomType:   atom.TypeMCQ,
		Difficulty: 3,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for missing course_id; got nil")
	}
}

func TestNewBound_RejectsBadDifficulty(t *testing.T) {
	t.Parallel()

	for _, d := range []int{-1, 6, 100} {
		_, err := atom.NewBound(atom.NewBoundParams{
			TenantID:   tenantA,
			Gcid:       gcidA,
			CourseID:   courseA,
			Title:      "x",
			Body:       "y",
			AtomType:   atom.TypeMCQ,
			Difficulty: d,
			SourceType: atom.SourceManual,
		})
		if err == nil {
			t.Errorf("expected error for difficulty=%d; got nil", d)
		}
	}
}

func TestNewBound_AcceptsAllAtomTypes(t *testing.T) {
	t.Parallel()

	for _, k := range []atom.AtomType{
		atom.TypeMCQ, atom.TypeFlashcard, atom.TypeVideo,
		atom.TypeEssay, atom.TypeOutline,
	} {
		_, err := atom.NewBound(atom.NewBoundParams{
			TenantID:   tenantA,
			Gcid:       gcidA,
			CourseID:   courseA,
			Title:      "x",
			Body:       "y",
			AtomType:   k,
			Difficulty: 1,
			SourceType: atom.SourceManual,
		})
		if err != nil {
			t.Errorf("type=%q unexpected error: %v", k, err)
		}
	}
}

func TestNewBound_RejectsBadSourceType(t *testing.T) {
	t.Parallel()

	_, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   courseA,
		Title:      "x",
		Body:       "y",
		AtomType:   atom.TypeMCQ,
		Difficulty: 3,
		SourceType: atom.SourceType("hand-drawn"),
	})
	if err == nil {
		t.Errorf("expected error for invalid source_type; got nil")
	}
}

// -----------------------------------------------------------------------------
// AppendRevision: append-only invariant per ddd-enforcement.md
// -----------------------------------------------------------------------------

func TestAppendRevision_AppendsWithIncrementingNumber(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantA,
		Gcid:       gcidA,
		CourseID:   courseA,
		Title:      "x",
		Body:       "v1",
		AtomType:   atom.TypeMCQ,
		Difficulty: 3,
		SourceType: atom.SourceManual,
	})
	first := a.CurrentRevision().RevisionID

	r2, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       "v2",
		AuthoredBy: gcidA,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("AppendRevision unexpected error: %v", err)
	}
	if r2.RevisionNumber != 2 {
		t.Errorf("second revision number = %d; want 2", r2.RevisionNumber)
	}
	if r2.RevisionID == first {
		t.Errorf("second revision must have new RevisionID; got %q same as first", r2.RevisionID)
	}
	if a.CurrentRevision().RevisionID != r2.RevisionID {
		t.Errorf("CurrentRevision must point at latest; got %q want %q",
			a.CurrentRevision().RevisionID, r2.RevisionID)
	}
}

func TestAppendRevision_HistoryStrictlyIncreasing(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	for i := 0; i < 4; i++ {
		_, err := a.AppendRevision(atom.AppendRevisionParams{
			Body:       "iter",
			AuthoredBy: gcidA,
			SourceType: atom.SourceAIAssist,
		})
		if err != nil {
			t.Fatalf("AppendRevision[%d] unexpected error: %v", i, err)
		}
	}
	hist := a.RevisionHistory()
	if len(hist) != 5 {
		t.Fatalf("history length = %d; want 5", len(hist))
	}
	for i := 1; i < len(hist); i++ {
		if hist[i].RevisionNumber != hist[i-1].RevisionNumber+1 {
			t.Errorf("revision number not strictly increasing at index %d", i)
		}
		if !hist[i].AuthoredAt.After(hist[i-1].AuthoredAt) &&
			!hist[i].AuthoredAt.Equal(hist[i-1].AuthoredAt) {
			t.Errorf("authored_at went backwards at index %d", i)
		}
	}
}

func TestAppendRevision_RejectsOnSoftDeletedAtom(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	_ = a.SoftDelete()

	_, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       "v2",
		AuthoredBy: gcidA,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error appending revision to soft-deleted atom")
	}
}

func TestAppendRevision_PriorRevisionsRemainImmutable(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	first := a.CurrentRevision()
	firstID := first.RevisionID
	firstBody := first.Body
	firstAuthored := first.AuthoredAt

	_, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       "v2",
		AuthoredBy: gcidA,
		SourceType: atom.SourceAIAssist,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	hist := a.RevisionHistory()
	if hist[0].RevisionID != firstID {
		t.Errorf("first revision RevisionID changed: was %q now %q", firstID, hist[0].RevisionID)
	}
	if hist[0].Body != firstBody {
		t.Errorf("first revision body changed (append-only violation): was %q now %q",
			firstBody, hist[0].Body)
	}
	if !hist[0].AuthoredAt.Equal(firstAuthored) {
		t.Errorf("first revision AuthoredAt mutated")
	}
	if hist[0].RevisionNumber != 1 {
		t.Errorf("first revision number changed: %d", hist[0].RevisionNumber)
	}
}

func TestAppendRevision_RecordsSourceMetadata(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	r, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       "v2",
		AuthoredBy: gcidA,
		SourceType: atom.SourceAIAssist,
		SourceMetadata: map[string]string{
			"model_used":         "vertex/gemini-1.5-pro",
			"screening_decision": "allow",
			"prompt_hash":        "abc123",
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.SourceMetadata["model_used"] != "vertex/gemini-1.5-pro" {
		t.Errorf("SourceMetadata[model_used] = %q; want vertex/gemini-1.5-pro",
			r.SourceMetadata["model_used"])
	}
	if r.SourceMetadata["screening_decision"] != "allow" {
		t.Errorf("SourceMetadata[screening_decision] missing")
	}
}

func TestAppendRevision_RejectsEmptyBody(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	_, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       strings.Repeat(" ", 4),
		AuthoredBy: gcidA,
		SourceType: atom.SourceManual,
	})
	if err == nil {
		t.Errorf("expected error for empty body; got nil")
	}
}

// -----------------------------------------------------------------------------
// Cascade soft-delete: aggregate root soft-delete also "hides" revision
// history queries on soft-deleted atoms (per ddd-enforcement invariant #6).
// -----------------------------------------------------------------------------

func TestSoftDelete_HidesRevisionHistoryFromActiveQueries(t *testing.T) {
	t.Parallel()

	a, _ := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA, Title: "x", Body: "v1",
		AtomType: atom.TypeMCQ, Difficulty: 3, SourceType: atom.SourceManual,
	})
	_ = a.SoftDelete()
	if a.IsActive() {
		t.Errorf("IsActive() = true after SoftDelete; want false")
	}
	// History is preserved (audit trail) but consumers can check IsActive
	// before exposing revisions to learners.
	if len(a.RevisionHistory()) != 1 {
		t.Errorf("revision history length = %d; want 1 (preserved)",
			len(a.RevisionHistory()))
	}
	// And the deleted_at timestamp is set.
	if a.DeletedAt == nil {
		t.Errorf("DeletedAt = nil after soft delete")
	}
	// Ensure the deletion time is non-zero.
	if a.DeletedAt.Equal(time.Time{}) {
		t.Errorf("DeletedAt is zero value")
	}
}
