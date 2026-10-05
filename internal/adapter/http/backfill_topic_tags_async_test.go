// backfill_topic_tags_async_test.go — CHO-2159.
//
// The contract these tests pin: nothing on the HTTP path is long-running any
// more. POST returns 202 in milliseconds having PERSISTED a `running` row and
// detached the execution; GET returns the persisted report. Both land far inside
// the 15s http.Server WriteTimeout — which is left ALONE (it is a whole-server
// setting; widening it for one internal endpoint would weaken every other
// route's slow-loris posture).
//
// Before this, a 74-atom run took ~280s and the server closed the connection at
// 15s: HTTP 000, report discarded, and on a REAL run the tags were written and
// atom.published.v1 re-emitted while the operator saw nothing — a mutating
// operation unobservable by construction.
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

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

const testRunID = "01919d3f-0000-7000-8000-000000000001"

// -----------------------------------------------------------------------------
// run-store double
//
// Mutex-guarded: the detached run writes the terminal row from a goroutine while
// the test reads it. `finished` lets a test await the detached write instead of
// sleeping.
// -----------------------------------------------------------------------------

type stubBackfillRuns struct {
	mu       sync.Mutex
	created  []atom.BackfillRun
	finished []atom.BackfillRun
	get      *atom.BackfillRun
	getErr   error

	swept     bool
	sweptFrom time.Time

	createErr error
	done      chan struct{}
}

func newStubBackfillRuns() *stubBackfillRuns {
	return &stubBackfillRuns{done: make(chan struct{}, 4)}
}

func (s *stubBackfillRuns) Create(_ context.Context, run *atom.BackfillRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, *run)
	return nil
}

func (s *stubBackfillRuns) Finish(_ context.Context, run *atom.BackfillRun) error {
	s.mu.Lock()
	s.finished = append(s.finished, *run)
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func (s *stubBackfillRuns) Get(_ context.Context, _, _ string) (*atom.BackfillRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.get, nil
}

func (s *stubBackfillRuns) SweepStranded(_ context.Context, _ string, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swept = true
	s.sweptFrom = olderThan
	return 0, nil
}

func (s *stubBackfillRuns) createdRuns() []atom.BackfillRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]atom.BackfillRun(nil), s.created...)
}

func (s *stubBackfillRuns) finishedRuns() []atom.BackfillRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]atom.BackfillRun(nil), s.finished...)
}

func (s *stubBackfillRuns) didSweep() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.swept
}

// awaitTerminal blocks until the DETACHED run has written its terminal row.
func (s *stubBackfillRuns) awaitTerminal(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the detached run never wrote a terminal row — the report would be lost exactly as it was before CHO-2159")
	}
}

type backfillAck struct {
	RunID    string `json:"run_id"`
	TenantID string `json:"tenant_id"`
	DryRun   bool   `json:"dry_run"`
	Status   string `json:"status"`
	PollURL  string `json:"poll_url"`
}

func decodeAck(t *testing.T, rec *httptest.ResponseRecorder) backfillAck {
	t.Helper()
	var ack backfillAck
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode ack: %v (body=%s)", err, rec.Body.String())
	}
	return ack
}

func asyncRouter(runs atom.BackfillRunStore) http.Handler {
	return NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
		BackfillRuns:    runs,
	})
}

// -----------------------------------------------------------------------------
// POST → 202 + detach
// -----------------------------------------------------------------------------

func TestBackfillTopicTags_ReturnsAcceptedWithPollURL(t *testing.T) {
	runs := newStubBackfillRuns()
	rec := postBackfill(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 (the run is detached; a 200 would mean we are still holding the request open past the 15s WriteTimeout), got %d (%s)", rec.Code, rec.Body.String())
	}
	ack := decodeAck(t, rec)
	if ack.RunID == "" {
		t.Fatal("the 202 must carry a run_id — without it the operator cannot poll for the report")
	}
	if ack.Status != string(atom.BackfillRunning) {
		t.Errorf("status = %q, want %q", ack.Status, atom.BackfillRunning)
	}
	if !strings.Contains(ack.PollURL, ack.RunID) {
		t.Errorf("poll_url %q must address the minted run_id %q", ack.PollURL, ack.RunID)
	}
	if !strings.Contains(ack.PollURL, "tenant_id=t1") {
		t.Errorf("poll_url %q must carry tenant_id — the run row is RLS-scoped and unreadable without it", ack.PollURL)
	}

	// The row must be PERSISTED before the 202: an in-memory record would not
	// survive a pod restart, and the poll can land on another replica.
	created := runs.createdRuns()
	if len(created) != 1 {
		t.Fatalf("want the run row persisted before the 202, got %d", len(created))
	}
	if created[0].Status != atom.BackfillRunning || created[0].RunID != ack.RunID {
		t.Errorf("persisted row does not match the ack: %+v vs %+v", created[0], ack)
	}
}

