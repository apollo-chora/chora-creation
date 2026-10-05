// question_search_test.go — HTTP-layer tests for the cross-atom question
// picker endpoint (FE B-FE-X5; ADR-155 D1):
//
//	GET /api/atoms/questions/search?q=&tenant_id=&types=&page=&per=
//
// Mirrors questions_handler_test.go's fakeQuestionRepository fixture pattern.
// The search method is mocked via a per-test override so the handler tests
// stay focused on the wire shape + query-string parsing + envelope.
//
// Per .claude/rules/development-execution.md TDD — RED first, then GREEN
// once the handler lands.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// searchableQRepo embeds fakeQuestionRepository with a per-test override of
// SearchQuestions so the handler tests can return seeded results without
// poking the underlying questions map.
type searchableQRepo struct {
	*fakeQuestionRepository
	searchFn func(ctx context.Context, f question.SearchFilter) ([]question.SearchResult, int, error)
}

func (r *searchableQRepo) SearchQuestions(ctx context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
	if r.searchFn != nil {
		return r.searchFn(ctx, f)
	}
	return []question.SearchResult{}, 0, nil
}

func newSearchableQServer(t *testing.T, search func(ctx context.Context, f question.SearchFilter) ([]question.SearchResult, int, error)) http.Handler {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	wrapped := &searchableQRepo{fakeQuestionRepository: qRepo, searchFn: search}
	return httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: wrapped,
		// ADR-229 WS-2 — production always wires the consent-context
		// fetcher; source=all/saved searches fail loud without it. The
		// generic search tests get an empty (grant-less) context; the
		// unwired/failing paths are exercised via newConsentQServer.
		ReuseContextFetcher: &fakeReuseContextFetcher{},
	})
}

// -----------------------------------------------------------------------------
// GET /api/atoms/questions/search — envelope + happy path
// -----------------------------------------------------------------------------

func TestSearchQuestions_DefaultPagination_Returns200WithEnvelope(t *testing.T) {
	t.Parallel()
	results := []question.SearchResult{
		{
			ID:           "00000000-0000-7000-8000-00000000a0a1",
			Title:        "A+ : Atomic Learning Primitives",
			Stem:         "A LearningAtom is the smallest unit of meaning ...",
			QuestionType: "outline",
			TenantID:     tenantA,
			AuthorGCID:   gcidA,
			CreatedAt:    time.Now().UTC().Add(-3 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-2 * time.Hour),
		},
		{
			ID:           "00000000-0000-7000-8000-00000000a0a2",
			Title:        "A+ : Straight-Up vs Graph Discovery",
			Stem:         "Two delivery modes coexist ...",
			QuestionType: "mcq",
			TenantID:     tenantA,
			AuthorGCID:   gcidA,
			CreatedAt:    time.Now().UTC().Add(-2 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-1 * time.Hour),
		},
		{
			ID:           "00000000-0000-7000-8000-00000000a0a5",
			Title:        "A+ : Creator Mode AI Assist",
			Stem:         "In A+ Creator mode, an instructor invokes the 6-agent gate ...",
			QuestionType: "essay",
			TenantID:     tenantA,
			AuthorGCID:   gcidA,
			CreatedAt:    time.Now().UTC().Add(-1 * time.Hour),
			UpdatedAt:    time.Now().UTC(),
		},
	}
	srv := newSearchableQServer(t, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return results, len(results), nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	items, _ := got["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items len = %d; want 3", len(items))
	}
	if got["page"] != float64(1) {
		t.Errorf("page = %v; want 1", got["page"])
	}
	if got["per"] != float64(20) {
		t.Errorf("per = %v; want 20", got["per"])
	}
	if got["total"] != float64(3) {
		t.Errorf("total = %v; want 3", got["total"])
	}
}

func TestSearchQuestions_ItemShape_MatchesFEContract(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{
			{
				ID:           "00000000-0000-7000-8000-00000000a0a2",
				Title:        "A+ : Straight-Up vs Graph Discovery",
				Stem:         "Two delivery modes coexist ...",
				QuestionType: "mcq",
				TenantID:     tenantA,
				AuthorGCID:   gcidA,
				CreatedAt:    time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC),
				UpdatedAt:    time.Date(2026, 5, 15, 11, 0, 0, 0, time.UTC),
			},
		}, 1, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d; want 1", len(items))
	}
	first, _ := items[0].(map[string]any)
	for _, key := range []string{"id", "title", "stem", "question_type", "tenant_id", "author_gcid", "created_at", "updated_at"} {
		if _, ok := first[key]; !ok {
			t.Errorf("item missing required field %q (got %v)", key, first)
		}
	}
	if first["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq", first["question_type"])
	}
}

