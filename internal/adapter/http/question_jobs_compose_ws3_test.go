// question_jobs_compose_ws3_test.go — ADR-195 WS3. The create handler now
// populates the unified compose model (Intent/Input/Plan) on every create path
// and validates it (fail-loud) before persisting, WITHOUT changing the
// generation events (back-compat — the FE + orchestrator are untouched until
// WS8/WS4). These tests assert the persisted job carries the right compose model.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func postJSONJob(t *testing.T, srv http.Handler, atomID string, body map[string]any) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	jid, _ := got["job_id"].(string)
	if jid == "" {
		t.Fatal("job_id missing")
	}
	return jid
}

// ai_draft (prompt) → intent=new_question, input=prompt, and count drives a
// non-empty Plan (count=3 → plan total 3, NOT the old SetMode=false single).
func TestCreateQuestionJob_AIDraft_SetsComposeModel(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)

	jid := postJSONJob(t, srv, atomID, map[string]any{
		"type":          "ai_draft",
		"question_type": "mcq",
		"prompt":        "Generate MCQs on Scrum roles",
		"count":         3,
	})
	j := jobRepo.jobs[jid]
	if j == nil {
		t.Fatal("job not persisted")
	}
	if j.Intent != question.IntentNewQuestion {
		t.Errorf("Intent = %q; want new_question", j.Intent)
	}
	if j.Input.Kind() != question.InputPrompt {
		t.Errorf("Input.Kind() = %q; want prompt", j.Input.Kind())
	}
	if j.Input.Prompt == "" {
		t.Errorf("Input.Prompt must carry the author prompt")
	}
	if j.Plan.IsEmpty() || j.Plan.TotalCount() != 3 {
		t.Errorf("Plan must be non-empty with total 3 (count drives the plan); got empty=%v total=%d", j.Plan.IsEmpty(), j.Plan.TotalCount())
	}
	// Compose validity holds.
	if err := j.Validate(); err != nil {
		t.Errorf("persisted ai_draft job must pass Validate(): %v", err)
	}
}

// type=manual (by-hand) → intent=new_question, input=by_hand, no Plan.
func TestCreateQuestionJob_Manual_SetsComposeModel(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)

	jid := postJSONJob(t, srv, atomID, map[string]any{"type": "manual"})
	j := jobRepo.jobs[jid]
	if j == nil {
		t.Fatal("manual job not persisted")
	}
	if j.Intent != question.IntentNewQuestion {
		t.Errorf("Intent = %q; want new_question", j.Intent)
	}
	if !j.Input.ByHand || j.Input.Kind() != question.InputByHand {
		t.Errorf("Input must be by_hand; got ByHand=%v Kind=%q", j.Input.ByHand, j.Input.Kind())
	}
	if !j.Plan.IsEmpty() {
		t.Errorf("by-hand authoring must carry no Plan; got total %d", j.Plan.TotalCount())
	}
	if err := j.Validate(); err != nil {
		t.Errorf("persisted manual job must pass Validate(): %v", err)
	}
}
