// ai_assist_subscriber_test.go — RED→GREEN coverage for the qgen 2-agent
// crew terminal-events subscriber per docs/m13/ack-oe-ai-assist-plan-
// 2026-05-17.md Step 4d.
package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
)

// -----------------------------------------------------------------------------
// Fake AiAssistJobsRepository — captures method calls + return errors
// -----------------------------------------------------------------------------

type fakeAiAssistJobsRepoForSub struct {
	mu                 sync.Mutex
	completedCalls     []completedCall
	refusedCalls       []refusedCall
	failedCalls        []failedCall
	updateCompletedErr error
	updateRefusedErr   error
}

type completedCall struct {
	tenantID, jobID              string
	resultPayload, pipelineTrace []byte
	qualityWarning               bool
	attemptCount, manaCharged    int
}

type refusedCall struct {
	tenantID, jobID                     string
	reason                              aiassist.RefusalReason
	armorVerdict, userFacingMsg         string
	lastCandidatePayload, pipelineTrace []byte
	attemptCount, manaCharged           int
}

type failedCall struct {
	tenantID, jobID, errMsg   string
	pipelineTrace             []byte
	attemptCount, manaCharged int
}

func (f *fakeAiAssistJobsRepoForSub) Create(_ context.Context, _ *aiassist.Job) error {
	return nil
}

func (f *fakeAiAssistJobsRepoForSub) Get(_ context.Context, _, _ string) (*aiassist.Job, error) {
	return nil, aiassist.ErrNotFound
}

func (f *fakeAiAssistJobsRepoForSub) UpdateCompleted(
	_ context.Context, tenantID, jobID string,
	resultPayload, pipelineTrace []byte,
	qualityWarning bool, attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateCompletedErr != nil {
		return f.updateCompletedErr
	}
	f.completedCalls = append(f.completedCalls, completedCall{
		tenantID: tenantID, jobID: jobID,
		resultPayload: resultPayload, pipelineTrace: pipelineTrace,
		qualityWarning: qualityWarning,
		attemptCount:   attemptCount, manaCharged: manaCharged,
	})
	return nil
}

func (f *fakeAiAssistJobsRepoForSub) UpdateRefused(
	_ context.Context, tenantID, jobID string,
	reason aiassist.RefusalReason,
	armorVerdict, userFacingMsg string,
	lastCandidatePayload, pipelineTrace []byte,
	attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateRefusedErr != nil {
		return f.updateRefusedErr
	}
	f.refusedCalls = append(f.refusedCalls, refusedCall{
		tenantID: tenantID, jobID: jobID,
		reason:       reason,
		armorVerdict: armorVerdict, userFacingMsg: userFacingMsg,
		lastCandidatePayload: lastCandidatePayload, pipelineTrace: pipelineTrace,
		attemptCount: attemptCount, manaCharged: manaCharged,
	})
	return nil
}

func (f *fakeAiAssistJobsRepoForSub) UpdateFailed(
	_ context.Context, tenantID, jobID string,
	pipelineTrace []byte, errMsg string,
	attemptCount, manaCharged int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failedCalls = append(f.failedCalls, failedCall{
		tenantID: tenantID, jobID: jobID, errMsg: errMsg,
		pipelineTrace: pipelineTrace,
		attemptCount:  attemptCount, manaCharged: manaCharged,
	})
	return nil
}

// -----------------------------------------------------------------------------
// HandleCompleted tests
// -----------------------------------------------------------------------------

func TestHandleCompleted_HappyPath(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID:             "job-1",
		TenantID:             "tenant-1",
		CandidatePayloadJSON: `{"stem":"x","oe_payload":{}}`,
		PipelineTraceJSON:    `[{"name":"generate","status":"COMPLETED"}]`,
		QualityWarning:       false,
		AttemptCount:         1,
		ManaCharged:          10,
	})
	if err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Fatalf("expected 1 UpdateCompleted call; got %d", len(repo.completedCalls))
	}
	c := repo.completedCalls[0]
	if c.tenantID != "tenant-1" || c.jobID != "job-1" {
		t.Errorf("UpdateCompleted args wrong: %+v", c)
	}
	if string(c.resultPayload) != `{"stem":"x","oe_payload":{}}` {
		t.Errorf("result_payload not forwarded verbatim")
	}
	if c.attemptCount != 1 || c.manaCharged != 10 {
		t.Errorf("attempt_count / mana_charged not forwarded")
	}
}

func TestHandleCompleted_Idempotent(t *testing.T) {
	// Same assist_id delivered twice — UpdateCompleted called ONCE.
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	payload := AiAssistCompletedPayload{
		AssistID: "job-1", TenantID: "tenant-1", AttemptCount: 1,
	}
	if err := sub.HandleCompleted(context.Background(), payload); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sub.HandleCompleted(context.Background(), payload); err != nil {
		t.Fatalf("second (should ACK as dedupe-hit): %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Errorf("expected 1 call (idempotent); got %d", len(repo.completedCalls))
	}
}

func TestHandleCompleted_QualityWarningForwarded(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	_ = sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID: "job-x", TenantID: "tenant-x",
		QualityWarning: true,
		AttemptCount:   4,
	})
	if len(repo.completedCalls) != 1 || !repo.completedCalls[0].qualityWarning {
		t.Errorf("quality_warning not forwarded to UpdateCompleted")
	}
}

