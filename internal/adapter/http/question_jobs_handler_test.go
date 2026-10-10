// question_jobs_handler_test.go — HTTP-layer tests for the AI single-question
// async path (P5-D). Mirrors questions_handler_test.go's fake repository +
// router fixture.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------------

type fakeJobRepo struct {
	mu        sync.Mutex
	jobs      map[string]*question.ComposeJob
	createErr error
	getErr    error
	updateErr error
	// forceTransitionLost makes TransitionFromSucceeded report a lost CAS
	// (simulates a concurrent accept winning between read and transition).
	forceTransitionLost bool
}

func newFakeJobRepo() *fakeJobRepo {
	return &fakeJobRepo{jobs: map[string]*question.ComposeJob{}}
}

func (r *fakeJobRepo) Create(_ context.Context, j *question.ComposeJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.jobs[j.JobID] = j
	return nil
}

func (r *fakeJobRepo) Get(_ context.Context, tenantID, jobID string) (*question.ComposeJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	j, ok := r.jobs[jobID]
	if !ok || j.TenantID != tenantID {
		return nil, question.ErrNotFound
	}
	return j, nil
}

func (r *fakeJobRepo) UpdateStatus(_ context.Context, tenantID, jobID string, to question.JobStatus, cand []byte, errMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	j, ok := r.jobs[jobID]
	// Emulate RLS: an empty or mismatched tenant sees zero rows (the live bug).
	if !ok || tenantID == "" || j.TenantID != tenantID {
		return question.ErrNotFound
	}
	if err := j.Transition(to); err != nil {
		return err
	}
	if cand != nil {
		j.CandidateQuestionsJSON = cand
	}
	if errMsg != "" {
		j.Error = errMsg
	}
	return nil
}

func (r *fakeJobRepo) UpdateStatusWithProposal(ctx context.Context, tenantID, jobID string, to question.JobStatus, cand, proposal, generationSummary, pipelineTrace []byte, errMsg string) error {
	if err := r.UpdateStatus(ctx, tenantID, jobID, to, cand, errMsg); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(proposal) > 0 {
		r.jobs[jobID].ProposedTestSetJSON = proposal
	}
	if len(generationSummary) > 0 {
		r.jobs[jobID].GenerationSummaryJSON = generationSummary
	}
	if len(pipelineTrace) > 0 {
		r.jobs[jobID].PipelineTraceJSON = pipelineTrace
	}
	return nil
}

// PatchCandidatesUnderLock (CHO-1819 P3c) — atomic read-modify-write of a job's
// candidates without a status change.
func (r *fakeJobRepo) PatchCandidatesUnderLock(_ context.Context, tenantID, jobID string, apply func(status string, current []byte) ([]byte, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	j, ok := r.jobs[jobID]
	if !ok || tenantID == "" || j.TenantID != tenantID {
		return question.ErrNotFound
	}
	patched, err := apply(string(j.Status), j.CandidateQuestionsJSON)
	if err != nil {
		return err
	}
	if len(patched) > 0 {
		j.CandidateQuestionsJSON = patched
	}
	return nil
}

// TransitionFromSucceeded mirrors the pg CAS semantics: succeeds only while
// the job is still in 'succeeded' (the W3 exactly-once guard).
func (r *fakeJobRepo) TransitionFromSucceeded(_ context.Context, tenantID, jobID string, to question.JobStatus) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return false, r.updateErr
	}
	if r.forceTransitionLost {
		return false, nil
	}
	j, ok := r.jobs[jobID]
	if !ok || tenantID == "" || j.TenantID != tenantID {
		return false, nil
	}
	if j.Status != question.JobStatusSucceeded {
		return false, nil
	}
	if err := j.Transition(to); err != nil {
		return false, err
	}
	return true, nil
}

// UpdatePipelineTraceOnly — mid-run live-trace writer (CR Phase 1). The HTTP
// handler never calls it (it's a Pub/Sub-subscriber concern), so a guarded
// no-op stub satisfies the port for these handler tests.
func (r *fakeJobRepo) UpdatePipelineTraceOnly(_ context.Context, tenantID, jobID string, pipelineTraceJSON []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return false, r.updateErr
	}
	j, ok := r.jobs[jobID]
	if !ok || tenantID == "" || j.TenantID != tenantID ||
		j.Status != question.JobStatusRunning || len(pipelineTraceJSON) == 0 {
		return false, nil
	}
	j.PipelineTraceJSON = pipelineTraceJSON
	return true, nil
}

