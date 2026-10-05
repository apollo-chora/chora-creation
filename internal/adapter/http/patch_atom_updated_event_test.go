// patch_atom_updated_event_test.go - PATCH /api/atoms/{atom_id} emits
// chora.creation.atom.updated.v1 when it moves metadata on a PUBLISHED atom
// (ADR-244 D5 standing-trigger prerequisite).
//
// RED before the producer lands. The topic, its BINARY schema binding, its DLQ
// and the chora-consumption KG-invalidation consumer are all live; nothing
// publishes. Until they do, editing a published atom silently leaves every
// other domain reading the pre-edit metadata.
//
// Contract under test:
//   - PUBLISHED atom + at least one field that actually moved -> 200 + the
//     write and the event in ONE unit of work (SaveAndPublish), changed_fields
//     naming exactly the fields that moved.
//   - DRAFT atom -> 200, persisted, NO event (nothing downstream has seen a
//     draft, so there is no projection to correct).
//   - PUBLISHED atom + a patch that changes nothing -> 200, NO event (a
//     phantom change would invalidate consumer caches for free).
package httpadapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// seedPatchAtom persists an atom in the requested status carrying every
// metadata field the PATCH path can touch, and returns it with a router wired
// to a recording publisher.
func seedPatchAtom(t *testing.T, status atom.Status) (*atom.LearningAtom, http.Handler, *recordingPub, *inmem.AtomRepository) {
	t.Helper()
	repo := inmem.NewAtomRepository()
	pub := &recordingPub{}
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA,
		Title: "Comparing fractions", Body: "body",
		Tags: []string{"fractions"}, Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.Status = status
	a.QuestionType = atom.AtomType("mcq")
	a.Stem = "Which fraction is larger?"
	a.Subject = "math"
	a.AuthorNote = "keep the denominators small"
	a.CognitiveLevel = atom.CognitiveLevelComprehension
	a.Difficulty = 3
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               repo,
		AtomEventPublisher: pub,
	})
	return a, srv, pub, repo
}

