// Tests for AiAssistTerminalSubscriber.
//
// Strategy: hand-construct wire bytes via the protowire helpers (mirrors
// the decoder_test.go pattern in the protomarshal package), feed them
// into HandleCompleted / HandleRefused, assert the captured repo calls.
//
// Stub repo (fakeAiAssistJobsRepo) records each Update* call's args so
// tests can verify the wire→port translation:
//
//   - tenant_id correctly preferred from attributes when present
//   - tenant_id falls back to decoded envelope when attribute missing
//   - aiassist.ErrNotFound returns nil (ACK semantics)
//   - decode failures return wrapped errors (NACK semantics)
package pubsub_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Wire builders (duplicated from decoder_test.go since cross-package
// _test.go files cannot share helpers without making them exported in
// the prod package — keeping them local).
// -----------------------------------------------------------------------------

func appendString(b []byte, field protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendString(b, v)
	return b
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	b = protowire.AppendVarint(b, v)
	return b
}

func appendBytesField(b []byte, field protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

func encodeEnvelopeBytes(eventID, idemp, tenant, gcid string) []byte {
	out := make([]byte, 0, 128)
	if eventID != "" {
		out = appendString(out, 1, eventID)
	}
	if idemp != "" {
		out = appendString(out, 2, idemp)
	}
	if tenant != "" {
		out = appendString(out, 3, tenant)
	}
	if gcid != "" {
		out = appendString(out, 4, gcid)
	}
	return out
}

// -----------------------------------------------------------------------------
// Fake repo capturing all 3 Update* calls.
// -----------------------------------------------------------------------------

type completedArgs struct {
	tenantID       string
	jobID          string
	resultPayload  []byte
	pipelineTrace  []byte
	qualityWarning bool
	attemptCount   int
	manaCharged    int
}

type refusedArgs struct {
	tenantID             string
	jobID                string
	reason               aiassist.RefusalReason
	armorVerdict         string
	userFacingMsg        string
	lastCandidatePayload []byte
	pipelineTrace        []byte
	attemptCount         int
	manaCharged          int
}

type fakeAiAssistJobsRepo struct {
	completedCalls []completedArgs
	refusedCalls   []refusedArgs
	completedErr   error
	refusedErr     error
}

func (r *fakeAiAssistJobsRepo) UpdateCompleted(
	_ context.Context,
	tenantID, jobID string,
	resultPayload, pipelineTrace []byte,
	qualityWarning bool,
	attemptCount, manaCharged int,
) error {
	r.completedCalls = append(r.completedCalls, completedArgs{
		tenantID:       tenantID,
		jobID:          jobID,
		resultPayload:  resultPayload,
		pipelineTrace:  pipelineTrace,
		qualityWarning: qualityWarning,
		attemptCount:   attemptCount,
		manaCharged:    manaCharged,
	})
	return r.completedErr
}

func (r *fakeAiAssistJobsRepo) UpdateRefused(
	_ context.Context,
	tenantID, jobID string,
	reason aiassist.RefusalReason,
	armorVerdict, userFacingMsg string,
	lastCandidatePayload, pipelineTrace []byte,
	attemptCount, manaCharged int,
) error {
	r.refusedCalls = append(r.refusedCalls, refusedArgs{
		tenantID:             tenantID,
		jobID:                jobID,
		reason:               reason,
		armorVerdict:         armorVerdict,
		userFacingMsg:        userFacingMsg,
		lastCandidatePayload: lastCandidatePayload,
		pipelineTrace:        pipelineTrace,
		attemptCount:         attemptCount,
		manaCharged:          manaCharged,
	})
	return r.refusedErr
}

func (r *fakeAiAssistJobsRepo) UpdateFailed(
	_ context.Context,
	_, _ string,
	_ []byte,
	_ string,
	_, _ int,
) error {
	return nil
}

// -----------------------------------------------------------------------------
// HandleCompleted
// -----------------------------------------------------------------------------

func TestHandleCompleted_DispatchesToRepoFromAttributes(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-1", "idemp-1", "tenant-from-env", "gcid-1")
	bz := make([]byte, 0, 128)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-1")
	bz = appendString(bz, 11, `{"x":1}`)
	bz = appendString(bz, 12, `[{"step":"v"}]`)
	bz = appendVarint(bz, 13, 1) // quality_warning = true
	bz = appendVarint(bz, 14, 3) // attempt_count
	bz = appendVarint(bz, 9, 50) // mana_charged

	msg := eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: "tenant-from-attr"}} // wins over envelope
	if err := sub.HandleCompleted(context.Background(), msg); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Fatalf("UpdateCompleted: got %d calls", len(repo.completedCalls))
	}
	c := repo.completedCalls[0]
	if c.tenantID != "tenant-from-attr" {
		t.Errorf("tenantID: got %q (envelope should win)", c.tenantID)
	}
	if c.jobID != "assist-1" {
		t.Errorf("jobID: got %q", c.jobID)
	}
	if string(c.resultPayload) != `{"x":1}` {
		t.Errorf("resultPayload: got %q", string(c.resultPayload))
	}
	if string(c.pipelineTrace) != `[{"step":"v"}]` {
		t.Errorf("pipelineTrace: got %q", string(c.pipelineTrace))
	}
	if !c.qualityWarning {
		t.Error("qualityWarning: false")
	}
	if c.attemptCount != 3 {
		t.Errorf("attemptCount: got %d", c.attemptCount)
	}
	if c.manaCharged != 50 {
		t.Errorf("manaCharged: got %d", c.manaCharged)
	}
}