func TestSearchQuestions_PassesFilterToRepo_TypesAndQ(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?q=math&types=mcq,oe&per=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if capturedFilter.Q != "math" {
		t.Errorf("filter.Q = %q; want %q", capturedFilter.Q, "math")
	}
	if capturedFilter.Per != 10 {
		t.Errorf("filter.Per = %d; want 10", capturedFilter.Per)
	}
	if len(capturedFilter.Types) != 2 {
		t.Errorf("filter.Types = %v; want [mcq oe]", capturedFilter.Types)
	}
}

// E2E-BE-6 (2026-05-16) — the OpenAPI spec for searchQuestions declares
// `question_type` as a multi-value query param with explode=true (one repeated
// `?question_type=mcq&question_type=oe` shape, not the legacy `?types=` CSV).
// The FE picker on /a/atoms/new still saw essay/outline atoms even with the
// mcq+oe filter because the handler was only parsing `types=` and dropping the
// `question_type=` repetitions silently. Worse — even when types= WAS parsed
// for `oe`, the pg adapter's `WHERE atom_type IN ('oe', ...)` predicate panics
// with SQLSTATE 22P02 (`invalid input value for enum atom_type: "oe"`) because
// the chora_creation.atom_type DB enum has values
// ('mcq','flashcard','video','essay','outline') with NO 'oe' member —
// OE-style atoms live under atom_type='essay' (see protomarshal.go#L781:
// ATOM_TYPE_ESSAY ↔ "essay" ↔ "oe"; questions_handler.go#L322 enforces this
// at create-time).
//
// Fix: handler translates QuestionType wire values (per OpenAPI
// `creation-questions.yaml#QuestionType` 16-enum) to atom_type DB-enum values
// before populating filter.Types. RED-first guards the boundary translation.
// Composes with question_search.go#L73-L83 (pg adapter WHERE atom_type IN).
func TestSearchQuestions_PassesFilterToRepo_QuestionTypeRepeated(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	// OpenAPI explode=true repeats the param key.
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?question_type=mcq&question_type=oe&per=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(capturedFilter.Types) != 2 {
		t.Fatalf("filter.Types = %v; want 2 (mcq + essay after wire→DB translation)", capturedFilter.Types)
	}
	// Order-insensitive containment check. Translation:
	//   wire `mcq` → DB `mcq`
	//   wire `oe`  → DB `essay`
	got := map[string]bool{capturedFilter.Types[0]: true, capturedFilter.Types[1]: true}
	for _, want := range []string{"mcq", "essay"} {
		if !got[want] {
			t.Errorf("filter.Types missing %q (got %v) — wire→DB translation broken", want, capturedFilter.Types)
		}
	}
	if got["oe"] {
		t.Errorf("filter.Types contains 'oe' — would trip SQLSTATE 22P02 at WHERE atom_type IN (got %v)", capturedFilter.Types)
	}
}

// E2E-BE-6 narrows-results path — when the FE picker filters to mcq+oe only,
// flashcard/video/outline rows MUST be excluded. The repo SQL already builds
// `atom_type IN ($N,...)` from filter.Types (see question_search.go#L73-L83);
// this guards the *handler* parses the question_type[] repetitions correctly
// AND translates wire→DB so the IN predicate gets the right values. The seed
// uses atom_type DB-enum values (mcq, essay, outline, flashcard) since
// SearchResult.QuestionType echoes back atom_type from the DB column.
func TestSearchQuestions_QuestionTypeFilter_NarrowsResults(t *testing.T) {
	t.Parallel()
	// Repo returns only the rows whose atom_type is in filter.Types — same
	// invariant the pg adapter encodes via WHERE atom_type IN (...).
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		all := []question.SearchResult{
			{ID: "a", QuestionType: "mcq", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "b", QuestionType: "essay", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "c", QuestionType: "outline", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "d", QuestionType: "flashcard", TenantID: tenantA, AuthorGCID: gcidA},
		}
		out := []question.SearchResult{}
		for _, r := range all {
			if f.MatchesType(r.QuestionType) {
				out = append(out, r)
			}
		}
		return out, len(out), nil
	})

	w := httptest.NewRecorder()
	// FE wire: mcq+oe → handler translates to mcq+essay → SQL matches mcq+essay rows.
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?question_type=mcq&question_type=oe", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items len = %d; want 2 (mcq + essay only)", len(items))
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		qt, _ := m["question_type"].(string)
		if qt != "mcq" && qt != "essay" {
			t.Errorf("leaked non-filter type %q in result: %v", qt, m)
		}
	}
}

