// question_jobs_accept_1c_test.go — RED→GREEN coverage for the Lane 1c W3
// accept path (CHO-1703 / ADR-180 D1/D5/D10):
//
//   - accept request gains an OPTIONAL `test_set` block; ABSENT ⇒ today's
//     path bit-identical (regression-pinned: no batch event published).
//   - PRESENT ⇒ after atom persistence chora-creation publishes
//     chora.creation.question_batch.accepted.v1 with the author-curated
//     title/desc + items in FINAL curated order (points: item override →
//     proposal → 10; display_order: override → proposal → submission order;
//     contiguous 1-based) + source_files from job settings.
//   - exactly-once per job: only the accept that wins the
//     succeeded→accepted transition publishes; a retry 409s and publishes
//     nothing.
//   - test_set is INPUT-AGNOSTIC (ADR-195 D4): valid on ANY compose job with
//     accepted candidates (the batch-only gate is retired). Still rejected on
//     empty accepted_candidates (400), unknown item draft_id (400), out-of-range
//     points (400), publisher not wired (503, BEFORE any persistence).
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// fakeBatchAcceptedPublisher records PublishQuestionBatchAccepted calls.
type fakeBatchAcceptedPublisher struct {
	mu     sync.Mutex
	events []ports.QuestionBatchAcceptedEvent
	err    error
}

func (f *fakeBatchAcceptedPublisher) PublishQuestionBatchAccepted(_ context.Context, evt ports.QuestionBatchAcceptedEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, evt)
	return nil
}

func (f *fakeBatchAcceptedPublisher) snapshot() []ports.QuestionBatchAcceptedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ports.QuestionBatchAcceptedEvent, len(f.events))
	copy(out, f.events)
	return out
}

// newAcceptServer seeds a SUCCEEDED batch job with 3 candidates + a
// composer proposal and returns the wired router.
func newAcceptServer(t *testing.T, batchPub ports.QuestionBatchAcceptedPublisher) (http.Handler, *fakeJobRepo, *fakeJobPublisher, string, string) {
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
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}

	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-00000000ee01", AtomID: a.AtomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		ManaActionCode: "question_authoring_batch_parse", ManaCharged: 50,
		SettingsJSON: []byte(`{"count":3,"source_files":[` +
			`{"blob_uri":"gs://b/t/j/source-1","mime_type":"application/pdf","role":"source","filename":"exam.pdf"},` +
			`{"blob_uri":"gs://b/t/j/rubric","mime_type":"text/plain","role":"rubric","filename":"marks.txt"}]}`),
		CandidateQuestionsJSON: []byte(`[` +
			`{"draft_id":"d1","type":"mcq","prompt":"Q1?","mcq_payload":{"options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}]}},` +
			`{"draft_id":"d2","type":"mcq","prompt":"Q2?","mcq_payload":{"options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}]}},` +
			`{"draft_id":"d3","type":"mcq","prompt":"Q3?","mcq_payload":{"options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}]}}]`),
		ProposedTestSetJSON: []byte(`{"title":"Proposed Quiz","description":"llm","order":["d2","d1","d3"],"points":{"d1":5,"d2":8}}`),
	}
	jobRepo.jobs[job.JobID] = job

	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                           atomRepo,
		QuestionRepository:             qRepo,
		QuestionJobRepository:          jobRepo,
		ManaLedger:                     &fakeManaLedger{successDebits: 10},
		JobEventPublisher:              pub,
		QuestionBatchAcceptedPublisher: batchPub,
	})
	return router, jobRepo, pub, a.AtomID, job.JobID
}

func postAccept(t *testing.T, srv http.Handler, atomID, jobID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	bz, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs/"+jobID+"/accept", bytes.NewReader(bz))
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(w, r)
	return w
}

func acceptAll() []map[string]any {
	return []map[string]any{
		{"draft_id": "d1"}, {"draft_id": "d2"}, {"draft_id": "d3"},
	}
}

// -----------------------------------------------------------------------------
// Regression — absent test_set ⇒ bit-identical (no batch event)
// -----------------------------------------------------------------------------

func TestAccept1c_NoTestSet_BitIdentical_NoBatchEvent(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, jobRepo, _, atomID, jobID := newAcceptServer(t, batchPub)

	w := postAccept(t, srv, atomID, jobID, map[string]any{"accepted_candidates": acceptAll()})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(batchPub.snapshot()) != 0 {
		t.Errorf("batch event published WITHOUT test_set — must be bit-identical to pre-1c")
	}
	if jobRepo.jobs[jobID].Status != question.JobStatusAccepted {
		t.Errorf("job status = %q; want accepted", jobRepo.jobs[jobID].Status)
	}
}

