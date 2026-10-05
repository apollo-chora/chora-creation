// Package questionbank_test exercises the QuestionBank aggregate invariants
// (W3.B.1 — reusable question-atom pool, 2026-06-28).
//
// TDD RED phase: these tests assert the aggregate constructor, mutators,
// invariants (name required, tag normalisation/cap, item-cap 1000,
// visibility ∈ {PRIVATE, TENANT_INTERNAL} — NO cross-tenant PUBLIC for
// exam-security) and soft-delete semantics BEFORE any implementation lands.
// They stay pure-domain — no infrastructure imports.
//
// Source-of-truth: W3.B.1 prompt + .claude/rules/ddd-enforcement.md +
// CLAUDE.md §1 (LearningAtom/Question primary aggregate roots; pools QUERY
// questions, never own them). Mirrors the proven Collection aggregate
// (internal/domain/collection) but for question references with tags.
package questionbank_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	ownerA  = "01970000-0000-7000-9000-000000000001"
)

// -----------------------------------------------------------------------------
// Constructor invariants
// -----------------------------------------------------------------------------

func TestNew_AssignsUUIDv7AndPrivateDefault(t *testing.T) {
	t.Parallel()

	b, err := questionbank.New(questionbank.NewParams{
		TenantID:  tenantA,
		OwnerGCID: ownerA,
		Name:      "Algebra mid-term pool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.QuestionBankID == "" {
		t.Errorf("QuestionBankID empty; want UUIDv7")
	}
	if b.Visibility != questionbank.VisibilityPrivate {
		t.Errorf("default visibility = %q; want PRIVATE", b.Visibility)
	}
	if b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", b.CreatedAt, b.UpdatedAt)
	}
	if b.DeletedAt != nil {
		t.Errorf("DeletedAt = %v; want nil", b.DeletedAt)
	}
	if len(b.Items) != 0 {
		t.Errorf("Items = %v; want empty", b.Items)
	}
}

func TestNew_RejectsMissingTenantID(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{OwnerGCID: ownerA, Name: "x"})
	if err == nil {
		t.Fatalf("expected error for missing tenant_id")
	}
}

func TestNew_RejectsMissingOwnerGCID(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{TenantID: tenantA, Name: "x"})
	if err == nil {
		t.Fatalf("expected error for missing owner_gcid")
	}
}

func TestNew_RejectsEmptyName(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{TenantID: tenantA, OwnerGCID: ownerA, Name: "   "})
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestNew_RejectsNameOver200Chars(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{
		TenantID:  tenantA,
		OwnerGCID: ownerA,
		Name:      strings.Repeat("a", 201),
	})
	if err == nil {
		t.Fatalf("expected error for >200-char name")
	}
}

func TestNew_RejectsDescriptionOver2000Chars(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{
		TenantID:    tenantA,
		OwnerGCID:   ownerA,
		Name:        "x",
		Description: strings.Repeat("d", 2001),
	})
	if err == nil {
		t.Fatalf("expected error for >2000-char description")
	}
}

func TestNew_AcceptsTenantInternalVisibility(t *testing.T) {
	t.Parallel()

	b, err := questionbank.New(questionbank.NewParams{
		TenantID:   tenantA,
		OwnerGCID:  ownerA,
		Name:       "Shared pool",
		Visibility: questionbank.VisibilityTenantInternal,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Visibility != questionbank.VisibilityTenantInternal {
		t.Errorf("Visibility = %q; want TENANT_INTERNAL", b.Visibility)
	}
}

func TestNew_RejectsInvalidVisibility(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{
		TenantID:   tenantA,
		OwnerGCID:  ownerA,
		Name:       "x",
		Visibility: questionbank.Visibility("nonsense"),
	})
	if err == nil {
		t.Fatalf("expected error for invalid visibility")
	}
}

// PUBLIC is deliberately NOT a valid QuestionBank visibility (exam-security:
// no cross-tenant question pools in v1).
func TestNew_RejectsPublicVisibility(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{
		TenantID:   tenantA,
		OwnerGCID:  ownerA,
		Name:       "x",
		Visibility: questionbank.Visibility("PUBLIC"),
	})
	if err == nil {
		t.Fatalf("expected error: PUBLIC is not a permitted QuestionBank visibility")
	}
}

