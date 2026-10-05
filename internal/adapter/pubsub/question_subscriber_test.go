// question_subscriber_test.go — TDD coverage for QuestionGenerationSubscriber
// (P5-E). The subscriber processes chora.creation.question.generation_requested.v1
// messages: load the job, transition to running, invoke QGen, persist
// candidates JSONB + final status, emit generation_completed.v1.
//
// On QGen failure: status → failed AND refund mana.
// Idempotency: re-processing a terminal-status job is a no-op (ack + skip).
package pubsub_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fakes (independent file scope per pubsub_test package)
// -----------------------------------------------------------------------------

type fakeSubJobRepo struct {
	mu           sync.Mutex
	jobs         map[string]*question.ComposeJob
	createErr    error
	getErr       error
	updateErr    error
	updateCalls  []updateCall
	traceUpdates []traceUpdateCall
	// CHO-2398 chunk applies: recorded calls + the programmable slot-guard
	// verdict (false emulates the SQL "slot already filled" no-op).
	chunkApplies     []chunkApplyCall
	chunkApplyResult bool
	chunkApplyErr    error
}

type chunkApplyCall struct {
	TenantID       string
	JobID          string
	ChunkIndex     int
	ChunkCount     int
	CandidatesJSON []byte
}

type traceUpdateCall struct {
	TenantID string
	JobID    string
	Trace    string
	Applied  bool
}

type updateCall struct {
	TenantID string
	JobID    string
	Status   question.JobStatus
	HasCands bool
	Err      string
}

func newSubJobRepo() *fakeSubJobRepo {
	return &fakeSubJobRepo{
		jobs:             map[string]*question.ComposeJob{},
		chunkApplyResult: true,
	}
}

// ApplyChunkCandidates (CHO-2398) records the guarded slot-apply attempt and
// returns the programmable verdict (the SQL guard's stand-in).
func (r *fakeSubJobRepo) ApplyChunkCandidates(_ context.Context, tenantID, jobID string, chunkIndex, chunkCount int, candidatesJSON []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chunkApplyErr != nil {
		return false, r.chunkApplyErr
	}
	r.chunkApplies = append(r.chunkApplies, chunkApplyCall{
		TenantID: tenantID, JobID: jobID,
		ChunkIndex: chunkIndex, ChunkCount: chunkCount,
		CandidatesJSON: candidatesJSON,
	})
	return r.chunkApplyResult, nil
}

func (r *fakeSubJobRepo) Create(_ context.Context, j *question.ComposeJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.jobs[j.JobID] = j
	return nil
}

