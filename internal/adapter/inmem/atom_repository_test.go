// Package inmem_test exercises the in-memory AtomRepository implementation.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

func mustNewAtom(t *testing.T, tenant, gcid, title string) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: tenant, Gcid: gcid, Title: title, Body: "y", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New unexpected: %v", err)
	}
	return a
}

func TestRepository_SaveAndGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewAtom(t, tenantA, gcidA, "x")
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save unexpected: %v", err)
	}
	got, err := r.Get(ctx, tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("Get unexpected: %v", err)
	}
	if got.AtomID != a.AtomID {
		t.Errorf("AtomID mismatch: got %s want %s", got.AtomID, a.AtomID)
	}
}

func TestRepository_Get_NotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	_, err := r.Get(ctx, tenantA, "nope")
	if err == nil {
		t.Errorf("expected error on missing atom; got nil")
	}
}

func TestRepository_Get_TenantIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewAtom(t, tenantA, gcidA, "x")
	_ = r.Save(ctx, a)
	_, err := r.Get(ctx, tenantB, a.AtomID)
	if err == nil {
		t.Errorf("tenantB must not see tenantA atom; got nil error")
	}
}

func TestRepository_Get_HidesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewAtom(t, tenantA, gcidA, "x")
	_ = a.SoftDelete()
	_ = r.Save(ctx, a)

	_, err := r.Get(ctx, tenantA, a.AtomID)
	if err == nil {
		t.Errorf("Get should hide soft-deleted; got nil error")
	}
}

func TestRepository_List_FiltersByTenantAndHidesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a1 := mustNewAtom(t, tenantA, gcidA, "a1")
	a2 := mustNewAtom(t, tenantA, gcidA, "a2-deleted")
	_ = a2.SoftDelete()
	a3 := mustNewAtom(t, tenantB, gcidA, "a3-other-tenant")

	_ = r.Save(ctx, a1)
	_ = r.Save(ctx, a2)
	_ = r.Save(ctx, a3)

	items, err := r.List(ctx, tenantA, atom.ListFilter{})
	if err != nil {
		t.Fatalf("List unexpected: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("List size = %d; want 1 (a1 only)", len(items))
	}
	if len(items) > 0 && items[0].AtomID != a1.AtomID {
		t.Errorf("List returned wrong atom: got %s want %s", items[0].AtomID, a1.AtomID)
	}
}

func TestRepository_List_FilterByStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	draft := mustNewAtom(t, tenantA, gcidA, "draft")
	_ = r.Save(ctx, draft)

	items, err := r.List(ctx, tenantA, atom.ListFilter{Status: atom.StatusDraft})
	if err != nil {
		t.Fatalf("List unexpected: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("List(draft) size = %d; want 1", len(items))
	}

	items2, _ := r.List(ctx, tenantA, atom.ListFilter{Status: atom.StatusPublished})
	if len(items2) != 0 {
		t.Errorf("List(published) size = %d; want 0", len(items2))
	}
}

func TestRepository_Save_Update_PersistsChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewAtom(t, tenantA, gcidA, "v1")
	_ = r.Save(ctx, a)

	newTitle := "v2"
	if err := a.ApplyUpdate(atom.UpdateParams{Title: &newTitle}); err != nil {
		t.Fatalf("ApplyUpdate unexpected: %v", err)
	}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save (update) unexpected: %v", err)
	}
	got, _ := r.Get(ctx, tenantA, a.AtomID)
	if got.Title != "v2" {
		t.Errorf("after update, title = %q; want v2", got.Title)
	}
	if got.Revision != 2 {
		t.Errorf("after update, revision = %d; want 2", got.Revision)
	}
}

func TestRepository_List_FilterByQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	match := mustNewAtom(t, tenantA, gcidA, "Photosynthesis basics")
	other := mustNewAtom(t, tenantA, gcidA, "Cell division")
	_ = r.Save(ctx, match)
	_ = r.Save(ctx, other)

	got, err := r.List(ctx, tenantA, atom.ListFilter{Query: "PHOTO"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].AtomID != match.AtomID {
		t.Errorf("List(query=PHOTO) = %+v; want match only (case-insensitive)", got)
	}
}