// TestPatchAtom_PublishedRealChange_EmitsAtomUpdated - the happy path: one
// moved field, one event, the post-patch snapshot on the wire.
func TestPatchAtom_PublishedRealChange_EmitsAtomUpdated(t *testing.T) {
	t.Parallel()
	a, srv, pub, repo := seedPatchAtom(t, atom.StatusPublished)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, map[string]any{
		"stem":    "Which fraction is larger, 3/4 or 5/8?",
		"subject": "math", // unchanged: supplied at its current value
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", w.Code, w.Body.String())
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1 atom.updated emit", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomUpdated {
		t.Fatalf("event type = %q; want %q", ev.Type, atom.EventTypeAtomUpdated)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("tenant_id = %q; want %q", ev.TenantID, tenantA)
	}
	if ev.Gcid != gcidA {
		t.Errorf("gcid = %q; want the author %q", ev.Gcid, gcidA)
	}
	if ev.TraceParent == "" {
		t.Error("traceparent empty; it is a mandatory envelope field")
	}
	if len(ev.ChangedFields) != 1 || ev.ChangedFields[0] != "stem" {
		t.Errorf("changed_fields = %v; want [stem] (subject was supplied unchanged)", ev.ChangedFields)
	}
	if ev.Stem != "Which fraction is larger, 3/4 or 5/8?" {
		t.Errorf("stem = %q; want the POST-patch value", ev.Stem)
	}
	if ev.Status != string(atom.StatusPublished) {
		t.Errorf("status = %q; want published", ev.Status)
	}
	if !strings.HasPrefix(ev.IdempotencyKey, a.AtomID+":updated:") {
		t.Errorf("idempotency_key = %q; want the %s:updated:<instant> shape", ev.IdempotencyKey, a.AtomID)
	}

	// The event is worthless if the write behind it did not land.
	stored, err := repo.Get(context.Background(), tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if stored.Stem != "Which fraction is larger, 3/4 or 5/8?" {
		t.Errorf("persisted stem = %q; want the patched value", stored.Stem)
	}
}

// TestPatchAtom_PublishedMultiField_ChangedFieldsListsOnlyMoved - the mixed
// case that decides whether changed_fields is trustworthy: three fields
// supplied, two of them at their current values.
func TestPatchAtom_PublishedMultiField_ChangedFieldsListsOnlyMoved(t *testing.T) {
	t.Parallel()
	a, srv, pub, _ := seedPatchAtom(t, atom.StatusPublished)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, map[string]any{
		"title":           "Comparing fractions",                 // unchanged
		"difficulty":      3,                                     // unchanged
		"cognitive_level": "analysis",                            // changed
		"tags":            []string{"fractions", "number-sense"}, // changed
		"author_note":     "prefer visual bars",                  // changed
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", w.Code, w.Body.String())
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1", len(pub.events))
	}
	ev := pub.events[0]
	got := strings.Join(ev.ChangedFields, ",")
	want := "author_note,cognitive_level,tags"
	if got != want {
		t.Errorf("changed_fields = %q; want %q (unchanged title + difficulty excluded)", got, want)
	}
	if ev.CognitiveLevel != "analysis" {
		t.Errorf("cognitive_level = %q; want the post-patch analysis", ev.CognitiveLevel)
	}
	if len(ev.Tags) != 2 || ev.Tags[1] != "number-sense" {
		t.Errorf("tags = %v; want the post-patch pair", ev.Tags)
	}
	if ev.AuthorNote != "prefer visual bars" {
		t.Errorf("author_note = %q; want the post-patch value", ev.AuthorNote)
	}
}

// TestPatchAtom_PublishedListFields_ChangeThenResend - the list-valued fields
// (IMDA labels, media assets) need element-wise comparison, not a pointer or
// length check: setting them is a change, re-sending the identical list is not.
func TestPatchAtom_PublishedListFields_ChangeThenResend(t *testing.T) {
	t.Parallel()
	a, srv, pub, _ := seedPatchAtom(t, atom.StatusPublished)

	lists := map[string]any{
		"imda_dimension_tags": []string{"transparency", "accountability"},
		"media_assets": []map[string]any{{
			"type":       "image",
			"url":        "gs://chora-atom-media-dev/fractions.png",
			"alt_text":   "two fraction bars",
			"mime":       "image/png",
			"size_bytes": 2048,
		}},
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, lists))
	if w.Code != http.StatusOK {
		t.Fatalf("first patch: status = %d body = %s; want 200", w.Code, w.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want 1 after setting both lists", len(pub.events))
	}
	if got := strings.Join(pub.events[0].ChangedFields, ","); got != "imda_dimension_tags,media_assets" {
		t.Errorf("changed_fields = %q; want %q", got, "imda_dimension_tags,media_assets")
	}
	if tags := pub.events[0].ImdaDimensionTags; len(tags) != 2 || tags[0] != "transparency" {
		t.Errorf("imda_dimension_tags = %v; want the post-patch pair", tags)
	}
	if assets := pub.events[0].MediaAssets; len(assets) != 1 || assets[0].SizeBytes != 2048 {
		t.Errorf("media_assets = %+v; want the post-patch image", assets)
	}

	// Re-sending the identical lists moves nothing.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, lists))
	if w2.Code != http.StatusOK {
		t.Fatalf("resend: status = %d body = %s; want 200", w2.Code, w2.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d after re-sending identical lists; want still 1", len(pub.events))
	}
}

// TestPatchAtom_Draft_EmitsNothingButPersists - a draft is invisible to every
// other domain, so its edits announce nothing. The write must still land.
func TestPatchAtom_Draft_EmitsNothingButPersists(t *testing.T) {
	t.Parallel()
	a, srv, pub, repo := seedPatchAtom(t, atom.StatusDraft)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, map[string]any{
		"stem": "Draft stem, revised",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", w.Code, w.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0 for a DRAFT patch (%+v)", len(pub.events), pub.events)
	}

	stored, err := repo.Get(context.Background(), tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if stored.Stem != "Draft stem, revised" {
		t.Errorf("persisted stem = %q; want the patched value", stored.Stem)
	}
}

// TestPatchAtom_PublishedNoOp_EmitsNothing - every supplied field already holds
// the supplied value. A consumer that invalidated its cache on this would be
// doing work for a change that never happened.
func TestPatchAtom_PublishedNoOp_EmitsNothing(t *testing.T) {
	t.Parallel()
	a, srv, pub, _ := seedPatchAtom(t, atom.StatusPublished)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, map[string]any{
		"title":       "Comparing fractions",
		"stem":        "Which fraction is larger?",
		"subject":     "math",
		"author_note": "keep the denominators small",
		"difficulty":  3,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", w.Code, w.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0 for a no-op patch (%+v)", len(pub.events), pub.events)
	}
}

// TestPatchAtom_PublishedEmptyBody_EmitsNothing - an empty PATCH body supplies
// no field at all; it still bumps the revision but announces nothing.
func TestPatchAtom_PublishedEmptyBody_EmitsNothing(t *testing.T) {
	t.Parallel()
	a, srv, pub, _ := seedPatchAtom(t, atom.StatusPublished)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/api/atoms/"+a.AtomID, map[string]any{}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", w.Code, w.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("captured events = %d; want 0 for a field-less patch (%+v)", len(pub.events), pub.events)
	}
}