func (r *fakeSubJobRepo) Get(_ context.Context, tenantID, jobID string) (*question.ComposeJob, error) {
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

func (r *fakeSubJobRepo) UpdateStatus(_ context.Context, tenantID, jobID string, to question.JobStatus, cand []byte, errMsg string) error {
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
	r.updateCalls = append(r.updateCalls, updateCall{
		TenantID: tenantID, JobID: jobID, Status: to, HasCands: cand != nil, Err: errMsg,
	})
	return nil
}

// UpdateStatusWithProposal (Lane 1c + CHO-1819 P2 + CHO-1826 Gap #4) —
// UpdateStatus + stash the proposal + the mixed-batch generation summary + the
// per-step pipeline trace.
func (r *fakeSubJobRepo) UpdateStatusWithProposal(ctx context.Context, tenantID, jobID string, to question.JobStatus, cand, proposal, generationSummary, pipelineTrace []byte, errMsg string) error {
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
// candidates WITHOUT a status transition. Reads the row's status + candidates,
// invokes apply, writes the result back (the in-memory stand-in for SELECT ...
// FOR UPDATE). Reuses updateErr so a NACK-on-patch test can force a transient
// failure; apply's error propagates verbatim (fail-soft vs transient classified
// by the caller).
func (r *fakeSubJobRepo) PatchCandidatesUnderLock(_ context.Context, tenantID, jobID string, apply func(status string, current []byte) ([]byte, error)) error {
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

// TransitionFromSucceeded (Lane 1c W3) — CAS on status='succeeded'.
func (r *fakeSubJobRepo) TransitionFromSucceeded(_ context.Context, tenantID, jobID string, to question.JobStatus) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return false, r.updateErr
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

func (r *fakeSubJobRepo) UpdatePipelineTraceOnly(_ context.Context, tenantID, jobID string, pipelineTraceJSON []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return false, r.updateErr
	}
	call := traceUpdateCall{TenantID: tenantID, JobID: jobID, Trace: string(pipelineTraceJSON)}
	j, ok := r.jobs[jobID]
	// Mirror the real repo's guards: only a RUNNING job + a non-empty trace
	// applies (the monotonic jsonb_array_length guard is unit-tested at the pg
	// layer; the fake records the call + the running/non-empty gate).
	if ok && tenantID != "" && j.TenantID == tenantID &&
		j.Status == question.JobStatusRunning && len(pipelineTraceJSON) > 0 {
		j.PipelineTraceJSON = pipelineTraceJSON
		call.Applied = true
	}
	r.traceUpdates = append(r.traceUpdates, call)
	return call.Applied, nil
}

type fakeSubMana struct {
	mu          sync.Mutex
	refundCalls int
	lastRefund  ports.RefundManaReq
}

func (m *fakeSubMana) Deduct(_ context.Context, _ ports.DeductManaReq) (ports.DeductManaResp, error) {
	return ports.DeductManaResp{Success: true}, nil
}
func (m *fakeSubMana) Refund(_ context.Context, req ports.RefundManaReq) (ports.RefundManaResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refundCalls++
	m.lastRefund = req
	return ports.RefundManaResp{Success: true}, nil
}
func (m *fakeSubMana) GetBalance(_ context.Context, _ string) (ports.BalanceResp, error) {
	return ports.BalanceResp{}, nil
}

type fakeSubPublisher struct {
	mu     sync.Mutex
	events []pubEvent
}
type pubEvent struct {
	Topic   string
	Payload any
}

func (p *fakeSubPublisher) PublishJobEvent(_ context.Context, topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, pubEvent{Topic: topic, Payload: payload})
	return nil
}

func (p *fakeSubPublisher) snapshot() []pubEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pubEvent, len(p.events))
	copy(out, p.events)
	return out
}

// fakeSyncPublisher is the load-bearing synchronous publisher used by the
// qgen-crew ai_draft path (W2 Seam A). It records each event AND can be told
// to fail loud so the subscriber's fail+refund path is exercised.
type fakeSyncPublisher struct {
	mu      sync.Mutex
	events  []pubEvent
	failErr error
}

func (p *fakeSyncPublisher) PublishJobEventSync(_ context.Context, topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failErr != nil {
		return p.failErr
	}
	p.events = append(p.events, pubEvent{Topic: topic, Payload: payload})
	return nil
}

func (p *fakeSyncPublisher) snapshot() []pubEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pubEvent, len(p.events))
	copy(out, p.events)
	return out
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func seedJob(repo *fakeSubJobRepo, shape testJobShape, status question.JobStatus) *question.ComposeJob {
	jid := uuid.NewString()
	intent, input := shape.composeVOs()
	j := &question.ComposeJob{
		JobID:      jid,
		AtomID:     "00000000-0000-7000-8000-00000000a0a2",
		TenantID:   "22222222-2222-7222-8222-222222222222",
		AuthorGCID: "01970000-0000-7000-9000-000000000001",
		// ADR-195 WS9: a repo-loaded job carries the reconstructed compose VOs;
		// the subscriber resolves intent from job.Intent (not a job_type column).
		Intent:         intent,
		Input:          input,
		Status:         status,
		ManaActionCode: "question_authoring_ai_draft",
		ManaCharged:    10,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	repo.jobs[jid] = j
	return j
}

func TestSubscriber_BatchSourceMaterial_FailsLoud_WithoutRefund(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjBatchSourceMaterial, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		// Source material drives the batch path now (no job_type discriminant):
		// hasSourceMaterial keys on source_blob_uri (ADR-195 WS9 step 3).
		SourceBlobURI: "gs://chora-batch-uploads/t/job/material.pdf",
	}
	_ = sub.Handle(context.Background(), evt)
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (P5 doesn't implement batch)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana should be refunded for unimplemented batch; got %d refund calls", mana.refundCalls)
	}
}