func TestHandleCompleted_FallsBackToEnvelopeWhenAttrAbsent(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-2", "idemp-2", "tenant-from-env", "gcid-2")
	bz := make([]byte, 0, 64)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-2")

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Fatalf("UpdateCompleted: got %d calls", len(repo.completedCalls))
	}
	if repo.completedCalls[0].tenantID != "tenant-from-env" {
		t.Errorf("tenantID: got %q (should fall back to envelope)", repo.completedCalls[0].tenantID)
	}
}

func TestHandleCompleted_MissingTenantOrAssist_FailsLoud(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	// Envelope present but no tenant_id field; no assist_id field either.
	envBz := encodeEnvelopeBytes("evt-3", "", "", "gcid-3")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	// no assist_id

	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error on missing tenant_id / assist_id")
	}
	if len(repo.completedCalls) != 0 {
		t.Errorf("UpdateCompleted should not have been called, got %d calls", len(repo.completedCalls))
	}
}

func TestHandleCompleted_ErrNotFound_ACKsWithWarn(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{completedErr: aiassist.ErrNotFound}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-nf", "", "tenant-nf", "gcid-nf")
	bz := make([]byte, 0, 64)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-nf")

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: ErrNotFound should ACK; got %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Errorf("UpdateCompleted: got %d calls (should still have attempted)", len(repo.completedCalls))
	}
}

func TestHandleCompleted_RepoErr_PropagatesForNACK(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{completedErr: errors.New("db down")}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-nack", "", "tenant-nack", "gcid-nack")
	bz := make([]byte, 0, 64)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-nack")

	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error to propagate to caller for NACK")
	}
}

func TestHandleCompleted_DecodeErr_PropagatesForNACK(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)
	// Truncated frame: tag for field 2 (string) claiming length 99 but no
	// actual bytes follow.
	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: []byte{0x12, 99}})
	if err == nil {
		t.Fatal("expected decode error to surface")
	}
}

func TestHandleCompleted_NilRepo_FailsLoud(t *testing.T) {
	sub := pubsub.NewAiAssistTerminalSubscriber(nil)
	envBz := encodeEnvelopeBytes("e", "", "t", "g")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "a")
	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error on nil repo")
	}
}

// -----------------------------------------------------------------------------
// HandleRefused
// -----------------------------------------------------------------------------

