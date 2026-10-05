package pubsub_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// progressWire builds chora.creation.ai_assist.progress.v1 wire bytes (mirrors
// the Python encode_ai_assist_progress field layout 1..9).
func progressWire(tenant, jobID, traceJSON string, stepIndex uint64, stepName string) []byte {
	env := encodeEnvelopeBytes("evt-"+jobID, "ai_assist.progress."+stepName+"."+jobID, tenant, "gcid-"+jobID)
	bz := make([]byte, 0, 256)
	bz = appendBytesField(bz, 1, env)       // envelope
	bz = appendString(bz, 2, jobID)         // assist_id
	bz = appendString(bz, 3, tenant)        // tenant_id (body)
	bz = appendString(bz, 4, "gcid-"+jobID) // author_gcid
	bz = appendString(bz, 5, traceJSON)     // pipeline_trace_json
	bz = appendVarint(bz, 6, stepIndex)     // step_index
	bz = appendString(bz, 7, stepName)      // step_name
	bz = appendString(bz, 8, "COMPLETED")   // step_status
	return bz
}

func TestHandleProgress_AppliesPartialTraceToRunningJob(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	trace := `[{"name":"validate_input","status":"ACCEPTED"},{"name":"generate","status":"COMPLETED"}]`
	bz := progressWire(job.TenantID, job.JobID, trace, 2, "generate")
	if err := sub.HandleProgress(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleProgress: %v", err)
	}

	// The RUNNING job's partial trace is now persisted; status is UNCHANGED.
	if string(job.PipelineTraceJSON) != trace {
		t.Errorf("PipelineTraceJSON = %q; want the partial trace", string(job.PipelineTraceJSON))
	}
	if job.Status != question.JobStatusRunning {
		t.Errorf("status = %q; want running (progress never transitions)", job.Status)
	}
	if len(qjobs.traceUpdates) != 1 || !qjobs.traceUpdates[0].Applied {
		t.Errorf("expected 1 applied trace update, got %+v", qjobs.traceUpdates)
	}
}

func TestHandleProgress_TerminalJob_AcksWithoutApplying(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	// A job already SUCCEEDED — a late/duplicate progress event must NOT clobber
	// the final trace nor regress the status; the handler ACKs (returns nil).
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusSucceeded)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := progressWire(job.TenantID, job.JobID, `[{"name":"generate"}]`, 1, "generate")
	if err := sub.HandleProgress(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleProgress: %v (want ACK/nil for terminal job)", err)
	}
	// resolveQuestionJob rejects non-running → not claimed → repo never called.
	if len(qjobs.traceUpdates) != 0 {
		t.Errorf("terminal job must not be written; got %+v", qjobs.traceUpdates)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Errorf("status = %q; want succeeded (unchanged)", job.Status)
	}
}

func TestHandleProgress_UnknownJob_Acks(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := progressWire("tenant-x", "unknown-job", `[{"name":"generate"}]`, 1, "generate")
	if err := sub.HandleProgress(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: "tenant-x"}}); err != nil {
		t.Fatalf("HandleProgress unknown job: %v (want ACK/nil)", err)
	}
	if len(qjobs.traceUpdates) != 0 {
		t.Errorf("unknown job must not be written; got %+v", qjobs.traceUpdates)
	}
}
