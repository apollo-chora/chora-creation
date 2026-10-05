// topic_tag_backfill_run_test.go — CHO-2159.
//
// The defect being fixed: the backfill was a long SYNCHRONOUS endpoint behind a
// 15s http.Server WriteTimeout. A 74-atom run takes ~280s, so the server closed
// the connection long before the handler wrote its report — HTTP 000, empty
// reply, report gone. On a REAL run that is dangerous: tags are written and
// atom.published.v1 re-emitted for up to 74 atoms while the operator never sees
// failed[]. A mutating operation whose report cannot be delivered is
// unobservable by construction.
//
// These tests pin the async 202 + poll contract at the DOMAIN level: the run
// record is PERSISTED (a 280s run outlives the request, and a poll can land on a
// different replica), it ALWAYS reaches a terminal state, and the terminal write
// never rides the dead request's context.
package atom_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// fakes
// -----------------------------------------------------------------------------

// fakeBackfill stands in for *TopicTagBackfillService. run is the whole seam:
// it may block, inspect its ctx, or fail.
type fakeBackfill struct {
	run func(ctx context.Context, p atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error)
	// params records what the runner passed through, so a dropped limit/salt
	// (which would silently change what the operator asked for) is caught.
	params []atom.BackfillTopicTagsParams
}

func (f *fakeBackfill) Run(ctx context.Context, p atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error) {
	f.params = append(f.params, p)
	if f.run != nil {
		return f.run(ctx, p)
	}
	return atom.BackfillTopicTagsResult{TenantID: p.TenantID, DryRun: p.DryRun}, nil
}

// fakeRunStore records the persisted run rows in order.
type fakeRunStore struct {
	created []atom.BackfillRun
	// finished snapshots the row AS PERSISTED at terminal time (by value — the
	// runner mutates the pointer, so holding it would hide a missing write).
	finished []atom.BackfillRun
	// finishCtxErr captures ctx.Err() AS SEEN BY the terminal write. It must be
	// nil even when the originating request was cancelled — that is the whole
	// point of the detached run (CHO-2145).
	finishCtxErr error

	createErr error
	finishErr error

	get    *atom.BackfillRun
	getErr error

	sweptTenant    string
	sweptOlderThan time.Time
	sweptN         int
	sweepErr       error
}

func (f *fakeRunStore) Create(_ context.Context, run *atom.BackfillRun) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, *run)
	return nil
}

func (f *fakeRunStore) Finish(ctx context.Context, run *atom.BackfillRun) error {
	f.finishCtxErr = ctx.Err()
	if f.finishErr != nil {
		return f.finishErr
	}
	f.finished = append(f.finished, *run)
	return nil
}

func (f *fakeRunStore) Get(_ context.Context, _, _ string) (*atom.BackfillRun, error) {
	return f.get, f.getErr
}

func (f *fakeRunStore) SweepStranded(_ context.Context, tenantID string, olderThan time.Time) (int, error) {
	f.sweptTenant = tenantID
	f.sweptOlderThan = olderThan
	return f.sweptN, f.sweepErr
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func newTestRunner(t *testing.T, bf atom.TopicTagBackfill, runs atom.BackfillRunStore, mutate func(*atom.BackfillRunnerConfig)) *atom.BackfillRunner {
	t.Helper()
	cfg := atom.BackfillRunnerConfig{
		Backfill: bf,
		Runs:     runs,
		Clock:    fixedClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)),
		NewID:    func() string { return "01919d3f-0000-7000-8000-000000000001" },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := atom.NewBackfillRunner(cfg)
	if err != nil {
		t.Fatalf("NewBackfillRunner: %v", err)
	}
	return r
}

// -----------------------------------------------------------------------------
// construction — fail loud, never a runner that silently drops runs
// -----------------------------------------------------------------------------

func TestBackfillRunner_RequiresCollaborators(t *testing.T) {
	if _, err := atom.NewBackfillRunner(atom.BackfillRunnerConfig{Runs: &fakeRunStore{}}); err == nil {
		t.Fatal("want error with no backfill service wired — a runner that cannot run must not construct")
	}
	if _, err := atom.NewBackfillRunner(atom.BackfillRunnerConfig{Backfill: &fakeBackfill{}}); err == nil {
		t.Fatal("want error with no run store wired — an unpersisted run is exactly the unobservability CHO-2159 fixes")
	}
}