func TestHandleRefused_DispatchesToRepoFromAttributes(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-r", "idemp-r", "tenant-env", "gcid-r")
	bz := make([]byte, 0, 128)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-r")
	bz = appendString(bz, 3, "GUARDRAIL_PRE")
	bz = appendString(bz, 4, "armor:pii_block")
	bz = appendString(bz, 6, "Cannot complete request.")
	bz = appendVarint(bz, 7, 25)
	bz = appendString(bz, 9, `{"last":"x"}`)
	bz = appendVarint(bz, 10, 1)

	msg := eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: "tenant-attr"}}
	if err := sub.HandleRefused(context.Background(), msg); err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if len(repo.refusedCalls) != 1 {
		t.Fatalf("UpdateRefused: got %d calls", len(repo.refusedCalls))
	}
	r := repo.refusedCalls[0]
	if r.tenantID != "tenant-attr" {
		t.Errorf("tenantID: got %q (envelope should win)", r.tenantID)
	}
	if r.jobID != "assist-r" {
		t.Errorf("jobID: got %q", r.jobID)
	}
	if r.reason != aiassist.RefusalReasonGuardrailPre {
		t.Errorf("reason: got %q", r.reason)
	}
	if r.armorVerdict != "armor:pii_block" {
		t.Errorf("armorVerdict: got %q", r.armorVerdict)
	}
	if r.userFacingMsg != "Cannot complete request." {
		t.Errorf("userFacingMsg: got %q", r.userFacingMsg)
	}
	if r.manaCharged != 25 {
		t.Errorf("manaCharged: got %d", r.manaCharged)
	}
	if string(r.lastCandidatePayload) != `{"last":"x"}` {
		t.Errorf("lastCandidatePayload: got %q", string(r.lastCandidatePayload))
	}
	if r.attemptCount != 1 {
		t.Errorf("attemptCount: got %d", r.attemptCount)
	}
}

func TestHandleRefused_EmptyReason_FailsLoud(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-e", "", "tenant-e", "gcid-e")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-e")
	// no refusal_reason

	err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error on empty refusal_reason")
	}
	if len(repo.refusedCalls) != 0 {
		t.Errorf("UpdateRefused should not have been called; got %d", len(repo.refusedCalls))
	}
}

func TestHandleRefused_InvalidReason_FailsLoud(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-i", "", "tenant-i", "gcid-i")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-i")
	bz = appendString(bz, 3, "MADE_UP_REASON")

	err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error on invalid refusal_reason")
	}
}

func TestHandleRefused_ErrNotFound_ACKs(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{refusedErr: aiassist.ErrNotFound}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-rnf", "", "tenant-rnf", "gcid-rnf")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-rnf")
	bz = appendString(bz, 3, "VALIDATION")

	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: ErrNotFound should ACK; got %v", err)
	}
}

func TestHandleRefused_RepoErr_PropagatesForNACK(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{refusedErr: errors.New("db down")}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)

	envBz := encodeEnvelopeBytes("evt-rn", "", "tenant-rn", "gcid-rn")
	bz := make([]byte, 0, 32)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, "assist-rn")
	bz = appendString(bz, 3, "GUARDRAIL_POST")

	err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected error to propagate to caller for NACK")
	}
}

func TestHandleRefused_DecodeErr_PropagatesForNACK(t *testing.T) {
	repo := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(repo)
	err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: []byte{0x12, 99}})
	if err == nil {
		t.Fatal("expected decode error to surface")
	}
}

// -----------------------------------------------------------------------------
// W2 Seam A — dual-dispatch to the question_generation_jobs path.
//
// When the terminal subscriber is wired WithQuestionJobs(...), it resolves the
// assist_id against question_generation_jobs FIRST. If found AND
// JobType==ai_draft AND status running → map onto the question-job (no
// ai_assist_jobs write). Else (question.ErrNotFound) → fall through to the
// legacy ai_assist_jobs path. The two surfaces share one wire + two subs; the
// existence-check is the only discriminator.
// -----------------------------------------------------------------------------

// completedWireMCQ builds a completed.v1 frame with a candidate_payload_json.
func completedWire(tenant, assist, candidateJSON string, qualityWarning bool, manaCharged uint64) []byte {
	envBz := encodeEnvelopeBytes("evt", "idemp", tenant, "gcid")
	bz := make([]byte, 0, 256)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, assist)
	if candidateJSON != "" {
		bz = appendString(bz, 11, candidateJSON)
	}
	if qualityWarning {
		bz = appendVarint(bz, 13, 1)
	}
	if manaCharged > 0 {
		bz = appendVarint(bz, 9, manaCharged)
	}
	return bz
}

