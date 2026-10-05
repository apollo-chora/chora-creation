package pubsub_test

// CHO-2398 (ADR-251 D5): the chunk_completed.v1 consumer half.
//
// HandleChunkCompleted persists one chunk's published-shape candidates onto
// the RUNNING question job, idempotently and slot-keyed by chunk_index (a
// duplicate or out-of-order delivery contributes nothing; the SQL slot guard
// owns idempotence, not racy Go read-then-write). A terminal or unknown job
// ACKs (the terminal completed.v1 is authoritative); a candidate_count
// mismatch against the decoded payload NACKs fail-loud (never a partial
// apply); a transient repo error NACKs.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// chunkWire builds chora.creation.ai_assist.chunk_completed.v1 wire bytes
// (mirrors the Python encode_ai_assist_chunk_completed field layout 1..14).
func chunkWire(
	tenant, jobID string,
	chunkIndex, chunkCount uint64,
	candidatesJSON string,
	candidateCount uint64,
) []byte {
	env := encodeEnvelopeBytes(
		"evt-"+jobID,
		"ai_assist.chunk_completed.0."+jobID,
		tenant,
		"gcid-"+jobID,
	)
	bz := make([]byte, 0, 256)
	bz = appendBytesField(bz, 1, env)        // envelope
	bz = appendString(bz, 2, jobID)          // assist_id
	bz = appendString(bz, 3, tenant)         // tenant_id (body)
	bz = appendString(bz, 4, "gcid-"+jobID)  // author_gcid
	bz = appendVarint(bz, 5, chunkIndex)     // chunk_index
	bz = appendVarint(bz, 6, chunkCount)     // chunk_count
	bz = appendString(bz, 7, candidatesJSON) // candidates_payload_json
	bz = appendVarint(bz, 8, candidateCount) // candidate_count
	bz = appendVarint(bz, 10, 1)             // warned_count
	bz = appendVarint(bz, 11, 2)             // images_rendered
	bz = appendVarint(bz, 14, 3)             // images_skipped
	return bz
}

func TestHandleChunkCompleted_AppliesSlotToRunningJob(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	cands := `[{"stem":"a"},{"stem":"b","quality_warning":true,"critic_notes":"n"}]`
	bz := chunkWire(job.TenantID, job.JobID, 0, 2, cands, 2)
	if err := sub.HandleChunkCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleChunkCompleted: %v", err)
	}

	if len(qjobs.chunkApplies) != 1 {
		t.Fatalf("expected 1 chunk apply, got %+v", qjobs.chunkApplies)
	}
	got := qjobs.chunkApplies[0]
	if got.ChunkIndex != 0 || got.ChunkCount != 2 {
		t.Errorf("chunk indices = %d/%d; want 0/2", got.ChunkIndex, got.ChunkCount)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(got.CandidatesJSON, &parsed); err != nil || len(parsed) != 2 {
		t.Errorf("candidates round-trip failed: %v %v", err, parsed)
	}
	if job.Status != question.JobStatusRunning {
		t.Errorf("status = %q; want running (chunk apply never transitions)", job.Status)
	}
}

func TestHandleChunkCompleted_TerminalJob_AcksWithoutApplying(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusSucceeded)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := chunkWire(job.TenantID, job.JobID, 0, 2, `[{"stem":"a"}]`, 1)
	if err := sub.HandleChunkCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleChunkCompleted: %v (want ACK/nil for terminal job)", err)
	}
	if len(qjobs.chunkApplies) != 0 {
		t.Errorf("terminal job must not be written; got %+v", qjobs.chunkApplies)
	}
}

func TestHandleChunkCompleted_CandidateCountMismatch_Nacks(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// Payload says 3 candidates but carries 1 - fail-loud, never partial apply.
	bz := chunkWire(job.TenantID, job.JobID, 0, 2, `[{"stem":"a"}]`, 3)
	err := sub.HandleChunkCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}})
	if err == nil {
		t.Fatalf("expected candidate_count mismatch to NACK (non-nil error)")
	}
	if !strings.Contains(err.Error(), "candidate_count") {
		t.Errorf("error must name the mismatch, got: %v", err)
	}
	if len(qjobs.chunkApplies) != 0 {
		t.Errorf("mismatched chunk must not be applied; got %+v", qjobs.chunkApplies)
	}
}

func TestHandleChunkCompleted_DuplicateSlot_Acks(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)
	qjobs.chunkApplyResult = false // SQL slot guard: already applied

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := chunkWire(job.TenantID, job.JobID, 1, 2, `[{"stem":"a"}]`, 1)
	if err := sub.HandleChunkCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("duplicate slot must ACK, got: %v", err)
	}
	if len(qjobs.chunkApplies) != 1 {
		t.Fatalf("the guarded apply is still attempted exactly once, got %+v", qjobs.chunkApplies)
	}
}

func TestHandleChunkCompleted_UnknownJob_Acks(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := chunkWire("11111111-1111-7111-8111-111111111111", "no-such-job", 0, 1, `[]`, 0)
	if err := sub.HandleChunkCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: "11111111-1111-7111-8111-111111111111"}}); err != nil {
		t.Fatalf("unknown job must ACK, got: %v", err)
	}
	if len(qjobs.chunkApplies) != 0 {
		t.Errorf("unknown job must not be written; got %+v", qjobs.chunkApplies)
	}
}