// E2E-BE-6 regression guard — empty filter (no `question_type` and no `types`)
// MUST NOT shrink results. The picker default state shows everything.
func TestSearchQuestions_NoFilter_ReturnsAll(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		all := []question.SearchResult{
			{ID: "a", QuestionType: "mcq", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "b", QuestionType: "essay", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "c", QuestionType: "outline", TenantID: tenantA, AuthorGCID: gcidA},
			{ID: "d", QuestionType: "flashcard", TenantID: tenantA, AuthorGCID: gcidA},
		}
		out := []question.SearchResult{}
		for _, r := range all {
			if f.MatchesType(r.QuestionType) {
				out = append(out, r)
			}
		}
		return out, len(out), nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items len = %d; want 4 (no filter — all types)", len(items))
	}
}

// E2E-BE-6 union — if both shapes are sent (back-compat scenario during FE
// rollover), the union of both lists applies. The legacy `types=` alias is
// ALSO translated (the prior implementation would have panicked on
// `?types=oe` for the same enum-mismatch reason — bug was undetected because
// existing test used `?types=mcq,outline` which doesn't trip the enum).
// Normalize() dedupes if both shapes resolve to the same atom_type.
func TestSearchQuestions_BothShapes_Union(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	// types=mcq + question_type=oe → translates to filter.Types = [mcq, essay].
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?types=mcq&question_type=oe", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(capturedFilter.Types) != 2 {
		t.Fatalf("filter.Types = %v; want 2 (mcq + essay)", capturedFilter.Types)
	}
	got := map[string]bool{capturedFilter.Types[0]: true, capturedFilter.Types[1]: true}
	for _, want := range []string{"mcq", "essay"} {
		if !got[want] {
			t.Errorf("filter.Types missing %q (got %v)", want, capturedFilter.Types)
		}
	}
}

// E2E-BE-6 legacy alias translation — the `?types=oe` CSV shape was always
// broken at the pg layer (atom_type ENUM has no 'oe') but undetected because
// no test exercised that input. RED-first locks in the translation so the
// alias path can't silently regress.
func TestSearchQuestions_LegacyTypesAlias_TranslatesOEToEssay(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?types=mcq,oe", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(capturedFilter.Types) != 2 {
		t.Fatalf("filter.Types = %v; want 2", capturedFilter.Types)
	}
	got := map[string]bool{capturedFilter.Types[0]: true, capturedFilter.Types[1]: true}
	if !got["mcq"] || !got["essay"] {
		t.Errorf("filter.Types = %v; want [mcq essay]", capturedFilter.Types)
	}
	if got["oe"] {
		t.Errorf("filter.Types contains 'oe' — would trip SQLSTATE 22P02 (got %v)", capturedFilter.Types)
	}
}

// E2E-BE-6 reserved_* + unknown values — the OpenAPI QuestionType enum has
// 14 reserved_* sentinel values for forward-compat; none have an atom_type
// analog in the current DB schema. They MUST be dropped from filter.Types
// (a no-match value would silently SQL-OK but return zero rows; dropping it
// means filter behaves like "match-all on the remaining valid values").
// Unknown / typo values follow the same drop-policy.
func TestSearchQuestions_QuestionTypeReserved_DroppedFromFilter(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	// mcq survives; reserved_drag_drop has no atom_type → dropped; bogus → dropped.
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?question_type=mcq&question_type=reserved_drag_drop&question_type=bogus", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(capturedFilter.Types) != 1 || capturedFilter.Types[0] != "mcq" {
		t.Errorf("filter.Types = %v; want [mcq] (reserved_* + bogus dropped)", capturedFilter.Types)
	}
}

func TestSearchQuestions_AcceptsExplicitTenantIdQueryParam(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	// tenant_id in query string MUST match header per RLS defence-in-depth.
	// Empty tenant_id query falls back to header.
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?tenant_id="+tenantA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if capturedFilter.TenantID != tenantA {
		t.Errorf("filter.TenantID = %q; want %q", capturedFilter.TenantID, tenantA)
	}
}