func refusedWire(tenant, assist, reason, userMsg string, manaCharged uint64) []byte {
	envBz := encodeEnvelopeBytes("evt", "idemp", tenant, "gcid")
	bz := make([]byte, 0, 128)
	bz = appendBytesField(bz, 1, envBz)
	bz = appendString(bz, 2, assist)
	bz = appendString(bz, 3, reason)
	if userMsg != "" {
		bz = appendString(bz, 6, userMsg)
	}
	if manaCharged > 0 {
		bz = appendVarint(bz, 7, manaCharged)
	}
	return bz
}

// errMana fails every Refund (transport error) but otherwise mirrors fakeSubMana.
type errMana struct {
	err         error
	refundCalls int
}

func (m *errMana) Deduct(_ context.Context, _ ports.DeductManaReq) (ports.DeductManaResp, error) {
	return ports.DeductManaResp{Success: true}, nil
}
func (m *errMana) Refund(_ context.Context, _ ports.RefundManaReq) (ports.RefundManaResp, error) {
	m.refundCalls++
	return ports.RefundManaResp{}, m.err
}
func (m *errMana) GetBalance(_ context.Context, _ string) (ports.BalanceResp, error) {
	return ports.BalanceResp{}, nil
}

// errPublisher fails every PublishJobEvent.
type errPublisher struct{ err error }

func (p *errPublisher) PublishJobEvent(_ context.Context, _ string, _ any) error { return p.err }

// testJobShape is a TEST-LOCAL label standing in for the retired question.JobType
// enum (removed in ADR-195 WS9 step 3). Each shape maps to the compose VOs
// (Intent/Input) a repo-loaded job carries, so the fixtures read like the old
// job_type cases while the production code branches purely on Intent/Input. Shared
// across the pubsub package tests (seedJob, seedQuestionJob).
type testJobShape int

const (
	tjAIDraft             testJobShape = iota // new_question / prompt
	tjBatchSourceMaterial                     // new_question / source_files
	tjManualDraft                             // new_question / by_hand
	tjAIModelAnswer                           // model_answer_fill / prompt
	tjImageRegen                              // image_regen / prompt
)

// composeVOs mirrors the repo read reconstruction (ADR-195 WS9 step 1) for the
// test fixtures: each shape maps to its intent + a representative Input, so the
// VO-driven terminal routing (step 2) behaves as it will in prod.
func (s testJobShape) composeVOs() (question.Intent, question.Input) {
	switch s {
	case tjBatchSourceMaterial:
		return question.IntentNewQuestion, question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}
	case tjManualDraft:
		return question.IntentNewQuestion, question.Input{ByHand: true}
	case tjAIModelAnswer:
		return question.IntentModelAnswerFill, question.Input{Prompt: "p"}
	case tjImageRegen:
		return question.IntentImageRegen, question.Input{Prompt: "p"}
	default: // tjAIDraft
		return question.IntentNewQuestion, question.Input{Prompt: "p"}
	}
}