// ApplyChunkCandidates (CHO-2398) - minimal port conformance; the http tests
// seed ChunkCandidatesJSON directly and never drive the apply path.
func (r *fakeJobRepo) ApplyChunkCandidates(_ context.Context, tenantID, jobID string, chunkIndex, chunkCount int, candidatesJSON []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return false, r.updateErr
	}
	j, ok := r.jobs[jobID]
	if !ok || tenantID == "" || j.TenantID != tenantID ||
		j.Status != question.JobStatusRunning || len(candidatesJSON) == 0 {
		return false, nil
	}
	j.ChunkCountTotal = chunkCount
	_ = chunkIndex
	return true, nil
}

// fakeManaLedger drives the test path. successCount > 0 → debit succeeds;
// successCount == 0 → returns *InsufficientManaError. Refund records call.
type fakeManaLedger struct {
	mu            sync.Mutex
	successDebits int  // permit this many before flipping to insufficient
	insufficient  bool // explicit override
	deductCalls   int
	refundCalls   int
	lastDeductReq ports.DeductManaReq
	deductReqs    []ports.DeductManaReq // every Deduct call, in order
	lastRefundReq ports.RefundManaReq

	rpcErr error // when set, Deduct returns this raw error (no envelope)
}

// resolvePrice mimics chora-identity's ADR-178 price-plan layer (FU-4(b)):
// an explicit Units>0 debit is applied as-is; a Units==0 debit resolves the
// per-unit price from the catalogue floor and multiplies a per_item action by
// context.item_count. Catalogue floor mirrors migration 0009/0010.
func (m *fakeManaLedger) resolvePrice(req ports.DeductManaReq) int64 {
	if req.Units > 0 {
		return int64(req.Units)
	}
	perUnit := map[string]int64{
		"question_authoring_model_answer":   5,
		"question_authoring_ai_draft":       10,
		"question_authoring_batch_parse":    50,
		"question_authoring_batch_per_item": 5,
		"question_authoring_generate":       10, // CHO-1826 U3b unified per-accepted text
		"question_authoring_generate_image": 20, // CHO-1826 U3b unified per-accepted image (2×)
		"atom_authoring_assist":             5,  // FU-4b crew metering (test price)
	}[req.ActionCode]
	n := int64(1)
	if v, err := strconv.ParseInt(strings.TrimSpace(req.Context["item_count"]), 10, 64); err == nil && v >= 1 {
		n = v
	}
	return perUnit * n
}

func (m *fakeManaLedger) Deduct(_ context.Context, req ports.DeductManaReq) (ports.DeductManaResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deductCalls++
	m.lastDeductReq = req
	m.deductReqs = append(m.deductReqs, req)
	if m.rpcErr != nil {
		return ports.DeductManaResp{}, m.rpcErr
	}
	charged := m.resolvePrice(req)
	if m.insufficient || m.successDebits == 0 {
		stripeURL := "https://checkout.stripe.com/c/pay/cs_test_xyz"
		return ports.DeductManaResp{
			Success:             false,
			RequiredUnits:       charged,
			CurrentBalanceUnits: 0,
		}, &clients.InsufficientManaError{
			RequiredUnits:         charged,
			CurrentBalanceUnits:   0,
			RecommendedTopupUnits: 100,
			RecommendedPlanCode:   "MANA_TOPUP_BASIC",
			StripeCheckoutURL:     &stripeURL,
		}
	}
	m.successDebits--
	return ports.DeductManaResp{Success: true, NewBalanceUnits: 95, LedgerEntryID: "entry-fake", ChargedUnits: charged}, nil
}

func (m *fakeManaLedger) Refund(_ context.Context, req ports.RefundManaReq) (ports.RefundManaResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refundCalls++
	m.lastRefundReq = req
	return ports.RefundManaResp{Success: true, NewBalanceUnits: 100, LedgerEntryID: "credit-fake"}, nil
}

func (m *fakeManaLedger) GetBalance(_ context.Context, _ string) (ports.BalanceResp, error) {
	return ports.BalanceResp{CurrentBalanceUnits: 0}, nil
}

type fakeJobPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}
type publishedEvent struct {
	Topic   string
	Payload any
}

func (p *fakeJobPublisher) PublishJobEvent(_ context.Context, topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{Topic: topic, Payload: payload})
	return nil
}

func (p *fakeJobPublisher) snapshot() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedEvent, len(p.events))
	copy(out, p.events)
	return out
}

// waitForEvents blocks until the publisher has recorded at least want events or
// the timeout elapses, returning the snapshot. The batch source-material path
// extracts chunks and publishes generation_requested on a detached goroutine
// (so the 202 does not wait on PDF parsing); the publish is the LAST step of
// that goroutine, so waiting for the event also guarantees extraction has run.
func (p *fakeJobPublisher) waitForEvents(t *testing.T, want int, timeout time.Duration) []publishedEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ev := p.snapshot(); len(ev) >= want {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %d published event(s); got %d", timeout, want, len(p.snapshot()))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Test server helper
// -----------------------------------------------------------------------------

func newJobsServer(t *testing.T, mana *fakeManaLedger) (http.Handler, *fakeJobRepo, *fakeQuestionRepository, *fakeJobPublisher, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	jobRepo := newFakeJobRepo()
	pub := &fakeJobPublisher{}

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "MCQ", Body: "Body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}

	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                  atomRepo,
		QuestionRepository:    qRepo,
		QuestionJobRepository: jobRepo,
		ManaLedger:            mana,
		JobEventPublisher:     pub,
	})
	return router, jobRepo, qRepo, pub, a.AtomID
}

func authedJobJSON(method, path string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer([]byte("{}"))
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// POST /question-jobs (ai_draft) — happy path
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_AIDraft_Returns202(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, pub, atomID := newJobsServer(t, mana)

	body := map[string]any{
		"type":          "ai_draft",
		"question_type": "mcq",
		"prompt":        "Generate one MCQ on Scrum roles",
		"difficulty":    3,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
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
	jid, _ := got["job_id"].(string)
	if jid == "" {
		t.Fatal("job_id missing")
	}
	// U3b — generation is no longer charged at create time; the author pays
	// per ACCEPTED question at accept. No upfront debit.
	if mana.deductCalls != 0 {
		t.Errorf("DeductMana called %d times; want 0 (no upfront generation charge)", mana.deductCalls)
	}
	if j := jobRepo.jobs[jid]; j != nil && j.ManaCharged != 0 {
		t.Errorf("job ManaCharged = %d; want 0 (charged at accept)", j.ManaCharged)
	}
	if len(jobRepo.jobs) != 1 {
		t.Errorf("job not persisted; got %d jobs", len(jobRepo.jobs))
	}
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	if events[0].Topic != "chora.creation.question.generation_requested.v2" {
		t.Errorf("event topic = %q", events[0].Topic)
	}
}

// CHO-1826 Gap #4 follow-up — the no-files ai_draft body must ACCEPT the
// composer's forced-image opt-in (image_for_stem / image_for_answer) and carry
// it into settings_json so the subscriber forwards it to the qgen crew. Before
// the fix the DisallowUnknownFields decoder 400s on these keys (silent drop in
// the FE was the originating bug).
func TestCreateQuestionJob_AIDraft_ForwardsImageFlagsIntoSettings(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, _, pub, atomID := newJobsServer(t, mana)

	body := map[string]any{
		"type":             "ai_draft",
		"question_type":    "mcq",
		"prompt":           "Generate one MCQ on photosynthesis",
		"difficulty":       3,
		"image_for_stem":   true,
		"image_for_answer": true,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202 (image flags must be accepted, not rejected by DisallowUnknownFields)", w.Code, w.Body.String())
	}
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not map[string]any: %T", events[0].Payload)
	}
	settingsRaw, _ := payload["settings_json"].(string)
	var settings map[string]any
	if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
		t.Fatalf("settings_json invalid: %v (%q)", err, settingsRaw)
	}
	if settings["image_for_stem"] != true {
		t.Errorf("settings_json image_for_stem = %v; want true", settings["image_for_stem"])
	}
	if settings["image_for_answer"] != true {
		t.Errorf("settings_json image_for_answer = %v; want true", settings["image_for_answer"])
	}
}