func TestSearchQuestions_FallsBackToHeaderTenantWhenQueryEmpty(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if capturedFilter.TenantID != tenantA {
		t.Errorf("filter.TenantID = %q; want fallback to header tenant %q", capturedFilter.TenantID, tenantA)
	}
}

func TestSearchQuestions_RejectsCrossTenantQuery_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	otherTenant := "33333333-3333-7333-8333-333333333333"
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?tenant_id="+otherTenant, nil))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 400 or 403 for cross-tenant query", w.Code)
	}
}

func TestSearchQuestions_DefaultsPageAndPerWhenMissing(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if capturedFilter.Page != 1 {
		t.Errorf("default page = %d; want 1", capturedFilter.Page)
	}
	if capturedFilter.Per != 20 {
		t.Errorf("default per = %d; want 20", capturedFilter.Per)
	}
}

func TestSearchQuestions_CapsPerAt100(t *testing.T) {
	t.Parallel()
	var capturedFilter question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		capturedFilter = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?per=500", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if capturedFilter.Per != 100 {
		t.Errorf("per cap = %d; want 100", capturedFilter.Per)
	}
}

func TestSearchQuestions_MissingTenantHeader_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	// Build a request with NO X-Tenant-Id header — middleware rejects it.
	r := httptest.NewRequest(http.MethodGet, "/api/atoms/questions/search", nil)
	r.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 missing-tenant", w.Code)
	}
	if !strings.Contains(w.Body.String(), "TENANT_REQUIRED") {
		t.Errorf("expected CREATION_TENANT_REQUIRED; got %s", w.Body.String())
	}
}

func TestSearchQuestions_OnlyGETAllowed(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

// -----------------------------------------------------------------------------
// CHO-1899 atom-sharing-redesign — extended filters + sort wiring + the
// fail-loud rejection of the 3 deferred (no-real-column / Phase-2) params.
// RED first per .claude/rules/development-execution.md.
// -----------------------------------------------------------------------------

func TestSearchQuestions_PassesAuthorGCIDToRepo(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?author_gcid="+gcidA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if captured.AuthorGCID != gcidA {
		t.Errorf("filter.AuthorGCID = %q; want %q", captured.AuthorGCID, gcidA)
	}
}

func TestSearchQuestions_RejectsMalformedAuthorGCID_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?author_gcid=not-a-uuid", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for malformed author_gcid", w.Code)
	}
}

func TestSearchQuestions_PassesTagsToRepo_RepeatedParam(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?tag=exam-2026&tag=review", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(captured.Tags) != 2 {
		t.Fatalf("filter.Tags = %v; want 2", captured.Tags)
	}
	got := map[string]bool{captured.Tags[0]: true, captured.Tags[1]: true}
	if !got["exam-2026"] || !got["review"] {
		t.Errorf("filter.Tags = %v; want [exam-2026 review]", captured.Tags)
	}
}

func TestSearchQuestions_PassesStatesToRepo_TranslatesEnumCase(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	w := httptest.NewRecorder()
	// Contract enum is UPPERCASE (DRAFT/PUBLISHED/ARCHIVED); DB atom_status is
	// lowercase — handler must translate (mirror question_type → atom_type).
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?state=DRAFT&state=PUBLISHED", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(captured.States) != 2 {
		t.Fatalf("filter.States = %v; want 2 (draft,published)", captured.States)
	}
	got := map[string]bool{captured.States[0]: true, captured.States[1]: true}
	if !got["draft"] || !got["published"] {
		t.Errorf("filter.States = %v; want [draft published] (lowercased DB enum)", captured.States)
	}
}

func TestSearchQuestions_DropsUnknownState(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	w := httptest.NewRecorder()
	// BOGUS has no atom_status analog → dropped (a `$N::atom_status` cast would
	// otherwise trip SQLSTATE 22P02). Mirrors the question_type drop policy.
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?state=BOGUS&state=published", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(captured.States) != 1 || captured.States[0] != "published" {
		t.Errorf("filter.States = %v; want [published] (BOGUS dropped)", captured.States)
	}
}

func TestSearchQuestions_PassesAtomIDsToRepo(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	a1 := "01970000-0000-7000-8000-0000000000a1"
	a2 := "01970000-0000-7000-8000-0000000000a2"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?atom_id="+a1+"&atom_id="+a2, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(captured.AtomIDs) != 2 {
		t.Errorf("filter.AtomIDs = %v; want 2", captured.AtomIDs)
	}
}

