// ai_assist_terminal_1c_test.go — RED→GREEN coverage for the Lane 1c
// (CHO-1703 / ADR-180 D15) verification pass in the ai_assist completed
// subscriber:
//
//   - candidate_payload_json first non-space char `[` → legacy bare array
//     (no proposal); `{` → BatchCandidatePayload{candidates,
//     proposed_test_set}.
//   - per-candidate citations {source_file, page, excerpt} are matched
//     against the job's source_material_chunks (normalised substring →
//     fuzzy ≥0.8) and stamped verified/chunk_id BEFORE the FE poll.
//   - image-source citations stay verified=null (AI-reported); unverified
//     citations are flagged false, NEVER dropped.
//   - the proposal persists into proposed_test_set_jsonb.
package pubsub_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

// fakeTermChunkRepo serves ListByJob for the verification pass.
type fakeTermChunkRepo struct {
	mu     sync.Mutex
	chunks []sourcechunk.Chunk
}

func (f *fakeTermChunkRepo) InsertChunks(_ context.Context, chunks []sourcechunk.Chunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, chunks...)
	return nil
}

func (f *fakeTermChunkRepo) ListByJob(_ context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sourcechunk.Chunk
	for _, c := range f.chunks {
		if c.TenantID == tenantID && c.JobID == jobID {
			out = append(out, c)
		}
	}
	return out, nil
}

const (
	mdURI  = "gs://b/tenants/t/jobs/j/source-1"
	pngURI = "gs://b/tenants/t/jobs/j/source-2"
)

func seedBatchJobWithFiles(repo *fakeSubJobRepo) *question.ComposeJob {
	j := seedQuestionJob(repo, tjBatchSourceMaterial, question.JobStatusRunning)
	j.SettingsJSON = []byte(`{"count":2,"source_files":[` +
		`{"blob_uri":"` + mdURI + `","mime_type":"text/markdown","role":"source","filename":"notes.md"},` +
		`{"blob_uri":"` + pngURI + `","mime_type":"image/png","role":"source","filename":"diagram.png"}]}`)
	return j
}

func chunkFor(job *question.ComposeJob, id, uri, text string) sourcechunk.Chunk {
	return sourcechunk.Chunk{
		ChunkID: id, JobID: job.JobID, TenantID: job.TenantID,
		FileURI: uri, FileRole: sourcechunk.RoleSource,
		ChunkIndex: 0, Text: text, CreatedAt: time.Now().UTC(),
	}
}

// mcqCandidateJSON builds one normalizable MCQ candidate with citations.
func mcqCandidateJSON(stem string, citations string) string {
	return mcqCandidateJSONWithID("", stem, citations)
}

// mcqCandidateJSONWithID — the W2 orchestrator stamps a DETERMINISTIC
// draft_id (uuid5 of {assist_id}:{i}) and keys proposed_test_set.order[] +
// points{} by it; the parser MUST preserve it (mint only when absent).
func mcqCandidateJSONWithID(draftID, stem, citations string) string {
	id := ""
	if draftID != "" {
		id = `"draft_id":"` + draftID + `",`
	}
	return `{` + id + `"stem":"` + stem + `","question_type":"mcq","options":[` +
		`{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},` +
		`{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}]` +
		`,"citations":` + citations + `}`
}

func decodeDrafts(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var drafts []map[string]any
	if err := json.Unmarshal(raw, &drafts); err != nil {
		t.Fatalf("drafts unmarshal: %v (%s)", err, raw)
	}
	return drafts
}

func draftCitations(t *testing.T, draft map[string]any) []map[string]any {
	t.Helper()
	raw, ok := draft["citations"].([]any)
	if !ok {
		t.Fatalf("draft has no citations: %v", draft)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestTerminal1c_BatchObjectPayload_VerifiesCitations_StoresProposal(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	chunks := &fakeTermChunkRepo{}
	job := seedBatchJobWithFiles(qjobs)
	chunks.chunks = append(chunks.chunks,
		chunkFor(job, "chunk-md-1", mdURI, "Photosynthesis converts light energy into chemical energy stored in glucose."),
	)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{}).
		WithChunkStore(chunks)

	// Candidate 1 cites the md chunk verbatim; candidate 2 cites the image.
	// draft_ids are orchestrator-stamped (deterministic) — the proposal's
	// order[]/points{} key on them, so the parser MUST preserve them.
	payload := `{"candidates":[` +
		mcqCandidateJSONWithID("draft-det-1", "Q1?", `[{"source_file":"`+mdURI+`","page":null,"excerpt":"Photosynthesis converts LIGHT energy into chemical energy"}]`) + `,` +
		mcqCandidateJSONWithID("draft-det-2", "Q2?", `[{"source_file":"`+pngURI+`","page":null,"excerpt":"a label read off the diagram"}]`) +
		`],"proposed_test_set":{"title":"Cell Energy Quiz","description":"From notes.md","order":["draft-det-2","draft-det-1"],"points":{"draft-det-1":5,"draft-det-2":10}}}`

	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: job.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("job status = %q; want succeeded", job.Status)
	}

	drafts := decodeDrafts(t, job.CandidateQuestionsJSON)
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d; want 2", len(drafts))
	}
	// W2 cross-agent contract: orchestrator-stamped draft_ids preserved
	// verbatim (random re-minting would orphan the proposal's keys).
	if drafts[0]["draft_id"] != "draft-det-1" || drafts[1]["draft_id"] != "draft-det-2" {
		t.Errorf("draft_ids = %v / %v; want draft-det-1 / draft-det-2 preserved",
			drafts[0]["draft_id"], drafts[1]["draft_id"])
	}

	// Candidate 1: verified true + chunk_id stamped.
	c1 := draftCitations(t, drafts[0])
	if len(c1) != 1 {
		t.Fatalf("draft0 citations = %d; want 1", len(c1))
	}
	if v, ok := c1[0]["verified"].(bool); !ok || !v {
		t.Errorf("draft0 citation verified = %v; want true", c1[0]["verified"])
	}
	if c1[0]["chunk_id"] != "chunk-md-1" {
		t.Errorf("draft0 citation chunk_id = %v; want chunk-md-1", c1[0]["chunk_id"])
	}

	// Candidate 2: image source → verified stays null/absent (AI-reported).
	c2 := draftCitations(t, drafts[1])
	if _, present := c2[0]["verified"]; present && c2[0]["verified"] != nil {
		t.Errorf("draft1 citation verified = %v; want null/absent for image source", c2[0]["verified"])
	}
	// Excerpt preserved verbatim — citations are NEVER dropped.
	if c2[0]["excerpt"] != "a label read off the diagram" {
		t.Errorf("draft1 citation excerpt mutated: %v", c2[0]["excerpt"])
	}

	// Proposal persisted.
	if !strings.Contains(string(job.ProposedTestSetJSON), "Cell Energy Quiz") {
		t.Errorf("ProposedTestSetJSON = %s; want the composer proposal", job.ProposedTestSetJSON)
	}
}

