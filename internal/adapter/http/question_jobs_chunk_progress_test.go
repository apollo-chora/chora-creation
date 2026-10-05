package httpadapter_test

// CHO-2398 (ADR-251 D5): the poll serves questions-so-far while the job runs.
//
// A RUNNING job with chunk slots returns the flattened candidates in
// chunk_index order plus a chunk_progress {chunks_applied, chunks_total}
// block; a terminal job keeps serving the authoritative full set from
// candidate_questions_jsonb exactly as before (chunk fields ignored).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestGetJob_Running_ServesQuestionsSoFarFromChunkSlots(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})

	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-00000000cc01", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"},
		Status: question.JobStatusRunning,
		// Slots deliberately keyed out of order; assembly sorts numerically
		// (10 after 2, never lexicographic).
		ChunkCandidatesJSON: []byte(`{"1":[{"stem":"c"}],"0":[{"stem":"a"},{"stem":"b"}]}`),
		ChunkCountTotal:     3,
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
	var resp struct {
		Status        string           `json:"status"`
		Candidates    []map[string]any `json:"candidate_questions"`
		ChunkProgress *struct {
			Applied int `json:"chunks_applied"`
			Total   int `json:"chunks_total"`
		} `json:"chunk_progress"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	if resp.Status != "running" {
		t.Errorf("status = %q; want running", resp.Status)
	}
	stems := make([]string, 0, len(resp.Candidates))
	for _, c := range resp.Candidates {
		stems = append(stems, c["stem"].(string))
	}
	if len(stems) != 3 || stems[0] != "a" || stems[1] != "b" || stems[2] != "c" {
		t.Errorf("questions-so-far = %v; want [a b c] in chunk order", stems)
	}
	if resp.ChunkProgress == nil || resp.ChunkProgress.Applied != 2 || resp.ChunkProgress.Total != 3 {
		t.Errorf("chunk_progress = %+v; want applied 2 of total 3", resp.ChunkProgress)
	}
}

func TestGetJob_Terminal_ChunkSlotsDoNotOverrideTheFullSet(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})

	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-00000000cc02", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"},
		Status:                 question.JobStatusSucceeded,
		CandidateQuestionsJSON: []byte(`[{"stem":"final"}]`),
		ChunkCandidatesJSON:    []byte(`{"0":[{"stem":"partial"}]}`),
		ChunkCountTotal:        2,
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
	var resp struct {
		Candidates    []map[string]any `json:"candidate_questions"`
		ChunkProgress any              `json:"chunk_progress"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0]["stem"] != "final" {
		t.Errorf("terminal candidates = %v; want the authoritative full set", resp.Candidates)
	}
	if resp.ChunkProgress != nil {
		t.Errorf("chunk_progress must be absent on a terminal job; got %v", resp.ChunkProgress)
	}
}
