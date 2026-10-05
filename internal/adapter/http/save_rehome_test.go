// save_rehome_test.go — ADR-210 Slice A1 TDD: durably re-home transient W8
// image refs at SAVE (accept-candidate + save-revision PATCH), not only at
// publish. Mirrors publish_atom_rehome_test.go, reusing its fakeReHomer /
// seedMCQWithImages / latestMCQ / deref helpers. A saved-but-unpublished
// question image must land on a durable chora-atom-media gs:// ref so it
// survives past the 7-day transient-bucket TTL (fully fixes CHO-1974).
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// transientGCSURL builds a path-style GCS V4 signed URL on the transient
// AI-assist bucket (what the orchestrator persists onto a candidate today).
func transientGCSURL(leaf string) string {
	return "https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/" +
		tenantA + "/jobs/j1/" + leaf + "?X-Goog-Expires=604800"
}

// -----------------------------------------------------------------------------
// Save-revision (PATCH) — re-home the carried-forward image on a text edit.
// -----------------------------------------------------------------------------

// patchQuestionWithRehome seeds an image-bearing MCQ question, wires a re-homer,
// then PATCHes a prompt-only edit (the Story-1 text-edit path: mcq_payload
// omitted ⇒ the prior payload — incl. its image — is reused).
func patchQuestionWithRehome(t *testing.T, reHomer ports.MediaReHomer, seedImg, seedAns string) (*httptest.ResponseRecorder, ports.QuestionRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "planets mcq", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	seedMCQWithImages(t, qRepo, a.AtomID, seedImg, seedAns)
	q, _, err := qRepo.GetByAtomID(context.Background(), tenantA, a.AtomID)
	if err != nil {
		t.Fatalf("seed GetByAtomID: %v", err)
	}

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo, MediaReHomer: reHomer,
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+a.AtomID+"/questions/"+q.QuestionID,
		map[string]any{"prompt": "Edited prompt"}))
	return w, qRepo, a.AtomID
}

// A text edit re-homes the carried-forward transient image to a durable gs://.
func TestPatchQuestion_ReHomesTransientImage(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{}
	w, qRepo, atomID := patchQuestionWithRehome(t, reHomer, transientGCSURL("stem.png"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 1 {
		t.Errorf("copier called %d times; want 1 (stem)", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("ImageURL = %v; want durable gs:// ref", deref(mcq.ImageURL))
	}
}

// An already-durable ref is a no-op — no copy, edit still succeeds, ref intact.
func TestPatchQuestion_AlreadyDurable_NoReHome(t *testing.T) {
	t.Parallel()
	durable := "gs://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/x/img.png"
	reHomer := &fakeReHomer{}
	w, qRepo, atomID := patchQuestionWithRehome(t, reHomer, durable, "")
	if w.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 0 {
		t.Errorf("copier called %d times for already-durable ref; want 0", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != durable {
		t.Errorf("ImageURL = %v; want unchanged %q", deref(mcq.ImageURL), durable)
	}
}

// A copy failure fails loud — 500 with the discriminated envelope.
func TestPatchQuestion_ReHomeFailure_500(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{err: errors.New("gcs copy denied")}
	w, _, _ := patchQuestionWithRehome(t, reHomer, transientGCSURL("x.png"), "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Accept-candidate — re-home the candidate's transient image on accept.
// -----------------------------------------------------------------------------

// mcqCandidate builds a single-candidate object for the accept path, with
// optional transient stem + answer images at the canonical top level.
func mcqCandidate(draftID, img, ans string) map[string]any {
	c := map[string]any{
		"draft_id": draftID, "type": "mcq", "prompt": "Q1?",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "1", "label": "A", "is_correct": true, "explainer": "e"},
			{"option_id": "2", "label": "B", "is_correct": false, "explainer": "e"},
		}},
	}
	if img != "" {
		c["image_url"] = img
	}
	if ans != "" {
		c["answer_image_url"] = ans
	}
	return c
}

// acceptCandWithRehome seeds a SUCCEEDED single-candidate job from the given
// candidate object, wires a re-homer, and accepts it. The route atom's type is
// derived from the candidate so both MCQ + OE accepts work.
func acceptCandWithRehome(t *testing.T, reHomer ports.MediaReHomer, candidate map[string]any) (*httptest.ResponseRecorder, ports.QuestionRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	jobRepo := newFakeJobRepo()
	pub := &fakeJobPublisher{}
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "Host", Body: "Body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType(candidate["type"].(string))
	_ = atomRepo.Save(context.Background(), a)

	candJSON, err := json.Marshal([]map[string]any{candidate})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-0000000ee0a1", AtomID: a.AtomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Status: question.JobStatusSucceeded,
		CandidateQuestionsJSON: candJSON,
	}
	jobRepo.jobs[job.JobID] = job

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                  atomRepo,
		QuestionRepository:    qRepo,
		QuestionJobRepository: jobRepo,
		ManaLedger:            &fakeManaLedger{successDebits: 10},
		JobEventPublisher:     pub,
		MediaReHomer:          reHomer,
	})
	body, _ := json.Marshal(map[string]any{"accepted_candidates": []map[string]any{{"draft_id": candidate["draft_id"]}}})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/atoms/"+a.AtomID+"/question-jobs/"+job.JobID+"/accept", bytes.NewReader(body))
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	return w, qRepo, a.AtomID
}

func latestOE(t *testing.T, qRepo ports.QuestionRepository, atomID string) *question.OEPayload {
	t.Helper()
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	if rev.OEPayload == nil {
		t.Fatal("latest revision has nil OEPayload")
	}
	return rev.OEPayload
}

// Accepting a candidate with a transient image persists a durable gs:// ref.
func TestAcceptQuestionJob_ReHomesTransientImage(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{}
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", transientGCSURL("cand.png"), ""))
	if w.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 1 {
		t.Errorf("copier called %d times; want 1 (candidate stem)", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("ImageURL = %v; want durable gs:// ref", deref(mcq.ImageURL))
	}
}

// Both the stem AND the model-answer image are re-homed on accept.
func TestAcceptQuestionJob_ReHomesStemAndAnswerImages(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{}
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", transientGCSURL("stem.png"), transientGCSURL("ans.png")))
	if w.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 2 {
		t.Errorf("copier called %d times; want 2 (stem + answer)", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("ImageURL = %v; want durable gs:// ref", deref(mcq.ImageURL))
	}
	if mcq.AnswerImageURL == nil || !strings.HasPrefix(*mcq.AnswerImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("AnswerImageURL = %v; want durable gs:// ref", deref(mcq.AnswerImageURL))
	}
}

// OE symmetry — an open-ended candidate's transient image is re-homed too.
func TestAcceptQuestionJob_OE_ReHomesTransientImage(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{}
	cand := map[string]any{
		"draft_id": "d1", "type": "oe", "prompt": "Explain photosynthesis",
		"oe_payload": map[string]any{"model_answer": "Plants convert light to energy."},
		"image_url":  transientGCSURL("oe.png"),
	}
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, cand)
	if w.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 1 {
		t.Errorf("copier called %d times; want 1 (OE stem)", reHomer.n)
	}
	oe := latestOE(t, qRepo, atomID)
	if oe.ImageURL == nil || !strings.HasPrefix(*oe.ImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("OE ImageURL = %v; want durable gs:// ref", deref(oe.ImageURL))
	}
}

// A copy failure on accept fails loud — 500 with the discriminated envelope.
func TestAcceptQuestionJob_ReHomeFailure_500(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{err: errors.New("gcs copy denied")}
	w, _, _ := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", transientGCSURL("cand.png"), ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("accept status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
}
