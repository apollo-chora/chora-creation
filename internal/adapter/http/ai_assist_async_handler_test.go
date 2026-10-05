// ai_assist_async_handler_test.go — RED→GREEN coverage for the qgen
// 2-agent crew async surface (Step 4c per docs/m13/ack-oe-ai-assist-
// plan-2026-05-17.md).
//
// Tests the NEW POST 202 path + GET status handler. Legacy sync test
// coverage stays in ai_assist_handler_test.go (no regression — the new
// path is the only ai-assist path (CHO-1920).
package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------------

// fakeAiAssistJobsRepo is an in-memory ports.AiAssistJobsRepository for
// unit tests. Indexes by job_id (tenant-scoped via the (tenantID, jobID)
// key tuple).
type fakeAiAssistJobsRepo struct {
	mu    sync.Mutex
	jobs  map[string]*aiassist.Job // jobID -> Job
	calls []string                 // method names invoked, in order
	// failNext forces the next method to return an error (transport-fail
	// simulation). Auto-resets after one method call.
	failNext error
}

func newFakeAiAssistJobsRepo() *fakeAiAssistJobsRepo {
	return &fakeAiAssistJobsRepo{jobs: map[string]*aiassist.Job{}}
}

func (f *fakeAiAssistJobsRepo) Create(_ context.Context, job *aiassist.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Create")
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	// Idempotent: re-emit doesn't double-create.
	if _, ok := f.jobs[job.ID]; ok {
		return nil
	}
	cp := *job
	f.jobs[job.ID] = &cp
	return nil
}

func (f *fakeAiAssistJobsRepo) Get(_ context.Context, tenantID, jobID string) (*aiassist.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Get")
	j, ok := f.jobs[jobID]
	if !ok || j.TenantID != tenantID {
		return nil, aiassist.ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (f *fakeAiAssistJobsRepo) UpdateCompleted(
	_ context.Context, tenantID, jobID string,
	resultPayload, pipelineTrace []byte,
	qualityWarning bool, attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "UpdateCompleted")
	j, ok := f.jobs[jobID]
	if !ok || j.TenantID != tenantID {
		return aiassist.ErrNotFound
	}
	return j.MarkCompleted(resultPayload, pipelineTrace, qualityWarning, attemptCount, manaCharged)
}

func (f *fakeAiAssistJobsRepo) UpdateRefused(
	_ context.Context, tenantID, jobID string,
	reason aiassist.RefusalReason,
	armorVerdict, userFacingMsg string,
	lastCandidatePayload, pipelineTrace []byte,
	attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "UpdateRefused")
	j, ok := f.jobs[jobID]
	if !ok || j.TenantID != tenantID {
		return aiassist.ErrNotFound
	}
	return j.MarkRefused(reason, armorVerdict, userFacingMsg, lastCandidatePayload, pipelineTrace, attemptCount, manaCharged)
}

func (f *fakeAiAssistJobsRepo) UpdateFailed(
	_ context.Context, tenantID, jobID string,
	pipelineTrace []byte, errMsg string,
	attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "UpdateFailed")
	j, ok := f.jobs[jobID]
	if !ok || j.TenantID != tenantID {
		return aiassist.ErrNotFound
	}
	return j.MarkFailed(pipelineTrace, errMsg, attemptCount, manaCharged)
}

// Compile-time port check.
var _ ports.AiAssistJobsRepository = (*fakeAiAssistJobsRepo)(nil)

// fakeAsyncPublisher captures published events. Implements
// httpadapter.JobEventPublisher.
type fakeAsyncPublisher struct {
	mu     sync.Mutex
	events []asyncPublishedEvent
}

type asyncPublishedEvent struct {
	Topic   string
	Payload any
}

func (p *fakeAsyncPublisher) PublishJobEvent(_ context.Context, topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, asyncPublishedEvent{Topic: topic, Payload: payload})
	return nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newAsyncRouter(t *testing.T) (*fakeAiAssistJobsRepo, *fakeAsyncPublisher, http.Handler) {
	t.Helper()
	repo := newFakeAiAssistJobsRepo()
	pub := &fakeAsyncPublisher{}
	router := NewRouterWithDeps(RouterDeps{
		Repo:              inmem.NewAtomRepository(),
		AiAssistJobs:      repo,
		AiAssistPublisher: pub,
	})
	return repo, pub, router
}