// -----------------------------------------------------------------------------
// Tag normalisation (trim, dedupe, cap count + per-tag length)
// -----------------------------------------------------------------------------

func TestNew_TrimsAndDedupesTags(t *testing.T) {
	t.Parallel()

	b, err := questionbank.New(questionbank.NewParams{
		TenantID:  tenantA,
		OwnerGCID: ownerA,
		Name:      "x",
		Tags:      []string{" algebra ", "algebra", "  ", "geometry"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b.Tags) != 2 {
		t.Fatalf("Tags = %v; want 2 (deduped+trimmed, empties dropped)", b.Tags)
	}
	if b.Tags[0] != "algebra" || b.Tags[1] != "geometry" {
		t.Errorf("Tags = %v; want [algebra geometry]", b.Tags)
	}
}

func TestNew_RejectsTooManyTags(t *testing.T) {
	t.Parallel()

	tags := make([]string, questionbank.MaxTags+1)
	for i := range tags {
		tags[i] = "tag-" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	_, err := questionbank.New(questionbank.NewParams{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "x", Tags: tags,
	})
	if err == nil {
		t.Fatalf("expected error for >%d tags", questionbank.MaxTags)
	}
}

func TestNew_RejectsOverlongTag(t *testing.T) {
	t.Parallel()

	_, err := questionbank.New(questionbank.NewParams{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "x",
		Tags: []string{strings.Repeat("z", questionbank.MaxTagLength+1)},
	})
	if err == nil {
		t.Fatalf("expected error for tag longer than %d chars", questionbank.MaxTagLength)
	}
}

// -----------------------------------------------------------------------------
// UpdateMetadata semantics
// -----------------------------------------------------------------------------

func TestUpdateMetadata_ChangesNameAndBumpsUpdatedAt(t *testing.T) {
	t.Parallel()

	b := newB(t)
	prev := b.UpdatedAt

	newName := "Updated pool name"
	if err := b.UpdateMetadata(questionbank.UpdateParams{Name: &newName}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if b.Name != "Updated pool name" {
		t.Errorf("Name = %q; want updated", b.Name)
	}
	if !b.UpdatedAt.After(prev) {
		t.Errorf("UpdatedAt = %v; want after %v", b.UpdatedAt, prev)
	}
}

func TestUpdateMetadata_RejectsBlankName(t *testing.T) {
	t.Parallel()

	b := newB(t)
	blank := "   "
	if err := b.UpdateMetadata(questionbank.UpdateParams{Name: &blank}); err == nil {
		t.Fatalf("expected error for blank name")
	}
}

func TestUpdateMetadata_ChangesVisibility(t *testing.T) {
	t.Parallel()

	b := newB(t)
	v := questionbank.VisibilityTenantInternal
	if err := b.UpdateMetadata(questionbank.UpdateParams{Visibility: &v}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if b.Visibility != questionbank.VisibilityTenantInternal {
		t.Errorf("Visibility = %q; want TENANT_INTERNAL", b.Visibility)
	}
}

func TestUpdateMetadata_RejectsPublicVisibility(t *testing.T) {
	t.Parallel()

	b := newB(t)
	v := questionbank.Visibility("PUBLIC")
	if err := b.UpdateMetadata(questionbank.UpdateParams{Visibility: &v}); err == nil {
		t.Fatalf("expected error: cannot set PUBLIC visibility")
	}
}

func TestUpdateMetadata_ReplacesTags(t *testing.T) {
	t.Parallel()

	b := newB(t)
	tags := []string{" newtag ", "newtag", "second"}
	if err := b.UpdateMetadata(questionbank.UpdateParams{Tags: &tags}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if len(b.Tags) != 2 || b.Tags[0] != "newtag" || b.Tags[1] != "second" {
		t.Errorf("Tags = %v; want [newtag second]", b.Tags)
	}
}

func TestUpdateMetadata_ChangesDescription(t *testing.T) {
	t.Parallel()

	b := newB(t)
	desc := "  refreshed description  "
	if err := b.UpdateMetadata(questionbank.UpdateParams{Description: &desc}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if b.Description != "refreshed description" {
		t.Errorf("Description = %q; want trimmed value", b.Description)
	}
}

func TestUpdateMetadata_RejectsOverlongDescription(t *testing.T) {
	t.Parallel()

	b := newB(t)
	desc := strings.Repeat("d", questionbank.MaxDescriptionLength+1)
	if err := b.UpdateMetadata(questionbank.UpdateParams{Description: &desc}); err == nil {
		t.Fatalf("expected error for over-long description")
	}
}

func TestUpdateMetadata_RejectsOverlongName(t *testing.T) {
	t.Parallel()

	b := newB(t)
	name := strings.Repeat("n", questionbank.MaxNameLength+1)
	if err := b.UpdateMetadata(questionbank.UpdateParams{Name: &name}); err == nil {
		t.Fatalf("expected error for over-long name")
	}
}

func TestUpdateMetadata_RejectsBadTags(t *testing.T) {
	t.Parallel()

	b := newB(t)
	tags := []string{strings.Repeat("z", questionbank.MaxTagLength+1)}
	if err := b.UpdateMetadata(questionbank.UpdateParams{Tags: &tags}); err == nil {
		t.Fatalf("expected error for over-long tag on update")
	}
}

func TestUpdateMetadata_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()

	b := newB(t)
	if err := b.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	name := "x"
	if err := b.UpdateMetadata(questionbank.UpdateParams{Name: &name}); err == nil {
		t.Fatalf("expected error updating soft-deleted question bank")
	}
}

// -----------------------------------------------------------------------------
// AddItem invariants (item-cap 1000, duplicate, soft-delete guard)
// -----------------------------------------------------------------------------

func TestAddItem_AppendsAtCorrectPosition(t *testing.T) {
	t.Parallel()

	b := newB(t)
	qid := "01970000-0000-7000-b000-000000000001"
	if err := b.AddItem(qid); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if len(b.Items) != 1 {
		t.Fatalf("len(Items) = %d; want 1", len(b.Items))
	}
	if b.Items[0].QuestionID != qid {
		t.Errorf("Items[0].QuestionID = %q; want %q", b.Items[0].QuestionID, qid)
	}
	if b.Items[0].Position != 0 {
		t.Errorf("Items[0].Position = %d; want 0", b.Items[0].Position)
	}
	if b.Items[0].QuestionBankID != b.QuestionBankID || b.Items[0].TenantID != b.TenantID {
		t.Errorf("child not stamped with parent ids: %+v", b.Items[0])
	}
}

func TestAddItem_AssignsIncrementingPositions(t *testing.T) {
	t.Parallel()

	b := newB(t)
	for i := 0; i < 5; i++ {
		id := makeQID(t, i)
		if err := b.AddItem(id); err != nil {
			t.Fatalf("AddItem[%d]: %v", i, err)
		}
		if b.Items[i].Position != i {
			t.Errorf("Items[%d].Position = %d; want %d", i, b.Items[i].Position, i)
		}
	}
}

func TestAddItem_RejectsDuplicateQuestionID(t *testing.T) {
	t.Parallel()

	b := newB(t)
	id := "01970000-0000-7000-b000-000000000001"
	if err := b.AddItem(id); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	err := b.AddItem(id)
	if err == nil {
		t.Fatalf("expected duplicate-question error")
	}
	if !errors.Is(err, questionbank.ErrDuplicateQuestion) {
		t.Errorf("err = %v; want ErrDuplicateQuestion", err)
	}
}

func TestAddItem_EnforcesItemCap(t *testing.T) {
	t.Parallel()

	b := newB(t)
	for i := 0; i < questionbank.MaxItemsPerBank; i++ {
		if err := b.AddItem(makeQID(t, i)); err != nil {
			t.Fatalf("AddItem[%d]: %v", i, err)
		}
	}
	err := b.AddItem(makeQID(t, questionbank.MaxItemsPerBank))
	if err == nil {
		t.Fatalf("expected cap-exceeded error at %d", questionbank.MaxItemsPerBank+1)
	}
	if !errors.Is(err, questionbank.ErrItemCapExceeded) {
		t.Errorf("err = %v; want ErrItemCapExceeded", err)
	}
}

func TestAddItem_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()

	b := newB(t)
	if err := b.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.AddItem("01970000-0000-7000-b000-000000000001"); err == nil {
		t.Fatalf("expected error adding item to soft-deleted bank")
	}
}

func TestAddItem_RejectsEmptyQuestionID(t *testing.T) {
	t.Parallel()

	b := newB(t)
	if err := b.AddItem(""); err == nil {
		t.Fatalf("expected error for empty question_id")
	}
}

// -----------------------------------------------------------------------------
// RemoveItem invariants
// -----------------------------------------------------------------------------

func TestRemoveItem_RemovesAndReindexesPositions(t *testing.T) {
	t.Parallel()

	b := newB(t)
	ids := []string{makeQID(t, 0), makeQID(t, 1), makeQID(t, 2)}
	for _, id := range ids {
		if err := b.AddItem(id); err != nil {
			t.Fatalf("AddItem: %v", err)
		}
	}
	if err := b.RemoveItem(ids[1]); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if len(b.Items) != 2 {
		t.Fatalf("len(Items) = %d; want 2", len(b.Items))
	}
	if b.Items[0].QuestionID != ids[0] || b.Items[0].Position != 0 {
		t.Errorf("Items[0] = %+v; want id=%s pos=0", b.Items[0], ids[0])
	}
	if b.Items[1].QuestionID != ids[2] || b.Items[1].Position != 1 {
		t.Errorf("Items[1] = %+v; want id=%s pos=1", b.Items[1], ids[2])
	}
}

func TestRemoveItem_ReturnsNotFoundForMissingQuestion(t *testing.T) {
	t.Parallel()

	b := newB(t)
	err := b.RemoveItem("01970000-0000-7000-b000-000000000999")
	if err == nil {
		t.Fatalf("expected error for missing question_id")
	}
	if !errors.Is(err, questionbank.ErrQuestionNotInBank) {
		t.Errorf("err = %v; want ErrQuestionNotInBank", err)
	}
}

func TestRemoveItem_RejectsOnSoftDeleted(t *testing.T) {
	t.Parallel()

	b := newB(t)
	_ = b.AddItem("01970000-0000-7000-b000-000000000001")
	_ = b.Delete()
	if err := b.RemoveItem("01970000-0000-7000-b000-000000000001"); err == nil {
		t.Fatalf("expected error removing from soft-deleted bank")
	}
}

// -----------------------------------------------------------------------------
// Delete (soft) semantics — idempotent
// -----------------------------------------------------------------------------

func TestDelete_SetsDeletedAtAndIsIdempotent(t *testing.T) {
	t.Parallel()

	b := newB(t)
	if err := b.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if b.DeletedAt == nil {
		t.Fatalf("DeletedAt nil after Delete")
	}
	first := *b.DeletedAt
	if err := b.Delete(); err != nil {
		t.Fatalf("Delete (2nd): %v", err)
	}
	if !b.DeletedAt.Equal(first) {
		t.Errorf("DeletedAt mutated on 2nd Delete (was %v, now %v)", first, *b.DeletedAt)
	}
}

func TestIsActive_ReflectsSoftDelete(t *testing.T) {
	t.Parallel()

	b := newB(t)
	if !b.IsActive() {
		t.Errorf("IsActive = false; want true on fresh bank")
	}
	_ = b.Delete()
	if b.IsActive() {
		t.Errorf("IsActive = true; want false after Delete")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newB(t *testing.T) *questionbank.QuestionBank {
	t.Helper()
	b, err := questionbank.New(questionbank.NewParams{
		TenantID:  tenantA,
		OwnerGCID: ownerA,
		Name:      "Test question bank",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// makeQID returns a deterministic synthetic question UUID. The domain does
// not validate UUID format (repository/handler do), so a stable pattern keeps
// duplicate/position/cap assertions meaningful. Init MUST be all-zeros (the
// right-to-left fill leaves higher positions untouched).
func makeQID(t *testing.T, i int) string {
	t.Helper()
	suffix := []byte("0000000000000000")
	idx := len(suffix) - 1
	v := i
	for idx >= 0 && v > 0 {
		suffix[idx] = byte('0' + v%10)
		v /= 10
		idx--
	}
	return "01970000-0000-7000-b000-" + string(suffix)
}
