// question_search_test.go — unit tests for QuestionRepository.SearchQuestions
// pgx adapter (FE B-FE-X5; ADR-155 D1). Mirrors the stubTxQuerier pattern from
// atom_repository_test.go: assert tenant-tx wrapping + SQL shape against an
// in-memory tx stub.
//
// Per .claude/rules/development-execution.md TDD: this test file was written
// RED first (no impl), then GREEN once the method lands. The pg adapter
// queries `learning_atoms` directly (per CLAUDE.md §3 — questions ARE atoms
// intra-domain; cross-DB queries forbidden).
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestQuestionRepository_SearchQuestions_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, Per: 20}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if len(tq.calls) == 0 {
		t.Fatalf("expected RunInTenantTx to be called; got 0 calls")
	}
	if tq.calls[0].tenantID != qTenant {
		t.Errorf("RunInTenantTx tenantID = %q; want %q", tq.calls[0].tenantID, qTenant)
	}
}

func TestQuestionRepository_SearchQuestions_EmitsSelectFromLearningAtoms(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// The adapter must SELECT from learning_atoms (atoms ARE questions
	// intra-domain per CLAUDE.md §3).
	if !strings.Contains(tq.querySQL, "learning_atoms") {
		t.Errorf("expected SELECT against learning_atoms; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "deleted_at IS NULL") {
		t.Errorf("expected soft-delete filter `deleted_at IS NULL` in SQL; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesTypesFilter(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, Types: []string{"mcq", "outline"}}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// Types filter compiles to `question_type IN ($N, $N+1, ...)` or
	// similar; the exact ANY/IN shape is adapter-internal but the WHERE
	// clause MUST reference question_type (column renamed from atom_type
	// in ADR-156 Phase 1 migration 0014).
	// ADR-206: the type filter keys off the REAL question type (q.question_type),
	// falling back to the atom's denormalised type for a draft with no question.
	if !strings.Contains(tq.querySQL, "COALESCE(q.question_type::text, la.question_type::text) IN (") {
		t.Errorf("expected real-type COALESCE(q, la) IN (...) filter; got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "atom_type") {
		t.Errorf("emitted SQL still references dropped atom_type column: %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesQNeedleAgainstTitleAndBody(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, Q: "math"}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// Case-insensitive substring against atom title/body OR the live question
	// prompt (ADR-206 — find a row by what the learner is actually asked).
	if !strings.Contains(strings.ToUpper(tq.querySQL), "ILIKE") {
		t.Errorf("expected ILIKE substring predicate; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "la.title ILIKE") ||
		!strings.Contains(tq.querySQL, "la.body ILIKE") ||
		!strings.Contains(tq.querySQL, "q.prompt ILIKE") {
		t.Errorf("expected ILIKE predicate against title + body + prompt; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesPagination(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, Page: 3, Per: 50}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if !strings.Contains(strings.ToUpper(tq.querySQL), "LIMIT") {
		t.Errorf("expected LIMIT clause for pagination; got %q", tq.querySQL)
	}
	if !strings.Contains(strings.ToUpper(tq.querySQL), "OFFSET") {
		t.Errorf("expected OFFSET clause for pagination; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_DerivesTenantFromFilterWhenSet(t *testing.T) {
	// When filter.TenantID is explicit, the adapter uses it as the RLS
	// session-scope. Empty TenantID would otherwise need to come from the
	// request context — but the contract says the handler always passes it.
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: "11111111-1111-7111-8111-111111111111"}.Normalize()
	_, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if tq.calls[0].tenantID != "11111111-1111-7111-8111-111111111111" {
		t.Errorf("RunInTenantTx tenantID = %q; want match filter.TenantID", tq.calls[0].tenantID)
	}
}

// -----------------------------------------------------------------------------
// CHO-1899 atom-sharing-redesign — extended filters + sort SQL shape.
// querySQL captures the LAST query (the list SELECT after the COUNT), which
// carries both the shared WHERE clause and the ORDER BY.
// -----------------------------------------------------------------------------

func TestQuestionRepository_SearchQuestions_AppliesAuthorGCIDFilter(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, AuthorGCID: "01970000-0000-7000-9000-000000000001"}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// Author column on learning_atoms is `gcid` (NOT author_gcid).
	if !strings.Contains(tq.querySQL, "gcid = $") {
		t.Errorf("expected `gcid = $N` author predicate; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesTagsFilter_JSONBAnyOperator(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, Tags: []string{"exam-2026", "review"}}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// JSONB array "contains ANY" via the GIN-indexable `?|` operator.
	if !strings.Contains(tq.querySQL, "tags ?|") {
		t.Errorf("expected `tags ?|` JSONB-any predicate (GIN); got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesStateFilter_AtomStatusEnum(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant, States: []string{"draft", "published"}}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if !strings.Contains(tq.querySQL, "status IN (") {
		t.Errorf("expected `status IN (...)` predicate; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "::atom_status") {
		t.Errorf("expected `::atom_status` enum cast (avoids 22P02); got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AppliesAtomIDFilter(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID: qTenant,
		AtomIDs:  []string{"01970000-0000-7000-8000-0000000000a1", "01970000-0000-7000-8000-0000000000a2"},
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if !strings.Contains(tq.querySQL, "atom_id IN (") {
		t.Errorf("expected `atom_id IN (...)` predicate; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "::uuid") {
		t.Errorf("expected `::uuid` cast on atom_id params; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_DefaultSortIsCreatedAtDescWithTiebreak(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// Preserve the pre-filter behaviour EXACTLY when sort is absent.
	if !strings.Contains(tq.querySQL, "ORDER BY la.created_at DESC, la.atom_id DESC") {
		t.Errorf("expected default ORDER BY la.created_at DESC, la.atom_id DESC; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_SortWhitelistedColumnAndDirection(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	// prompt → the display-label expression (ADR-206: live prompt, stem/title
	// fallback); asc → ASC; stable la.atom_id DESC tiebreak retained.
	filter := question.SearchFilter{TenantID: qTenant, Sorts: []question.Sort{{Field: "prompt", Direction: "asc"}}}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if !strings.Contains(tq.querySQL, "ORDER BY COALESCE(q.prompt, la.stem, la.title) ASC, la.atom_id DESC") {
		t.Errorf("expected ORDER BY COALESCE(q.prompt, la.stem, la.title) ASC, la.atom_id DESC; got %q", tq.querySQL)
	}
}

// ADR-206 read-path: the search LEFT JOINs the 1:1 `questions` row so the picker
// returns the live prompt + the REAL question_type + the question_id (the atom's
// denormalised title/type drift; the bank member-list already JOINs this way).
func TestQuestionRepository_SearchQuestions_JoinsQuestionForPromptRealTypeAndQuestionID(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{TenantID: qTenant}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// LEFT JOIN (not INNER) so question-less draft atoms still surface.
	if !strings.Contains(tq.querySQL, "LEFT JOIN questions q") {
		t.Errorf("expected LEFT JOIN to questions; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "q.atom_id = la.atom_id") ||
		!strings.Contains(tq.querySQL, "q.tenant_id = la.tenant_id") {
		t.Errorf("expected join on (atom_id, tenant_id); got %q", tq.querySQL)
	}
	// Projection carries the live prompt + question_id + the REAL type.
	if !strings.Contains(tq.querySQL, "COALESCE(q.prompt, '')") {
		t.Errorf("expected q.prompt projected; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "q.question_id::text") {
		t.Errorf("expected q.question_id projected; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "COALESCE(q.question_type::text, la.question_type::text") {
		t.Errorf("expected real question_type via COALESCE(q, la); got %q", tq.querySQL)
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2133) — chokepoint 1: the picker SQL narrows to the
// consent disjunct  mine ∪ (saved ∩ entitled) ∪ tenant-visible ∪ granted.
// Amendment A1.4: the friends-visible leg is DEFERRED (seam documented in
// question_search.go); orphan editions surface ONLY via the granted leg —
// this SQL must reference NO orphan column (cross-lane contract with the
// CHO-2132 orphan machinery).
// -----------------------------------------------------------------------------

const (
	qCaller  = "33333333-3333-7333-8333-333333333333"
	qGranted = "44444444-4444-7444-8444-444444444444"
	qSaved   = "55555555-5555-7555-8555-555555555555"
)

func TestQuestionRepository_SearchQuestions_SourceAll_EmitsConsentDisjunct(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:       qTenant,
		Source:         question.SourceAll,
		CallerGCID:     qCaller,
		GrantedAtomIDs: []string{qGranted},
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// mine leg — la-qualified (the LEFT JOIN questions q makes bare column
	// refs ambiguous where q carries the same column).
	if !strings.Contains(tq.querySQL, "la.gcid = $") {
		t.Errorf("expected la-qualified mine leg; got %q", tq.querySQL)
	}
	// tenant-visible leg — published only, shaped for the mig-0029 partial
	// index (tenant_id, reuse_visibility) WHERE deleted_at IS NULL.
	if !strings.Contains(tq.querySQL, "la.reuse_visibility = 'tenant' AND la.status = 'published'") {
		t.Errorf("expected tenant-visible leg (reuse_visibility='tenant' AND published); got %q", tq.querySQL)
	}
	// granted leg — active AtomUsageGrant atom ids from GetReuseContext.
	if !strings.Contains(tq.querySQL, "la.atom_id = ANY($") {
		t.Errorf("expected granted leg la.atom_id = ANY($n); got %q", tq.querySQL)
	}
	// A1.4 — the friends disjunct must NOT be wired yet.
	if strings.Contains(tq.querySQL, "friends") {
		t.Errorf("friends disjunct must stay deferred (A1.4); got %q", tq.querySQL)
	}
	// Cross-lane contract — never reference orphan columns.
	if strings.Contains(tq.querySQL, "orphan") {
		t.Errorf("SQL must not reference orphan columns (CHO-2132 lane owns them); got %q", tq.querySQL)
	}
	// The granted ids must be bound as an argument.
	found := false
	for _, a := range tq.queryArgs {
		if ids, ok := a.([]string); ok {
			for _, id := range ids {
				if id == qGranted {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected granted atom ids bound as query arg; got %v", tq.queryArgs)
	}
}

func TestQuestionRepository_SearchQuestions_SourceAll_EmptyCaller_MatchesNothing(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID: qTenant,
		Source:   question.SourceAll,
		// no CallerGCID — an identity-less caller must not harvest
		// tenant-visible rows (fail-closed).
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if !strings.Contains(tq.querySQL, "FALSE") {
		t.Errorf("expected FALSE disjunct for identity-less caller; got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "reuse_visibility = 'tenant'") {
		t.Errorf("tenant-visible leg must not be reachable without a caller identity; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_SourceSaved_IntersectsEntitled(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:       qTenant,
		Source:         question.SourceSaved,
		CallerGCID:     qCaller,
		SavedAtomIDs:   []string{qSaved},
		GrantedAtomIDs: []string{qGranted},
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// saved ∩ entitled — the saved leg must nest the entitlement disjunct
	// (ADR-229 D4.1: a bookmark alone no longer authorises reuse).
	if !strings.Contains(tq.querySQL, "la.atom_id = ANY($") {
		t.Errorf("expected la-qualified saved leg (42702 ambiguity fix vs questions.atom_id); got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "(atom_id = ANY($") {
		t.Errorf("bare atom_id reference is ambiguous against the questions JOIN; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "la.reuse_visibility = 'tenant' AND la.status = 'published'") {
		t.Errorf("expected entitled disjunct nested in the saved leg; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "la.gcid = $") {
		t.Errorf("expected mine leg inside the entitled disjunct; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_SourceMine_NoConsentLegs(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:       qTenant,
		Source:         question.SourceMine,
		CallerGCID:     qCaller,
		GrantedAtomIDs: []string{qGranted},
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// The projection column may carry reuse_visibility (provenance badge),
	// but no consent PREDICATE may narrow/widen a mine-only search.
	if strings.Contains(tq.querySQL, "reuse_visibility = 'tenant'") {
		t.Errorf("source=mine must not carry the tenant-visible predicate; got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "ANY($") {
		t.Errorf("source=mine must not carry granted/saved legs; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "la.gcid = $") {
		t.Errorf("expected mine leg; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_GrantedEmpty_OmitsGrantedLeg(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:   qTenant,
		Source:     question.SourceAll,
		CallerGCID: qCaller,
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if strings.Contains(tq.querySQL, "ANY($") {
		t.Errorf("expected no ANY(...) legs when granted+saved are empty; got %q", tq.querySQL)
	}
	// The entitled disjunct still carries mine + tenant-visible.
	if !strings.Contains(tq.querySQL, "la.reuse_visibility = 'tenant' AND la.status = 'published'") {
		t.Errorf("expected tenant-visible leg; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_SearchQuestions_AuthorFilter_SinglePredicate(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:   qTenant,
		Source:     question.SourceMine,
		CallerGCID: qCaller,
		AuthorGCID: qCaller,
	}.Normalize()
	if _, _, err := r.SearchQuestions(context.Background(), filter); err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	// Regression: the pre-WS-2 builder appended the author predicate TWICE
	// (once la-qualified with ::uuid, once bare). Exactly one author
	// predicate + one mine leg may reference gcid.
	if got := strings.Count(tq.querySQL, "gcid = $"); got != 2 {
		t.Errorf("expected exactly 2 gcid predicates (author + mine), got %d in %q", got, tq.querySQL)
	}
	if got := strings.Count(tq.querySQL, "la.gcid = $"); got != 2 {
		t.Errorf("every gcid predicate must be la-qualified; got %d of 2 in %q", got, tq.querySQL)
	}
}

// sourceHint — the per-row provenance badge now also stamps `granted` and
// `tenant` so the FE "Usable by me" affordance can say WHY a row is usable.
func TestQuestionRepository_SearchQuestions_StampsGrantedAndTenantSourceHints(t *testing.T) {
	now := time.Now().UTC()
	mkRow := func(atomID, gcid, reuseVis string) []any {
		// Scan order: atom_id, title, stem/body, question_type, tenant_id,
		// gcid, created_at, updated_at, prompt, question_id, reuse_visibility.
		return []any{atomID, "T", "B", "mcq", qTenant, gcid, now, now, "P", "q-1", reuseVis}
	}
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{rows: [][]any{
		mkRow(qGranted, "99999999-9999-7999-8999-999999999999", "private"),
		mkRow("66666666-6666-7666-8666-666666666666", "99999999-9999-7999-8999-999999999999", "tenant"),
	}}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	filter := question.SearchFilter{
		TenantID:       qTenant,
		Source:         question.SourceAll,
		CallerGCID:     qCaller,
		GrantedAtomIDs: []string{qGranted},
	}.Normalize()
	results, _, err := r.SearchQuestions(context.Background(), filter)
	if err != nil {
		t.Fatalf("SearchQuestions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d; want 2", len(results))
	}
	if results[0].Source != "granted" {
		t.Errorf("row0 source = %q; want granted", results[0].Source)
	}
	if results[1].Source != "tenant" {
		t.Errorf("row1 source = %q; want tenant", results[1].Source)
	}
	if results[1].ReuseVisibility != "tenant" {
		t.Errorf("row1 reuse_visibility = %q; want tenant", results[1].ReuseVisibility)
	}
}
