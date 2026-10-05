// collection_reuse_facts_test.go — ADR-233 pg slice (WS-4), RED-first.
//
// SQL-shape + arg + tenant-tx coverage for the collection.AtomReuseFactLookup
// port via the stub TxQuerier idiom. Live schema behaviour is verified by the
// PREPARE-smoke lane (feedback_pg_prepare_smoke_over_exec_stubs) — an exec stub
// cannot tell you a column does not exist.
package pg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// tsFixed is a deterministic timestamp for scan fixtures.
var tsFixed = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

const (
	rfAtom1   = "01970000-0000-7000-a000-000000000001"
	rfAtom2   = "01970000-0000-7000-a000-000000000002"
	rfAtom3   = "01970000-0000-7000-a000-000000000003"
	rfLearner = "01970000-0000-7000-9000-0000000000aa"
)

// findQuery returns the first captured Query() SQL containing needle.
func findQuery(q *stubTxQuerier, needle string) (string, bool) {
	for _, s := range q.tx.querySQLs {
		if strings.Contains(s, needle) {
			return s, true
		}
	}
	return "", false
}

func TestReuseFacts_OneBatchQueryInTenantTx(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewCollectionRepositoryFromTxQuerier(q)

	// Seed the batch read: atom_id, author gcid, reuse_visibility, published.
	q.tx.queryRows[sqlReuseFacts] = [][]any{
		{rfAtom1, rfLearner, "tenant", true},
		{rfAtom2, rfLearner, "private", false},
	}

	// rfAtom3 is requested but soft-deleted → simply absent from the result.
	facts, err := repo.ReuseFacts(context.Background(), testTenantID, []string{rfAtom1, rfAtom2, rfAtom3})
	if err != nil {
		t.Fatalf("ReuseFacts: %v", err)
	}

	if q.runCount != 1 {
		t.Errorf("RunInTenantTx count = %d; want 1 (ONE batch query, not N+1)", q.runCount)
	}
	if len(q.tenants) != 1 || q.tenants[0] != testTenantID {
		t.Errorf("tenants = %v; want [%s] — RLS must be applied in the same tx", q.tenants, testTenantID)
	}
	if len(q.tx.querySQLs) != 1 {
		t.Errorf("queries = %d; want exactly 1", len(q.tx.querySQLs))
	}

	if len(facts) != 2 {
		t.Fatalf("len(facts) = %d; want 2 (the soft-deleted atom is absent)", len(facts))
	}
	if _, present := facts[rfAtom3]; present {
		t.Errorf("soft-deleted atom %s present in facts; EvaluateAll must see ATOM_NOT_FOUND", rfAtom3)
	}

	f1 := facts[rfAtom1]
	if f1.AtomID != rfAtom1 || f1.AuthorGCID != rfLearner {
		t.Errorf("facts[%s] = %+v; want atom/author populated", rfAtom1, f1)
	}
	if f1.Audience != audience.Tenant {
		t.Errorf("facts[%s].Audience = %q; want tenant", rfAtom1, f1.Audience)
	}
	if !f1.Published {
		t.Errorf("facts[%s].Published = false; want true", rfAtom1)
	}

	f2 := facts[rfAtom2]
	if f2.Audience != audience.Private || f2.Published {
		t.Errorf("facts[%s] = %+v; want private + unpublished", rfAtom2, f2)
	}
}

// The SQL contract: one batch read over learning_atoms, id list via = ANY($2),
// soft-delete filtered, and the publish state derived from `status`.
func TestReuseFacts_SQLShape(t *testing.T) {
	t.Parallel()

	sql := sqlReuseFacts
	for _, needle := range []string{
		"learning_atoms",
		"= ANY($2)",          // batch id list — never N+1
		"reuse_visibility",   // ADR-229 D1: creation owns the audience bit
		"deleted_at IS NULL", // soft-delete filter (ddd-enforcement #6)
		"tenant_id = $1",     // explicit tenant predicate on top of RLS
		"published",          // publish state rides the reuse
	} {
		if !strings.Contains(sql, needle) {
			t.Errorf("sqlReuseFacts missing %q:\n%s", needle, sql)
		}
	}
	// The author column on learning_atoms is `gcid` — NOT author_gcid / owner_gcid.
	if !strings.Contains(sql, "gcid") {
		t.Errorf("sqlReuseFacts must read the author column `gcid`:\n%s", sql)
	}
	// COALESCE guards rows written before migration 0029 backfilled the column.
	if !strings.Contains(sql, "COALESCE") {
		t.Errorf("sqlReuseFacts should COALESCE reuse_visibility to 'private':\n%s", sql)
	}
}