func TestSearchQuestions_RejectsMalformedAtomID_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?atom_id=not-a-uuid", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for malformed atom_id", w.Code)
	}
}

func TestSearchQuestions_PassesSortToRepo(t *testing.T) {
	t.Parallel()
	var captured question.SearchFilter
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		captured = f
		return nil, 0, nil
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?sort=updated_at:asc", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(captured.Sorts) != 1 || captured.Sorts[0].Field != "updated_at" || captured.Sorts[0].Direction != "asc" {
		t.Errorf("filter.Sorts = %v; want [{updated_at,asc}]", captured.Sorts)
	}
}

func TestSearchQuestions_RejectsMalformedSort_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, nil)
	for _, bad := range []string{"bogus:asc", "created_at:sideways", "nonsuch:asc"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?sort="+bad, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("sort=%q: status = %d; want 400", bad, w.Code)
		}
	}
}

// TestSearchQuestions_AcceptsPickerSortOptions_200 locks the deployed
// atom-question-picker sort dropdown options. title:asc + question_type:asc were
// a live 400 regression before the question-search whitelist was widened.
func TestSearchQuestions_AcceptsPickerSortOptions_200(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return nil, 0, nil
	})
	for _, opt := range []string{"created_at:desc", "updated_at:desc", "title:asc", "question_type:asc"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?sort="+opt, nil))
		if w.Code != http.StatusOK {
			t.Errorf("sort=%q: status = %d body=%s; want 200 (live picker option)", opt, w.Code, w.Body.String())
		}
	}
}

// --- Deferred params: FAIL LOUD with 400; never silently ignore -------------

func TestSearchQuestions_RejectsTopicNodeID_400(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?topic_node_id=01970000-0000-7000-8000-0000000000c1", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (topic_node_id deferred)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "topic_node_id filter not yet supported") {
		t.Errorf("body = %s; want the documented topic_node_id deferral message", w.Body.String())
	}
}

// (Pre-WS-2 usable_by deferral tests removed — ADR-229 WS-2 shipped the
// server-side gate: include_usable=true is now truthfully accepted and
// usable_by_gcid keeps failing loud with the caller-scoped rationale. See
// TestSearchQuestions_IncludeUsableTrue_NowAccepted +
// TestSearchQuestions_UsableByGcidParam_StillRejected below.)

func TestSearchQuestions_AllowsIncludeUsableFalse(t *testing.T) {
	t.Parallel()
	// `include_usable=false` is the catalogue-browse default (no entitlement
	// union) == current behaviour; it MUST NOT 400.
	srv := newSearchableQServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?include_usable=false", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (include_usable=false is a no-op default)", w.Code)
	}
}

// Honest narrowing: the fake repo applies the domain matchers so the filter is
// real, not bypassed (no inmem SearchQuestions exists — matchers ARE the
// shared, tested filter logic the pg adapter mirrors in SQL).
func TestSearchQuestions_StateAndTagFilters_NarrowResults(t *testing.T) {
	t.Parallel()
	srv := newSearchableQServer(t, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		type row struct {
			res   question.SearchResult
			state string
			tags  []string
		}
		all := []row{
			{question.SearchResult{ID: "a", QuestionType: "mcq"}, "published", []string{"exam-2026"}},
			{question.SearchResult{ID: "b", QuestionType: "mcq"}, "draft", []string{"exam-2026"}},
			{question.SearchResult{ID: "c", QuestionType: "mcq"}, "published", []string{"warmup"}},
		}
		out := []question.SearchResult{}
		for _, r := range all {
			if f.MatchesState(r.state) && f.MatchesTags(r.tags) {
				out = append(out, r.res)
			}
		}
		return out, len(out), nil
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet,
		"/api/atoms/questions/search?state=PUBLISHED&tag=exam-2026", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d; want 1 (only published ∧ exam-2026 row 'a')", len(items))
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2133) — chokepoint 1: the handler hydrates the granted
// disjunct from chora-sharing GetReuseContext once per search and fails LOUD
// when the consent context cannot be read (never wide-open, never silently
// narrowed).
// -----------------------------------------------------------------------------

type fakeReuseContextFetcher struct {
	rc        ports.ReuseContext
	err       error
	calls     int
	gotGCID   string
	gotTenant string
}

func (f *fakeReuseContextFetcher) GetReuseContext(_ context.Context, gcid, tenantID string) (ports.ReuseContext, error) {
	f.calls++
	f.gotGCID = gcid
	f.gotTenant = tenantID
	if f.err != nil {
		return ports.ReuseContext{}, f.err
	}
	return f.rc, nil
}

// newConsentQServer builds a search server with an explicit reuse-context
// fetcher (nil allowed — exercises the fail-loud unwired path).
func newConsentQServer(t *testing.T, fetcher ports.ReuseContextFetcher, search func(ctx context.Context, f question.SearchFilter) ([]question.SearchResult, int, error)) http.Handler {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	wrapped := &searchableQRepo{fakeQuestionRepository: qRepo, searchFn: search}
	return httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                atomRepo,
		QuestionRepository:  wrapped,
		ReuseContextFetcher: fetcher,
	})
}