func seedQuestionJob(repo *fakeSubJobRepo, shape testJobShape, status question.JobStatus) *question.ComposeJob {
	jid := uuid.NewString()
	intent, input := shape.composeVOs()
	j := &question.ComposeJob{
		JobID:      jid,
		AtomID:     "00000000-0000-7000-8000-00000000a0a2",
		TenantID:   "tenant-qjob",
		AuthorGCID: "gcid-qjob",
		// ADR-195 WS9 step 1: a repo-loaded job carries the reconstructed compose
		// VOs. The terminal subscriber (WS9 step 2) routes on these, not job_type.
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

// TestTerminal_Completed_RoutesByComposeVOs_NotJobType is the ADR-195 WS9 step-2
// RED→GREEN: the terminal subscriber must claim + complete a job on its compose
// VOs (Intent/Input) alone — with job_type UNSET, as it will be once the column is
// dropped (step 3). On the pre-swap code (claimable reads job_type) this job is not
// claimed and the completion falls through to the legacy path, leaving it running.
func TestTerminal_Completed_RoutesByComposeVOs_NotJobType(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	pub := &fakeSubPublisher{}
	jid := uuid.NewString()
	job := &question.ComposeJob{
		JobID:      jid,
		AtomID:     "00000000-0000-7000-8000-00000000a0a2",
		TenantID:   "tenant-qjob",
		AuthorGCID: "gcid-qjob",
		// JobType intentionally UNSET — only the reconstructed compose VOs drive routing.
		Intent:         question.IntentNewQuestion,
		Input:          question.Input{Prompt: "p"},
		Status:         question.JobStatusRunning,
		ManaActionCode: "question_authoring_ai_draft",
		ManaCharged:    10,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	qjobs.jobs[jid] = job

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, pub)
	cand := `{"candidate":{"stem":"Q?","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}}`
	bz := completedWire(job.TenantID, job.JobID, cand, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("Status = %q; want succeeded (claimable must read Intent/Input, not job_type)", job.Status)
	}
	if len(legacy.completedCalls) != 0 {
		t.Errorf("legacy path touched %d times; want 0 (must be claimed as a compose job via VOs)", len(legacy.completedCalls))
	}
}

func TestTerminal_Completed_QuestionJob_MapsToSucceeded(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, mana, pub)

	cand := `{"candidate":{"stem":"Q?","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}}`
	bz := completedWire(job.TenantID, job.JobID, cand, false, 10)

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("question-job Status = %q; want succeeded", job.Status)
	}
	if len(job.CandidateQuestionsJSON) == 0 {
		t.Error("candidate JSON not persisted on the question-job")
	}
	// Legacy ai_assist_jobs path must NOT have been touched.
	if len(legacy.completedCalls) != 0 {
		t.Errorf("legacy UpdateCompleted called %d times; want 0 (question-job path)", len(legacy.completedCalls))
	}
	// generation_completed.v1 emitted.
	events := pub.snapshot()
	if len(events) != 1 || events[0].Topic != "chora.creation.question.generation_completed.v2" {
		t.Errorf("expected 1 generation_completed event; got %+v", events)
	}
}

// TestTerminal_Completed_ModelAnswerFill_MapsToSucceeded — CHO-1658: model_answer_fill
// is now crew-dispatched (runModelAnswer publishes started.v2 instead of calling the
// dead Agent Engine), so its completed.v1 MUST be claimed by the question-jobs path
// (resolveQuestionJob) and mapped to a candidate draft — NOT fall through to legacy.
func TestTerminal_Completed_ModelAnswerFill_MapsToSucceeded(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIModelAnswer, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, pub)

	// The fill crew returns a full OE candidate (stem + the generated model answer).
	cand := `{"candidate":{"stem":"Explain photosynthesis.","question_type":"oe","oe_payload":{"model_answer":"Photosynthesis converts light energy into chemical energy stored in glucose using chlorophyll in the chloroplasts."}}}`
	bz := completedWire(job.TenantID, job.JobID, cand, false, 5)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("model_answer_fill job Status = %q; want succeeded (must be claimed via Intent=model_answer_fill, not fall through)", job.Status)
	}
	if len(job.CandidateQuestionsJSON) == 0 {
		t.Error("candidate JSON not persisted on the model_answer_fill job")
	}
	if len(legacy.completedCalls) != 0 {
		t.Errorf("legacy ai_assist_jobs path touched %d times; want 0 (crew-dispatched model_answer_fill is a question-job)", len(legacy.completedCalls))
	}
}

// TestTerminal_Completed_AIDraft_BatchPayload_NormalizesToNDrafts is the
// regression for the live ADR-195 WS8 BE-1 "count=5 -> 0 candidates" failure
// (job 99fa72ba, 2026-06-26). A count>1 ai_draft is dispatched through the set
// lane (job_kind=batch + type_plan), so the orchestrator returns a
// {candidates:[...]} batch shape — but the JOB stays type=ai_draft, so the
// terminal subscriber routed it to the SINGLE normalizer, which hit the
// envelope's missing candidate fields and failed "cannot resolve question_type".
// The set-shaped payload MUST normalize into N drafts regardless of JobType.
func TestTerminal_Completed_AIDraft_BatchPayload_NormalizesToNDrafts(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// The pure-prompt set lane returns N candidates as a {candidates:[...]}
	// object (the same batch shape batch_source_material emits).
	payload := `{"candidates":[` +
		mcqCandidateJSON("Q1?", `[]`) + `,` +
		mcqCandidateJSON("Q2?", `[]`) + `,` +
		mcqCandidateJSON("Q3?", `[]`) +
		`]}`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 30)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("status = %q; want succeeded (count>1 ai_draft set lane normalizes the batch shape)", job.Status)
	}
	drafts := decodeDrafts(t, job.CandidateQuestionsJSON)
	if len(drafts) != 3 {
		t.Fatalf("drafts = %d; want 3 (the N-candidate set, not 1 or 0)", len(drafts))
	}
}

