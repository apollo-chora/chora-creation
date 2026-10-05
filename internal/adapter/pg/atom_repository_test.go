package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// stubQuerier — captures the SQL emitted by the repo + injects deterministic
// return values for Get / List path scans. Keeps the unit test free of pgx.
type stubQuerier struct {
	execSQL  string
	execArgs []any
	execErr  error

	// firstExecSQL captures the very first Exec call across the stub's
	// lifecycle (so tests covering an Exec-then-Exec pair — e.g. Save:
	// INSERT questions THEN INSERT question_revisions — can assert both
	// emissions). Subsequent Exec calls overwrite execSQL but NOT
	// firstExecSQL.
	firstExecSQL  string
	firstExecArgs []any
	execCount     int

	// execErrSequence — when non-nil, each Exec consumes the head of the
	// slice as its return error (rest of the slice survives for the next
	// call). When the slice is exhausted, execErr is used. Lets a test
	// script "fail the first Exec but pass the second" — needed for the
	// 23505 duplicate-key path on Save.
	execErrSequence []error

	querySQL  string
	queryArgs []any
	queryRow  *stubRow
	queryRows *stubRows
	queryErr  error
}

type stubRow struct {
	err  error
	cols []any // values to populate dest in order
}

func (r *stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		if i >= len(r.cols) {
			return nil
		}
		assign(d, r.cols[i])
	}
	return nil
}

type stubRows struct {
	rows  [][]any
	idx   int
	err   error
	close bool
}

func (r *stubRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	return true
}

func (r *stubRows) Scan(dest ...any) error {
	row := r.rows[r.idx]
	r.idx++
	for i, d := range dest {
		if i >= len(row) {
			return nil
		}
		assign(d, row[i])
	}
	return nil
}

func (r *stubRows) Close() error { r.close = true; return nil }
func (r *stubRows) Err() error   { return r.err }

func (q *stubQuerier) Exec(_ context.Context, sql string, args ...any) error {
	q.execSQL = sql
	q.execArgs = args
	if q.execCount == 0 {
		q.firstExecSQL = sql
		q.firstExecArgs = args
	}
	q.execCount++
	if len(q.execErrSequence) > 0 {
		err := q.execErrSequence[0]
		q.execErrSequence = q.execErrSequence[1:]
		return err
	}
	return q.execErr
}

func (q *stubQuerier) QueryRow(_ context.Context, sql string, args ...any) pg.Row {
	q.querySQL = sql
	q.queryArgs = args
	if q.queryRow == nil {
		return &stubRow{err: pg.ErrNoRows}
	}
	return q.queryRow
}

func (q *stubQuerier) Query(_ context.Context, sql string, args ...any) (pg.Rows, error) {
	q.querySQL = sql
	q.queryArgs = args
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	if q.queryRows == nil {
		return &stubRows{}, nil
	}
	return q.queryRows, nil
}

// assign sets *dst = src using a tiny type-switch sufficient for the
// LearningAtom column scan (string / time.Time / *time.Time / int).
func assign(dst, src any) {
	switch d := dst.(type) {
	case *string:
		if s, ok := src.(string); ok {
			*d = s
		}
	case *int:
		if n, ok := src.(int); ok {
			*d = n
		}
	case *int32:
		if n, ok := src.(int32); ok {
			*d = n
		}
	case *time.Time:
		if t, ok := src.(time.Time); ok {
			*d = t
		}
	case **time.Time:
		if t, ok := src.(*time.Time); ok {
			*d = t
		}
	case *[]string:
		if v, ok := src.([]string); ok {
			*d = v
		}
	}
}

// -----------------------------------------------------------------------------
// AtomRepository unit tests (RED → GREEN against stubQuerier)
// -----------------------------------------------------------------------------

