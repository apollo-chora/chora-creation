// question_job_repository_test.go — TDD coverage for the
// QuestionJobRepository pgx adapter (P5-C).
//
// Mirrors the existing question_repository_test.go stubQuerier +
// stubTxQuerier pattern so SQL emission is observable without a live DB.
// All paths run inside RunInTenantTx so SET LOCAL chora.tenant_id applies
// before any read/write (chora_creation_app_rw is NOBYPASSRLS).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	jobID   = "01970000-0000-7000-8000-000000000001"
	jobTen  = "22222222-2222-7222-8222-222222222222"
	jobAtom = "00000000-0000-7000-8000-00000000a0a2"
	jobAuth = "00000000-0000-7000-8000-000000001001"
)

func validJob() *question.ComposeJob {
	now := time.Now().UTC()
	return &question.ComposeJob{
		JobID:          jobID,
		AtomID:         jobAtom,
		TenantID:       jobTen,
		AuthorGCID:     jobAuth,
		Status:         question.JobStatusRequested,
		ManaActionCode: "question_authoring_model_answer",
		ManaCharged:    5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// -----------------------------------------------------------------------------
// Create — inserts a freshly-constructed job in REQUESTED status
// -----------------------------------------------------------------------------

func TestQuestionJobRepository_Create_InsertsRow_InTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	if err := r.Create(context.Background(), validJob()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx called once, got %d", len(tq.calls))
	}
	if tq.calls[0].tenantID != jobTen {
		t.Errorf("RunInTenantTx tenantID = %q; want %q", tq.calls[0].tenantID, jobTen)
	}
	if !strings.Contains(tq.execSQL, "INSERT INTO question_generation_jobs") {
		t.Errorf("expected INSERT INTO question_generation_jobs; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "mana_idempotency_key") {
		t.Errorf("expected mana_idempotency_key column referenced; got %q", tq.execSQL)
	}
}

// ADR-195 WS3 — Create persists the compose discriminant columns (intent at
// $17, input_kind at $18). NULLIF maps an unset intent to NULL (back-compat).
func TestQuestionJobRepository_Create_PersistsComposeColumns(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	job := validJob()
	job.Intent = question.IntentNewQuestion
	job.Input = question.Input{Prompt: "x"}
	if err := r.Create(context.Background(), job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(tq.execSQL, "intent") || !strings.Contains(tq.execSQL, "input_kind") {
		t.Errorf("INSERT must reference intent + input_kind columns; got %q", tq.execSQL)
	}
	if len(tq.execArgs) < 17 {
		t.Fatalf("expected >=17 INSERT args; got %d", len(tq.execArgs))
	}
	if tq.execArgs[15] != "new_question" {
		t.Errorf("intent arg ($16) = %v; want new_question", tq.execArgs[15])
	}
	if tq.execArgs[16] != "prompt" {
		t.Errorf("input_kind arg ($17) = %v; want prompt", tq.execArgs[16])
	}

	// Unset intent → empty string (NULLIF → NULL): back-compat for any caller
	// not yet on the compose model.
	tq2 := &stubTxQuerier{}
	r2 := pg.NewQuestionJobRepositoryFromTxQuerier(tq2)
	if err := r2.Create(context.Background(), validJob()); err != nil {
		t.Fatalf("Create (no intent): %v", err)
	}
	if tq2.execArgs[15] != "" || tq2.execArgs[16] != "" {
		t.Errorf("unset intent must emit empty strings; got intent=%v input_kind=%v", tq2.execArgs[15], tq2.execArgs[16])
	}
}

func TestQuestionJobRepository_Create_RejectsNil(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if err := r.Create(context.Background(), nil); err == nil {
		t.Error("expected error on nil job")
	}
}

// -----------------------------------------------------------------------------
// Get — loads job with candidates_jsonb
// -----------------------------------------------------------------------------

func TestQuestionJobRepository_Get_ReturnsJobWithCandidates(t *testing.T) {
	now := timeNowVal()
	candJSON := `[{"draft_id":"d1","question":"Q?","answer":"A","distractors":["d1","d2"]}]`
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{
		cols: []any{
			jobID,
			jobAtom,
			jobTen,
			jobAuth,
			"succeeded",
			"question_authoring_ai_draft",
			int32(10),
			"", // source_blob_uri
			"", // source_mime_type
			candJSON,
			"", // error
			"key-1",
			now,             // created_at
			(*timePtr)(nil), // started_at
			(*timePtr)(nil), // completed_at
			(*timePtr)(nil), // accepted_at
			now,             // updated_at
		},
	}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	got, err := r.Get(context.Background(), jobTen, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil job")
	}
	if got.JobID != jobID {
		t.Errorf("JobID = %q", got.JobID)
	}
	if got.Status != question.JobStatusSucceeded {
		t.Errorf("Status = %q; want succeeded", got.Status)
	}
	if len(got.CandidateQuestionsJSON) == 0 {
		t.Error("CandidateQuestionsJSON empty; expected populated")
	}
	if !strings.Contains(tq.querySQL, "FROM question_generation_jobs") {
		t.Errorf("expected FROM question_generation_jobs in SQL; got %q", tq.querySQL)
	}
}

// composeJobRow builds a fully column-aligned stubRow for sqlSelectJob with the
// ADR-195 WS9 intent + input_kind columns appended after updated_at. Only the
// compose-relevant columns vary; the rest are fixed valid values.
func composeJobRow(intent, inputKind, sourceBlobURI, sourceMimeType, settingsJSON string) []any {
	now := timeNowVal()
	return []any{
		jobID, jobAtom, jobTen, jobAuth,
		"succeeded",
		"question_authoring", int32(0),
		sourceBlobURI, sourceMimeType,
		`[{"draft_id":"d1"}]`, // candidate_questions_jsonb
		settingsJSON,          // settings
		"", "", "",            // proposed_test_set / generation_summary / pipeline_trace
		"",                                                          // error
		"key-1",                                                     // mana_idempotency_key
		now, (*timePtr)(nil), (*timePtr)(nil), (*timePtr)(nil), now, // created/started/completed/accepted/updated
		intent, inputKind, // ADR-195 WS9 — appended after updated_at
	}
}

// TestQuestionJobRepository_Get_ReconstructsComposeVOs covers ADR-195 WS9 step 1:
// the repo read must SELECT intent + input_kind and reconstruct job.Intent +
// job.Input so the domain branches on the explicit compose VOs — NOT the legacy
// job_type fallback. The "explicit intent wins" case deliberately stores an intent
// that DISAGREES with job_type to prove the read reconstruction is authoritative.
func TestQuestionJobRepository_Get_ReconstructsComposeVOs(t *testing.T) {
	cases := []struct {
		name       string
		row        []any
		wantIntent question.Intent
		wantKind   question.InputKind
		wantPrompt string
		wantFiles  int
		wantByHand bool
	}{
		{
			name:       "source_files via source_blob_uri",
			row:        composeJobRow("new_question", "source_files", "gs://b/m.pdf", "application/pdf", ""),
			wantIntent: question.IntentNewQuestion, wantKind: question.InputSourceFiles, wantFiles: 1,
		},
		{
			name:       "source_files via settings.source_files (no blob)",
			row:        composeJobRow("new_question", "source_files", "", "", `{"source_files":[{"blob_uri":"gs://b/a.pdf","mime_type":"application/pdf","role":"source"},{"blob_uri":"gs://b/b.pdf","mime_type":"application/pdf","role":"source"}]}`),
			wantIntent: question.IntentNewQuestion, wantKind: question.InputSourceFiles, wantFiles: 2,
		},
		{
			name:       "explicit intent wins over job_type (model_answer_fill vs ai_draft)",
			row:        composeJobRow("model_answer_fill", "prompt", "", "", `{"prompt":"stem text"}`),
			wantIntent: question.IntentModelAnswerFill, wantKind: question.InputPrompt, wantPrompt: "stem text",
		},
		{
			name:       "by_hand manual draft",
			row:        composeJobRow("new_question", "by_hand", "", "", "{}"),
			wantIntent: question.IntentNewQuestion, wantKind: question.InputByHand, wantByHand: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tq := &stubTxQuerier{}
			tq.queryRow = &stubRow{cols: tc.row}
			r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

			got, err := r.Get(context.Background(), jobTen, jobID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Intent != tc.wantIntent {
				t.Errorf("Intent = %q; want %q (read must reconstruct from the intent column, not job_type)", got.Intent, tc.wantIntent)
			}
			if got.Input.Kind() != tc.wantKind {
				t.Errorf("Input.Kind() = %q; want %q", got.Input.Kind(), tc.wantKind)
			}
			if got.Input.Prompt != tc.wantPrompt {
				t.Errorf("Input.Prompt = %q; want %q", got.Input.Prompt, tc.wantPrompt)
			}
			if len(got.Input.SourceFiles) != tc.wantFiles {
				t.Errorf("len(Input.SourceFiles) = %d; want %d", len(got.Input.SourceFiles), tc.wantFiles)
			}
			if got.Input.ByHand != tc.wantByHand {
				t.Errorf("Input.ByHand = %v; want %v", got.Input.ByHand, tc.wantByHand)
			}
			// ComposeModelForJob on the loaded job must take the explicit-VO path:
			// the reconstructed Intent wins (from the intent column, not a retired
			// job_type fallback).
			gotIntent, gotKind := question.ComposeModelForJob(got)
			if gotIntent != tc.wantIntent || gotKind != tc.wantKind {
				t.Errorf("ComposeModelForJob(loaded) = (%q, %q); want (%q, %q)", gotIntent, gotKind, tc.wantIntent, tc.wantKind)
			}
			// The SELECT must reference the compose discriminant columns.
			if !strings.Contains(tq.querySQL, "intent") || !strings.Contains(tq.querySQL, "input_kind") {
				t.Errorf("SELECT must reference intent + input_kind; got %q", tq.querySQL)
			}
		})
	}
}

func TestQuestionJobRepository_Get_NotFound(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	_, err := r.Get(context.Background(), jobTen, jobID)
	if !errors.Is(err, question.ErrNotFound) {
		t.Errorf("err = %v; want question.ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// UpdateStatus — atomic status transition + optional candidates JSON + err
// -----------------------------------------------------------------------------

func TestQuestionJobRepository_UpdateStatus_PersistsCandidatesAndStatus(t *testing.T) {
	// Single-stage: the caller passes the owning tenant_id, so the UPDATE runs
	// directly inside that tenant's tx — NO all-zeros preflight self-resolve.
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	candJSON := []byte(`[{"draft_id":"d1"}]`)
	if err := r.UpdateStatus(context.Background(), jobTen, jobID, question.JobStatusSucceeded, candJSON, ""); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if !strings.Contains(tq.execSQL, "UPDATE question_generation_jobs") {
		t.Errorf("expected UPDATE question_generation_jobs; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "SET status") {
		t.Errorf("expected SET status; got %q", tq.execSQL)
	}
	// Exactly ONE tenant tx — the deleted preflight no longer fires a second.
	if len(tq.calls) != 1 {
		t.Fatalf("expected RunInTenantTx exactly once (no preflight); got %d", len(tq.calls))
	}
	// The tx MUST be against the real (passed) tenant_id, never all-zeros.
	if tq.calls[0].tenantID != jobTen {
		t.Errorf("RunInTenantTx tenantID = %q; want %q (the passed tenant, not all-zeros)", tq.calls[0].tenantID, jobTen)
	}
	if tq.calls[0].tenantID == "00000000-0000-0000-0000-000000000000" {
		t.Error("RunInTenantTx ran with the all-zeros placeholder tenant — RLS bug reintroduced")
	}
}

func TestQuestionJobRepository_UpdateStatus_FailedRecordsError(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	if err := r.UpdateStatus(context.Background(), jobTen, jobID, question.JobStatusFailed, nil, "Vertex AI 5xx"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if !strings.Contains(tq.execSQL, "error") {
		t.Errorf("expected error column update; got %q", tq.execSQL)
	}
}

func TestQuestionJobRepository_UpdateStatus_NoTxQuerier_Errors(t *testing.T) {
	r := pg.NewQuestionJobRepositoryFromTxQuerier(nil)
	if err := r.UpdateStatus(context.Background(), jobTen, jobID, question.JobStatusRunning, nil, ""); err == nil {
		t.Error("expected error without TxQuerier")
	}
}

func TestQuestionJobRepository_UpdateStatus_RejectsEmptyTenant(t *testing.T) {
	// Fail loud: an empty tenant_id would set an empty RLS context and the
	// UPDATE would no-op against real (NOBYPASSRLS) Postgres. Reject up front.
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if err := r.UpdateStatus(context.Background(), "", jobID, question.JobStatusRunning, nil, ""); err == nil {
		t.Error("expected error on empty tenantID")
	}
	if len(tq.calls) != 0 {
		t.Errorf("expected NO RunInTenantTx on empty tenant; got %d", len(tq.calls))
	}
}

// -----------------------------------------------------------------------------
// PatchCandidatesUnderLock — atomic read-modify-write of candidate_questions_jsonb
// (SELECT ... FOR UPDATE) with NO status change (CHO-1819 P3c lost-update guard)
// -----------------------------------------------------------------------------

func TestQuestionJobRepository_PatchCandidatesUnderLock_LocksThenWrites(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{"succeeded", `[{"draft_id":"D1"}]`}}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	var gotStatus, gotCurrent string
	patched := []byte(`[{"draft_id":"D1","mcq_payload":{"image_url":"https://cdn/new.png"}}]`)
	err := r.PatchCandidatesUnderLock(context.Background(), jobTen, jobID,
		func(status string, current []byte) ([]byte, error) {
			gotStatus = status
			gotCurrent = string(current)
			return patched, nil
		})
	if err != nil {
		t.Fatalf("PatchCandidatesUnderLock: %v", err)
	}
	// apply saw the locked row's status + candidates.
	if gotStatus != "succeeded" {
		t.Errorf("apply status = %q; want succeeded", gotStatus)
	}
	if !strings.Contains(gotCurrent, "D1") {
		t.Errorf("apply current = %q; want the locked candidates", gotCurrent)
	}
	// Row-locked read.
	if !strings.Contains(tq.querySQL, "FOR UPDATE") {
		t.Errorf("expected SELECT ... FOR UPDATE; got %q", tq.querySQL)
	}
	// Write rewrites candidates, NOT status (parent stays succeeded; no self-loop).
	if !strings.Contains(tq.execSQL, "UPDATE question_generation_jobs") ||
		!strings.Contains(tq.execSQL, "candidate_questions_jsonb") {
		t.Errorf("expected candidates UPDATE; got %q", tq.execSQL)
	}
	if strings.Contains(tq.execSQL, "status") {
		t.Errorf("the write must not touch status; got %q", tq.execSQL)
	}
	// One tenant-scoped tx against the passed tenant (RLS); patched blob is an arg.
	if len(tq.calls) != 1 || tq.calls[0].tenantID != jobTen {
		t.Errorf("RunInTenantTx calls = %v; want one against %q", tq.calls, jobTen)
	}
	found := false
	for _, a := range tq.execArgs {
		if s, ok := a.(string); ok && strings.Contains(s, "https://cdn/new.png") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected patched candidates among exec args; got %v", tq.execArgs)
	}
}

func TestQuestionJobRepository_PatchCandidatesUnderLock_NotFound(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	applyCalled := false
	err := r.PatchCandidatesUnderLock(context.Background(), jobTen, jobID,
		func(_ string, _ []byte) ([]byte, error) {
			applyCalled = true
			return []byte(`[{}]`), nil
		})
	if !errors.Is(err, question.ErrNotFound) {
		t.Errorf("err = %v; want question.ErrNotFound", err)
	}
	if applyCalled {
		t.Error("apply must not run when the row is missing")
	}
	if tq.execSQL != "" {
		t.Errorf("no UPDATE expected when the row is missing; got %q", tq.execSQL)
	}
}

func TestQuestionJobRepository_PatchCandidatesUnderLock_ApplyError_NoWrite(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{"succeeded", `[]`}}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	sentinel := errors.New("fail soft")
	err := r.PatchCandidatesUnderLock(context.Background(), jobTen, jobID,
		func(_ string, _ []byte) ([]byte, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want the apply error verbatim", err)
	}
	if tq.execSQL != "" {
		t.Errorf("apply error must abort without a write; got %q", tq.execSQL)
	}
}

func TestQuestionJobRepository_PatchCandidatesUnderLock_RejectsEmptyTenant(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	err := r.PatchCandidatesUnderLock(context.Background(), "", jobID,
		func(_ string, c []byte) ([]byte, error) { return c, nil })
	if err == nil {
		t.Error("expected error on empty tenantID")
	}
	if len(tq.calls) != 0 {
		t.Errorf("expected NO RunInTenantTx on empty tenant; got %d", len(tq.calls))
	}
}

func TestQuestionJobRepository_PatchCandidatesUnderLock_RejectsNilApply(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if err := r.PatchCandidatesUnderLock(context.Background(), jobTen, jobID, nil); err == nil {
		t.Error("expected error on nil apply")
	}
	if len(tq.calls) != 0 {
		t.Errorf("expected NO RunInTenantTx on nil apply; got %d", len(tq.calls))
	}
}

func TestQuestionJobRepository_PatchCandidatesUnderLock_NoTxQuerier_Errors(t *testing.T) {
	r := pg.NewQuestionJobRepositoryFromTxQuerier(nil)
	err := r.PatchCandidatesUnderLock(context.Background(), jobTen, jobID,
		func(_ string, c []byte) ([]byte, error) { return c, nil })
	if err == nil {
		t.Error("expected error without TxQuerier")
	}
}

// -----------------------------------------------------------------------------
// UpdatePipelineTraceOnly — CR Phase 1 live-trace streaming. Status-preserving,
// monotonic, RLS-scoped partial-trace write (RETURNING-as-CAS for applied).
// -----------------------------------------------------------------------------

func TestQuestionJobRepository_UpdatePipelineTraceOnly_AppliesInTenantTx(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{jobID}} // RETURNING a row ⇒ applied
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	applied, err := r.UpdatePipelineTraceOnly(context.Background(), jobTen, jobID,
		[]byte(`[{"name":"generate"}]`))
	if err != nil {
		t.Fatalf("UpdatePipelineTraceOnly: %v", err)
	}
	if !applied {
		t.Error("applied = false; want true (RETURNING row)")
	}
	// Status-preserving + monotonic + running-only guards in the SQL.
	if !strings.Contains(tq.querySQL, "UPDATE question_generation_jobs") {
		t.Errorf("expected UPDATE question_generation_jobs; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "pipeline_trace_jsonb = $2::jsonb") {
		t.Errorf("expected pipeline_trace write; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "status = 'running'") {
		t.Errorf("expected status='running' guard (no terminal clobber); got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "jsonb_array_length") {
		t.Errorf("expected monotonic jsonb_array_length guard; got %q", tq.querySQL)
	}
	if strings.Contains(tq.querySQL, "SET status") {
		t.Errorf("must NOT transition status; got %q", tq.querySQL)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != jobTen {
		t.Errorf("expected one RunInTenantTx on tenant %q; got %+v", jobTen, tq.calls)
	}
}

func TestQuestionJobRepository_UpdatePipelineTraceOnly_GuardReject_NotApplied(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows} // guard rejected (terminal / not-newer)
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	applied, err := r.UpdatePipelineTraceOnly(context.Background(), jobTen, jobID,
		[]byte(`[{"name":"generate"}]`))
	if err != nil {
		t.Fatalf("guard reject must be a no-op, not an error: %v", err)
	}
	if applied {
		t.Error("applied = true; want false (ErrNoRows ⇒ guard rejected)")
	}
}

func TestQuestionJobRepository_UpdatePipelineTraceOnly_RejectsEmptyTenant(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if _, err := r.UpdatePipelineTraceOnly(context.Background(), "", jobID, []byte(`[]`)); err == nil {
		t.Error("expected error on empty tenantID (RLS no-op guard)")
	}
	if len(tq.calls) != 0 {
		t.Errorf("expected NO RunInTenantTx on empty tenant; got %d", len(tq.calls))
	}
}

func TestQuestionJobRepository_UpdatePipelineTraceOnly_EmptyTrace_NoOp(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	applied, err := r.UpdatePipelineTraceOnly(context.Background(), jobTen, jobID, nil)
	if err != nil {
		t.Fatalf("empty trace must be a no-op, not an error: %v", err)
	}
	if applied {
		t.Error("applied = true; want false (empty trace never writes)")
	}
	if len(tq.calls) != 0 {
		t.Errorf("expected NO RunInTenantTx on empty trace; got %d", len(tq.calls))
	}
}

func TestQuestionJobRepository_UpdatePipelineTraceOnly_NoTxQuerier_Errors(t *testing.T) {
	r := pg.NewQuestionJobRepositoryFromTxQuerier(nil)
	if _, err := r.UpdatePipelineTraceOnly(context.Background(), jobTen, jobID, []byte(`[]`)); err == nil {
		t.Error("expected error without TxQuerier")
	}
}
