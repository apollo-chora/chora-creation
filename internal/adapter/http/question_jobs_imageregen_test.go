// question_jobs_imageregen_test.go — RED→GREEN for CHO-1819 P3 review image
// regenerate (CHO-1822 P3a). POST /api/atoms/{atom_id}/question-jobs/{job_id}/
// regenerate-image creates a lightweight image_regen job (settings carry
// parent_job_id + draft_id + placement + prompt) + publishes generation_requested
// with job_type=image_regen; the in-proc subscriber then publishes
// ai_assist.started.v1 (job_kind=image_regen + regen f22).
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// seedReviewableParent seeds a succeeded batch job with one candidate draft.
func seedReviewableParent(jobRepo *fakeJobRepo, atomID, draftID string) string {
	pid := uuid.NewString()
	cand := `[{"draft_id":"` + draftID + `","type":"mcq","prompt":"What is photosynthesis?"}]`
	jobRepo.jobs[pid] = &question.ComposeJob{
		JobID: pid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		CandidateQuestionsJSON: []byte(cand),
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	return pid
}

func TestCreateImageRegenJob_Returns202_CreatesImageRegenJob(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, pub, atomID := newJobsServer(t, mana)
	pid := seedReviewableParent(jobRepo, atomID, "draft-7")

	body := map[string]any{"draft_id": "draft-7", "placement": "stem", "prompt": "A clearer diagram"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+pid+"/regenerate-image", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["status"] != "requested" {
		t.Errorf("status = %v; want requested", got["status"])
	}
	regenJobID, _ := got["job_id"].(string)
	if regenJobID == "" || regenJobID == pid {
		t.Fatalf("expected a NEW image_regen job_id distinct from parent; got %q (parent %q)", regenJobID, pid)
	}

	// New image_regen job persisted with the regen spec in settings.
	rj := jobRepo.jobs[regenJobID]
	if rj == nil {
		t.Fatalf("image_regen job %s not persisted", regenJobID)
	}
	if rj.Intent != question.IntentImageRegen {
		t.Errorf("intent = %q; want image_regen", rj.Intent)
	}
	var settings map[string]any
	_ = json.Unmarshal(rj.SettingsJSON, &settings)
	if settings["parent_job_id"] != pid {
		t.Errorf("settings.parent_job_id = %v; want %s", settings["parent_job_id"], pid)
	}
	if settings["draft_id"] != "draft-7" || settings["placement"] != "stem" {
		t.Errorf("settings = {draft_id:%v, placement:%v}; want {draft-7, stem}", settings["draft_id"], settings["placement"])
	}
	if settings["prompt"] != "A clearer diagram" {
		t.Errorf("settings.prompt = %v", settings["prompt"])
	}

	// generation_requested published with intent=image_regen (ADR-195 WS9: the
	// retired job_type discriminant is replaced by the compose intent).
	found := false
	for _, e := range pub.snapshot() {
		if e.Topic == "chora.creation.question.generation_requested.v2" {
			if p, ok := e.Payload.(map[string]any); ok && p["intent"] == "image_regen" {
				found = true
			}
		}
	}
	if !found {
		t.Error("no generation_requested with intent=image_regen published")
	}
}

// TestCreateImageRegenJob_WiresQuestionContext is the RED for Bug 1: a regenerate
// must carry the parent candidate's CURRENT question context (stem + model answer)
// and the original authoring grounding into the image_regen settings + published
// event, so the orchestrator's ImageRegenRunner renders an image of the ACTUAL
// question instead of an unrelated one (the "red apple" symptom). Without these
// the producer sends ImageRegenSpec f5/f6/f7 empty and the model free-associates.
func TestCreateImageRegenJob_WiresQuestionContext(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, pub, atomID := newJobsServer(t, mana)

	// Succeeded parent batch job: one OE candidate with a real stem + model
	// answer, grounded on a source file with author context preserved on settings.
	pid := uuid.NewString()
	const stem = "Explain how photosynthesis converts light energy into chemical energy."
	const modelAns = "Chlorophyll absorbs light; the light reactions split water and yield ATP/NADPH; the Calvin cycle fixes CO2 into glucose."
	cand := `[{"draft_id":"draft-9","type":"oe","prompt":"` + stem + `","oe_payload":{"model_answer":"` + modelAns + `"}}]`
	jobRepo.jobs[pid] = &question.ComposeJob{
		JobID: pid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent:                 question.IntentNewQuestion,
		Input:                  question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/biology.pdf"}}},
		Status:                 question.JobStatusSucceeded,
		SettingsJSON:           []byte(`{"context":"Grade 9 biology unit on cellular energy","filename":"biology.pdf"}`),
		CandidateQuestionsJSON: []byte(cand),
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{"draft_id": "draft-9", "placement": "answer", "prompt": "change the image art style to crayon-style", "mode": "scene"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+pid+"/regenerate-image", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	regenJobID, _ := got["job_id"].(string)
	rj := jobRepo.jobs[regenJobID]
	if rj == nil {
		t.Fatalf("image_regen job %s not persisted", regenJobID)
	}

	// Persisted settings carry the question context (the subscriber lifts these
	// into the ImageRegenSpec on the started event).
	var settings map[string]any
	if err := json.Unmarshal(rj.SettingsJSON, &settings); err != nil {
		t.Fatalf("settings json: %v", err)
	}
	if settings["current_stem"] != stem {
		t.Errorf("settings.current_stem = %v; want the candidate stem", settings["current_stem"])
	}
	if settings["current_model_answer"] != modelAns {
		t.Errorf("settings.current_model_answer = %v; want the OE model answer", settings["current_model_answer"])
	}
	if src, _ := settings["original_source"].(string); src == "" {
		t.Errorf("settings.original_source empty; want the authoring grounding")
	}
	if settings["original_mode"] != "scene" {
		t.Errorf("settings.original_mode = %v; want scene", settings["original_mode"])
	}

	// The published generation_requested event carries the same settings_json
	// (the subscriber decodes evt.SettingsJSON to build the regen spec).
	var evtSettings map[string]any
	for _, e := range pub.snapshot() {
		p, ok := e.Payload.(map[string]any)
		if !ok || p["intent"] != "image_regen" {
			continue
		}
		sj, _ := p["settings_json"].(string)
		_ = json.Unmarshal([]byte(sj), &evtSettings)
	}
	if evtSettings == nil {
		t.Fatalf("no image_regen generation_requested event with settings_json")
	}
	if evtSettings["current_stem"] != stem {
		t.Errorf("event settings_json.current_stem = %v; want the candidate stem", evtSettings["current_stem"])
	}
	if evtSettings["current_model_answer"] != modelAns {
		t.Errorf("event settings_json.current_model_answer = %v", evtSettings["current_model_answer"])
	}
}

func TestCreateImageRegenJob_UnknownDraft_404_NoCharge(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)
	pid := seedReviewableParent(jobRepo, atomID, "draft-7")

	body := map[string]any{"draft_id": "nonexistent", "placement": "stem", "prompt": "x"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+pid+"/regenerate-image", body))
	if w.Code != http.StatusNotFound && w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 404/422 for unknown draft_id", w.Code)
	}
	if mana.deductCalls != 0 {
		t.Errorf("mana deducted %d times; want 0 (validate draft before charging)", mana.deductCalls)
	}
}

func TestCreateImageRegenJob_BadPlacement_400(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)
	pid := seedReviewableParent(jobRepo, atomID, "draft-7")

	body := map[string]any{"draft_id": "draft-7", "placement": "sidebar", "prompt": "x"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+pid+"/regenerate-image", body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for bad placement", w.Code)
	}
}

func TestCreateImageRegenJob_ParentNotSucceeded_Rejected(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)
	pid := uuid.NewString()
	jobRepo.jobs[pid] = &question.ComposeJob{
		JobID: pid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusRunning, // not in review
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	body := map[string]any{"draft_id": "draft-7", "placement": "stem", "prompt": "x"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+pid+"/regenerate-image", body))
	if w.Code != http.StatusConflict && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 409/400 for a non-succeeded parent job", w.Code)
	}
}