func TestTerminal_Completed_QuestionJob_QualityWarning_StillSucceeds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	cand := `{"stem":"Q?","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}`
	bz := completedWire(job.TenantID, job.JobID, cand, true /* quality_warning */, 10)

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("Status = %q; want succeeded (quality_warning candidate is best-effort, not dropped)", job.Status)
	}
}

func TestTerminal_Completed_QuestionJob_EmptyCandidate_FailsAndRefunds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// Completed event with an un-normalizable candidate (no options) — the
	// job cannot succeed; map to failed + refund (no broken success).
	cand := `{"stem":"","question_type":"mcq"}`
	bz := completedWire(job.TenantID, job.JobID, cand, false, 10)

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (empty candidate)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Refused_QuestionJob_MapsToFailed_RefundsMana(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := refusedWire(job.TenantID, job.JobID, "GUARDRAIL_PRE", "Cannot complete request.", 10)

	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	if job.Error == "" {
		t.Error("Error message empty on refusal")
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
	if mana.lastRefund.IdempotencyKey != job.JobID+"-refund" {
		t.Errorf("refund idempotency = %q; want %q", mana.lastRefund.IdempotencyKey, job.JobID+"-refund")
	}
	// Legacy path untouched.
	if len(legacy.refusedCalls) != 0 {
		t.Errorf("legacy UpdateRefused called %d times; want 0", len(legacy.refusedCalls))
	}
	events := pub.snapshot()
	if len(events) != 1 || events[0].Topic != "chora.creation.question.generation_completed.v2" {
		t.Errorf("expected 1 generation_completed (failed) event; got %+v", events)
	}
}

func TestTerminal_Completed_NotAQuestionJob_FallsThroughToLegacy(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo() // empty — assist_id won't resolve
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire("tenant-legacy", "assist-legacy", `{"x":1}`, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	// Fell through to the legacy ai_assist_jobs path.
	if len(legacy.completedCalls) != 1 {
		t.Fatalf("legacy UpdateCompleted called %d times; want 1 (fall-through)", len(legacy.completedCalls))
	}
	if string(legacy.completedCalls[0].resultPayload) != `{"x":1}` {
		t.Errorf("legacy resultPayload = %q (must pass verbatim)", string(legacy.completedCalls[0].resultPayload))
	}
	// Question-job path must NOT have fired mana / events.
	if mana.refundCalls != 0 || len(pub.snapshot()) != 0 {
		t.Error("question-job side effects fired on a legacy fall-through")
	}
}

func TestTerminal_Refused_NotAQuestionJob_FallsThroughToLegacy(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := refusedWire("tenant-legacy", "assist-legacy", "VALIDATION", "nope", 50)
	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if len(legacy.refusedCalls) != 1 {
		t.Fatalf("legacy UpdateRefused called %d times; want 1 (fall-through)", len(legacy.refusedCalls))
	}
	if mana.refundCalls != 0 {
		t.Error("question-job refund fired on a legacy fall-through")
	}
}

func TestTerminal_Completed_QuestionJob_ByHand_FallsThroughToLegacy(t *testing.T) {
	// A row exists in question_generation_jobs but it's a BY-HAND new_question
	// (author supplied candidates inline; Input.RequiresLLM()==false) — never
	// crew-dispatched, so a terminal event for it must NOT be claimed. Fall
	// through. (model_answer_fill, formerly the fall-through case here, is now
	// crew-dispatched + claimable per CHO-1658 — see
	// TestTerminal_Completed_ModelAnswerFill_MapsToSucceeded.)
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjManualDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire(job.TenantID, job.JobID, `{"x":1}`, false, 5)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(legacy.completedCalls) != 1 {
		t.Errorf("legacy UpdateCompleted called %d times; want 1 (by-hand new_question falls through)", len(legacy.completedCalls))
	}
	if job.Status != question.JobStatusRunning {
		t.Errorf("by-hand question-job touched; status = %q want running", job.Status)
	}
}

func TestTerminal_Completed_QuestionJob_NotRunning_FallsThroughToLegacy(t *testing.T) {
	// Idempotency / dual-dispatch guard: a job already terminal must not be
	// re-claimed by a duplicate terminal event. Fall through to legacy
	// (which is also idempotent).
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusSucceeded)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire(job.TenantID, job.JobID, `{"stem":"Q","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(legacy.completedCalls) != 1 {
		t.Errorf("legacy UpdateCompleted called %d times; want 1 (already-terminal job falls through)", len(legacy.completedCalls))
	}
}

func TestTerminal_Completed_QuestionJob_RefundError_StillFailsJob(t *testing.T) {
	// A mana-refund transport error is best-effort: logged, NOT NACK'd. The
	// job is already failed + the refund is idempotent on a later retry.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &errMana{err: errors.New("identity gRPC unavailable")}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// Un-normalizable candidate → fail path → refund attempt (which errors).
	bz := completedWire(job.TenantID, job.JobID, `{"stem":"","question_type":"mcq"}`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: refund error must NOT propagate (ACK); got %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	// Failed completion event still emitted with manaRefunded=0 (refund failed).
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 completion event; got %d", len(events))
	}
}

func TestTerminal_Refused_QuestionJob_PublishError_StillFailsJob(t *testing.T) {
	// A generation_completed publish error is best-effort (job row already
	// persisted; FE polls the row). Must NOT NACK.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &errPublisher{err: errors.New("schema reject")}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := refusedWire(job.TenantID, job.JobID, "VALIDATION", "no", 10)
	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: publish error must NOT propagate; got %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund should still fire; got %d", mana.refundCalls)
	}
}

func TestTerminal_Completed_QuestionJob_ZeroMana_NoRefund(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)
	job.ManaCharged = 0 // nothing charged → nothing to refund

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := refusedWire(job.TenantID, job.JobID, "VALIDATION", "no", 0)
	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if mana.refundCalls != 0 {
		t.Errorf("no refund when ManaCharged=0; got %d", mana.refundCalls)
	}
}

func TestTerminal_Completed_QuestionJob_RepoUpdateErr_NACKs(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	qjobs.updateErr = errors.New("db down")
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire(job.TenantID, job.JobID, `{"stem":"Q","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}`, false, 10)
	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected UpdateStatus error to propagate for NACK")
	}
}

func TestTerminal_Completed_QuestionJob_LookupErr_NACKs(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	qjobs.getErr = errors.New("db unreachable") // non-NotFound → transient
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire("t", "assist", `{"x":1}`, false, 0)
	err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz})
	if err == nil {
		t.Fatal("expected transient lookup error to propagate for NACK")
	}
}