func TestBackfillRunner_StaleCutoffMustExceedRunTimeout(t *testing.T) {
	// A sweep cutoff inside the run window would reclaim a LIVE run as stranded:
	// the operator would poll a row marked failed while the run is still writing
	// tags. That is a worse lie than the bug we are fixing, so it must not boot.
	_, err := atom.NewBackfillRunner(atom.BackfillRunnerConfig{
		Backfill:    &fakeBackfill{},
		Runs:        &fakeRunStore{},
		RunTimeout:  30 * time.Minute,
		StaleCutoff: 10 * time.Minute,
	})
	if err == nil {
		t.Fatal("want a loud refusal when StaleCutoff <= RunTimeout (the sweep would reclaim live runs)")
	}
}

func TestBackfillRunner_DefaultsAreSane(t *testing.T) {
	r := newTestRunner(t, &fakeBackfill{}, &fakeRunStore{}, nil)
	if got := r.RunTimeout(); got != 30*time.Minute {
		t.Errorf("default RunTimeout = %v, want 30m", got)
	}
	if r.StaleCutoff() <= r.RunTimeout() {
		t.Errorf("default StaleCutoff (%v) must exceed RunTimeout (%v)", r.StaleCutoff(), r.RunTimeout())
	}
}

// -----------------------------------------------------------------------------
// Start — the 202 lane persists a `running` row BEFORE the request returns
// -----------------------------------------------------------------------------

