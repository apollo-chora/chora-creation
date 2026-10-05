// Package atom_test exercises the LearningAtom aggregate root invariants.
//
// TDD RED phase — these tests assume the implementation does NOT yet exist.
// Each test names an invariant from .claude/rules/ddd-enforcement.md or the
// OpenAPI contract chora-contracts/openapi/creation-admin.yaml.
package atom_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

// -----------------------------------------------------------------------------
// New / construction invariants
// -----------------------------------------------------------------------------

func TestNew_AssignsUUIDv7AtomID(t *testing.T) {
	t.Parallel()

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA,
		Gcid:     gcidA,
		Title:    "Sample title",
		Body:     "Hello body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	if a.AtomID == "" {
		t.Errorf("AtomID was empty; expected UUIDv7")
	}
	// UUIDv7 has version nibble 7 in the 13th hex char (index 14 in canonical
	// 8-4-4-4-12 form). Smoke-check it.
	if len(a.AtomID) != 36 {
		t.Errorf("AtomID length = %d; want 36", len(a.AtomID))
	}
	if a.AtomID[14] != '7' {
		t.Errorf("AtomID version char = %q; want '7' (UUIDv7)", string(a.AtomID[14]))
	}
}

func TestNew_DefaultsToDraftStatus(t *testing.T) {
	t.Parallel()

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if a.Status != atom.StatusDraft {
		t.Errorf("Status = %q; want %q", a.Status, atom.StatusDraft)
	}
}

func TestNew_StartsAtRevisionOne(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if a.Revision != 1 {
		t.Errorf("Revision = %d; want 1", a.Revision)
	}
}

func TestNew_SetsCreatedAndUpdatedAt(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	after := time.Now().UTC()

	if a.CreatedAt.Before(before) || a.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v; want between %v and %v", a.CreatedAt, before, after)
	}
	if !a.UpdatedAt.Equal(a.CreatedAt) {
		t.Errorf("UpdatedAt = %v; want equal to CreatedAt %v on creation", a.UpdatedAt, a.CreatedAt)
	}
}

func TestNew_DeletedAtIsNilOnCreation(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if a.DeletedAt != nil {
		t.Errorf("DeletedAt = %v; want nil on creation", a.DeletedAt)
	}
}

func TestNew_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()

	_, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "   ", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err == nil {
		t.Errorf("expected error for empty title; got nil")
	}
}

func TestNew_RejectsTitleOver256Chars(t *testing.T) {
	t.Parallel()

	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	_, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: string(long), Body: "y", Mode: atom.ModeStraightUp,
	})
	if err == nil {
		t.Errorf("expected error for title > 256 chars; got nil")
	}
}

func TestNew_RejectsMissingTenantID(t *testing.T) {
	t.Parallel()

	_, err := atom.New(atom.NewParams{
		TenantID: "", Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err == nil {
		t.Errorf("expected error for missing TenantID; got nil")
	}
}

func TestNew_RejectsMissingGcid(t *testing.T) {
	t.Parallel()

	_, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: "", Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err == nil {
		t.Errorf("expected error for missing Gcid; got nil")
	}
}

func TestNew_RejectsInvalidMode(t *testing.T) {
	t.Parallel()

	_, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: "freeform",
	})
	if err == nil {
		t.Errorf("expected error for invalid mode; got nil")
	}
}

func TestNew_AcceptsModeStraightUpAndGraphBased(t *testing.T) {
	t.Parallel()

	for _, m := range []atom.Mode{atom.ModeStraightUp, atom.ModeGraphBased} {
		_, err := atom.New(atom.NewParams{
			TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: m,
		})
		if err != nil {
			t.Errorf("New(mode=%q) unexpected error: %v", m, err)
		}
	}
}

func TestNew_NormalisesTrimsTitleAndBody(t *testing.T) {
	t.Parallel()

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA,
		Title: "  trimmed  ", Body: "  body  ", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Title != "trimmed" {
		t.Errorf("Title = %q; want %q", a.Title, "trimmed")
	}
	if a.Body != "body" {
		t.Errorf("Body = %q; want %q", a.Body, "body")
	}
}

