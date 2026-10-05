// question_jobs_typeplan_test.go — RED→GREEN coverage for the CHO-1819 P2
// mixed-type batch HTTP intake on POST /api/atoms/{atom_id}/question-jobs and
// the generation_summary projection on GET. The handler maps the settings
// `type_plan` array onto the aiassist.TypePlan VO (fail-loud 400 on a bad plan)
// and threads it through settings_json; the event question_type stays a valid
// enum (content_type rides "mixed" downstream).
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestCreateQuestionJob_Batch_MixedTypePlan_Returns202_ThreadsTypePlan(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeBlobStore{}
	srv, jobRepo, pub, atomID := newBatchJobsServer(t, mana, blob)

	// content_type "mixed" + a type_plan summing to count=10.
	settings := `{"job_type":"batch_source_material","question_type":"mixed","count":10,` +
		`"type_plan":[{"question_type":"mcq","count":8,"max_images":2},{"question_type":"oe","count":2,"max_images":0}]}`
	buf, contentType, err := buildMultipart("source.pdf", "application/pdf", []byte("%PDF-1.4 fake"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}

	// The batch path publishes generation_requested on the detached finish
	// goroutine; wait for it.
	events := pub.waitForEvents(t, 1, 2*time.Second)
	if len(events) != 1 || events[0].Topic != "chora.creation.question.generation_requested.v2" {
		t.Fatalf("expected 1 generation_requested event; got %+v", events)
	}
	p, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not map[string]any: %T", events[0].Payload)
	}
	// Event question_type must be a VALID enum (the first quota), never "mixed"
	// (that would dead-letter at the generation_requested binary encoder).
	if p["question_type"] != "mcq" {
		t.Errorf("event question_type = %v; want mcq (first quota — valid enum)", p["question_type"])
	}
	sj, _ := p["settings_json"].(string)
	if !strings.Contains(sj, "type_plan") {
		t.Errorf("settings_json must carry type_plan; got %s", sj)
	}
	if !strings.Contains(sj, `"count":10`) {
		t.Errorf("settings_json count must equal the sum (10); got %s", sj)
	}

	// Persisted job settings also carry the breakdown.
	if len(jobRepo.jobs) != 1 {
		t.Fatalf("job not persisted")
	}
	for _, j := range jobRepo.jobs {
		if !strings.Contains(string(j.SettingsJSON), "type_plan") {
			t.Errorf("persisted settings missing type_plan: %s", j.SettingsJSON)
		}
	}
}

func TestCreateQuestionJob_Batch_InvalidTypePlan_Returns400(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, _, atomID := newBatchJobsServer(t, mana, &fakeBlobStore{})

	// type_plan sums to 10 but count declares 9 → fail-loud mismatch.
	settings := `{"job_type":"batch_source_material","question_type":"mixed","count":9,` +
		`"type_plan":[{"question_type":"mcq","count":8,"max_images":0},{"question_type":"oe","count":2,"max_images":0}]}`
	buf, contentType, err := buildMultipart("source.pdf", "application/pdf", []byte("%PDF-1.4 fake"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["code"] != "CREATION_JOB_INVALID_TYPE_PLAN" {
		t.Errorf("error.code = %v; want CREATION_JOB_INVALID_TYPE_PLAN", resp["code"])
	}
	// No mana should be charged for a request rejected before the debit.
	if mana.deductCalls != 0 {
		t.Errorf("DeductMana called %d times; want 0 (rejected pre-debit)", mana.deductCalls)
	}
}

func TestCreateQuestionJob_Batch_InvalidQuestionTypeInPlan_Returns400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newBatchJobsServer(t, &fakeManaLedger{successDebits: 1}, &fakeBlobStore{})

	settings := `{"job_type":"batch_source_material","question_type":"mixed","count":5,` +
		`"type_plan":[{"question_type":"essay","count":5,"max_images":0}]}`
	buf, contentType, err := buildMultipart("source.pdf", "application/pdf", []byte("%PDF-1.4 fake"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != "CREATION_JOB_INVALID_TYPE_PLAN" {
		t.Errorf("error.code = %v; want CREATION_JOB_INVALID_TYPE_PLAN", resp["code"])
	}
}

func TestGetQuestionJob_SurfacesGenerationSummary(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newBatchJobsServer(t, &fakeManaLedger{successDebits: 1}, &fakeBlobStore{})

	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-0000000000c1", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		GenerationSummaryJSON: []byte(`{"requested_total":10,"generated_total":9,"generated_per_type":{"mcq":7,"oe":2},"shortfall_reason":"model_refused_one_oe"}`),
	}
	jobRepo.jobs[job.JobID] = job

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs/"+job.JobID, nil)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	gs, ok := resp["generation_summary"].(map[string]any)
	if !ok {
		t.Fatalf("generation_summary missing: %s", w.Body.String())
	}
	if gs["shortfall_reason"] != "model_refused_one_oe" {
		t.Errorf("shortfall_reason = %v", gs["shortfall_reason"])
	}
}

func TestGetQuestionJob_NoGenerationSummary_FieldAbsent(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newBatchJobsServer(t, &fakeManaLedger{successDebits: 1}, &fakeBlobStore{})
	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-0000000000c2", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
	}
	jobRepo.jobs[job.JobID] = job

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs/"+job.JobID, nil)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "generation_summary") {
		t.Errorf("generation_summary must be omitted when absent; body=%s", w.Body.String())
	}
}