func TestBackfillTopicTags_DetachedRunPersistsTerminalReport(t *testing.T) {
	// The request returning must NOT cancel the run (net/http cancels
	// r.Context() the instant the 202 completes). The terminal row must land.
	runs := newStubBackfillRuns()
	rec := postBackfill(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	runs.awaitTerminal(t)
	fin := runs.finishedRuns()
	if len(fin) != 1 {
		t.Fatalf("want one terminal write, got %d", len(fin))
	}
	if fin[0].Status != atom.BackfillCompleted {
		t.Errorf("status = %q, want %q", fin[0].Status, atom.BackfillCompleted)
	}
	if fin[0].FinishedAt == nil {
		t.Error("the terminal row must stamp finished_at")
	}
}

func TestBackfillTopicTags_UnwiredRunStoreFailsLoud(t *testing.T) {
	// no-stubs: without a persistence seam the report could not be returned —
	// which is the entire defect. Refuse rather than run a mutation unobserved.
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 with an unwired run store, got %d (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !contains(body, "CREATION_BACKFILL_RUNS_NOT_WIRED") {
		t.Fatalf("want the precise fail-loud envelope, got %s", body)
	}
}

func TestBackfillTopicTags_PersistFailureAbortsThe202(t *testing.T) {
	// If the row cannot be persisted we must NOT 202 and detach: the operator
	// would poll a run_id that never resolves while the mutation ran unobserved.
	runs := newStubBackfillRuns()
	runs.createErr = context.DeadlineExceeded
	rec := postBackfill(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want a loud 500 when the run row cannot be persisted, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET → the persisted report
// -----------------------------------------------------------------------------

func getRun(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestBackfillRun_GetReturnsPersistedReport(t *testing.T) {
	fin := time.Date(2026, 7, 14, 12, 4, 40, 0, time.UTC)
	runs := newStubBackfillRuns()
	runs.get = &atom.BackfillRun{
		RunID: testRunID, TenantID: "t1", DryRun: true,
		Status:  atom.BackfillCompleted,
		Scanned: 117, Candidates: 74, Tagged: 0, Emitted: 0,
		Proposed:   []atom.ProposedTopicTags{{AtomID: "a1", Title: "T1", Tags: []string{"fractions"}}},
		Failed:     []atom.TopicTagFailure{{AtomID: "a2", Title: "T2", Reason: "classify: boom"}},
		StartedAt:  fin.Add(-280 * time.Second),
		FinishedAt: &fin,
	}

	rec := getRun(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags/"+testRunID+"?tenant_id=t1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 on the poll, got %d (%s)", rec.Code, rec.Body.String())
	}

	var got atom.BackfillRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode run: %v (body=%s)", err, rec.Body.String())
	}
	if got.Status != atom.BackfillCompleted || got.Scanned != 117 || got.Candidates != 74 {
		t.Errorf("report not returned intact: %+v", got)
	}
	// THE deliverable of the story: the per-atom failure reasons (the
	// undiagnosable failed=2) must reach the operator.
	if len(got.Failed) != 1 || got.Failed[0].Reason != "classify: boom" {
		t.Errorf("failed[] with reasons must be returned; got %+v", got.Failed)
	}
	if len(got.Proposed) != 1 {
		t.Errorf("proposed[] must be returned in full (all 74, not the first 3 that fit in 15s); got %+v", got.Proposed)
	}
}

func TestBackfillRun_GetUnknownRunIs404(t *testing.T) {
	runs := newStubBackfillRuns()
	runs.getErr = atom.ErrBackfillRunNotFound

	rec := getRun(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags/"+testRunID+"?tenant_id=t1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an unknown run_id, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestBackfillRun_GetRequiresTenant(t *testing.T) {
	// The run row is RLS-scoped; without a tenant the read would be unscoped.
	rec := getRun(t, asyncRouter(newStubBackfillRuns()), "/api/internal/atoms/backfill-topic-tags/"+testRunID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without tenant_id, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestBackfillRun_GetRejectsMalformedRunID(t *testing.T) {
	// A non-UUID run_id would reach Postgres and throw 22P02 — a user error
	// masquerading as a 500. Refuse it precisely instead.
	rec := getRun(t, asyncRouter(newStubBackfillRuns()), "/api/internal/atoms/backfill-topic-tags/not-a-uuid?tenant_id=t1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a malformed run_id, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestBackfillRun_GetSweepsStrandedRuns(t *testing.T) {
	// CHO-2138 precedent. A pod that dies mid-run leaves the row `running`
	// forever; without an opportunistic sweep the operator polls a lie.
	runs := newStubBackfillRuns()
	runs.getErr = atom.ErrBackfillRunNotFound

	_ = getRun(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags/"+testRunID+"?tenant_id=t1")
	if !runs.didSweep() {
		t.Fatal("the poll must opportunistically reclaim this tenant's stranded `running` rows — otherwise a pod death strands them forever")
	}
}

func TestBackfillRun_GetMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	asyncRouter(newStubBackfillRuns()).ServeHTTP(rec,
		httptest.NewRequest(http.MethodDelete, "/api/internal/atoms/backfill-topic-tags/"+testRunID+"?tenant_id=t1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 on DELETE, got %d", rec.Code)
	}
}