func TestNew_CopiesTagsDefensively(t *testing.T) {
	t.Parallel()

	tags := []string{"go", "ddd"}
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA,
		Title: "x", Body: "y", Mode: atom.ModeStraightUp, Tags: tags,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	tags[0] = "MUTATED"
	if a.Tags[0] == "MUTATED" {
		t.Errorf("Tags should be copied defensively; got mutation leakage")
	}
}

// -----------------------------------------------------------------------------
// Update / patch invariants
// -----------------------------------------------------------------------------

func TestApplyUpdate_BumpsRevisionAndUpdatedAt(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	prevRev := a.Revision
	prevUpdated := a.UpdatedAt

	// Tiny sleep so UpdatedAt is observably newer (1 ms).
	time.Sleep(time.Millisecond)

	newTitle := "updated"
	if err := a.ApplyUpdate(atom.UpdateParams{Title: &newTitle}); err != nil {
		t.Fatalf("ApplyUpdate unexpected error: %v", err)
	}

	if a.Revision != prevRev+1 {
		t.Errorf("Revision = %d; want %d", a.Revision, prevRev+1)
	}
	if !a.UpdatedAt.After(prevUpdated) {
		t.Errorf("UpdatedAt = %v; want after %v", a.UpdatedAt, prevUpdated)
	}
	if a.Title != newTitle {
		t.Errorf("Title = %q; want %q", a.Title, newTitle)
	}
}

func TestApplyUpdate_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	empty := "   "
	if err := a.ApplyUpdate(atom.UpdateParams{Title: &empty}); err == nil {
		t.Errorf("expected error for empty title; got nil")
	}
}

func TestApplyUpdate_RejectsTitleOver256(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	s := string(long)
	if err := a.ApplyUpdate(atom.UpdateParams{Title: &s}); err == nil {
		t.Errorf("expected error for oversize title; got nil")
	}
}

func TestApplyUpdate_RejectsInvalidMode(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	bad := atom.Mode("freeform")
	if err := a.ApplyUpdate(atom.UpdateParams{Mode: &bad}); err == nil {
		t.Errorf("expected error for invalid mode; got nil")
	}
}