// CHO-1657 regression (re)fix — the no-files ai_draft body must ACCEPT the author's
// metadata hint map (subject / cognitive_level / difficulty) and carry it into
// settings_json so the subscriber lifts it onto the started event's metadata map
// (proto field 12 → orchestrator *_hint stamping + ADR-197 prompt_conditions). The
// DisallowUnknownFields decoder 400s on an unknown `metadata` key before the fix;
// the CHO-1826 unified canvas stopped sending it, so the hints/conditions vanished.
func TestCreateQuestionJob_AIDraft_ForwardsMetadataIntoSettings(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, _, pub, atomID := newJobsServer(t, mana)

	body := map[string]any{
		"type":          "ai_draft",
		"question_type": "mcq",
		"prompt":        "Generate one MCQ on photosynthesis",
		"difficulty":    4,
		"metadata": map[string]any{
			"subject":         "Photosynthesis",
			"cognitive_level": "analysis",
			"difficulty":      "advanced",
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202 (metadata must be accepted, not rejected by DisallowUnknownFields)", w.Code, w.Body.String())
	}
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not map[string]any: %T", events[0].Payload)
	}
	settingsRaw, _ := payload["settings_json"].(string)
	var settings map[string]any
	if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
		t.Fatalf("settings_json invalid: %v (%q)", err, settingsRaw)
	}
	md, ok := settings["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("settings_json metadata missing/not object: %T (%v)", settings["metadata"], settings["metadata"])
	}
	if md["subject"] != "Photosynthesis" || md["cognitive_level"] != "analysis" || md["difficulty"] != "advanced" {
		t.Errorf("settings_json metadata = %v; want subject/cognitive_level/difficulty forwarded", md)
	}
}

// TestCreateQuestionJob_AIDraft_402WhenInsufficientMana was removed in U3b:
// generation no longer charges, so an insufficient-mana 402 can only occur at
// ACCEPT time — see TestAccept_InsufficientMana_402_NoPersist.

// -----------------------------------------------------------------------------
// POST /questions/{qid}/ai-model-answer-jobs — happy path
// -----------------------------------------------------------------------------

func TestCreateModelAnswerJob_Returns202(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, qRepo, pub, atomID := newJobsServer(t, mana)

	// Seed a question on the atom (the model-answer job augments an existing q).
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomID, AuthorGcid: gcidA,
		Type:       question.TypeMCQ,
		Prompt:     "Which Scrum role owns the backlog?",
		SourceType: atom.SourceManual,
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: true, Explainer: "yes"},
			{OptionID: "o2", Label: "B", IsCorrect: false, Explainer: "no"},
		}},
	})
	rev, _ := question.NewRevision(q, q.Prompt, q.MCQ, nil, gcidA, atom.SourceManual)
	q.LatestRevisionID = rev.RevisionID
	if err := qRepo.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("seed q: %v", err)
	}

	path := "/api/atoms/" + atomID + "/questions/" + q.QuestionID + "/ai-model-answer-jobs"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, path, map[string]any{"tone_hint": "concise"}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	if mana.deductCalls != 1 {
		t.Errorf("DeductMana called %d", mana.deductCalls)
	}
	if mana.lastDeductReq.ActionCode != "question_authoring_model_answer" {
		t.Errorf("debit action = %q", mana.lastDeductReq.ActionCode)
	}
	// FU-4(b): Units==0 ⇒ server-resolved price (tenant-aware price-plan layer).
	if mana.lastDeductReq.Units != 0 {
		t.Errorf("debit units = %d; want 0 (server-resolved)", mana.lastDeductReq.Units)
	}
	if len(jobRepo.jobs) != 1 {
		t.Errorf("expected 1 job persisted; got %d", len(jobRepo.jobs))
	}
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
}

func TestCreateModelAnswerJob_QuestionNotOwned_403(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, qRepo, _, atomID := newJobsServer(t, mana)

	// Seed a question owned by a DIFFERENT gcid.
	q, _ := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomID, AuthorGcid: "01970000-0000-7000-9000-deadbeefdead",
		Type:       question.TypeMCQ,
		Prompt:     "X?",
		SourceType: atom.SourceManual,
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "o1", Label: "A", IsCorrect: true, Explainer: "yes"},
			{OptionID: "o2", Label: "B", IsCorrect: false, Explainer: "no"},
		}},
	})
	rev, _ := question.NewRevision(q, q.Prompt, q.MCQ, nil, "01970000-0000-7000-9000-deadbeefdead", atom.SourceManual)
	q.LatestRevisionID = rev.RevisionID
	_ = qRepo.Save(context.Background(), q, rev)

	path := "/api/atoms/" + atomID + "/questions/" + q.QuestionID + "/ai-model-answer-jobs"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, path, nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /question-jobs/{jid} — happy path
