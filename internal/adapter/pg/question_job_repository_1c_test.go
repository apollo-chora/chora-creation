// question_job_repository_1c_test.go — RED→GREEN coverage for the Lane 1c
// (CHO-1703 / ADR-180) QuestionJobRepository extensions:
//
//   - Create persists the job's settings JSONB (source_files roles ride
//     there — migration 0008 column existed but was never written).
//   - Get scans settings + proposed_test_set_jsonb (migration 0017).
//   - UpdateStatusWithProposal stamps candidates + the composer proposal
//     in ONE UPDATE.
//   - TransitionFromSucceeded is the conditional accept transition
//     (WHERE status='succeeded' ... RETURNING) backing the W3
//     exactly-once publish guard.
package pg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestQuestionJobRepository_Create_PersistsSettingsJSON(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	j := validJob()
	j.SettingsJSON = []byte(`{"count":3,"source_files":[{"blob_uri":"gs://b/k","mime_type":"application/pdf","role":"source"}]}`)
	if err := r.Create(context.Background(), j); err != nil {
		t.Fatalf("Create: %v", err)
	}
	found := false
	for _, a := range tq.execArgs {
		if s, ok := a.(string); ok && strings.Contains(s, `"source_files"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected settings JSON among insert args; got %v", tq.execArgs)
	}
}

func TestQuestionJobRepository_Get_ScansSettingsAndProposal(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{
		jobID, jobAtom, jobTen, jobAuth,
		"succeeded",
		"question_authoring_batch_parse", int32(50),
		"gs://b/tenants/t/jobs/j/source-1", "application/pdf",
		`[{"draft_id":"d1"}]`, // candidate_questions_jsonb
		`{"source_files":[{"blob_uri":"gs://b/k","role":"source"}]}`, // settings
		`{"title":"Proposed set","order":["d1"]}`,                    // proposed_test_set_jsonb
		"",       // error
		"idem-1", // mana_idempotency_key
		timeNowVal(), timePtr(nil), timePtr(nil), timePtr(nil), timeNowVal(),
	}}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	got, err := r.Get(context.Background(), jobTen, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(tq.querySQL, "settings") {
		t.Errorf("expected settings column in SELECT; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "proposed_test_set_jsonb") {
		t.Errorf("expected proposed_test_set_jsonb column in SELECT; got %q", tq.querySQL)
	}
	if !strings.Contains(string(got.SettingsJSON), "source_files") {
		t.Errorf("SettingsJSON = %q; want source_files content", got.SettingsJSON)
	}
	if !strings.Contains(string(got.ProposedTestSetJSON), "Proposed set") {
		t.Errorf("ProposedTestSetJSON = %q; want proposal content", got.ProposedTestSetJSON)
	}
}

func TestQuestionJobRepository_UpdateStatusWithProposal_EmitsBothColumns(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	err := r.UpdateStatusWithProposal(context.Background(), jobTen, jobID,
		question.JobStatusSucceeded,
		[]byte(`[{"draft_id":"d1"}]`),
		[]byte(`{"title":"T","order":["d1"]}`),
		[]byte(`{"requested_total":10,"generated_total":9}`),
		[]byte(`[{"name":"generator","status":"completed"}]`),
		"")
	if err != nil {
		t.Fatalf("UpdateStatusWithProposal: %v", err)
	}
	if len(tq.calls) != 1 || tq.calls[0].tenantID != jobTen {
		t.Fatalf("RunInTenantTx calls = %+v", tq.calls)
	}
	if !strings.Contains(tq.execSQL, "UPDATE question_generation_jobs") {
		t.Errorf("expected UPDATE; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "proposed_test_set_jsonb") {
		t.Errorf("expected proposed_test_set_jsonb in UPDATE; got %q", tq.execSQL)
	}
	if !strings.Contains(tq.execSQL, "generation_summary_jsonb") {
		t.Errorf("expected generation_summary_jsonb in UPDATE; got %q", tq.execSQL)
	}
	// CHO-1826 Gap #4 — the pipeline trace rides the same one-shot UPDATE.
	if !strings.Contains(tq.execSQL, "pipeline_trace_jsonb") {
		t.Errorf("expected pipeline_trace_jsonb in UPDATE; got %q", tq.execSQL)
	}
}

func TestQuestionJobRepository_UpdateStatusWithProposal_NilProposalKeepsColumn(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if err := r.UpdateStatusWithProposal(context.Background(), jobTen, jobID,
		question.JobStatusSucceeded, []byte(`[]`), nil, nil, nil, ""); err != nil {
		t.Fatalf("UpdateStatusWithProposal(nil proposal): %v", err)
	}
	// COALESCE/NULLIF semantics — the SQL must not hard-overwrite with NULL
	// when no proposal is supplied (single-question jobs).
	if !strings.Contains(tq.execSQL, "COALESCE") {
		t.Errorf("expected COALESCE-preserving update; got %q", tq.execSQL)
	}
}

func TestQuestionJobRepository_TransitionFromSucceeded_ReturnsTrueOnRowHit(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{cols: []any{jobID}}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	ok, err := r.TransitionFromSucceeded(context.Background(), jobTen, jobID, question.JobStatusAccepted)
	if err != nil {
		t.Fatalf("TransitionFromSucceeded: %v", err)
	}
	if !ok {
		t.Error("ok = false; want true when the conditional UPDATE returned a row")
	}
	if !strings.Contains(tq.querySQL, "status = 'succeeded'") {
		t.Errorf("expected status='succeeded' guard; got %q", tq.querySQL)
	}
	if !strings.Contains(tq.querySQL, "RETURNING") {
		t.Errorf("expected RETURNING for row-hit detection; got %q", tq.querySQL)
	}
}

func TestQuestionJobRepository_TransitionFromSucceeded_FalseWhenAlreadyTerminal(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)

	ok, err := r.TransitionFromSucceeded(context.Background(), jobTen, jobID, question.JobStatusAccepted)
	if err != nil {
		t.Fatalf("TransitionFromSucceeded: %v", err)
	}
	if ok {
		t.Error("ok = true; want false when no row matched (already accepted — exactly-once guard)")
	}
}

func TestQuestionJobRepository_TransitionFromSucceeded_RejectsNonAcceptTargets(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewQuestionJobRepositoryFromTxQuerier(tq)
	if _, err := r.TransitionFromSucceeded(context.Background(), jobTen, jobID, question.JobStatusRunning); err == nil {
		t.Error("expected error for non-accept target status")
	}
}