func mustPostAsync(t *testing.T, srvURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/atoms/ai-assist", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func mustGetAsync(t *testing.T, srvURL, jobID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srvURL+"/api/atoms/ai-assist/"+jobID, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// -----------------------------------------------------------------------------
// POST /api/atoms/ai-assist (qgen crew, async)
// -----------------------------------------------------------------------------

func TestAiAssistAsync_HappyPathOEReturns202(t *testing.T) {
	repo, pub, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{
		"question_type": "oe",
		"prompt": "Explain chlorophyll in 2-3 sentences",
		"metadata": {"subject":"Biology","cognitive_level":"comprehension"},
		"max_retries": 3
	}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 Accepted", resp.StatusCode)
	}
	var env aiAssistJobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.JobID == "" {
		t.Errorf("expected job_id set")
	}
	if env.Status != string(aiassist.StatusQueued) {
		t.Errorf("status = %q; want QUEUED", env.Status)
	}
	if env.QuestionType != "oe" {
		t.Errorf("question_type = %q; want oe", env.QuestionType)
	}
	// Row persisted in repo.
	if _, err := repo.Get(context.Background(), "tenant-test", env.JobID); err != nil {
		t.Errorf("expected row persisted; Get err = %v", err)
	}
	// Pub/Sub started.v1 emitted.
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 published event; got %d", len(pub.events))
	}
	if pub.events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("topic = %q; want chora.creation.ai_assist.started.v2", pub.events[0].Topic)
	}
	// Event payload carries the right correlator + actor + question_type.
	payload, ok := pub.events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is not map[string]any; got %T", pub.events[0].Payload)
	}
	if payload["assist_id"] != env.JobID {
		t.Errorf("payload assist_id = %v; want %s", payload["assist_id"], env.JobID)
	}
	if payload["tenant_id"] != "tenant-test" {
		t.Errorf("payload tenant_id = %v; want tenant-test", payload["tenant_id"])
	}
	if payload["question_type"] != "oe" {
		t.Errorf("payload question_type = %v; want oe", payload["question_type"])
	}
}

// TestAiAssistAsync_ThreadsImageFlagsIntoStartedEvent asserts the W8
// author-opt-in flags (image_for_stem / image_for_answer) are read off the
// request body and threaded into the chora.creation.ai_assist.started.v1
// event payload so the encoder can emit fields 13/14 for the orchestrator.
func TestAiAssistAsync_ThreadsImageFlagsIntoStartedEvent(t *testing.T) {
	_, pub, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{
		"question_type": "mcq",
		"prompt": "What gas do plants release during photosynthesis?",
		"image_for_stem": true,
		"image_for_answer": true
	}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 published event; got %d", len(pub.events))
	}
	payload, ok := pub.events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is not map[string]any; got %T", pub.events[0].Payload)
	}
	if got, _ := payload["image_for_stem"].(bool); !got {
		t.Errorf("payload image_for_stem = %v; want true", payload["image_for_stem"])
	}
	if got, _ := payload["image_for_answer"].(bool); !got {
		t.Errorf("payload image_for_answer = %v; want true", payload["image_for_answer"])
	}
}

// TestAiAssistAsync_ImageFlagsDefaultFalseWhenAbsent asserts that a request
// omitting the flags threads explicit false into the started event (default
// false per the OpenAPI contract; the encoder then omits fields 13/14).
func TestAiAssistAsync_ImageFlagsDefaultFalseWhenAbsent(t *testing.T) {
	_, pub, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{"question_type":"oe","prompt":"Explain chlorophyll"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	payload, ok := pub.events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is not map[string]any; got %T", pub.events[0].Payload)
	}
	if got, _ := payload["image_for_stem"].(bool); got {
		t.Errorf("payload image_for_stem = %v; want false (default)", payload["image_for_stem"])
	}
	if got, _ := payload["image_for_answer"].(bool); got {
		t.Errorf("payload image_for_answer = %v; want false (default)", payload["image_for_answer"])
	}
}