func TestSubscriber_AlreadyTerminalJob_NoOps(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusSucceeded) // already terminal

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times on terminal-job replay; want 0", mana.refundCalls)
	}
	if len(pub.snapshot()) != 0 {
		t.Errorf("Events emitted on terminal-job replay; want none")
	}
}

// -----------------------------------------------------------------------------
// W2 Seam A — qgen-crew ai_draft path (flag QGenCrewEnabled=true).
//
// runAIDraft no longer calls QGen directly; it publishes
// chora.creation.ai_assist.started.v1 synchronously + leaves the job in
// running. The orchestrator runs the qgen 2-agent crew + publishes the
// terminal event, which the ai_assist terminal subscriber maps back.
// -----------------------------------------------------------------------------

func TestSubscriber_AIDraft_CrewEnabled_PublishesStarted_LeavesRunning(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo:         repo,
		Mana:            mana,
		Publisher:       pub,
		SyncPublisher:   sync,
		QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Generate a question about photosynthesis","difficulty":3}`,
		Traceparent:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Job stays running — terminal subscriber transitions it later.
	if job.Status != question.JobStatusRunning {
		t.Errorf("Status = %q; want running (crew path leaves job running)", job.Status)
	}
	// No mana refund on a successful dispatch.
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times; want 0", mana.refundCalls)
	}
	// Exactly one started event, synchronously published.
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	if events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("started topic = %q", events[0].Topic)
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("started payload not map[string]any: %T", events[0].Payload)
	}
	if payload["assist_id"] != job.JobID {
		t.Errorf("assist_id = %v; want job_id %s", payload["assist_id"], job.JobID)
	}
	if payload["content_type"] != "mcq" {
		t.Errorf("content_type = %v; want mcq (orchestrator reads question_type via content_type)", payload["content_type"])
	}
	if payload["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq", payload["question_type"])
	}
	if payload["prompt"] != "Generate a question about photosynthesis" {
		t.Errorf("prompt = %v", payload["prompt"])
	}
	if payload["max_retries"] != 0 {
		t.Errorf("max_retries = %v; want 0", payload["max_retries"])
	}
	if payload["traceparent"] != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %v; should propagate from event", payload["traceparent"])
	}
	// No generation_completed emitted on dispatch (terminal subscriber owns that).
	if len(pub.snapshot()) != 0 {
		t.Errorf("expected 0 completion events on dispatch; got %d", len(pub.snapshot()))
	}
}

// ADR-195 WS8 BE-1 — a count=N>1 topic (ai_draft) job must route through the
// batch/set lane so the orchestrator generates N candidates (fixes "count=5 →
// 1 question"). The dispatch forwards job_kind=batch + requested_count +
// content_type=mixed + a single-entry type_plan {question_type, count}.
func TestSubscriber_AIDraft_CrewEnabled_CountGreaterThanOne_DispatchesBatchSet(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Generate MCQs about photosynthesis","difficulty":3,"count":5}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	p, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("started payload not map[string]any: %T", events[0].Payload)
	}
	if p["job_kind"] != "batch" {
		t.Errorf("job_kind = %v; want batch (count>1 routes to the set lane)", p["job_kind"])
	}
	if p["requested_count"] != 5 {
		t.Errorf("requested_count = %v; want 5", p["requested_count"])
	}
	if p["content_type"] != "mixed" {
		t.Errorf("content_type = %v; want mixed (orchestrator reads type_plan)", p["content_type"])
	}
	tp, ok := p["type_plan"].([]map[string]any)
	if !ok || len(tp) != 1 {
		t.Fatalf("type_plan = %v; want one entry", p["type_plan"])
	}
	if tp[0]["question_type"] != "mcq" || tp[0]["count"] != 5 {
		t.Errorf("type_plan[0] = %v; want {question_type:mcq, count:5}", tp[0])
	}
}