func TestRepository_List_LimitOffset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	for _, title := range []string{"a1", "a2", "a3"} {
		a := mustNewAtom(t, tenantA, gcidA, title)
		if err := r.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := r.List(ctx, tenantA, atom.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List(limit=2) size = %d; want 2", len(got))
	}

	got, _ = r.List(ctx, tenantA, atom.ListFilter{Offset: 2})
	if len(got) != 1 {
		t.Errorf("List(offset=2) size = %d; want 1", len(got))
	}

	got, _ = r.List(ctx, tenantA, atom.ListFilter{Offset: 99})
	if len(got) != 0 {
		t.Errorf("List(offset beyond end) size = %d; want 0", len(got))
	}
}

// -----------------------------------------------------------------------------
// Phyllis MVP: ListByCourse + revision-history persistence (append-only)
// -----------------------------------------------------------------------------

const courseA = "01970000-0000-7000-7000-000000000001"
const courseB = "01970000-0000-7000-7000-000000000002"

func mustNewBound(t *testing.T, tenant, gcid, course, title string) *atom.LearningAtom {
	t.Helper()
	a, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenant,
		Gcid:       gcid,
		CourseID:   course,
		Title:      title,
		Body:       "body",
		AtomType:   atom.TypeMCQ,
		Difficulty: 1,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("NewBound: %v", err)
	}
	return a
}

func TestRepository_ListByCourse_FiltersAndIsolates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()

	a1 := mustNewBound(t, tenantA, gcidA, courseA, "tA-cA-1")
	a2 := mustNewBound(t, tenantA, gcidA, courseA, "tA-cA-2")
	a3 := mustNewBound(t, tenantA, gcidA, courseB, "tA-cB-1")
	a4 := mustNewBound(t, tenantB, gcidA, courseA, "tB-cA-1")
	for _, a := range []*atom.LearningAtom{a1, a2, a3, a4} {
		if err := r.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := r.ListByCourse(ctx, tenantA, courseA)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d; want 2 (tenant-A + course-A only)", len(got))
	}
	for _, a := range got {
		if a.TenantID != tenantA {
			t.Errorf("returned cross-tenant atom: %s", a.TenantID)
		}
		if a.CourseID != courseA {
			t.Errorf("returned wrong-course atom: %s", a.CourseID)
		}
	}
}

func TestRepository_ListByCourse_HidesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()

	live := mustNewBound(t, tenantA, gcidA, courseA, "live")
	gone := mustNewBound(t, tenantA, gcidA, courseA, "gone")
	_ = gone.SoftDelete()

	_ = r.Save(ctx, live)
	_ = r.Save(ctx, gone)

	got, _ := r.ListByCourse(ctx, tenantA, courseA)
	if len(got) != 1 {
		t.Errorf("len = %d; want 1 (deleted hidden)", len(got))
	}
}

func TestRepository_PreservesAppendOnlyRevisionHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewBound(t, tenantA, gcidA, courseA, "v1")
	_, _ = a.AppendRevision(atom.AppendRevisionParams{
		Body: "v2", AuthoredBy: gcidA, SourceType: atom.SourceAIAssist,
		SourceMetadata: map[string]string{"k": "v"},
	})
	_, _ = a.AppendRevision(atom.AppendRevisionParams{
		Body: "v3", AuthoredBy: gcidA, SourceType: atom.SourceManual,
	})
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := r.Get(ctx, tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	hist := got.RevisionHistory()
	if len(hist) != 3 {
		t.Errorf("history len = %d; want 3", len(hist))
	}
	if hist[0].Body != "body" || hist[1].Body != "v2" || hist[2].Body != "v3" {
		t.Errorf("revisions out of order or mutated: %+v", hist)
	}
	// Confirm defensive copy: mutating returned slice does not affect repo.
	hist[0].Body = "MUTATED"
	got2, _ := r.Get(ctx, tenantA, a.AtomID)
	if got2.RevisionHistory()[0].Body == "MUTATED" {
		t.Errorf("repo leaked internal AppendOnlyRevision (must clone)")
	}
}

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — in-mem round-trip for the new fields per plan §1
// -----------------------------------------------------------------------------