func TestAiAssistAsync_HappyPathMCQReturns202(t *testing.T) {
	_, pub, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{
		"question_type": "mcq",
		"prompt": "What gas do plants release as a byproduct of photosynthesis?",
		"mcq_hint": {"option_count": 4, "scoring_mode": "single_correct"}
	}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if pub.events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("wrong topic")
	}
}

func TestAiAssistAsync_RejectsInvalidQuestionType(t *testing.T) {
	_, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()
	resp := mustPostAsync(t, srv.URL, `{"question_type":"flashcard","prompt":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on unsupported question_type", resp.StatusCode)
	}
}

func TestAiAssistAsync_RejectsMissingPrompt(t *testing.T) {
	_, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()
	resp := mustPostAsync(t, srv.URL, `{"question_type":"oe"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on missing prompt", resp.StatusCode)
	}
}

func TestAiAssistAsync_503WhenRepoNotWired(t *testing.T) {
	router := NewRouterWithDeps(RouterDeps{
		Repo:              inmem.NewAtomRepository(),
		AiAssistJobs:      nil, // not wired
		AiAssistPublisher: &fakeAsyncPublisher{},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	resp := mustPostAsync(t, srv.URL, `{"question_type":"oe","prompt":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 ai_assist_jobs_not_wired", resp.StatusCode)
	}
}

func TestAiAssistAsync_503WhenPublisherNotWired(t *testing.T) {
	router := NewRouterWithDeps(RouterDeps{
		Repo:              inmem.NewAtomRepository(),
		AiAssistJobs:      newFakeAiAssistJobsRepo(),
		AiAssistPublisher: nil, // not wired
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	resp := mustPostAsync(t, srv.URL, `{"question_type":"oe","prompt":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 ai_assist_publisher_not_wired", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// GET /api/atoms/ai-assist/{job_id}
// -----------------------------------------------------------------------------

func TestGetAiAssistJob_ReturnsJobEnvelope(t *testing.T) {
	repo, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Seed the repo with a QUEUED row.
	job, err := aiassist.NewQueuedJob(
		"job-xxx", "tenant-test", "gcid-test",
		aiassist.QuestionTypeOE, []byte(`{"prompt":"x"}`),
	)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := mustGetAsync(t, srv.URL, "job-xxx")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var env aiAssistJobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.JobID != "job-xxx" || env.Status != "QUEUED" {
		t.Errorf("envelope wrong: %+v", env)
	}
}

// CHO-2268 (SECURITY) — cross-USER IDOR inside a tenant. The handler did
// `tenantID, _ := tenantAndGCIDFromRequest(r)`, explicitly discarding the caller
// gcid, then returned jobToEnvelope(job, true) — which carries the generated
// Candidate (the qgen MCQ, WITH is_correct + explainer) plus the RequestPayload
// and PipelineTrace. So any caller sharing a tenant could read another author's
// generated answer key from their job.
//
// This is an inconsistency, not a design decision: the sibling getQuestionJob
// DOES enforce `job.AuthorGCID != authorGCID` -> 403 CREATION_JOB_NOT_OWNED
// (question_jobs_handler.go:1532).
func TestGetAiAssistJob_403OnCrossUserSameTenant(t *testing.T) {
	repo, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Same tenant as the caller, but authored by SOMEONE ELSE.
	job, err := aiassist.NewQueuedJob(
		"job-not-mine", "tenant-test", "gcid-OTHER",
		aiassist.QuestionTypeMCQ, []byte(`{"prompt":"x"}`),
	)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := mustGetAsync(t, srv.URL, "job-not-mine") // caller = gcid-test
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reading another author's ai-assist job must be 403; got %d", resp.StatusCode)
	}

	// POSITIVE CONTROL — the OWNER of the same job must still get it, otherwise
	// the assertion above would pass even if the route were simply broken.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/atoms/ai-assist/job-not-mine", nil)
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-OTHER")
	ownerResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("owner Do: %v", err)
	}
	defer ownerResp.Body.Close()
	if ownerResp.StatusCode != http.StatusOK {
		t.Fatalf("the job OWNER must still read their own job; got %d — this test is not exercising the gate",
			ownerResp.StatusCode)
	}
}

func TestGetAiAssistJob_404OnMissing(t *testing.T) {
	_, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()
	resp := mustGetAsync(t, srv.URL, "job-does-not-exist")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetAiAssistJob_404OnCrossTenant(t *testing.T) {
	repo, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Seed a row under tenant-OTHER.
	job, _ := aiassist.NewQueuedJob(
		"job-other", "tenant-OTHER", "gcid-test",
		aiassist.QuestionTypeMCQ, []byte(`{"prompt":"x"}`),
	)
	_ = repo.Create(context.Background(), job)

	// Request from tenant-test → 404 (cross-tenant).
	resp := mustGetAsync(t, srv.URL, "job-other")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-tenant)", resp.StatusCode)
	}
}

func TestGetAiAssistJob_405OnPost(t *testing.T) {
	_, _, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/atoms/ai-assist/job-xxx", nil)
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Smoke: end-to-end POST → subscriber-style UpdateCompleted → GET shape
// -----------------------------------------------------------------------------

func TestAiAssistAsync_PostThenSubscriberUpdate_GetReflectsCompletion(t *testing.T) {
	// Simulate the Step 4d Pub/Sub subscriber flow without actually wiring
	// it — call UpdateCompleted directly on the fake repo to mimic the
	// subscriber's UPDATE on chora.creation.ai_assist.completed.v1.
	repo, pub, router := newAsyncRouter(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// POST → 202
	postResp := mustPostAsync(t, srv.URL, `{"question_type":"oe","prompt":"x"}`)
	defer postResp.Body.Close()
	var posted aiAssistJobEnvelope
	_ = json.NewDecoder(postResp.Body).Decode(&posted)

	// Subscriber-like UPDATE (idempotent terminal transition).
	if err := repo.UpdateCompleted(
		context.Background(), "tenant-test", posted.JobID,
		[]byte(`{"stem":"x","oe_payload":{}}`),
		[]byte(`[{"name":"generate","status":"COMPLETED"}]`),
		false, 1, 10,
	); err != nil {
		t.Fatalf("UpdateCompleted: %v", err)
	}

	// GET → 200 + status reflects COMPLETED
	getResp := mustGetAsync(t, srv.URL, posted.JobID)
	defer getResp.Body.Close()
	var got aiAssistJobEnvelope
	_ = json.NewDecoder(getResp.Body).Decode(&got)
	if got.Status != "COMPLETED" {
		t.Errorf("Status = %q; want COMPLETED", got.Status)
	}
	if got.QualityWarning {
		t.Errorf("quality_warning should be false on happy-path completion")
	}
	if got.AttemptCount != 1 {
		t.Errorf("attempt_count = %d; want 1", got.AttemptCount)
	}
	if len(got.Candidate) == 0 {
		t.Errorf("Candidate should be populated on COMPLETED")
	}
	if got.CompletedAt == nil {
		t.Errorf("CompletedAt should be set on terminal")
	}
	// Just one started.v1 published (no double-publish on the GET).
	if len(pub.events) != 1 {
		t.Errorf("expected exactly 1 published event; got %d", len(pub.events))
	}
	// Avoid unused-import lint on the time package if no failure path exercises it.
	_ = time.Now
}

// -----------------------------------------------------------------------------
// FU-4(b) — qgen crew authoring metering via the price-plan layer (CHO-1661)
// -----------------------------------------------------------------------------

// crewManaFake is a minimal ports.ManaLedger for the crew-metering tests
// (this is package httpadapter, so it can't see fakeManaLedger in
// httpadapter_test). resolvePrice mirrors the price-plan layer: Units==0 ⇒
// resolved from the action_code; an explicit Units>0 is applied as-is.
type crewManaFake struct {
	insufficient bool
	deductCalls  int
	lastDeduct   ports.DeductManaReq
}

func (m *crewManaFake) resolved(req ports.DeductManaReq) int64 {
	if req.Units > 0 {
		return int64(req.Units)
	}
	return map[string]int64{"atom_authoring_assist": 5}[req.ActionCode]
}

func (m *crewManaFake) Deduct(_ context.Context, req ports.DeductManaReq) (ports.DeductManaResp, error) {
	m.deductCalls++
	m.lastDeduct = req
	charged := m.resolved(req)
	if m.insufficient {
		return ports.DeductManaResp{Success: false, RequiredUnits: charged},
			&clients.InsufficientManaError{RequiredUnits: charged, RecommendedTopupUnits: 100, RecommendedPlanCode: "MANA_TOPUP_BASIC"}
	}
	return ports.DeductManaResp{Success: true, ChargedUnits: charged, NewBalanceUnits: 1000}, nil
}

func (m *crewManaFake) Refund(_ context.Context, _ ports.RefundManaReq) (ports.RefundManaResp, error) {
	return ports.RefundManaResp{Success: true}, nil
}

func (m *crewManaFake) GetBalance(_ context.Context, _ string) (ports.BalanceResp, error) {
	return ports.BalanceResp{CurrentBalanceUnits: 1000}, nil
}

func newAsyncRouterWithMana(t *testing.T, mana *crewManaFake) (*fakeAiAssistJobsRepo, http.Handler) {
	t.Helper()
	repo := newFakeAiAssistJobsRepo()
	router := NewRouterWithDeps(RouterDeps{
		Repo:              inmem.NewAtomRepository(),
		AiAssistJobs:      repo,
		AiAssistPublisher: &fakeAsyncPublisher{},
		ManaLedger:        mana,
	})
	return repo, router
}

// The crew debits the configurable atom_authoring_assist price (Units==0 ⇒
// resolved server-side) — proving the AI Assist button can be metered via the
// price-plan layer. The fake resolves atom_authoring_assist → 5.
func TestAiAssistAsync_CrewMetering_ChargesConfiguredPrice(t *testing.T) {
	mana := &crewManaFake{}
	_, router := newAsyncRouterWithMana(t, mana)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{"question_type":"mcq","prompt":"Boiling point of water"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var env aiAssistJobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mana.deductCalls != 1 {
		t.Fatalf("DeductMana called %d times; want 1", mana.deductCalls)
	}
	if mana.lastDeduct.ActionCode != "atom_authoring_assist" {
		t.Errorf("action_code = %q; want atom_authoring_assist", mana.lastDeduct.ActionCode)
	}
	// Units==0 ⇒ the price is resolved server-side via the price-plan layer.
	if mana.lastDeduct.Units != 0 {
		t.Errorf("debit units = %d; want 0 (server-resolved)", mana.lastDeduct.Units)
	}
	if env.ManaCharged != 5 {
		t.Errorf("mana_charged = %d; want 5 (resolved crew price)", env.ManaCharged)
	}
}

// Insufficient balance hard-blocks the crew with a 402 (the only blocking case;
// everything else fails open to free).
func TestAiAssistAsync_CrewMetering_402OnInsufficient(t *testing.T) {
	mana := &crewManaFake{insufficient: true}
	_, router := newAsyncRouterWithMana(t, mana)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{"question_type":"mcq","prompt":"x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (insufficient mana)", resp.StatusCode)
	}
}

// Fail-open: with no ManaLedger wired the crew is FREE (no debit), so authoring
// is never blocked by un-wired metering. 202, no charge.
func TestAiAssistAsync_CrewMetering_FreeWhenUnwired(t *testing.T) {
	_, _, router := newAsyncRouter(t) // no ManaLedger
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := mustPostAsync(t, srv.URL, `{"question_type":"mcq","prompt":"x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (free when metering unwired)", resp.StatusCode)
	}
	var env aiAssistJobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.ManaCharged != 0 {
		t.Errorf("mana_charged = %d; want 0 (free)", env.ManaCharged)
	}
}