func TestTerminal_Completed_QuestionJob_NilJobPub_StillSucceeds(t *testing.T) {
	// emitQuestionCompletion is best-effort — a nil jobPub must not crash the
	// terminal mapping (the FE polls the job row, not the event).
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, nil)

	bz := completedWire(job.TenantID, job.JobID, `{"stem":"Q","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("Status = %q; want succeeded (nil jobPub is tolerated)", job.Status)
	}
}

func TestTerminal_Completed_QuestionJob_FailUpdateErr_StillACKs(t *testing.T) {
	// failQuestionJob's UpdateStatus error is logged, not propagated — the
	// terminal event ACKs (the orchestrator is the source of truth + the
	// FE's GET will surface running until the next reconcile).
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)
	// Get succeeds (resolveQuestionJob), but UpdateStatus errors.
	qjobs.updateErr = errors.New("db write failed")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// Un-normalizable candidate → failQuestionJob path → UpdateStatus errors.
	bz := completedWire(job.TenantID, job.JobID, `{"stem":"","question_type":"mcq"}`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: fail-path UpdateStatus error must be swallowed (ACK); got %v", err)
	}
}

func TestTerminal_Completed_QuestionJobsNotWired_UsesLegacyOnly(t *testing.T) {
	// Without WithQuestionJobs the subscriber behaves exactly as before.
	legacy := &fakeAiAssistJobsRepo{}
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy)
	bz := completedWire("t", "assist", `{"x":1}`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(legacy.completedCalls) != 1 {
		t.Errorf("legacy UpdateCompleted called %d times; want 1", len(legacy.completedCalls))
	}
}

// -----------------------------------------------------------------------------
// EPIC-1a — batch_source_material terminal mapping.
//
// A batch job's completed.v1 carries candidate_payload_json as a JSON ARRAY of
// N AiAssistCandidate objects (the QGenBatchRunner accumulates them). The
// terminal subscriber claims the batch job + persists all N drafts → succeeded.
// An empty array or any un-normalizable element fails the job + refunds.
// -----------------------------------------------------------------------------

func mcqCandidate(stem string) string {
	return `{"stem":"` + stem + `","options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}],"question_type":"mcq"}`
}

func TestTerminal_Completed_BatchJob_ArrayMapsToNDrafts(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	arr := "[" + mcqCandidate("Q1?") + "," + mcqCandidate("Q2?") + "," + mcqCandidate("Q3?") + "]"
	bz := completedWire(job.TenantID, job.JobID, arr, false, 0)

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("batch job Status = %q; want succeeded", job.Status)
	}
	var drafts []json.RawMessage
	if err := json.Unmarshal(job.CandidateQuestionsJSON, &drafts); err != nil {
		t.Fatalf("persisted candidates not a JSON array: %v", err)
	}
	if len(drafts) != 3 {
		t.Errorf("persisted %d drafts; want 3", len(drafts))
	}
	if len(legacy.completedCalls) != 0 {
		t.Errorf("legacy path touched %d times; want 0", len(legacy.completedCalls))
	}
	if mana.refundCalls != 0 {
		t.Errorf("refund fired %d times on a successful batch; want 0", mana.refundCalls)
	}
	events := pub.snapshot()
	if len(events) != 1 || events[0].Topic != "chora.creation.question.generation_completed.v2" {
		t.Fatalf("expected 1 generation_completed event; got %+v", events)
	}
	cb, _ := json.Marshal(events[0].Payload)
	if !strings.Contains(string(cb), `"candidate_count":3`) {
		t.Errorf("completion event candidate_count != 3: %s", cb)
	}
}