// TestRepository_RoundTripsPhase1Fields confirms the in-mem repo persists +
// returns every new Phase 1 field (stem, subject, cognitive_level,
// imda_dimension_tags, author_note, media_assets, question_type alias) without
// loss + with defensive copies on slices.
func TestRepository_RoundTripsPhase1Fields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()

	a := mustNewAtom(t, tenantA, gcidA, "phase 1 fields")
	a.Stem = "What gas do plants release as byproduct of photosynthesis?"
	a.Subject = "biology"
	a.CognitiveLevel = atom.CognitiveLevelComprehension
	a.ImdaDimensionTags = []atom.ImdaDimTag{
		atom.ImdaDimTransparency,
		atom.ImdaDimSafetyRobustness,
	}
	a.AuthorNote = "Phyllis demo MCQ"
	a.MediaAssets = []atom.MediaAsset{
		{Type: "image", URL: "gs://chora-atom-media-dev/x.png",
			AltText: "diagram", MIME: "image/png", SizeBytes: 12345},
	}
	a.QuestionType = atom.QuestionTypeMCQ

	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Stem != a.Stem {
		t.Errorf("Stem round-trip: got %q want %q", got.Stem, a.Stem)
	}
	if got.Subject != "biology" {
		t.Errorf("Subject round-trip: got %q", got.Subject)
	}
	if got.CognitiveLevel != atom.CognitiveLevelComprehension {
		t.Errorf("CognitiveLevel round-trip: got %q", got.CognitiveLevel)
	}
	if len(got.ImdaDimensionTags) != 2 {
		t.Errorf("ImdaDimensionTags len = %d; want 2", len(got.ImdaDimensionTags))
	}
	if got.AuthorNote != "Phyllis demo MCQ" {
		t.Errorf("AuthorNote round-trip: got %q", got.AuthorNote)
	}
	if len(got.MediaAssets) != 1 {
		t.Errorf("MediaAssets len = %d; want 1", len(got.MediaAssets))
	}
	if got.MediaAssets[0].URL != "gs://chora-atom-media-dev/x.png" {
		t.Errorf("MediaAsset URL round-trip: got %q", got.MediaAssets[0].URL)
	}
	if got.QuestionType != atom.QuestionTypeMCQ {
		t.Errorf("QuestionType round-trip: got %q", got.QuestionType)
	}

	// Mutation defence: callers must not be able to mutate the repo's
	// internal slices via the returned aggregate.
	got.ImdaDimensionTags[0] = atom.ImdaDimAccountability
	got.MediaAssets[0].URL = "MUTATED"
	got2, _ := r.Get(ctx, tenantA, a.AtomID)
	if got2.ImdaDimensionTags[0] != atom.ImdaDimTransparency {
		t.Errorf("repo leaked ImdaDimensionTags slice; got %q",
			got2.ImdaDimensionTags[0])
	}
	if got2.MediaAssets[0].URL == "MUTATED" {
		t.Errorf("repo leaked MediaAssets slice")
	}
}

// TestRepository_RoundTripsEmptyPhase1Fields covers the optional path where
// none of the new fields are populated (back-compat: existing legacy rows).
func TestRepository_RoundTripsEmptyPhase1Fields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := inmem.NewAtomRepository()
	a := mustNewAtom(t, tenantA, gcidA, "legacy-shape")

	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Stem != "" || got.Subject != "" || got.AuthorNote != "" {
		t.Errorf("expected empty Phase 1 strings; got Stem=%q Subject=%q AuthorNote=%q",
			got.Stem, got.Subject, got.AuthorNote)
	}
	if got.CognitiveLevel != "" {
		t.Errorf("expected empty CognitiveLevel; got %q", got.CognitiveLevel)
	}
	if got.ImdaDimensionTags != nil {
		t.Errorf("expected nil ImdaDimensionTags; got %v", got.ImdaDimensionTags)
	}
	if got.MediaAssets != nil {
		t.Errorf("expected nil MediaAssets; got %v", got.MediaAssets)
	}
}