// -----------------------------------------------------------------------------
// Happy path — curated test_set publishes ONE event with resolved items
// -----------------------------------------------------------------------------

func TestAccept1c_TestSet_PublishesCuratedEvent(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, jobRepo, _, atomID, jobID := newAcceptServer(t, batchPub)

	// Author curation: d3 pinned to position 1 with 20 points; d1/d2 fall
	// back to the proposal (order d2 then d1; points d1=5, d2=8).
	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set": map[string]any{
			"title":       "Final Quiz",
			"description": "curated",
			"items": []map[string]any{
				{"draft_id": "d3", "points": 20, "display_order": 1},
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}

	events := batchPub.snapshot()
	if len(events) != 1 {
		t.Fatalf("batch events = %d; want exactly 1", len(events))
	}
	evt := events[0]
	if evt.JobID != jobID || evt.HostAtomID != atomID || evt.TenantID != tenantA || evt.AuthorGCID != gcidA {
		t.Errorf("event identity = %+v", evt)
	}
	if evt.TestSetTitle != "Final Quiz" || evt.TestSetDescription != "curated" {
		t.Errorf("test_set = %q/%q", evt.TestSetTitle, evt.TestSetDescription)
	}
	if len(evt.Items) != 3 {
		t.Fatalf("items = %d; want 3", len(evt.Items))
	}
	// Final curated order: d3 (override 1) → d2 (proposal pos 1) → d1
	// (proposal pos 2). Points are distinctive per draft (d3 override 20,
	// d2 proposal 8, d1 proposal 5) so they pin the ordering.
	wantPoints := []int{20, 8, 5}
	for i, item := range evt.Items {
		if item.DisplayOrder != i+1 {
			t.Errorf("items[%d].DisplayOrder = %d; want %d (contiguous 1-based)", i, item.DisplayOrder, i+1)
		}
		if item.Points != wantPoints[i] {
			t.Errorf("items[%d].Points = %d; want %d", i, item.Points, wantPoints[i])
		}
		if item.QuestionAtomID == "" || item.QuestionID == "" {
			t.Errorf("items[%d] missing atom/question ids: %+v", i, item)
		}
		if item.QuestionType != "mcq" {
			t.Errorf("items[%d].QuestionType = %q", i, item.QuestionType)
		}
	}
	// source_files provenance from job settings (role-tagged, no filename).
	if len(evt.SourceFiles) != 2 {
		t.Fatalf("source_files = %d; want 2", len(evt.SourceFiles))
	}
	if evt.SourceFiles[0].BlobURI != "gs://b/t/j/source-1" || evt.SourceFiles[0].Role != "source" {
		t.Errorf("source_files[0] = %+v", evt.SourceFiles[0])
	}
	if evt.SourceFiles[1].Role != "rubric" {
		t.Errorf("source_files[1] = %+v", evt.SourceFiles[1])
	}
	if evt.AcceptedAt.IsZero() {
		t.Error("AcceptedAt zero")
	}
	if jobRepo.jobs[jobID].Status != question.JobStatusAccepted {
		t.Errorf("job status = %q; want accepted", jobRepo.jobs[jobID].Status)
	}
}

func TestAccept1c_PointsDefault10_WhenNoOverrideNoProposal(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, _, _, atomID, jobID := newAcceptServer(t, batchPub)

	// d3 has NO override and NO proposal points → default 10.
	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": []map[string]any{{"draft_id": "d3"}},
		"test_set":            map[string]any{"title": "Solo"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	events := batchPub.snapshot()
	if len(events) != 1 || len(events[0].Items) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Items[0].Points != 10 {
		t.Errorf("points = %d; want default 10", events[0].Items[0].Points)
	}
	if events[0].Items[0].DisplayOrder != 1 {
		t.Errorf("display_order = %d; want 1", events[0].Items[0].DisplayOrder)
	}
}

// -----------------------------------------------------------------------------
// Exactly-once — retry must NOT publish again
// -----------------------------------------------------------------------------

func TestAccept1c_RetryAfterAccept_409_NoSecondPublish(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, _, _, atomID, jobID := newAcceptServer(t, batchPub)

	body := map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set":            map[string]any{"title": "Once Only"},
	}
	w1 := postAccept(t, srv, atomID, jobID, body)
	if w1.Code != http.StatusOK {
		t.Fatalf("first accept = %d body=%s", w1.Code, w1.Body.String())
	}
	w2 := postAccept(t, srv, atomID, jobID, body)
	if w2.Code != http.StatusConflict {
		t.Fatalf("retry accept = %d; want 409 (job no longer succeeded)", w2.Code)
	}
	if got := len(batchPub.snapshot()); got != 1 {
		t.Errorf("publishes = %d; want exactly 1 (retry must not re-publish)", got)
	}
}