func TestHandleCompleted_UnknownJobAcks(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{updateCompletedErr: aiassist.ErrNotFound}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	// ErrNotFound should ACK (not NACK).
	err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID: "job-unknown", TenantID: "tenant-x",
	})
	if err != nil {
		t.Errorf("ErrNotFound should ACK; got err=%v", err)
	}
}

func TestHandleCompleted_TransientErrorPropagates(t *testing.T) {
	// Non-ErrNotFound errors must propagate so Pub/Sub NACKs + retries.
	repo := &fakeAiAssistJobsRepoForSub{updateCompletedErr: errors.New("conn lost")}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID: "job-1", TenantID: "tenant-1",
	})
	if err == nil {
		t.Errorf("transient error should NACK; got nil")
	}
	// Important: the inbox MUST NOT claim the key on fn error — retry
	// must re-run the fn. Demonstrate by clearing the err + retrying:
	// since the key was never claimed, the 2nd call should also run.
	repo.updateCompletedErr = nil
	if err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID: "job-1", TenantID: "tenant-1",
	}); err != nil {
		t.Fatalf("retry after transient: %v", err)
	}
	if len(repo.completedCalls) != 1 {
		t.Errorf("expected exactly 1 successful UpdateCompleted; got %d", len(repo.completedCalls))
	}
}

func TestHandleCompleted_RejectsMissingIDs(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	if err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{TenantID: "x"}); err == nil {
		t.Errorf("expected error on missing assist_id")
	}
	if err := sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{AssistID: "x"}); err == nil {
		t.Errorf("expected error on missing tenant_id")
	}
}

// -----------------------------------------------------------------------------
// HandleRefused tests
// -----------------------------------------------------------------------------

func TestHandleRefused_HappyPath(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	err := sub.HandleRefused(context.Background(), AiAssistRefusedPayload{
		AssistID:                 "job-r",
		TenantID:                 "tenant-r",
		RefusalReason:            "GUARDRAIL_PRE",
		ModelArmorVerdict:        "armor:pii_high_risk_block",
		UserFacingMessage:        "Your prompt couldn't be processed.",
		LastCandidatePayloadJSON: "",
		AttemptCount:             0,
	})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if len(repo.refusedCalls) != 1 {
		t.Fatalf("expected 1 UpdateRefused call; got %d", len(repo.refusedCalls))
	}
	c := repo.refusedCalls[0]
	if c.tenantID != "tenant-r" || c.jobID != "job-r" {
		t.Errorf("UpdateRefused args wrong: %+v", c)
	}
	if c.reason != aiassist.RefusalReasonGuardrailPre {
		t.Errorf("reason = %q; want GUARDRAIL_PRE", c.reason)
	}
	if c.armorVerdict != "armor:pii_high_risk_block" {
		t.Errorf("armor_verdict not forwarded")
	}
}

func TestHandleRefused_RejectsEmptyReason(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	err := sub.HandleRefused(context.Background(), AiAssistRefusedPayload{
		AssistID: "job-1", TenantID: "tenant-1", RefusalReason: "",
	})
	if err == nil {
		t.Errorf("empty refusal_reason should error")
	}
	if len(repo.refusedCalls) != 0 {
		t.Errorf("repo should not be touched on validation failure")
	}
}

func TestHandleRefused_RejectsInvalidReason(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	err := sub.HandleRefused(context.Background(), AiAssistRefusedPayload{
		AssistID: "job-1", TenantID: "tenant-1", RefusalReason: "POLICY",
	})
	if err == nil {
		t.Errorf("invalid refusal_reason should error")
	}
}

func TestHandleRefused_Idempotent(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore())
	payload := AiAssistRefusedPayload{
		AssistID: "job-r", TenantID: "tenant-r",
		RefusalReason: "GUARDRAIL_POST",
	}
	_ = sub.HandleRefused(context.Background(), payload)
	_ = sub.HandleRefused(context.Background(), payload)
	if len(repo.refusedCalls) != 1 {
		t.Errorf("expected 1 call (idempotent); got %d", len(repo.refusedCalls))
	}
}

// -----------------------------------------------------------------------------
// Inbox TTL override
// -----------------------------------------------------------------------------

func TestSubscriber_WithInboxTTL(t *testing.T) {
	repo := &fakeAiAssistJobsRepoForSub{}
	sub := NewAiAssistSubscriber(repo, idempotent.NewMemoryStore()).
		WithInboxTTL(5 * time.Minute)
	_ = sub.HandleCompleted(context.Background(), AiAssistCompletedPayload{
		AssistID: "j", TenantID: "t",
	})
	if sub.ttl != 5*time.Minute {
		t.Errorf("ttl override not applied: %v", sub.ttl)
	}
}