func TestApplyUpdate_AppliesBodyTagsAndMode(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	newBody := "  fresh body  "
	newTags := []string{"go", "tdd"}
	newMode := atom.ModeGraphBased
	if err := a.ApplyUpdate(atom.UpdateParams{
		Body: &newBody, Tags: &newTags, Mode: &newMode,
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Body != "fresh body" {
		t.Errorf("Body = %q; want %q", a.Body, "fresh body")
	}
	if len(a.Tags) != 2 || a.Tags[0] != "go" {
		t.Errorf("Tags = %v; want [go tdd]", a.Tags)
	}
	if a.Mode != atom.ModeGraphBased {
		t.Errorf("Mode = %q; want %q", a.Mode, atom.ModeGraphBased)
	}
	// Verify Tags were copied defensively from caller's slice.
	newTags[0] = "MUTATED"
	if a.Tags[0] == "MUTATED" {
		t.Errorf("Tags should be copied defensively on update")
	}
}

func TestApplyUpdate_RejectsOnSoftDeletedAtom(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err := a.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete unexpected error: %v", err)
	}

	newTitle := "updated"
	if err := a.ApplyUpdate(atom.UpdateParams{Title: &newTitle}); err == nil {
		t.Errorf("expected error updating a soft-deleted atom; got nil")
	}
}

// -----------------------------------------------------------------------------
// Soft-delete invariants (ddd-enforcement aggregate invariant #5)
// -----------------------------------------------------------------------------

func TestSoftDelete_SetsDeletedAt(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err := a.SoftDelete(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.DeletedAt == nil {
		t.Errorf("DeletedAt = nil; want non-nil after SoftDelete")
	}
	if a.Status != atom.StatusArchived {
		t.Errorf("Status = %q; want %q after SoftDelete", a.Status, atom.StatusArchived)
	}
}

func TestSoftDelete_IsIdempotent(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = a.SoftDelete()
	first := *a.DeletedAt
	if err := a.SoftDelete(); err != nil {
		t.Errorf("second SoftDelete should be no-op; got error %v", err)
	}
	if !a.DeletedAt.Equal(first) {
		t.Errorf("second SoftDelete should not change DeletedAt; was %v now %v", first, *a.DeletedAt)
	}
}

func TestIsActive_FalseAfterSoftDelete(t *testing.T) {
	t.Parallel()

	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if !a.IsActive() {
		t.Errorf("IsActive() = false; want true on fresh atom")
	}
	_ = a.SoftDelete()
	if a.IsActive() {
		t.Errorf("IsActive() = true; want false after soft delete")
	}
}

// -----------------------------------------------------------------------------
// Publish — A22 Phase K (publishAtom)
// -----------------------------------------------------------------------------

func TestPublish_FlipsDraftToPublished(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if a.Status != atom.StatusDraft {
		t.Fatalf("seed status = %v; want draft", a.Status)
	}
	prevUpdated := a.UpdatedAt
	time.Sleep(time.Millisecond) // ensure UpdatedAt strictly later

	if err := a.Publish(); err != nil {
		t.Fatalf("Publish() returned %v; want nil", err)
	}
	if a.Status != atom.StatusPublished {
		t.Errorf("status = %v; want published", a.Status)
	}
	if !a.UpdatedAt.After(prevUpdated) {
		t.Errorf("UpdatedAt = %v; want strictly later than %v", a.UpdatedAt, prevUpdated)
	}
}

func TestPublish_IdempotentOnAlreadyPublished(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = a.Publish()
	first := a.UpdatedAt

	if err := a.Publish(); err != nil {
		t.Errorf("re-Publish should be no-op; got %v", err)
	}
	if !a.UpdatedAt.Equal(first) {
		t.Errorf("re-Publish must not bump UpdatedAt; was %v now %v", first, a.UpdatedAt)
	}
	if a.Status != atom.StatusPublished {
		t.Errorf("status after re-Publish = %v; want published", a.Status)
	}
}

func TestPublish_RejectsArchived(t *testing.T) {
	t.Parallel()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = a.SoftDelete()
	err := a.Publish()
	if err == nil {
		t.Fatalf("Publish() on archived atom should error; got nil")
	}
	if err != atom.ErrAtomArchived {
		t.Errorf("err = %v; want ErrAtomArchived", err)
	}
	if a.Status != atom.StatusArchived {
		t.Errorf("status after rejected Publish = %v; want archived", a.Status)
	}
}

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — new types + struct extensions per plan §1
// -----------------------------------------------------------------------------

// QuestionType is the rename of AtomType per Decision #2. The old identifier
// MUST remain available as a type alias for one release cycle so existing
// callers keep compiling (backwards-compat shim per plan §8).
func TestQuestionType_AliasingPreservesAtomType(t *testing.T) {
	t.Parallel()
	// AtomType is a type alias of QuestionType; assignment between the two
	// must compile + interconvert without an explicit cast.
	var qt atom.QuestionType = atom.QuestionTypeMCQ
	var at atom.AtomType = qt // alias compat
	if string(at) != "mcq" {
		t.Errorf("AtomType alias lost value; got %q want %q", at, "mcq")
	}
	if qt != atom.QuestionType(atom.TypeMCQ) {
		t.Errorf("QuestionType cross-cast mismatch")
	}
}

func TestQuestionType_Valid(t *testing.T) {
	t.Parallel()
	for _, qt := range []atom.QuestionType{
		atom.QuestionTypeMCQ, atom.QuestionTypeFlashcard, atom.QuestionTypeVideo,
		atom.QuestionTypeEssay, atom.QuestionTypeOutline,
	} {
		if !qt.Valid() {
			t.Errorf("QuestionType %q expected Valid()=true", qt)
		}
	}
	if atom.QuestionType("not-a-type").Valid() {
		t.Errorf("invalid QuestionType slipped Valid()")
	}
}

// CognitiveLevel — Bloom 6-enum per Decision #3.
func TestCognitiveLevel_Valid(t *testing.T) {
	t.Parallel()
	for _, c := range []atom.CognitiveLevel{
		atom.CognitiveLevelKnowledge,
		atom.CognitiveLevelComprehension,
		atom.CognitiveLevelApplication,
		atom.CognitiveLevelAnalysis,
		atom.CognitiveLevelSynthesis,
		atom.CognitiveLevelEvaluation,
	} {
		if !c.Valid() {
			t.Errorf("CognitiveLevel %q expected Valid()=true", c)
		}
	}
	if atom.CognitiveLevel("").Valid() {
		t.Errorf("empty CognitiveLevel should NOT be Valid()")
	}
	if atom.CognitiveLevel("applying").Valid() {
		t.Errorf("non-Bloom CognitiveLevel slipped Valid()")
	}
}

// ImdaDimTag — 4-value enum per Decision #6 + ADR-141 canonical labels.
func TestImdaDimTag_Valid(t *testing.T) {
	t.Parallel()
	for _, i := range []atom.ImdaDimTag{
		atom.ImdaDimAccountability,
		atom.ImdaDimTransparency,
		atom.ImdaDimSafetyRobustness,
		atom.ImdaDimFairnessHumanOversight,
	} {
		if !i.Valid() {
			t.Errorf("ImdaDimTag %q expected Valid()=true", i)
		}
	}
	if atom.ImdaDimTag("").Valid() {
		t.Errorf("empty ImdaDimTag should NOT be Valid()")
	}
	if atom.ImdaDimTag("internal_governance").Valid() {
		t.Errorf("deprecated label internal_governance must not validate")
	}
}

// MediaAsset — Phase 1 ≤1 image entry per Decision #5.
func TestMediaAsset_ValidImage(t *testing.T) {
	t.Parallel()
	m := atom.MediaAsset{
		Type:      "image",
		URL:       "gs://chora-atom-media-dev/x.png",
		AltText:   "diagram",
		MIME:      "image/png",
		SizeBytes: 12345,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() returned %v; want nil", err)
	}
}

func TestMediaAsset_RejectsNonImageType(t *testing.T) {
	t.Parallel()
	m := atom.MediaAsset{Type: "video", URL: "gs://x.mp4", MIME: "video/mp4", SizeBytes: 1}
	if err := m.Validate(); err == nil {
		t.Errorf("expected error for type='video' in Phase 1; got nil")
	}
}

func TestMediaAsset_RejectsUnsupportedMIME(t *testing.T) {
	t.Parallel()
	m := atom.MediaAsset{Type: "image", URL: "gs://x.bmp", MIME: "image/bmp", SizeBytes: 1}
	if err := m.Validate(); err == nil {
		t.Errorf("expected error for image/bmp; got nil")
	}
}

func TestMediaAsset_RejectsZeroSize(t *testing.T) {
	t.Parallel()
	m := atom.MediaAsset{Type: "image", URL: "gs://x.png", MIME: "image/png", SizeBytes: 0}
	if err := m.Validate(); err == nil {
		t.Errorf("expected error for size_bytes=0; got nil")
	}
}

func TestMediaAsset_RejectsMissingURL(t *testing.T) {
	t.Parallel()
	m := atom.MediaAsset{Type: "image", MIME: "image/jpeg", SizeBytes: 1}
	if err := m.Validate(); err == nil {
		t.Errorf("expected error for missing url; got nil")
	}
}

// LearningAtom.Validate covers the Phase 1 invariants: stem ≤4096, title
// ≤256 (optional), subject ≤64, cognitive_level Bloom enum, IMDA enum array,
// media_assets ≤1 image with valid MIME.
func TestLearningAtom_ValidatePhase1_OK(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID:         "00000000-0000-7000-8000-000000000001",
		TenantID:       tenantA,
		Gcid:           gcidA,
		Stem:           "What gas do plants release as byproduct of photosynthesis?",
		Title:          "Photosynthesis basics",
		Subject:        "biology",
		CognitiveLevel: atom.CognitiveLevelComprehension,
		ImdaDimensionTags: []atom.ImdaDimTag{
			atom.ImdaDimTransparency,
		},
		AuthorNote:   "Phyllis demo MCQ",
		QuestionType: atom.QuestionTypeMCQ,
		Mode:         atom.ModeStraightUp,
		Status:       atom.StatusDraft,
		Revision:     1,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := a.ValidatePhase1(); err != nil {
		t.Fatalf("ValidatePhase1 returned %v; want nil", err)
	}
}

func TestLearningAtom_ValidatePhase1_RejectsEmptyStem(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA, Stem: "",
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for empty stem; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsOversizeStem(t *testing.T) {
	t.Parallel()
	long := make([]byte, 4097)
	for i := range long {
		long[i] = 'a'
	}
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA, Stem: string(long),
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for stem > 4096; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_AllowsEmptyTitle(t *testing.T) {
	t.Parallel()
	// Title is OPTIONAL per Decision #1.
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok", Title: "",
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err != nil {
		t.Errorf("empty title should be permitted; got %v", err)
	}
}

func TestLearningAtom_ValidatePhase1_RejectsOversizeTitle(t *testing.T) {
	t.Parallel()
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok", Title: string(long),
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for title > 256; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsOversizeSubject(t *testing.T) {
	t.Parallel()
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok", Subject: string(long),
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for subject > 64; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsInvalidCognitiveLevel(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem:           "ok",
		CognitiveLevel: atom.CognitiveLevel("applying"),
		QuestionType:   atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for non-Bloom cognitive_level; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_AllowsEmptyCognitiveLevel(t *testing.T) {
	t.Parallel()
	// CognitiveLevel is OPTIONAL initially (Decision #3).
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem:           "ok",
		CognitiveLevel: atom.CognitiveLevel(""),
		QuestionType:   atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err != nil {
		t.Errorf("empty cognitive_level should be permitted; got %v", err)
	}
}

func TestLearningAtom_ValidatePhase1_RejectsInvalidImdaDimensionTag(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok",
		ImdaDimensionTags: []atom.ImdaDimTag{
			atom.ImdaDimTag("internal_governance"), // deprecated v1 label
		},
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for deprecated IMDA label; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsImdaTagOver4(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok",
		// 5 entries — exceeds maxItems=4 from OpenAPI.
		ImdaDimensionTags: []atom.ImdaDimTag{
			atom.ImdaDimAccountability,
			atom.ImdaDimTransparency,
			atom.ImdaDimSafetyRobustness,
			atom.ImdaDimFairnessHumanOversight,
			atom.ImdaDimAccountability,
		},
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for > 4 IMDA tags; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsMoreThanOneMediaAsset(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok",
		MediaAssets: []atom.MediaAsset{
			{Type: "image", URL: "gs://a.png", MIME: "image/png", SizeBytes: 1},
			{Type: "image", URL: "gs://b.png", MIME: "image/png", SizeBytes: 1},
		},
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for > 1 media asset in Phase 1; got nil")
	}
}

func TestLearningAtom_ValidatePhase1_RejectsInvalidMediaAsset(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{
		AtomID: "x", TenantID: tenantA, Gcid: gcidA,
		Stem: "ok",
		MediaAssets: []atom.MediaAsset{
			{Type: "image", URL: "gs://a.bmp", MIME: "image/bmp", SizeBytes: 1},
		},
		QuestionType: atom.QuestionTypeMCQ, Mode: atom.ModeStraightUp,
		Status: atom.StatusDraft,
	}
	if err := a.ValidatePhase1(); err == nil {
		t.Errorf("expected error for unsupported MIME; got nil")
	}
}

// DisplayLabel — FE convenience derived from stem when title empty per
// Decision #1. Centralised in the domain so all surfaces use the same shape.
func TestLearningAtom_DisplayLabel_FallsBackToStem(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{Stem: "What gas do plants release?", Title: ""}
	got := a.DisplayLabel()
	if got != "What gas do plants release?" {
		t.Errorf("DisplayLabel() = %q; want stem", got)
	}
}

func TestLearningAtom_DisplayLabel_PrefersTitleWhenPresent(t *testing.T) {
	t.Parallel()
	a := &atom.LearningAtom{Stem: "stem text", Title: "Photosynthesis"}
	if a.DisplayLabel() != "Photosynthesis" {
		t.Errorf("DisplayLabel() = %q; want title", a.DisplayLabel())
	}
}

func TestLearningAtom_DisplayLabel_TrimsToReasonableLengthFromStem(t *testing.T) {
	t.Parallel()
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	a := &atom.LearningAtom{Stem: string(long), Title: ""}
	out := a.DisplayLabel()
	if len(out) == 0 {
		t.Errorf("DisplayLabel() returned empty")
	}
	if len(out) > 256 {
		t.Errorf("DisplayLabel() = %d chars; want ≤256 (display cap)", len(out))
	}
}
