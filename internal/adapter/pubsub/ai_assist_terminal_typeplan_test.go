// ai_assist_terminal_typeplan_test.go — RED→GREEN coverage for the CHO-1819 P2
// mixed-batch projection: the ai_assist completed.v1 subscriber decodes the
// GenerationSummary (field 16) and persists it onto the question-job's
// generation_summary_jsonb so the FE poll response can render a shortfall
// banner. ABSENT summary (today's orchestrator) leaves the column untouched.
package pubsub_test

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// genSummaryBytes encodes the nested GenerationSummary submessage
// {1: requested_total, 2: generated_total, 3: map<string,int32>, 4: shortfall}.
func genSummaryBytes(requested, generated int32, perType map[string]int32, shortfall string) []byte {
	out := make([]byte, 0, 64)
	if requested != 0 {
		out = appendVarint(out, 1, uint64(uint32(requested)))
	}
	if generated != 0 {
		out = appendVarint(out, 2, uint64(uint32(generated)))
	}
	for _, k := range []string{"mcq", "oe"} {
		if v, ok := perType[k]; ok {
			entry := appendString(nil, 1, k)
			entry = appendVarint(entry, 2, uint64(uint32(v)))
			out = appendBytesField(out, 3, entry)
		}
	}
	if shortfall != "" {
		out = appendString(out, 4, shortfall)
	}
	return out
}

func TestTerminalP2_GenerationSummary_PersistedOnQuestionJob(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	payload := `[` + mcqCandidateJSONWithID("d1", "Q1?", `[]`) + `,` + mcqCandidateJSONWithID("d2", "Q2?", `[]`) + `]`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	bz = appendBytesField(bz, 16, genSummaryBytes(10, 9, map[string]int32{"mcq": 7, "oe": 2}, "model_refused_one_oe"))

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("status = %q; want succeeded", job.Status)
	}
	if len(job.GenerationSummaryJSON) == 0 {
		t.Fatal("generation_summary_jsonb was not persisted")
	}
	var got struct {
		RequestedTotal   int32            `json:"requested_total"`
		GeneratedTotal   int32            `json:"generated_total"`
		GeneratedPerType map[string]int32 `json:"generated_per_type"`
		ShortfallReason  string           `json:"shortfall_reason"`
	}
	if err := json.Unmarshal(job.GenerationSummaryJSON, &got); err != nil {
		t.Fatalf("generation_summary_jsonb not valid JSON: %v (%s)", err, job.GenerationSummaryJSON)
	}
	if got.RequestedTotal != 10 || got.GeneratedTotal != 9 {
		t.Errorf("totals = %d/%d; want 10/9", got.RequestedTotal, got.GeneratedTotal)
	}
	if got.ShortfallReason != "model_refused_one_oe" {
		t.Errorf("shortfall_reason = %q; want model_refused_one_oe", got.ShortfallReason)
	}
	if got.GeneratedPerType["mcq"] != 7 || got.GeneratedPerType["oe"] != 2 {
		t.Errorf("generated_per_type = %v; want mcq=7 oe=2", got.GeneratedPerType)
	}
}

func TestTerminalP2_NoGenerationSummary_LeavesColumnEmpty(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// No field 16 — today's orchestrator. The column must stay empty.
	payload := `[` + mcqCandidateJSONWithID("d1", "Q1?", `[]`) + `]`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("status = %q; want succeeded", job.Status)
	}
	if len(job.GenerationSummaryJSON) != 0 {
		t.Errorf("generation_summary_jsonb = %s; want empty when field 16 absent", job.GenerationSummaryJSON)
	}
}

// CHO-1826 Gap #4 — the per-step pipeline trace (field 12 pipeline_trace_json)
// rides the SAME completed.v1 the single-mode AI-Assist drawer uses, and must
// now persist onto the question-job's pipeline_trace_jsonb so the canvas can
// render the transparency card. Previously decoded-but-dropped.
func TestTerminalGap4_PipelineTrace_PersistedOnQuestionJob(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	trace := `[{"name":"validator","status":"completed"},{"name":"generator","status":"completed","output_tokens":128}]`
	payload := `[` + mcqCandidateJSONWithID("d1", "Q1?", `[]`) + `]`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	bz = appendString(bz, 12, trace) // field 12 = pipeline_trace_json

	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("status = %q; want succeeded", job.Status)
	}
	if string(job.PipelineTraceJSON) != trace {
		t.Errorf("pipeline_trace_jsonb = %s; want %s", job.PipelineTraceJSON, trace)
	}
}

func TestTerminalGap4_NoPipelineTrace_LeavesColumnEmpty(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// No field 12 — the column must stay empty (refused/legacy parity).
	payload := `[` + mcqCandidateJSONWithID("d1", "Q1?", `[]`) + `]`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(job.PipelineTraceJSON) != 0 {
		t.Errorf("pipeline_trace_jsonb = %s; want empty when field 12 absent", job.PipelineTraceJSON)
	}
}

// keep protowire import live for the wire-builder helpers shared in-package.
var _ = protowire.Number(16)