func TestBackfillRunner_StartPersistsRunningRow(t *testing.T) {
	runs := &fakeRunStore{}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	r := newTestRunner(t, &fakeBackfill{}, runs, func(c *atom.BackfillRunnerConfig) {
		c.Clock = fixedClock(now)
	})

	run, err := r.Start(context.Background(), atom.BackfillTopicTagsParams{
		TenantID: "tenant-1", DryRun: true, Limit: 7, ReemitSalt: "s1",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(runs.created) != 1 {
		t.Fatalf("want the run row PERSISTED before the 202 (an in-memory map would not survive a pod restart or a poll on another replica); created %d rows", len(runs.created))
	}
	got := runs.created[0]
	if got.Status != atom.BackfillRunning {
		t.Errorf("persisted status = %q, want %q", got.Status, atom.BackfillRunning)
	}
	if got.RunID == "" || got.RunID != run.RunID {
		t.Errorf("run_id must be minted and returned; persisted=%q returned=%q", got.RunID, run.RunID)
	}
	if got.TenantID != "tenant-1" || !got.DryRun || got.LimitN != 7 || got.ReemitSalt != "s1" {
		t.Errorf("run row lost a parameter: %+v", got)
	}
	if !got.StartedAt.Equal(now) {
		t.Errorf("started_at = %v, want %v", got.StartedAt, now)
	}
	if got.FinishedAt != nil {
		t.Errorf("finished_at must be nil on a running row, got %v", got.FinishedAt)
	}
}

func TestBackfillRunner_StartFailsLoudWhenStoreRejects(t *testing.T) {
	// If the row cannot be persisted we must NOT hand back a 202 and detach a
	// goroutine — the operator would poll a run_id that does not exist while the
	// mutation runs unobserved. Exactly the failure mode being fixed.
	runs := &fakeRunStore{createErr: errors.New("boom")}
	r := newTestRunner(t, &fakeBackfill{}, runs, nil)

	if _, err := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1"}); err == nil {
		t.Fatal("want a loud error when the run row cannot be persisted")
	}
}

func TestBackfillRunner_StartRequiresTenant(t *testing.T) {
	r := newTestRunner(t, &fakeBackfill{}, &fakeRunStore{}, nil)
	if _, err := r.Start(context.Background(), atom.BackfillTopicTagsParams{}); err == nil {
		t.Fatal("want an error without a tenant — the row is RLS-scoped and could not be read back")
	}
}

// -----------------------------------------------------------------------------
// Execute — the detached run ALWAYS lands a terminal row
// -----------------------------------------------------------------------------

func TestBackfillRunner_ExecutePersistsTerminalReport(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 5, 0, 0, time.UTC)
	bf := &fakeBackfill{run: func(_ context.Context, p atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error) {
		return atom.BackfillTopicTagsResult{
			TenantID: p.TenantID, DryRun: p.DryRun,
			Scanned: 117, Candidates: 74, Tagged: 72, Emitted: 72,
			Proposed: []atom.ProposedTopicTags{{AtomID: "a1", Title: "T1", Tags: []string{"fractions"}}},
			Failed:   []atom.TopicTagFailure{{AtomID: "a2", Title: "T2", Reason: "classify: boom"}},
		}, nil
	}}
	runs := &fakeRunStore{}
	r := newTestRunner(t, bf, runs, func(c *atom.BackfillRunnerConfig) { c.Clock = fixedClock(now) })

	run, err := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1", ReemitSalt: "s1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Execute(context.Background(), run); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(runs.finished) != 1 {
		t.Fatalf("want exactly one terminal write, got %d", len(runs.finished))
	}
	got := runs.finished[0]
	if got.Status != atom.BackfillCompleted {
		t.Errorf("status = %q, want %q", got.Status, atom.BackfillCompleted)
	}
	if got.Scanned != 117 || got.Candidates != 74 || got.Tagged != 72 || got.Emitted != 72 {
		t.Errorf("counts not persisted: %+v", got)
	}
	// The whole point of the story: failed[] must survive to the poll.
	if len(got.Failed) != 1 || got.Failed[0].Reason != "classify: boom" {
		t.Errorf("failed[] must be persisted with its reason — that is the undiagnosable failed=2 in the story; got %+v", got.Failed)
	}
	if len(got.Proposed) != 1 || got.Proposed[0].AtomID != "a1" {
		t.Errorf("proposed[] must be persisted; got %+v", got.Proposed)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(now) {
		t.Errorf("finished_at = %v, want %v", got.FinishedAt, now)
	}
	// A per-atom failure is NOT a run failure — the run completed and reported it.
	if got.Error != "" {
		t.Errorf("a completed run with per-atom failures must not carry a run-level error, got %q", got.Error)
	}
}

func TestBackfillRunner_ExecuteCarriesRunParamsThrough(t *testing.T) {
	bf := &fakeBackfill{}
	runs := &fakeRunStore{}
	r := newTestRunner(t, bf, runs, nil)

	run, err := r.Start(context.Background(), atom.BackfillTopicTagsParams{
		TenantID: "tenant-1", DryRun: true, Limit: 3, ReemitSalt: "s1",
		TraceParent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Execute(context.Background(), run); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(bf.params) != 1 {
		t.Fatalf("want the backfill invoked once, got %d", len(bf.params))
	}
	p := bf.params[0]
	if p.TenantID != "tenant-1" || !p.DryRun || p.Limit != 3 || p.ReemitSalt != "s1" {
		t.Errorf("the detached run must execute EXACTLY what the operator asked for, got %+v", p)
	}
	// OTLP-everywhere: the trace context must cross into the detached run, or the
	// run's spans (and the re-emitted events' traceparent) orphan.
	if p.TraceParent == "" {
		t.Error("traceparent must propagate into the detached run")
	}
}

func TestBackfillRunner_ExecuteFailureWritesFailedStatus(t *testing.T) {
	// A run-level abort must NEVER leave the row `running` — the operator would
	// poll a lie forever.
	bf := &fakeBackfill{run: func(context.Context, atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error) {
		return atom.BackfillTopicTagsResult{}, errors.New("list published atoms: connection refused")
	}}
	runs := &fakeRunStore{}
	r := newTestRunner(t, bf, runs, nil)

	run, _ := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1"})
	err := r.Execute(context.Background(), run)
	if err == nil {
		t.Fatal("want Execute to surface the run failure to the caller (loud log), not swallow it")
	}

	if len(runs.finished) != 1 {
		t.Fatalf("a failed run must still land a terminal row, got %d writes", len(runs.finished))
	}
	got := runs.finished[0]
	if got.Status != atom.BackfillFailed {
		t.Errorf("status = %q, want %q", got.Status, atom.BackfillFailed)
	}
	if got.Error == "" {
		t.Error("a failed run must persist WHY — an empty error is the unobservability all over again")
	}
	if got.FinishedAt == nil {
		t.Error("a failed run must stamp finished_at, or the stale-sweep will chase it forever")
	}
}

func TestBackfillRunner_TerminalWriteSurvivesRequestCancellation(t *testing.T) {
	// THE CHO-2145 INVARIANT. net/http cancels r.Context() the moment the 202
	// response completes. If the terminal write rode that context it would be
	// cancelled before it could persist — and the row would sit `running`
	// forever while the mutation actually finished. The report would be lost
	// exactly as it is today, just at a different layer.
	ctx, cancel := context.WithCancel(context.Background())
	bf := &fakeBackfill{run: func(context.Context, atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error) {
		cancel() // the 202 response completes here; net/http kills the request ctx
		return atom.BackfillTopicTagsResult{Scanned: 5, Candidates: 1, Tagged: 1, Emitted: 1}, nil
	}}
	runs := &fakeRunStore{}
	r := newTestRunner(t, bf, runs, nil)

	run, _ := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1", ReemitSalt: "s1"})
	if err := r.Execute(ctx, run); err != nil {
		t.Fatalf("Execute must complete despite the request ctx dying: %v", err)
	}
	if runs.finishCtxErr != nil {
		t.Fatalf("the terminal write rode a cancelled context (%v) — it must persist on a context of its own", runs.finishCtxErr)
	}
	if len(runs.finished) != 1 || runs.finished[0].Status != atom.BackfillCompleted {
		t.Fatalf("want a persisted completed row after request cancellation, got %+v", runs.finished)
	}
}

func TestBackfillRunner_RunTimeoutBoundsAWedgedRun(t *testing.T) {
	// The detached ctx has no deadline of its own, so RunTimeout is the ONLY
	// thing between a wedged model-gateway call and a row stuck `running`.
	bf := &fakeBackfill{run: func(ctx context.Context, _ atom.BackfillTopicTagsParams) (atom.BackfillTopicTagsResult, error) {
		<-ctx.Done() // a classifier that never returns
		return atom.BackfillTopicTagsResult{}, ctx.Err()
	}}
	runs := &fakeRunStore{}
	r := newTestRunner(t, bf, runs, func(c *atom.BackfillRunnerConfig) {
		c.RunTimeout = 20 * time.Millisecond
		c.StaleCutoff = time.Hour
	})

	run, _ := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1", ReemitSalt: "s1"})
	if err := r.Execute(context.Background(), run); err == nil {
		t.Fatal("want the timed-out run surfaced as an error")
	}
	if len(runs.finished) != 1 {
		t.Fatalf("a timed-out run must STILL land a terminal row, got %d", len(runs.finished))
	}
	if got := runs.finished[0]; got.Status != atom.BackfillFailed || got.FinishedAt == nil {
		t.Fatalf("want failed + finished_at after RunTimeout, got %+v", got)
	}
	// And the terminal write must not itself be poisoned by the expired run ctx.
	if runs.finishCtxErr != nil {
		t.Fatalf("terminal write rode the EXPIRED run context (%v) — it must use a context of its own", runs.finishCtxErr)
	}
}

func TestBackfillRunner_TerminalWriteFailureIsLoud(t *testing.T) {
	// If we cannot persist the terminal row, the row stays `running` and the
	// stale-sweep will reclaim it. That must be shouted, never swallowed.
	runs := &fakeRunStore{finishErr: errors.New("db down")}
	r := newTestRunner(t, &fakeBackfill{}, runs, nil)

	run, _ := r.Start(context.Background(), atom.BackfillTopicTagsParams{TenantID: "tenant-1", ReemitSalt: "s1"})
	if err := r.Execute(context.Background(), run); err == nil {
		t.Fatal("want a loud error when the terminal write fails")
	}
}

// -----------------------------------------------------------------------------
// stale-sweep — a pod death mid-run must not strand the row at `running`
// -----------------------------------------------------------------------------

func TestBackfillRunner_SweepStaleNowReclaimsWithCutoff(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	runs := &fakeRunStore{sweptN: 2}
	r := newTestRunner(t, &fakeBackfill{}, runs, func(c *atom.BackfillRunnerConfig) {
		c.Clock = fixedClock(now)
		c.RunTimeout = 30 * time.Minute
		c.StaleCutoff = 45 * time.Minute
	})

	n, err := r.SweepStaleNow(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("SweepStaleNow: %v", err)
	}
	if n != 2 {
		t.Errorf("reclaimed = %d, want 2", n)
	}
	if runs.sweptTenant != "tenant-1" {
		t.Errorf("the sweep must be TENANT-SCOPED (no cross-tenant scan), swept %q", runs.sweptTenant)
	}
	want := now.Add(-45 * time.Minute)
	if !runs.sweptOlderThan.Equal(want) {
		t.Errorf("cutoff = %v, want %v (now - StaleCutoff)", runs.sweptOlderThan, want)
	}
}

func TestBackfillRunner_SweepRequiresTenant(t *testing.T) {
	r := newTestRunner(t, &fakeBackfill{}, &fakeRunStore{}, nil)
	if _, err := r.SweepStaleNow(context.Background(), ""); err == nil {
		t.Fatal("want an error without a tenant — an unscoped sweep would cross tenants")
	}
}