// CHO-1826 Gap #4 follow-up — the no-files ai_draft path must honour the
// composer's forced-image opt-in (image_for_stem / image_for_answer) so the
// single qgen runner's render_image node fires and the canvas trace widget
// surfaces the Illustration card. The flags ride settings_json → the
// ai_assist.started.v1 payload (proto fields 13/14, the SAME path the
// single-mode drawer already uses).
func TestSubscriber_AIDraft_CrewEnabled_ForwardsImageFlags(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Photosynthesis in plants","difficulty":3,"image_for_stem":true,"image_for_answer":true}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("started payload not map[string]any: %T", events[0].Payload)
	}
	if payload["image_for_stem"] != true {
		t.Errorf("image_for_stem = %v; want true (forced-image opt-in must reach the crew on the no-files ai_draft path)", payload["image_for_stem"])
	}
	if payload["image_for_answer"] != true {
		t.Errorf("image_for_answer = %v; want true", payload["image_for_answer"])
	}
}

// When the author opts OUT of images, the flags must be ELIDED from the started
// payload (proto3-false omission / byte-stable parity with every pre-fix
// ai_draft event).
func TestSubscriber_AIDraft_CrewEnabled_OmitsImageFlagsWhenUnset(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Photosynthesis in plants","difficulty":3}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	if _, present := payload["image_for_stem"]; present {
		t.Errorf("image_for_stem must be omitted when the author did not opt in (byte-stable legacy event)")
	}
	if _, present := payload["image_for_answer"]; present {
		t.Errorf("image_for_answer must be omitted when the author did not opt in")
	}
}