func TestAccept1c_LostTransitionRace_NoPublish(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, jobRepo, _, atomID, jobID := newAcceptServer(t, batchPub)

	// Simulate a concurrent accept winning the CAS between this request's
	// status read and its transition.
	jobRepo.forceTransitionLost = true

	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set":            map[string]any{"title": "Race"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (atoms persisted)", w.Code, w.Body.String())
	}
	if got := len(batchPub.snapshot()); got != 0 {
		t.Errorf("publishes = %d; want 0 when the transition was lost", got)
	}
}

// -----------------------------------------------------------------------------
// Validation failures
// -----------------------------------------------------------------------------

// ADR-195 WS5 (D4) — test_set is INPUT-AGNOSTIC: a topic-authored (non-batch)
// compose job with accepted candidates may compose a test set. The legacy
// "batch_source_material only" gate (the :1837 false coupling) is retired; this
// is the exact defect that blocked the W2.E offering walk (topic test sets 400'd).
func TestAccept1c_TestSetOnNonBatchJob_NowAccepted(t *testing.T) {
	t.Parallel()
	batchPub := &fakeBatchAcceptedPublisher{}
	srv, jobRepo, _, atomID, jobID := newAcceptServer(t, batchPub)
	// topic-authored (prompt seed), not batch source-files
	jobRepo.jobs[jobID].Intent = question.IntentNewQuestion
	jobRepo.jobs[jobID].Input = question.Input{Prompt: "p"}

	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": []map[string]any{{"draft_id": "d1"}, {"draft_id": "d2"}},
		"test_set":            map[string]any{"title": "Composed from a topic"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (test_set is input-agnostic per ADR-195 D4)", w.Code, w.Body.String())
	}
	events := batchPub.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 question_batch.accepted event for the topic test set; got %d", len(events))
	}
	if events[0].TestSetTitle != "Composed from a topic" {
		t.Errorf("event title = %q; want %q", events[0].TestSetTitle, "Composed from a topic")
	}
	if len(events[0].Items) != 2 {
		t.Errorf("event items = %d; want 2 (both accepted candidates)", len(events[0].Items))
	}
}

func TestAccept1c_TestSetWithNoCandidates_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID, jobID := newAcceptServer(t, &fakeBatchAcceptedPublisher{})
	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": []map[string]any{},
		"test_set":            map[string]any{"title": "Empty"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (cannot compose an empty test set)", w.Code)
	}
}

func TestAccept1c_TestSetMissingTitle_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID, jobID := newAcceptServer(t, &fakeBatchAcceptedPublisher{})
	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set":            map[string]any{"title": "   "},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (title required)", w.Code)
	}
}

func TestAccept1c_ItemUnknownDraftID_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID, jobID := newAcceptServer(t, &fakeBatchAcceptedPublisher{})
	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set": map[string]any{
			"title": "X",
			"items": []map[string]any{{"draft_id": "ghost", "points": 5}},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (item draft_id must reference accepted_candidates)", w.Code)
	}
}

func TestAccept1c_PointsOutOfRange_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID, jobID := newAcceptServer(t, &fakeBatchAcceptedPublisher{})
	for _, pts := range []int{0, 101, -5} {
		w := postAccept(t, srv, atomID, jobID, map[string]any{
			"accepted_candidates": acceptAll(),
			"test_set": map[string]any{
				"title": "X",
				"items": []map[string]any{{"draft_id": "d1", "points": pts}},
			},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("points=%d: status = %d; want 400 (points 1..100)", pts, w.Code)
		}
	}
}

func TestAccept1c_PublisherNotWired_503_BeforePersistence(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID, jobID := newAcceptServer(t, nil) // publisher nil

	w := postAccept(t, srv, atomID, jobID, map[string]any{
		"accepted_candidates": acceptAll(),
		"test_set":            map[string]any{"title": "X"},
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s; want 503 (fail loud, not silent drop)", w.Code, w.Body.String())
	}
	// Fails BEFORE persistence/debit — job must still be in succeeded.
	if jobRepo.jobs[jobID].Status != question.JobStatusSucceeded {
		t.Errorf("job status = %q; want still succeeded (nothing persisted)", jobRepo.jobs[jobID].Status)
	}
}