func TestSearchQuestions_SourceAll_HydratesGrantedDisjunctFromSharing(t *testing.T) {
	t.Parallel()
	granted := "0197cccc-0000-7000-8000-000000000042"
	fetcher := &fakeReuseContextFetcher{rc: ports.ReuseContext{
		GrantedAtomIDs: []string{granted},
		FriendGCIDs:    []string{"0197dddd-0000-7000-8000-0000000000f1"},
	}}
	var seen question.SearchFilter
	srv := newConsentQServer(t, fetcher, func(_ context.Context, f question.SearchFilter) ([]question.SearchResult, int, error) {
		seen = f
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if fetcher.calls != 1 {
		t.Fatalf("GetReuseContext calls = %d; want 1 (once per search)", fetcher.calls)
	}
	if fetcher.gotGCID != gcidA || fetcher.gotTenant != tenantA {
		t.Errorf("GetReuseContext(%q, %q); want (%q, %q)", fetcher.gotGCID, fetcher.gotTenant, gcidA, tenantA)
	}
	if len(seen.GrantedAtomIDs) != 1 || seen.GrantedAtomIDs[0] != granted {
		t.Errorf("filter.GrantedAtomIDs = %v; want [%s]", seen.GrantedAtomIDs, granted)
	}
}

func TestSearchQuestions_SourceMine_SkipsReuseContextFetch(t *testing.T) {
	t.Parallel()
	fetcher := &fakeReuseContextFetcher{}
	srv := newConsentQServer(t, fetcher, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?source=mine", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if fetcher.calls != 0 {
		t.Errorf("GetReuseContext calls = %d; want 0 for source=mine", fetcher.calls)
	}
}

func TestSearchQuestions_ReuseContextFetchError_Fails502Loud(t *testing.T) {
	t.Parallel()
	fetcher := &fakeReuseContextFetcher{err: context.DeadlineExceeded}
	repoCalled := false
	srv := newConsentQServer(t, fetcher, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		repoCalled = true
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502 (fail LOUD — never wide-open, never mine-only)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_REUSE_CONTEXT_UNAVAILABLE") {
		t.Errorf("body = %s; want CREATION_REUSE_CONTEXT_UNAVAILABLE envelope", w.Body.String())
	}
	if repoCalled {
		t.Errorf("repo must NOT be queried when the consent context read fails")
	}
}

func TestSearchQuestions_ReuseContextUnwired_Fails500Loud(t *testing.T) {
	t.Parallel()
	srv := newConsentQServer(t, nil, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500 (unwired consent context = loud misconfig, not a silent narrow)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_REUSE_CONTEXT_NOT_WIRED") {
		t.Errorf("body = %s; want CREATION_REUSE_CONTEXT_NOT_WIRED envelope", w.Body.String())
	}
}

func TestSearchQuestions_IncludeUsableTrue_NowAccepted(t *testing.T) {
	t.Parallel()
	fetcher := &fakeReuseContextFetcher{}
	srv := newConsentQServer(t, fetcher, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	// Post-WS-2 the server-side gate is ALWAYS on — include_usable=true is
	// truthfully satisfied (was a 400 while the gate didn't exist).
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?include_usable=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (gate is always-on)", w.Code, w.Body.String())
	}
}

func TestSearchQuestions_UsableByGcidParam_StillRejected(t *testing.T) {
	t.Parallel()
	fetcher := &fakeReuseContextFetcher{}
	srv := newConsentQServer(t, fetcher, func(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
		return []question.SearchResult{}, 0, nil
	})

	// Filtering by an ARBITRARY user's usability stays unsupported — the
	// consent context is caller-scoped (fail loud, never silently ignore).
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodGet, "/api/atoms/questions/search?usable_by_gcid="+gcidA, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}