func TestAtomRepository_Save_EmitsUpsertSQL(t *testing.T) {
	q := &stubQuerier{}
	r := pg.NewAtomRepositoryWithQuerier(q)

	a, err := atom.New(atom.NewParams{
		TenantID: "01935b5a-9bcf-7000-7000-000000000001",
		Gcid:     "00000000-0000-7000-8000-000000001001",
		Title:    "RED test",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("new atom: %v", err)
	}

	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(q.execSQL, "INSERT INTO learning_atoms") {
		t.Errorf("expected INSERT INTO learning_atoms in emitted SQL; got %q", q.execSQL)
	}
	if !strings.Contains(q.execSQL, "ON CONFLICT (atom_id) DO UPDATE") {
		t.Errorf("expected UPSERT clause; got %q", q.execSQL)
	}
}

func TestAtomRepository_Save_Nil(t *testing.T) {
	q := &stubQuerier{}
	r := pg.NewAtomRepositoryWithQuerier(q)
	if err := r.Save(context.Background(), nil); err == nil {
		t.Fatalf("expected error on nil atom")
	}
}

func TestAtomRepository_Get_NotFound(t *testing.T) {
	q := &stubQuerier{queryRow: &stubRow{err: pg.ErrNoRows}}
	r := pg.NewAtomRepositoryWithQuerier(q)
	_, err := r.Get(context.Background(), "tenantA", "missing")
	if !errors.Is(err, atom.ErrNotFound) {
		t.Fatalf("expected atom.ErrNotFound, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Tenant-scoped transaction wrapping (RLS path)
//
// chora_creation_app_rw is NOBYPASSRLS; a bare QueryRow against learning_atoms
// without `SET LOCAL chora.tenant_id` silently returns 0 rows. The fix: when
// the repository is constructed with a real *PgxPoolQuerier (i.e. a TxQuerier
// is available), every read/write MUST run inside RunInTenantTx so the
// SET LOCAL applies before the SQL fires. The stubTxQuerier below captures
// the (tenantID, fn) call so the tests can assert wrapping happened.
// -----------------------------------------------------------------------------

// stubTxQuerier records each RunInTenantTx invocation and runs fn against an
// embedded inner Querier so the underlying SQL emit is still observable.
type stubTxQuerier struct {
	stubQuerier
	calls []txCall
	// runErr forces RunInTenantTx to short-circuit before invoking fn (used
	// to assert error-propagation if needed).
	runErr error
}

type txCall struct {
	tenantID string
}

// RunInTenantTx records the call and runs fn with the embedded stubQuerier
// adapted to the Tx surface so the SQL emitted by the repo lands on the
// stub's querySQL / execSQL fields.
func (q *stubTxQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pg.Tx) error) error {
	q.calls = append(q.calls, txCall{tenantID: tenantID})
	if q.runErr != nil {
		return q.runErr
	}
	return fn(ctx, &stubTx{q: &q.stubQuerier})
}

// stubTx adapts the embedded stubQuerier to pg.Tx so fn can call Exec/Query/
// QueryRow inside the RunInTenantTx callback.
type stubTx struct {
	q *stubQuerier
}

func (t *stubTx) Exec(ctx context.Context, sql string, args ...any) error {
	return t.q.Exec(ctx, sql, args...)
}
func (t *stubTx) Query(ctx context.Context, sql string, args ...any) (pg.Rows, error) {
	return t.q.Query(ctx, sql, args...)
}
func (t *stubTx) QueryRow(ctx context.Context, sql string, args ...any) pg.Row {
	return t.q.QueryRow(ctx, sql, args...)
}

func TestAtomRepository_Get_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, err := r.Get(context.Background(),
		"22222222-2222-7222-8222-222222222222",
		"00000000-0000-7000-8000-00000000a0a1",
	)
	if !errors.Is(err, atom.ErrNotFound) {
		t.Fatalf("expected atom.ErrNotFound (stub returns ErrNoRows), got %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx to be called exactly once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != "22222222-2222-7222-8222-222222222222" {
		t.Errorf("expected RunInTenantTx tenantID = %q, got %q",
			"22222222-2222-7222-8222-222222222222", tq.calls[0].tenantID)
	}
	if !strings.Contains(tq.querySQL, "FROM learning_atoms") {
		t.Errorf("expected SELECT FROM learning_atoms to be emitted; got %q", tq.querySQL)
	}
}

func TestAtomRepository_Get_NoTxQuerier_FallsBackToBareQuerier(t *testing.T) {
	// Constructed via NewAtomRepositoryWithQuerier — bare Querier, no
	// TxQuerier. The bare-querier path must still work so unit tests with
	// the existing stubQuerier do not need an RLS-aware tx seam.
	q := &stubQuerier{queryRow: &stubRow{err: pg.ErrNoRows}}
	r := pg.NewAtomRepositoryWithQuerier(q)

	_, err := r.Get(context.Background(), "tenantA", "missing")
	if !errors.Is(err, atom.ErrNotFound) {
		t.Fatalf("expected atom.ErrNotFound, got %v", err)
	}
	// Bare querier path: the stub's querySQL captured the SELECT directly
	// (no tx wrapping in this branch).
	if !strings.Contains(q.querySQL, "FROM learning_atoms") {
		t.Errorf("expected SELECT FROM learning_atoms via bare querier; got %q", q.querySQL)
	}
}

func TestAtomRepository_Save_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	a, err := atom.New(atom.NewParams{
		TenantID: "22222222-2222-7222-8222-222222222222",
		Gcid:     "00000000-0000-7000-8000-000000001002",
		Title:    "tx-wrapped save",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("new atom: %v", err)
	}
	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx to be called exactly once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != a.TenantID {
		t.Errorf("expected RunInTenantTx tenantID = %q, got %q", a.TenantID, tq.calls[0].tenantID)
	}
	if !strings.Contains(tq.execSQL, "INSERT INTO learning_atoms") {
		t.Errorf("expected INSERT INTO learning_atoms inside tx; got %q", tq.execSQL)
	}
}

func TestAtomRepository_List_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{} // empty result OK
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, err := r.List(context.Background(),
		"22222222-2222-7222-8222-222222222222",
		atom.ListFilter{},
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx to be called exactly once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != "22222222-2222-7222-8222-222222222222" {
		t.Errorf("expected RunInTenantTx tenantID match; got %q", tq.calls[0].tenantID)
	}
	if !strings.Contains(tq.querySQL, "FROM learning_atoms") {
		t.Errorf("expected SELECT FROM learning_atoms inside tx; got %q", tq.querySQL)
	}
}

func TestAtomRepository_ListByCourse_WrapsInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRows = &stubRows{}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, err := r.ListByCourse(context.Background(),
		"22222222-2222-7222-8222-222222222222",
		"33333333-3333-7333-8333-333333333333",
	)
	if err != nil {
		t.Fatalf("list by course: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx to be called exactly once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != "22222222-2222-7222-8222-222222222222" {
		t.Errorf("tenantID mismatch: got %q", tq.calls[0].tenantID)
	}
	if !strings.Contains(tq.querySQL, "FROM learning_atoms") ||
		!strings.Contains(tq.querySQL, "course_id") {
		t.Errorf("expected course-scoped SELECT inside tx; got %q", tq.querySQL)
	}
}

func TestAtomRepository_List_BareQuerier_FallbackPath(t *testing.T) {
	// Bare-querier path (test seam) must still iterate rows correctly so the
	// stubQuerier-based tests are not silently bypassed by the new tx wrap.
	q := &stubQuerier{queryRows: &stubRows{}}
	r := pg.NewAtomRepositoryWithQuerier(q)
	out, err := r.List(context.Background(),
		"22222222-2222-7222-8222-222222222222",
		atom.ListFilter{Status: atom.StatusDraft, Limit: 10, Offset: 5},
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 rows, got %d", len(out))
	}
	if !strings.Contains(q.querySQL, "status = $2::atom_status") {
		t.Errorf("expected status filter clause; got %q", q.querySQL)
	}
	if !strings.Contains(q.querySQL, "LIMIT 10") || !strings.Contains(q.querySQL, "OFFSET 5") {
		t.Errorf("expected LIMIT/OFFSET in SQL; got %q", q.querySQL)
	}
}

func TestAtomRepository_ListByCourse_BareQuerier_FallbackPath(t *testing.T) {
	q := &stubQuerier{queryRows: &stubRows{}}
	r := pg.NewAtomRepositoryWithQuerier(q)
	out, err := r.ListByCourse(context.Background(),
		"22222222-2222-7222-8222-222222222222",
		"33333333-3333-7333-8333-333333333333",
	)
	if err != nil {
		t.Fatalf("list by course: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 rows, got %d", len(out))
	}
	if !strings.Contains(q.querySQL, "course_id = $2::uuid") {
		t.Errorf("expected course_id filter clause; got %q", q.querySQL)
	}
}

func TestAtomRepository_Save_BareQuerier_FallbackPath(t *testing.T) {
	q := &stubQuerier{}
	r := pg.NewAtomRepositoryWithQuerier(q)
	a, err := atom.New(atom.NewParams{
		TenantID: "22222222-2222-7222-8222-222222222222",
		Gcid:     "00000000-0000-7000-8000-000000001002",
		Title:    "bare save",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("new atom: %v", err)
	}
	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(q.execSQL, "INSERT INTO learning_atoms") {
		t.Errorf("expected INSERT in bare-querier path; got %q", q.execSQL)
	}
}

func TestValidateTenantID_Rejects(t *testing.T) {
	// Direct test of the validator on the RunInTenantTx path: feed a tenant
	// id with non-hex chars and assert it never reaches fn.
	tq := pg.NewPgxPoolQuerier(nil) // pool unused; validator runs first
	called := false
	err := tq.RunInTenantTx(context.Background(), "not-a-uuid;DROP TABLE--",
		func(ctx context.Context, tx pg.Tx) error {
			called = true
			return nil
		})
	if err == nil {
		t.Fatalf("expected validation error for malformed tenant id")
	}
	if called {
		t.Errorf("fn must NOT run when validation rejects the tenant id")
	}
}

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — Save UPSERT must include new columns (per plan §1).
// -----------------------------------------------------------------------------

// TestAtomRepository_Save_UpsertIncludesPhase1Columns confirms the emitted
// SQL contains the new Phase 1 columns: question_type (renamed from
// atom_type), stem, subject, cognitive_level, imda_dimension_tags,
// media_assets, author_note. Old atom_type column must NOT appear in the
// emitted SQL (Decision #2 — dropped post-migration).
func TestAtomRepository_Save_UpsertIncludesPhase1Columns(t *testing.T) {
	q := &stubQuerier{}
	r := pg.NewAtomRepositoryWithQuerier(q)

	a, err := atom.New(atom.NewParams{
		TenantID: "01935b5a-9bcf-7000-7000-000000000001",
		Gcid:     "00000000-0000-7000-8000-000000001001",
		Title:    "phase1",
		Body:     "body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("new atom: %v", err)
	}
	a.Stem = "what is photosynthesis?"
	a.Subject = "biology"
	a.CognitiveLevel = atom.CognitiveLevelComprehension
	a.ImdaDimensionTags = []atom.ImdaDimTag{atom.ImdaDimTransparency}
	a.AuthorNote = "demo MCQ"
	a.MediaAssets = []atom.MediaAsset{
		{Type: "image", URL: "gs://x.png", MIME: "image/png", SizeBytes: 1},
	}
	a.QuestionType = atom.QuestionTypeMCQ

	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, col := range []string{
		"question_type",
		"stem",
		"subject",
		"cognitive_level",
		"imda_dimension_tags",
		"media_assets",
		"author_note",
	} {
		if !strings.Contains(q.execSQL, col) {
			t.Errorf("emitted SQL missing Phase 1 column %q; got %q", col, q.execSQL)
		}
	}
	// Old column removed per Decision #2 — `atom_type` MUST NOT appear in
	// any SQL clause (column references in INSERT / UPDATE SET). It can
	// appear in a comment but our emit does not include comments.
	if strings.Contains(q.execSQL, "atom_type") {
		t.Errorf("emitted SQL still references dropped atom_type column: %q", q.execSQL)
	}
}

// TestAtomRepository_Select_IncludesPhase1Columns confirms the SELECT
// projection includes the new columns so Get/List can scan them.
func TestAtomRepository_Select_IncludesPhase1Columns(t *testing.T) {
	q := &stubQuerier{queryRow: &stubRow{err: pg.ErrNoRows}}
	r := pg.NewAtomRepositoryWithQuerier(q)
	_, _ = r.Get(context.Background(), "tenantA", "missing")
	for _, col := range []string{
		"question_type",
		"stem",
		"subject",
		"cognitive_level",
		"imda_dimension_tags",
		"media_assets",
		"author_note",
	} {
		if !strings.Contains(q.querySQL, col) {
			t.Errorf("emitted SELECT missing Phase 1 column %q; got %q", col, q.querySQL)
		}
	}
}

// TestAtomRepository_Save_OnConflictIncludesPhase1Columns confirms the
// ON CONFLICT clause sets the new Phase 1 columns so an UPSERT update
// path (existing atom_id) refreshes them — not just the original columns.
func TestAtomRepository_Save_OnConflictIncludesPhase1Columns(t *testing.T) {
	q := &stubQuerier{}
	r := pg.NewAtomRepositoryWithQuerier(q)

	a, _ := atom.New(atom.NewParams{
		TenantID: "01935b5a-9bcf-7000-7000-000000000001",
		Gcid:     "00000000-0000-7000-8000-000000001001",
		Title:    "upsert", Body: "y", Mode: atom.ModeStraightUp,
	})
	a.Stem = "stem text"
	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Each Phase 1 column MUST appear in the EXCLUDED.<col> setter on the
	// ON CONFLICT UPDATE branch — confirms the upsert refreshes the new
	// columns on conflict, not just the initial INSERT.
	for _, col := range []string{
		"question_type",
		"stem",
		"subject",
		"cognitive_level",
		"imda_dimension_tags",
		"media_assets",
		"author_note",
	} {
		needle := col + "       " // catch the "= EXCLUDED." right after
		if !strings.Contains(q.execSQL, "EXCLUDED."+col) && !strings.Contains(q.execSQL, needle) {
			t.Errorf("ON CONFLICT SET missing EXCLUDED.%s; got %q", col, q.execSQL)
		}
	}
}

// TestScanAtom_ReadsPhase1Columns exercises the scan-side deserialization
// for the 6 new columns. We feed a populated stubRow with the same column
// count as atomColumns + Phase 1 additions; the scan must hydrate the
// aggregate's new fields end-to-end.
func TestScanAtom_ReadsPhase1Columns(t *testing.T) {
	now := time.Now().UTC()
	row := &stubRow{cols: []any{
		// 15 legacy columns: atom_id, tenant_id, gcid, course_id, title,
		// body, tags_json, mode, status, question_type, difficulty,
		// revision, created_at, updated_at, deleted_at
		"00000000-0000-7000-8000-000000000001", // atom_id
		"01935b5a-9bcf-7000-7000-000000000001", // tenant_id
		"00000000-0000-7000-8000-000000001001", // gcid
		"",                                     // course_id (COALESCED empty)
		"a title",                              // title
		"a body",                               // body
		`["go"]`,                               // tags_json
		"straight-up",                          // mode
		"draft",                                // status
		"mcq",                                  // question_type
		3,                                      // difficulty
		1,                                      // revision
		now,                                    // created_at
		now,                                    // updated_at
		(*time.Time)(nil),                      // deleted_at
		// 6 Phase 1 columns
		"stem text",                            // stem
		"biology",                              // subject
		"comprehension",                        // cognitive_level
		`["transparency","safety_robustness"]`, // imda_dimension_tags
		`[{"type":"image","url":"gs://x.png","mime":"image/png","size_bytes":1}]`, // media_assets
		"private note", // author_note
	}}

	q := &stubQuerier{queryRow: row}
	r := pg.NewAtomRepositoryWithQuerier(q)
	got, err := r.Get(context.Background(), "01935b5a-9bcf-7000-7000-000000000001",
		"00000000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("Get unexpected: %v", err)
	}
	if got == nil {
		t.Fatalf("Get returned nil atom")
	}
	if got.Stem != "stem text" {
		t.Errorf("Stem = %q; want %q", got.Stem, "stem text")
	}
	if got.Subject != "biology" {
		t.Errorf("Subject = %q; want biology", got.Subject)
	}
	if got.CognitiveLevel != atom.CognitiveLevelComprehension {
		t.Errorf("CognitiveLevel = %q; want comprehension", got.CognitiveLevel)
	}
	if len(got.ImdaDimensionTags) != 2 ||
		got.ImdaDimensionTags[0] != atom.ImdaDimTransparency ||
		got.ImdaDimensionTags[1] != atom.ImdaDimSafetyRobustness {
		t.Errorf("ImdaDimensionTags = %v; want [transparency safety_robustness]",
			got.ImdaDimensionTags)
	}
	if len(got.MediaAssets) != 1 || got.MediaAssets[0].URL != "gs://x.png" {
		t.Errorf("MediaAssets[0].URL = %q; want gs://x.png", got.MediaAssets[0].URL)
	}
	if got.AuthorNote != "private note" {
		t.Errorf("AuthorNote = %q; want private note", got.AuthorNote)
	}
	if got.QuestionType != atom.QuestionTypeMCQ {
		t.Errorf("QuestionType = %q; want mcq", got.QuestionType)
	}
}
