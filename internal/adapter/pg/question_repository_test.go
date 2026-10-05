// question_repository_test.go — unit tests for the QuestionRepository pgx
// adapter. Mirrors the atom_repository_test.go stubQuerier + stubTxQuerier
// pattern (#27 RLS-fix seam) so the SQL surface is observable without a live
// DB.
//
// Per P3-A in /Users/daleleung/.claude/plans/golden-hopping-owl.md, the
// adapter satisfies ports.QuestionRepository:
//   - Save(ctx, q, rev) — atomic INSERT questions + INSERT question_revisions
//   - GetByAtomID + GetByID — JOIN with latest revision (ORDER BY revision_number DESC)
//   - AppendRevision — append a new revision row with monotonic revision_number
//   - SoftDelete — UPDATE deleted_at
//
// All paths wrap RLS via RunInTenantTx so SET LOCAL chora.tenant_id applies
// before any tenant-scoped SQL (chora_creation_app_rw is NOBYPASSRLS).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	qTenant   = "22222222-2222-7222-8222-222222222222"
	qAtomID   = "00000000-0000-7000-8000-00000000a0a2"
	qAuthorID = "00000000-0000-7000-8000-000000001001"
)

func validQ(t *testing.T) (*question.Question, *question.QuestionRevision) {
	t.Helper()
	mcq := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt-1", Label: "A", IsCorrect: false, Explainer: "wrong because reason"},
			{OptionID: "opt-2", Label: "B", IsCorrect: true, Explainer: "right because reason"},
		},
		XPOnCorrect: 10,
	}
	q, err := question.New(question.NewParams{
		TenantID:   qTenant,
		AtomID:     qAtomID,
		AuthorGcid: qAuthorID,
		Type:       question.TypeMCQ,
		Prompt:     "Which Scrum role owns the backlog?",
		SourceType: atom.SourceManual,
		MCQ:        mcq,
	})
	if err != nil {
		t.Fatalf("question.New: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, mcq, nil, qAuthorID, atom.SourceManual)
	if err != nil {
		t.Fatalf("question.NewRevision: %v", err)
	}
	q.LatestRevisionID = rev.RevisionID
	return q, rev
}

// -----------------------------------------------------------------------------
// Save — creates Question + first QuestionRevision atomically inside a tenant tx
// -----------------------------------------------------------------------------

func TestQuestionRepository_Save_CreatesQuestionAndFirstRevision_InTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	q, rev := validQ(t)

	if err := r.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("save: %v", err)
	}

	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx called once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != qTenant {
		t.Errorf("RunInTenantTx tenantID = %q; want %q", tq.calls[0].tenantID, qTenant)
	}
	// Both INSERTs must hit inside the same tx. We captured the LAST exec — assert
	// it's the revisions INSERT (committed after the question INSERT).
	if !strings.Contains(tq.execSQL, "INSERT INTO question_revisions") {
		t.Errorf("expected INSERT INTO question_revisions in tx; got %q", tq.execSQL)
	}
	// And the first exec was the questions INSERT.
	if !strings.Contains(tq.firstExecSQL, "INSERT INTO questions") {
		t.Errorf("expected first INSERT INTO questions in tx; got %q", tq.firstExecSQL)
	}
}