// -----------------------------------------------------------------------------

func TestGetQuestionJob_ReturnsJobState(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)

	// Seed a job directly.
	jid := uuid.NewString()
	job := &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaActionCode: "question_authoring_ai_draft", ManaCharged: 10,
		CandidateQuestionsJSON: []byte(`[{"draft_id":"d1","question":"x","answer":"y"}]`),
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	jobRepo.jobs[jid] = job

	path := "/api/atoms/" + atomID + "/question-jobs/" + jid
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"succeeded"`) {
		t.Errorf("body missing status:succeeded: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "candidate_questions") {
		t.Errorf("body should include candidate_questions; got %s", w.Body.String())
	}
}

func TestGetQuestionJob_NotOwnedReturns403(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: "different-gcid",
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs/"+jid, nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /question-jobs/{jid}/accept — happy path (full accept, no overrides)
// -----------------------------------------------------------------------------

func TestAcceptQuestionJob_PersistsAIBodyVerbatim(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, qRepo, pub, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	drafts := []map[string]any{
		{
			"draft_id": "d1",
			"type":     "mcq",
			"prompt":   "AI-drafted MCQ?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "x1", "label": "Correct", "is_correct": true, "explainer": "yes"},
					{"option_id": "x2", "label": "Wrong", "is_correct": false, "explainer": "no"},
				},
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{
		"accepted_candidates": []map[string]any{{"draft_id": "d1"}},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(qRepo.questions) != 1 {
		t.Errorf("expected 1 persisted question; got %d", len(qRepo.questions))
	}
	for _, q := range qRepo.questions {
		if q.Prompt != "AI-drafted MCQ?" {
			t.Errorf("Prompt should match AI body; got %q", q.Prompt)
		}
	}
	// Two events expected: authored + completed.
	if len(pub.snapshot()) < 2 {
		t.Errorf("expected ≥2 events (authored + completed); got %d", len(pub.snapshot()))
	}
}

func TestAcceptQuestionJob_HonorsOverrides_EditabilityInvariant(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, qRepo, _, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	drafts := []map[string]any{
		{
			"draft_id": "d1", "type": "mcq",
			"prompt": "AI prompt — should be replaced",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "ai1", "label": "AI option correct", "is_correct": true, "explainer": "ai-correct"},
					{"option_id": "ai2", "label": "AI option wrong", "is_correct": false, "explainer": "ai-wrong"},
				},
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	// Override the prompt and MCQ options.
	overriddenPrompt := "User-edited prompt — this MUST persist"
	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{
				"draft_id":        "d1",
				"prompt_override": overriddenPrompt,
				"mcq_payload_override": map[string]any{
					"options": []map[string]any{
						{"option_id": "u1", "label": "User correct", "is_correct": true, "explainer": "user-correct"},
						{"option_id": "u2", "label": "User wrong A", "is_correct": false, "explainer": "user-wrong-a"},
						{"option_id": "u3", "label": "User wrong B", "is_correct": false, "explainer": "user-wrong-b"},
					},
				},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(qRepo.questions) != 1 {
		t.Fatalf("expected 1 persisted question; got %d", len(qRepo.questions))
	}
	for _, q := range qRepo.questions {
		if q.Prompt != overriddenPrompt {
			t.Errorf("Prompt = %q; want override %q (editability invariant violated)", q.Prompt, overriddenPrompt)
		}
		if q.MCQ == nil || len(q.MCQ.Options) != 3 {
			t.Errorf("MCQ options not overridden; got %+v", q.MCQ)
		}
		if q.MCQ != nil && len(q.MCQ.Options) == 3 {
			for i, want := range []bool{true, false, false} {
				if got := q.MCQ.Options[i].IsCorrect; got != want {
					t.Errorf("persisted option %d IsCorrect = %t; want %t", i, got, want)
				}
			}
		}
	}
}

// TestAcceptQuestionJob_OEOverride_PreservesRubricAndGraderTier locks the
// §4.2b fix: when the author edits ONLY the OE model answer at accept time
// (oe_payload_override carries just model_answer), the AI draft's rubric +
// grader_tier + min/max_response_chars MUST survive into the persisted
// question. Before the fix, question_jobs_handler.go rebuilt the payload as
// &question.OEPayload{ModelAnswer: override} — silently dropping the rubric,
// so an edited OE question shipped with NO grading rubric.
func TestAcceptQuestionJob_OEOverride_PreservesRubricAndGraderTier(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, qRepo, _, atomID := newJobsServer(t, mana)

	graderT2 := "T2"
	minChars := 80
	maxChars := 600
	// Draft is persisted in the candidateDraft (domain question.OEPayload) JSON
	// shape: rubric is {criteria:[{criterion_id,description,weight_percent}]}.
	drafts := []map[string]any{
		{
			"draft_id": "d1", "type": "oe",
			"prompt": "Explain how photosynthesis works.",
			"oe_payload": map[string]any{
				"model_answer": "AI-drafted model answer describing the light-dependent reactions and the Calvin cycle in enough depth to be a valid baseline answer.",
				"rubric": map[string]any{
					"criteria": []map[string]any{
						{"criterion_id": "c1", "description": "Light-dependent reactions", "weight_percent": 50},
						{"criterion_id": "c2", "description": "Calvin cycle", "weight_percent": 50},
					},
				},
				"grader_tier":        graderT2,
				"min_response_chars": minChars,
				"max_response_chars": maxChars,
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jid := uuid.NewString()
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	// Author edits ONLY the model answer.
	editedAnswer := "Author-edited model answer that is sufficiently long to demonstrate the expected depth and pass domain validation cleanly."
	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{
				"draft_id":            "d1",
				"oe_payload_override": map[string]any{"model_answer": editedAnswer},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(qRepo.questions) != 1 {
		t.Fatalf("expected 1 persisted question; got %d", len(qRepo.questions))
	}
	for _, q := range qRepo.questions {
		if q.OE == nil {
			t.Fatal("persisted question OE payload nil")
		}
		if q.OE.ModelAnswer != editedAnswer {
			t.Errorf("ModelAnswer = %q; want the override", q.OE.ModelAnswer)
		}
		// The rubric from the AI draft MUST survive the model-answer-only edit.
		if q.OE.WeightedRubric == nil || len(q.OE.WeightedRubric.Criteria) != 2 {
			t.Fatalf("rubric dropped on OE override (the §4.2b bug); got %+v", q.OE.WeightedRubric)
		}
		if q.OE.GraderTier == nil || *q.OE.GraderTier != "T2" {
			t.Errorf("GraderTier = %v; want T2 preserved", q.OE.GraderTier)
		}
		if q.OE.MinResponseChars == nil || *q.OE.MinResponseChars != 80 {
			t.Errorf("MinResponseChars = %v; want 80 preserved", q.OE.MinResponseChars)
		}
		if q.OE.MaxResponseChars == nil || *q.OE.MaxResponseChars != 600 {
			t.Errorf("MaxResponseChars = %v; want 600 preserved", q.OE.MaxResponseChars)
		}
	}
}

// TestAcceptQuestionJob_BatchEmitsAtomCreatedWithGradingFields asserts the
// batch_source_material accept path emits chora.creation.atom.created.v1
// carrying the CHO-1627 MCQ grading ground-truth (correct_option_id +
// answer_count) for the new per-candidate atom. chora-consumption scores MCQ
// submissions against correct_option_id; without it on atom.created the L1
// grading has no server-side answer.
func TestAcceptQuestionJob_BatchEmitsAtomCreatedWithGradingFields(t *testing.T) {
	t.Parallel()
	// successDebits=2 covers the upfront parse debit (charged at job create,
	// not here) + the per-item batch debit applied at accept time.
	mana := &fakeManaLedger{successDebits: 2}
	srv, jobRepo, _, pub, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	drafts := []map[string]any{
		{
			"draft_id": "d1",
			"type":     "mcq",
			"prompt":   "Batch MCQ?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "b1", "label": "Wrong", "is_correct": false, "explainer": "no"},
					{"option_id": "b2", "label": "Right", "is_correct": true, "explainer": "yes"},
					{"option_id": "b3", "label": "Wrong2", "is_correct": false, "explainer": "no"},
				},
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		ManaCharged: 50, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{
		"accepted_candidates": []map[string]any{{"draft_id": "d1"}},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}

	var atomCreated map[string]any
	for _, ev := range pub.snapshot() {
		if ev.Topic == "chora.creation.atom.created.v1" {
			m, ok := ev.Payload.(map[string]any)
			if !ok {
				t.Fatalf("atom.created payload is %T; want map[string]any", ev.Payload)
			}
			atomCreated = m
		}
	}
	if atomCreated == nil {
		t.Fatalf("no chora.creation.atom.created.v1 event published on batch accept; events=%+v", pub.snapshot())
	}
	// CHO-2272 — the grading key is the DERIVED served id of the correct option's
	// display content (label "Right"), NOT the stored wire id "b2".
	wantKey, kerr := (&question.MCQPayload{Options: []question.MCQOption{
		{Label: "Wrong", IsCorrect: false},
		{Label: "Right", IsCorrect: true},
		{Label: "Wrong2", IsCorrect: false},
	}}).ServedCorrectOptionID()
	if kerr != nil {
		t.Fatalf("compute expected derived key: %v", kerr)
	}
	if got := atomCreated["correct_option_id"]; got != wantKey {
		t.Errorf("atom.created correct_option_id = %v; want the derived served id %q (not the stored wire id b2)", got, wantKey)
	}
	if got := atomCreated["correct_option_id"]; got == "b2" {
		t.Errorf("atom.created leaked the stored wire id b2")
	}
	if got, _ := atomCreated["answer_count"].(int32); got != 3 {
		t.Errorf("atom.created answer_count = %v; want int32(3)", atomCreated["answer_count"])
	}
}

func TestAcceptQuestionJob_JobNotReady_409(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, _, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusRunning, // not succeeded
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	w := httptest.NewRecorder()
	body := map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}}}
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d; want 409 (job not ready)", w.Code)
	}
}

func TestAcceptQuestionJob_EmptyArray_TransitionsToCancelled(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, qRepo, _, atomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: atomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		CandidateQuestionsJSON: []byte(`[{"draft_id":"d1"}]`),
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jid+"/accept",
		map[string]any{"accepted_candidates": []any{}}))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
	if jobRepo.jobs[jid].Status != question.JobStatusCancelled {
		t.Errorf("status = %q; want cancelled", jobRepo.jobs[jid].Status)
	}
	if len(qRepo.questions) != 0 {
		t.Errorf("expected 0 persisted questions on cancel; got %d", len(qRepo.questions))
	}
}

// -----------------------------------------------------------------------------
// batch_source_material via JSON now 400 — P6 enforces multipart/form-data
// (the route still accepts batch via multipart; this test guards against
// callers using a wrong Content-Type).
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_BatchViaJSON_Returns400(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, _, _, atomID := newJobsServer(t, mana)

	body := map[string]any{"type": "batch_source_material", "question_type": "mcq", "prompt": "x"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
	// Post-P6: batch via JSON body returns 400 with INVALID_BODY pointing
	// the caller at multipart/form-data. The multipart path is exercised
	// in question_jobs_batch_test.go.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (batch via JSON should now be 400 directing caller to multipart)", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Unwired deps surface 503 (defense)
// -----------------------------------------------------------------------------

func TestQuestionJobsRoutes_UnwiredReturns503(t *testing.T) {
	t.Parallel()
	// Build a router with the P5 deps absent.
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		// QuestionJobRepository / ManaLedger / JobEventPublisher all nil.
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/question-jobs",
		map[string]any{"type": "ai_draft", "question_type": "mcq", "prompt": "x"}))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (unwired)", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Sanity check on error.As semantics for InsufficientManaError (catches
// any future regression where the typed error is wrapped twice).
// -----------------------------------------------------------------------------

func TestInsufficientManaError_TypedErrorsAs(t *testing.T) {
	t.Parallel()
	url := "u"
	src := &clients.InsufficientManaError{RequiredUnits: 5, StripeCheckoutURL: &url}
	var iErr *clients.InsufficientManaError
	if !errors.As(src, &iErr) {
		t.Fatal("errors.As did not unwrap")
	}
	if iErr.RequiredUnits != 5 {
		t.Errorf("RequiredUnits not preserved")
	}
}