func TestTerminal_Completed_BatchJob_EmptyArray_FailsAndRefunds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire(job.TenantID, job.JobID, `[]`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("batch job Status = %q; want failed (empty array)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund fired %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Completed_BatchJob_UnusableElement_FailsAndRefunds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// 2nd element has no options + no model_answer → un-normalizable → fail whole batch.
	arr := "[" + mcqCandidate("Q1?") + `,{"stem":"broken","question_type":"mcq"}]`
	bz := completedWire(job.TenantID, job.JobID, arr, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("batch job Status = %q; want failed (unusable element)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund fired %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Completed_BatchJob_SingleObject_TolerantOneDraft(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// A non-array single object is tolerated as a 1-element batch.
	bz := completedWire(job.TenantID, job.JobID, mcqCandidate("Solo?"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("batch job Status = %q; want succeeded", job.Status)
	}
	var drafts []json.RawMessage
	_ = json.Unmarshal(job.CandidateQuestionsJSON, &drafts)
	if len(drafts) != 1 {
		t.Errorf("persisted %d drafts; want 1", len(drafts))
	}
}

func TestTerminal_Completed_BatchJob_EmptyPayload_FailsAndRefunds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// Empty candidate_payload_json (proto field 11 absent).
	bz := completedWire(job.TenantID, job.JobID, "", false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (empty payload)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund fired %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Completed_BatchJob_ScalarPayload_FailsAndRefunds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// A JSON scalar string — neither array nor object → un-normalizable batch.
	bz := completedWire(job.TenantID, job.JobID, `"not a candidate"`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (scalar payload)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund fired %d times; want 1", mana.refundCalls)
	}
}