func TestReuseFacts_EmptyIDListSkipsTheQuery(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewCollectionRepositoryFromTxQuerier(q)

	facts, err := repo.ReuseFacts(context.Background(), testTenantID, nil)
	if err != nil {
		t.Fatalf("ReuseFacts(nil): %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("facts = %v; want empty", facts)
	}
	if q.runCount != 0 {
		t.Errorf("RunInTenantTx count = %d; want 0 — an empty batch needs no round-trip", q.runCount)
	}
}

func TestReuseFacts_UnwiredQuerierFailsLoud(t *testing.T) {
	t.Parallel()

	repo := NewCollectionRepositoryFromTxQuerier(nil)
	_, err := repo.ReuseFacts(context.Background(), testTenantID, []string{rfAtom1})
	if err == nil {
		t.Fatalf("expected a loud error when the tx querier is unwired")
	}
}

// Compile-time proof the pg repo satisfies the domain's fact-lookup port.
func TestCollectionRepository_ImplementsAtomReuseFactLookup(t *testing.T) {
	t.Parallel()
	var _ collection.AtomReuseFactLookup = (*CollectionRepository)(nil)
}

// -----------------------------------------------------------------------------
// ADR-233 D7 — the PG enum is gone; visibility is TEXT
// -----------------------------------------------------------------------------

func TestCollectionSQL_NoLongerCastsToRetiredEnum(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewCollectionRepositoryFromTxQuerier(q)

	c, err := collection.New(collection.NewParams{
		TenantID:   testTenantID,
		OwnerGcid:  testOwnerID,
		Title:      "Audience",
		Visibility: audience.Tenant,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, sql := range q.tx.execSQLs {
		if strings.Contains(sql, "collection_visibility") {
			t.Errorf("SQL still casts to the retired enum `collection_visibility`:\n%s", sql)
		}
	}
	// The value written must be the canonical lowercase audience token.
	var wrote bool
	for i, sql := range q.tx.execSQLs {
		if !strings.Contains(sql, "INSERT INTO collections") {
			continue
		}
		for _, a := range q.tx.execArgs[i] {
			if s, ok := a.(string); ok && s == "tenant" {
				wrote = true
			}
			if s, ok := a.(string); ok && (s == "TENANT_INTERNAL" || s == "PUBLIC" || s == "PRIVATE") {
				t.Errorf("SQL wrote the RETIRED vocabulary %q; want the canonical audience token", s)
			}
		}
	}
	if !wrote {
		t.Errorf("INSERT INTO collections did not carry visibility='tenant'; args=%v", q.tx.execArgs)
	}
}

func TestCollectionRepository_Get_ScansAudienceFromText(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewCollectionRepositoryFromTxQuerier(q)

	sql, ok := func() (string, bool) {
		// Force the Get SQL to be registered by probing the const directly.
		return sqlGetCollection, true
	}()
	if !ok {
		t.Fatal("unreachable")
	}
	if strings.Contains(sql, "visibility::text") {
		t.Errorf("Get SQL still casts visibility::text; the column IS text now:\n%s", sql)
	}

	q.tx.queryRows[sqlGetCollection] = [][]any{
		{testCollID, testTenantID, testOwnerID, "Set", "", "friends", tsFixed, tsFixed, nil},
	}
	got, err := repo.Get(context.Background(), testTenantID, testCollID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Visibility != audience.Friends {
		t.Errorf("Visibility = %q; want the audience VO value `friends`", got.Visibility)
	}
	if !got.Visibility.Valid() {
		t.Errorf("scanned visibility %q is not a valid audience", got.Visibility)
	}
}

// A row still carrying the RETIRED vocabulary must NOT be silently coerced —
// migration 0031 rewrites those rows, and a straggler has to be visible.
func TestCollectionRepository_Get_RetiredVocabularyIsNotSilentlyCoerced(t *testing.T) {
	t.Parallel()

	q := newStubTxQuerier()
	repo := NewCollectionRepositoryFromTxQuerier(q)
	q.tx.queryRows[sqlGetCollection] = [][]any{
		{testCollID, testTenantID, testOwnerID, "Set", "", "TENANT_INTERNAL", tsFixed, tsFixed, nil},
	}
	got, err := repo.Get(context.Background(), testTenantID, testCollID)
	if err != nil {
		// Failing loud here is acceptable too.
		return
	}
	if got.Visibility.Valid() {
		t.Errorf("legacy value TENANT_INTERNAL scanned as a VALID audience (%q) — it must not be coerced", got.Visibility)
	}
}