func TestTerminal1c_UnverifiedCitation_FlaggedFalse_NotDropped(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	chunks := &fakeTermChunkRepo{}
	job := seedBatchJobWithFiles(qjobs)
	chunks.chunks = append(chunks.chunks,
		chunkFor(job, "chunk-md-1", mdURI, "Photosynthesis converts light energy."),
	)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{}).
		WithChunkStore(chunks)

	payload := `{"candidates":[` +
		mcqCandidateJSON("Q?", `[{"source_file":"`+mdURI+`","page":null,"excerpt":"quantum entanglement of superconducting qubits"}]`) +
		`]}`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	drafts := decodeDrafts(t, job.CandidateQuestionsJSON)
	cits := draftCitations(t, drafts[0])
	if len(cits) != 1 {
		t.Fatalf("citations = %d; want 1 (never dropped)", len(cits))
	}
	if v, ok := cits[0]["verified"].(bool); !ok || v {
		t.Errorf("verified = %v; want false (hallucination suspect, surfaced)", cits[0]["verified"])
	}
}

func TestTerminal1c_LegacyArrayPayload_NoProposal_StillSucceeds(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	payload := `[` + mcqCandidateJSONWithID("det-a", "Q1?", `[]`) + `,` + mcqCandidateJSON("Q2?", `[]`) + `]`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusSucceeded {
		t.Fatalf("status = %q; want succeeded", job.Status)
	}
	if len(job.ProposedTestSetJSON) != 0 {
		t.Errorf("ProposedTestSetJSON = %s; want empty for legacy array", job.ProposedTestSetJSON)
	}
	drafts := decodeDrafts(t, job.CandidateQuestionsJSON)
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d; want 2", len(drafts))
	}
	// draft_id preservation applies on BOTH branches: supplied id kept
	// verbatim; absent id minted (non-empty).
	if drafts[0]["draft_id"] != "det-a" {
		t.Errorf("legacy-array draft_id = %v; want det-a preserved", drafts[0]["draft_id"])
	}
	if id, _ := drafts[1]["draft_id"].(string); id == "" {
		t.Error("absent draft_id must be minted, got empty")
	}
}

func TestTerminal1c_NoChunkStoreWired_CitationsPassThroughUnstamped(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedBatchJobWithFiles(qjobs)

	// NO WithChunkStore — verification must degrade to pass-through.
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	payload := `{"candidates":[` +
		mcqCandidateJSON("Q?", `[{"source_file":"`+mdURI+`","page":1,"excerpt":"some excerpt"}]`) +
		`],"proposed_test_set":{"title":"T","order":[]}}`
	bz := completedWire(job.TenantID, job.JobID, payload, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	drafts := decodeDrafts(t, job.CandidateQuestionsJSON)
	cits := draftCitations(t, drafts[0])
	if _, present := cits[0]["verified"]; present && cits[0]["verified"] != nil {
		t.Errorf("verified = %v; want null/absent when no chunk store is wired", cits[0]["verified"])
	}
	// Proposal still persists (independent of verification).
	if !strings.Contains(string(job.ProposedTestSetJSON), `"T"`) {
		t.Errorf("proposal missing: %s", job.ProposedTestSetJSON)
	}
}

func TestTerminal1c_BatchObjectWithEmptyCandidates_FailsJob(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	job := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusRunning)

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).
		WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(job.TenantID, job.JobID, `{"candidates":[]}`, false, 50)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v (empty batch should fail the job, not NACK)", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("status = %q; want failed for empty candidates", job.Status)
	}
}