func TestQuestionRepository_Save_DuplicateAtomReturnsErrAtomHasQuestion(t *testing.T) {
	// Stub raises a pg unique-violation 23505 on the questions INSERT (D2
	// partial UNIQUE index uq_questions_atom_alive). Adapter must map that to
	// question.ErrAtomHasQuestion.
	tq := &stubTxQuerier{}
	// The first Exec is INSERT INTO questions — fail it with a 23505 imitation.
	tq.execErrSequence = []error{errors.New(`pg.Tx.Exec: ERROR: duplicate key value violates unique constraint "uq_questions_atom_alive" (SQLSTATE 23505)`)}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	q, rev := validQ(t)
	err := r.Save(context.Background(), q, rev)
	if !errors.Is(err, question.ErrAtomHasQuestion) {
		t.Fatalf("expected question.ErrAtomHasQuestion, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// GetByAtomID — returns latest revision
// -----------------------------------------------------------------------------

func TestQuestionRepository_GetByAtomID_ReturnsLatestRevision(t *testing.T) {
	tq := &stubTxQuerier{}
	// Stub one row with the joined columns we expect: question + latest revision.
	tq.queryRow = &stubRow{
		cols: []any{
			"01970000-0000-7000-8000-aaaaaaaaaaaa", // question_id
			qAtomID,                                // atom_id
			qTenant,                                // tenant_id
			qAuthorID,                              // author_gcid
			"mcq",                                  // question_type
			"Which Scrum role owns the backlog?",   // prompt
			"manual",                               // source_type
			"01970000-0000-7000-8000-bbbbbbbbbbbb", // latest_revision_id
			int32(1),                               // revision
			(*timePtr)(nil),                        // deleted_at
			timeNowVal(),                           // created_at
			timeNowVal(),                           // updated_at
			`{"options":[{"option_id":"opt-1","label":"A","is_correct":false,"explainer":"wrong"},{"option_id":"opt-2","label":"B","is_correct":true,"explainer":"right"}],"xp_on_correct":10}`, // mcq_payload (JSONB as text)
			"",                                     // oe_payload
			"01970000-0000-7000-8000-bbbbbbbbbbbb", // revision.revision_id
			int32(1),                               // revision.revision_number
			"manual",                               // revision.source_type
			"Which Scrum role owns the backlog?",   // revision.prompt
			timeNowVal(),                           // revision.authored_at
		},
	}

	r := pg.NewQuestionRepositoryFromTxQuerier(tq)
	q, rev, err := r.GetByAtomID(context.Background(), qTenant, qAtomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	if q == nil || rev == nil {
		t.Fatalf("expected both q and rev populated")
	}
	if q.Type != question.TypeMCQ {
		t.Errorf("type = %q; want mcq", string(q.Type))
	}
	if q.MCQ == nil || len(q.MCQ.Options) != 2 {
		t.Errorf("MCQ payload not decoded; got %+v", q.MCQ)
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("revision_number = %d; want 1", rev.RevisionNumber)
	}
	if !strings.Contains(tq.querySQL, "FROM questions") {
		t.Errorf("expected FROM questions in SQL; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "JOIN question_revisions") {
		t.Errorf("expected JOIN question_revisions in SQL; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "ORDER BY") {
		t.Errorf("expected ORDER BY (revision_number DESC) in SQL; got %q", tq.querySQL)
	}
}

func TestQuestionRepository_GetByAtomID_NotFound(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	_, _, err := r.GetByAtomID(context.Background(), qTenant, qAtomID)
	if !errors.Is(err, question.ErrNotFound) {
		t.Fatalf("expected question.ErrNotFound, got %v", err)
	}
}

func TestQuestionRepository_GetByID_RoundTripsByQuestionID(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{
		cols: []any{
			"01970000-0000-7000-8000-aaaaaaaaaaaa", // question_id
			qAtomID,
			qTenant,
			qAuthorID,
			"oe",
			"Explain why story points are preferred over hours.",
			"manual",
			"01970000-0000-7000-8000-bbbbbbbbbbbb",
			int32(1),
			(*timePtr)(nil),
			timeNowVal(),
			timeNowVal(),
			"",
			`{"model_answer":"Because they capture complexity and risk rather than time."}`,
			"01970000-0000-7000-8000-bbbbbbbbbbbb",
			int32(1),
			"manual",
			"Explain why story points are preferred over hours.",
			timeNowVal(),
		},
	}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	q, rev, err := r.GetByID(context.Background(), qTenant, "01970000-0000-7000-8000-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if q.Type != question.TypeOpenEnded {
		t.Errorf("type = %q; want oe", string(q.Type))
	}
	if q.OE == nil || q.OE.ModelAnswer == "" {
		t.Errorf("OE payload not decoded; got %+v", q.OE)
	}
	if rev == nil || rev.RevisionNumber != 1 {
		t.Errorf("revision missing or wrong number: %+v", rev)
	}
	if !strings.Contains(tq.querySQL, "q.question_id = $") {
		t.Errorf("expected question_id WHERE clause; got %q", tq.querySQL)
	}
}

// -----------------------------------------------------------------------------
// AppendRevision — monotonic revision_number
// -----------------------------------------------------------------------------

func TestQuestionRepository_AppendRevision_BumpsNumberMonotonically(t *testing.T) {
	tq := &stubTxQuerier{}
	// Two SQL hits: SELECT MAX(revision_number) then INSERT INTO question_revisions.
	tq.queryRow = &stubRow{cols: []any{int32(3)}}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	_, rev := validQ(t)
	// Pretend this revision is being appended after 3 existing rows.
	rev.RevisionNumber = 0 // adapter must compute the next number

	if err := r.AppendRevision(context.Background(), rev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if rev.RevisionNumber != 4 {
		t.Errorf("revision_number = %d; want 4", rev.RevisionNumber)
	}
	// The adapter emits two Exec calls in sequence (INSERT INTO question_revisions
	// then UPDATE questions latest_revision_id). The FIRST Exec is the INSERT;
	// the LAST is the UPDATE. Assert on the first.
	if !strings.Contains(tq.firstExecSQL, "INSERT INTO question_revisions") {
		t.Errorf("expected first INSERT INTO question_revisions; got %q", tq.firstExecSQL)
	}
	if !strings.Contains(tq.execSQL, "UPDATE questions") {
		t.Errorf("expected last UPDATE questions (latest_revision_id pointer bump); got %q", tq.execSQL)
	}
	if !strings.Contains(tq.querySQL, "MAX(revision_number)") {
		t.Errorf("expected MAX(revision_number) SELECT; got %q", tq.querySQL)
	}
}

// AppendRevision MUST sync the current-state projection (questions.mcq_payload
// + prompt) to the new revision — fetchOne / SnapshotQuestionByID read the
// payload from questions, NOT question_revisions, so an appended revision
// (PATCH edit OR W8 image re-home) is invisible to readers without this sync.
// Regression guard for the pg/fake divergence that hid the OT#4 re-home bug
// (the in-mem fake reflected rev→parent; pg did not).
func TestQuestionRepository_AppendRevision_SyncsPayloadToProjection(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{int32(1)}}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	_, rev := validQ(t)
	if err := r.AppendRevision(context.Background(), rev); err != nil {
		t.Fatalf("append: %v", err)
	}
	// The LAST Exec is the UPDATE questions projection sync.
	if !strings.Contains(tq.execSQL, "UPDATE questions") {
		t.Fatalf("expected UPDATE questions; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "mcq_payload") {
		t.Errorf("UPDATE must sync mcq_payload to the new revision; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "latest_revision_id") {
		t.Errorf("UPDATE must still bump latest_revision_id; got %q", tq.execSQL)
	}
	// The rev's payload JSON must be among the UPDATE args (not just the
	// pointer bump) so the projection reflects the new content.
	var sawPayload bool
	for _, a := range tq.execArgs {
		if s, ok := a.(string); ok && strings.Contains(s, "\"options\"") {
			sawPayload = true
		}
	}
	if !sawPayload {
		t.Errorf("UPDATE args must carry the new revision's mcq_payload JSON; got %#v", tq.execArgs)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete
// -----------------------------------------------------------------------------

func TestQuestionRepository_SoftDelete_SetsDeletedAt(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	if err := r.SoftDelete(context.Background(), qTenant, "01970000-0000-7000-8000-aaaaaaaaaaaa"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != qTenant {
		t.Errorf("expected RunInTenantTx with %q; got %+v", qTenant, tq.calls)
	}
	if !strings.Contains(tq.execSQL, "UPDATE questions") {
		t.Errorf("expected UPDATE questions; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "SET deleted_at") {
		t.Errorf("expected SET deleted_at; got %q", tq.execSQL)
	}
}

// -----------------------------------------------------------------------------
// Nil-arg + wiring guards
// -----------------------------------------------------------------------------

func TestQuestionRepository_Save_RejectsNilArgs(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)
	if err := r.Save(context.Background(), nil, nil); err == nil {
		t.Errorf("expected error on nil args")
	}
}

func TestQuestionRepository_AppendRevision_RejectsNil(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)
	if err := r.AppendRevision(context.Background(), nil); err == nil {
		t.Errorf("expected error on nil rev")
	}
}

// -----------------------------------------------------------------------------
// Save — OE path encodes oe_payload JSON
// -----------------------------------------------------------------------------

func TestQuestionRepository_Save_OE_EncodesOEPayload(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	oe := &question.OEPayload{
		ModelAnswer: "Because story points capture complexity + risk rather than time.",
	}
	q, err := question.New(question.NewParams{
		TenantID:   qTenant,
		AtomID:     qAtomID,
		AuthorGcid: qAuthorID,
		Type:       question.TypeOpenEnded,
		Prompt:     "Why are story points preferred over hours?",
		SourceType: atom.SourceManual,
		OE:         oe,
	})
	if err != nil {
		t.Fatalf("New OE: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, nil, oe, qAuthorID, atom.SourceManual)
	if err != nil {
		t.Fatalf("NewRevision OE: %v", err)
	}
	q.LatestRevisionID = rev.RevisionID

	if err := r.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("save OE: %v", err)
	}

	// First exec is questions INSERT; the args include the oe_payload JSON
	// at position 10 (1-indexed in SQL = index 9 in args). Assert that the
	// MARSHALLED JSON contains the model_answer text.
	if len(tq.firstExecArgs) < 10 {
		t.Fatalf("not enough args captured: %d", len(tq.firstExecArgs))
	}
	mcqJSON, _ := tq.firstExecArgs[8].(string)
	oeJSON, _ := tq.firstExecArgs[9].(string)
	if mcqJSON != "" {
		t.Errorf("OE save should have empty mcq_payload arg; got %q", mcqJSON)
	}
	if !strings.Contains(oeJSON, "model_answer") {
		t.Errorf("expected oe_payload arg to carry model_answer; got %q", oeJSON)
	}
}

// -----------------------------------------------------------------------------
// AppendRevision — bumps from zero (first revision case)
// -----------------------------------------------------------------------------

func TestQuestionRepository_AppendRevision_FirstRevisionBumpsToOne(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{int32(0)}} // no prior revisions
	r := pg.NewQuestionRepositoryFromTxQuerier(tq)

	_, rev := validQ(t)
	rev.RevisionNumber = 99 // adapter must overwrite

	if err := r.AppendRevision(context.Background(), rev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("revision_number = %d; want 1", rev.RevisionNumber)
	}
}