func TestSubscriber_AIDraft_CrewEnabled_EmptyPrompt_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"difficulty":3}`, // no prompt
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (empty prompt)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
	if len(sync.snapshot()) != 0 {
		t.Errorf("no started event should be published for empty prompt; got %d", len(sync.snapshot()))
	}
}

func TestSubscriber_AIDraft_CrewEnabled_PublishFails_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{failErr: errors.New("schema registry reject")}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Valid prompt"}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// Load-bearing publish: on failure the job MUST NOT strand in running.
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (started publish failed)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1 (refund on publish failure)", mana.refundCalls)
	}
}

func TestSubscriber_AIDraft_CrewEnabled_NoSyncPublisher_FailsLoud(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	// QGenCrewEnabled but SyncPublisher nil → misconfiguration → fail loud.
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		QGenCrewEnabled: true, // no SyncPublisher
	})
	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"valid"}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (misconfigured: no sync publisher)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

func TestSubscriber_AIDraft_CrewDisabled_FailsLoud(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: false, // CHO-1658 — legacy QGen path retired
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Generate something"}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// CHO-1658 — the legacy direct-engine QGen path (dead Agent Engine) is retired;
	// flag-off now fails loud + refunds rather than calling a 404'ing engine.
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (crew disabled → fail loud)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1 (fail-loud refunds)", mana.refundCalls)
	}
	if len(sync.snapshot()) != 0 {
		t.Errorf("must NOT publish started when the crew is disabled; got %d", len(sync.snapshot()))
	}
}

// -----------------------------------------------------------------------------
// JSON envelope decode test — the subscriber should accept the canonical
// Pub/Sub message body shape.
// -----------------------------------------------------------------------------

func TestQuestionGenerationRequestedEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	in := pubsub.QuestionGenerationRequestedEvent{
		JobID: "j", AtomID: "a", AuthorGCID: "g", TenantID: "t",
		QuestionType: "mcq",
		SettingsJSON: `{"prompt":"x","difficulty":3}`,
		RequestedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(in)
	var out pubsub.QuestionGenerationRequestedEvent
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if out.JobID != in.JobID {
		t.Errorf("JobID = %q", out.JobID)
	}
}

// -----------------------------------------------------------------------------
// EPIC-1a — batch_source_material crew dispatch.
//
// runBatchSourceMaterial publishes ONE chora.creation.ai_assist.started.v1 with
// job_kind=batch + the grounding material (source_blob_uri) + requested_count.
// The orchestrator's QGenBatchRunner loops the qgen graph N times and publishes
// ONE completed.v1 whose candidate_payload_json is a JSON array; the terminal
// subscriber parses the array → N drafts. The legacy BlobStore+Extractor
// download/extract path (chora-doc-parser) is retired per ADR-169 / B1.
// -----------------------------------------------------------------------------

func seedBatchJob(repo *fakeSubJobRepo) *question.ComposeJob {
	j := seedJob(repo, tjBatchSourceMaterial, question.JobStatusRequested)
	j.ManaActionCode = "question_authoring_batch_parse"
	j.SourceBlobURI = "gs://chora-batch-uploads/22222222/job/material.pdf"
	j.SourceMimeType = "application/pdf"
	return j
}

func batchEvt(job *question.ComposeJob, settingsJSON string) pubsub.QuestionGenerationRequestedEvent {
	return pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID:       job.TenantID,
		QuestionType:   "mcq",
		SourceBlobURI:  job.SourceBlobURI,
		SourceMimeType: job.SourceMimeType,
		SettingsJSON:   settingsJSON,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
}

func TestSubscriber_Batch_CrewEnabled_PublishesBatchStarted_LeavesRunning(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedBatchJob(repo)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := batchEvt(job, `{"count":5,"grounding_mode":"strict","target_growth_edges":["fractions","ratios"],"difficulty":3,"context":"Chapter 4 only"}`)
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if job.Status != question.JobStatusRunning {
		t.Errorf("Status = %q; want running (batch leaves job running)", job.Status)
	}
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times; want 0", mana.refundCalls)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 batch started event; got %d", len(events))
	}
	if events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("started topic = %q", events[0].Topic)
	}
	p, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not map[string]any: %T", events[0].Payload)
	}
	if p["assist_id"] != job.JobID {
		t.Errorf("assist_id = %v; want %s", p["assist_id"], job.JobID)
	}
	if p["job_kind"] != "batch" {
		t.Errorf("job_kind = %v; want batch", p["job_kind"])
	}
	if p["requested_count"] != 5 {
		t.Errorf("requested_count = %v; want 5", p["requested_count"])
	}
	if p["grounding_mode"] != "strict" {
		t.Errorf("grounding_mode = %v; want strict", p["grounding_mode"])
	}
	if p["source_blob_uri"] != job.SourceBlobURI {
		t.Errorf("source_blob_uri = %v", p["source_blob_uri"])
	}
	if p["source_mime_type"] != "application/pdf" {
		t.Errorf("source_mime_type = %v", p["source_mime_type"])
	}
	if p["prompt"] != "Chapter 4 only" {
		t.Errorf("prompt = %v; want author context 'Chapter 4 only'", p["prompt"])
	}
	if p["question_type"] != "mcq" || p["content_type"] != "mcq" {
		t.Errorf("question_type/content_type = %v/%v; want mcq", p["question_type"], p["content_type"])
	}
	edges, ok := p["target_growth_edges"].([]string)
	if !ok || len(edges) != 2 || edges[0] != "fractions" || edges[1] != "ratios" {
		t.Errorf("target_growth_edges = %v; want [fractions ratios]", p["target_growth_edges"])
	}
	if p["traceparent"] != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %v; should propagate", p["traceparent"])
	}
	// No generation_completed emitted on dispatch (terminal subscriber owns that).
	if len(pub.snapshot()) != 0 {
		t.Errorf("expected 0 completion events on dispatch; got %d", len(pub.snapshot()))
	}
}

func TestSubscriber_Batch_CrewEnabled_ClampsCountAndDefaultsGroundingMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in            string
		wantCount     int
		wantGrounding string
	}{
		{`{"count":0}`, 1, "starting_point"},
		// ADR-251 D1 — the ceiling is 200 now: 99 passes through unclamped,
		// anything above the ceiling clamps to it.
		{`{"count":99}`, 99, "starting_point"},
		{`{"count":250}`, 200, "starting_point"},
		{`{}`, 1, "starting_point"},
		{`{"count":7,"grounding_mode":"starting_point"}`, 7, "starting_point"},
		{`{"count":2,"grounding_mode":"strict"}`, 2, "strict"},
	}
	for _, tc := range cases {
		repo := newSubJobRepo()
		sync := &fakeSyncPublisher{}
		job := seedBatchJob(repo)
		sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
			JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
			SyncPublisher: sync, QGenCrewEnabled: true,
		})
		if err := sub.Handle(context.Background(), batchEvt(job, tc.in)); err != nil {
			t.Fatalf("settings %s: Handle: %v", tc.in, err)
		}
		ev := sync.snapshot()
		if len(ev) != 1 {
			t.Fatalf("settings %s: expected 1 event; got %d", tc.in, len(ev))
		}
		p := ev[0].Payload.(map[string]any)
		if p["requested_count"] != tc.wantCount {
			t.Errorf("settings %s: requested_count = %v; want %d", tc.in, p["requested_count"], tc.wantCount)
		}
		if p["grounding_mode"] != tc.wantGrounding {
			t.Errorf("settings %s: grounding_mode = %v; want %s", tc.in, p["grounding_mode"], tc.wantGrounding)
		}
	}
}

func TestSubscriber_Batch_OE_StrictMode_DefaultPromptInstruction(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedBatchJob(repo)
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	// OE + strict + NO author context → default closed-book OE instruction.
	evt := batchEvt(job, `{"count":2,"grounding_mode":"strict"}`)
	evt.QuestionType = "oe"
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p := sync.snapshot()[0].Payload.(map[string]any)
	prompt, _ := p["prompt"].(string)
	if !strings.Contains(prompt, "open-ended") || !strings.Contains(prompt, "strictly") {
		t.Errorf("OE strict default prompt = %q; want 'open-ended' + 'strictly'", prompt)
	}
	if p["question_type"] != "oe" {
		t.Errorf("question_type = %v; want oe", p["question_type"])
	}
}

func TestSubscriber_Batch_CrewEnabled_MissingBlob_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	job := seedBatchJob(repo)
	job.SourceBlobURI = "" // no uploaded material

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	evt := batchEvt(job, `{"count":3}`)
	evt.SourceBlobURI = ""
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (missing blob)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
	if len(sync.snapshot()) != 0 {
		t.Errorf("no started event on missing blob; got %d", len(sync.snapshot()))
	}
}

func TestSubscriber_Batch_CrewEnabled_PublishFails_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{failErr: errors.New("schema registry reject")}
	job := seedBatchJob(repo)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	if err := sub.Handle(context.Background(), batchEvt(job, `{"count":3}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (publish error)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

func TestSubscriber_Batch_CrewEnabled_NoSyncPublisher_FailsLoud(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	job := seedBatchJob(repo)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		QGenCrewEnabled: true, // no SyncPublisher → misconfiguration
	})
	if err := sub.Handle(context.Background(), batchEvt(job, `{"count":3}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (misconfig)", job.Status)
	}
}

func TestSubscriber_Batch_CrewDisabled_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	job := seedBatchJob(repo)
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		QGenCrewEnabled: false, // crew disabled → batch unsupported (no legacy doc-parser)
	})
	if err := sub.Handle(context.Background(), batchEvt(job, `{"count":3}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (crew disabled)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}
