// topic_tag_backfill_run.go — async 202 + poll for the topic-tag backfill (CHO-2159).
//
// THE DEFECT. The backfill (CHO-2142) was a long SYNCHRONOUS endpoint: 74 atoms
// x ~3.5s of model-gateway classification is ~280s, and chora-creation's
// http.Server carries WriteTimeout: 15s. The server closed the connection at 15s
// and the handler — still running — wrote its report to a socket nobody was
// listening on. Measured live 2026-07-14: the full dry run returned HTTP 000 /
// empty reply after 280s while the pod log showed the run had COMPLETED
// (scanned=117 candidates=74 failed=2). A limit=3 run returned 200 in 11.9s,
// pinning the boundary at the WriteTimeout exactly.
//
// On a REAL run that is not merely annoying, it is dangerous: the run writes
// learning_atoms.tags and re-emits atom.published.v1 for up to 74 atoms while
// the operator gets HTTP 000 and never sees failed[]. A mutating operation whose
// report cannot be delivered is UNOBSERVABLE BY CONSTRUCTION — which is why
// failed=2 was undiagnosable: the reasons existed only in the unreturnable body.
//
// THE FIX (the CHO-2145 precedent, already proven for ritual runs). Nothing on
// the HTTP path stays long-running:
//
//	POST  → mint run_id, persist a `running` row, return 202 in milliseconds
//	        and execute the run DETACHED from the request
//	GET   → read the persisted report (status, counts, proposed[], failed[])
//
// Both return far inside the 15s WriteTimeout, so WriteTimeout is left ALONE —
// it is a whole-server setting and widening it for one internal endpoint would
// weaken every other route's slow-loris posture.
//
// WHY THE RUN RECORD IS PERSISTED, NOT AN IN-MEMORY MAP. A 280s run outlives a
// pod restart, and a poll can land on a different replica. An in-memory map
// would reintroduce the exact unobservability this story exists to remove — the
// report would again be lost, just at a different layer.
//
// TERMINALITY IS THE INVARIANT. A row must NEVER be left `running` while the
// operator polls it:
//
//   - the terminal write runs on a context of its OWN (context.WithoutCancel +
//     its own deadline): net/http cancels r.Context() the instant the 202
//     completes, and RunTimeout expires the run context — neither may be allowed
//     to cancel the write that records what happened;
//   - RunTimeout bounds a wedged classifier (the detached context has no
//     deadline of its own, so this is the only thing that can free it);
//   - a pod death mid-run is backstopped by SweepStaleNow, which reclaims rows
//     still `running` past StaleCutoff as `failed` (stranded). Without it a pod
//     restart would strand the row at `running` forever and the operator would
//     poll a lie.
package atom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Run record
// -----------------------------------------------------------------------------

// BackfillRunStatus is the run's lifecycle state. `completed` and `failed` are
// TERMINAL; `running` is the only non-terminal state and is what the stale-sweep
// reclaims.
type BackfillRunStatus string

const (
	// BackfillRunning — the detached execution is in flight (or its pod died).
	BackfillRunning BackfillRunStatus = "running"
	// BackfillCompleted — the run finished. Per-atom failures live in Failed;
	// they do NOT make the run itself failed (the run ran, and reported them).
	BackfillCompleted BackfillRunStatus = "completed"
	// BackfillFailed — the run ABORTED (a collaborator/param/DB error, or
	// RunTimeout), or it was reclaimed as stranded by the stale-sweep. Error
	// says why.
	BackfillFailed BackfillRunStatus = "failed"
)

// ErrBackfillRunNotFound is returned by BackfillRunStore.Get for an unknown
// run_id (the handler maps it to 404).
var ErrBackfillRunNotFound = errors.New("topic-tag backfill: run not found")

// BackfillRun is the PERSISTED run record — the whole reason the report is
// returnable at all. It is written `running` before the 202 and rewritten
// terminal when the detached execution ends.
type BackfillRun struct {
	RunID    string            `json:"run_id"`
	TenantID string            `json:"tenant_id"`
	DryRun   bool              `json:"dry_run"`
	Status   BackfillRunStatus `json:"status"`

	// LimitN / ReemitSalt echo the operator's request, so a poll shows what was
	// actually run (not what the poller assumed).
	LimitN     int    `json:"limit,omitempty"`
	ReemitSalt string `json:"reemit_salt,omitempty"`

	Scanned    int `json:"scanned"`
	Candidates int `json:"candidates"`
	Tagged     int `json:"tagged"`
	Emitted    int `json:"emitted"`

	// Proposed + Failed are the report body (persisted as JSONB). Failed is the
	// point of the story: the per-atom reasons must survive to the poll.
	Proposed []ProposedTopicTags `json:"proposed,omitempty"`
	Failed   []TopicTagFailure   `json:"failed,omitempty"`

	// Error is the RUN-level abort reason (empty on a completed run, even one
	// carrying per-atom failures).
	Error string `json:"error,omitempty"`

	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// TraceParent / TraceState are NOT persisted — they are carried in-process
	// from the POST into the detached execution (which always runs on the replica
	// that minted the row) so the run's LLM spans and the re-emitted events'
	// envelopes stay on the operator's trace. A poll that lands on another
	// replica reads the row from the DB and needs neither.
	TraceParent string `json:"-"`
	TraceState  string `json:"-"`
}

// Params reconstructs the backfill parameters from the persisted run, so the
// detached execution runs EXACTLY what the operator asked for.
func (r *BackfillRun) Params() BackfillTopicTagsParams {
	return BackfillTopicTagsParams{
		TenantID:    r.TenantID,
		DryRun:      r.DryRun,
		Limit:       r.LimitN,
		ReemitSalt:  r.ReemitSalt,
		TraceParent: r.TraceParent,
		TraceState:  r.TraceState,
	}
}

// -----------------------------------------------------------------------------
// Ports
// -----------------------------------------------------------------------------

// BackfillRunStore persists run records. Every method is TENANT-SCOPED: the
// table carries FORCE RLS keyed on chora.tenant_id, so the adapter MUST run each
// statement inside the tenant-scoped transaction seam (RunInTenantTx). An
// explicit tenant_id predicate does NOT substitute for the GUC (CHO-2170).
type BackfillRunStore interface {
	// Create persists the `running` row. It must fail loud — a 202 handed out
	// for a row that does not exist would leave the operator polling a run_id
	// that never resolves while the mutation ran unobserved.
	Create(ctx context.Context, run *BackfillRun) error
	// Finish writes the TERMINAL row (status + counts + report + finished_at, or
	// status=failed + error).
	Finish(ctx context.Context, run *BackfillRun) error
	// Get returns the persisted run, or ErrBackfillRunNotFound.
	Get(ctx context.Context, tenantID, runID string) (*BackfillRun, error)
	// SweepStranded reclaims rows still `running` and started before olderThan
	// (a pod died mid-run) as failed. Returns how many were reclaimed.
	SweepStranded(ctx context.Context, tenantID string, olderThan time.Time) (int, error)
}

// TopicTagBackfill is the slice of TopicTagBackfillService the runner drives.
// Keeping it an interface is what lets the runner be tested against a wedged /
// failing / cancelling backfill without a model gateway.
type TopicTagBackfill interface {
	Run(ctx context.Context, p BackfillTopicTagsParams) (BackfillTopicTagsResult, error)
}

// Compile-time check: the CHO-2142 service satisfies the runner's port.
var _ TopicTagBackfill = (*TopicTagBackfillService)(nil)

// -----------------------------------------------------------------------------
// Runner
// -----------------------------------------------------------------------------

// BackfillRunnerConfig wires the runner. Durations come from env at the
// composition root (no inline config); the defaults below apply when unset.
type BackfillRunnerConfig struct {
	Backfill TopicTagBackfill
	Runs     BackfillRunStore

	// RunTimeout bounds ONE whole detached run. The detached context has no
	// deadline of its own (it is deliberately un-cancellable — it must outlive
	// the request), so this is the ONLY thing standing between a wedged
	// model-gateway call and a row stuck `running`. Must comfortably exceed a
	// legit full run (74 atoms x ~3.5s ~= 280s). ⚙ 30m.
	RunTimeout time.Duration
	// TerminateTimeout bounds the terminal write, which runs on a context of its
	// own so a dead request / expired run context cannot cancel it. ⚙ 30s.
	TerminateTimeout time.Duration
	// StaleCutoff: a `running` row older than this is reclaimed as stranded by
	// SweepStaleNow. MUST exceed RunTimeout — a cutoff inside the run window
	// would reclaim a LIVE run and the operator would poll `failed` while the
	// backfill was still writing tags. ⚙ RunTimeout x 1.5 (45m).
	StaleCutoff time.Duration

	Clock func() time.Time
	NewID func() string
}

// BackfillRunner owns the async lane: persist → detach → terminate → sweep.
type BackfillRunner struct {
	cfg BackfillRunnerConfig
}

// NewBackfillRunner validates the wiring and applies defaults. A runner that
// cannot persist runs must not construct — an unpersisted run is precisely the
// unobservability CHO-2159 exists to remove.
func NewBackfillRunner(cfg BackfillRunnerConfig) (*BackfillRunner, error) {
	if cfg.Backfill == nil {
		return nil, errors.New("atom.BackfillRunner: backfill service is required")
	}
	if cfg.Runs == nil {
		return nil, errors.New("atom.BackfillRunner: run store is required (an in-memory run record would not survive a pod restart, and a poll can land on another replica)")
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 30 * time.Minute
	}
	if cfg.TerminateTimeout <= 0 {
		cfg.TerminateTimeout = 30 * time.Second
	}
	if cfg.StaleCutoff <= 0 {
		// Derive from RunTimeout so the invariant below holds by construction for
		// any operator-tuned RunTimeout.
		cfg.StaleCutoff = cfg.RunTimeout + cfg.RunTimeout/2
	}
	if cfg.StaleCutoff <= cfg.RunTimeout {
		return nil, fmt.Errorf(
			"atom.BackfillRunner: StaleCutoff (%s) must exceed RunTimeout (%s) — otherwise the sweep reclaims LIVE runs and the operator polls `failed` while tags are still being written",
			cfg.StaleCutoff, cfg.RunTimeout)
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = func() string { return uuid.Must(uuid.NewV7()).String() }
	}
	return &BackfillRunner{cfg: cfg}, nil
}

// RunTimeout exposes the effective run bound (the handler reports it on the 202
// so the operator knows how long to keep polling).
func (r *BackfillRunner) RunTimeout() time.Duration { return r.cfg.RunTimeout }

// StaleCutoff exposes the effective sweep cutoff.
func (r *BackfillRunner) StaleCutoff() time.Duration { return r.cfg.StaleCutoff }

// Start mints a run_id, PERSISTS a `running` row, and returns it. The caller
// (the POST handler) then 202s and hands the run to Execute on a detached
// context. A persist failure is loud and aborts the 202 — never a detached
// mutation with no row to observe it.
func (r *BackfillRunner) Start(ctx context.Context, p BackfillTopicTagsParams) (*BackfillRun, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("atom.BackfillRunner: tenant_id is required (the run row is RLS-scoped and could not be read back)")
	}
	run := &BackfillRun{
		RunID:       r.cfg.NewID(),
		TenantID:    p.TenantID,
		DryRun:      p.DryRun,
		Status:      BackfillRunning,
		LimitN:      p.Limit,
		ReemitSalt:  p.ReemitSalt,
		StartedAt:   r.cfg.Clock(),
		TraceParent: p.TraceParent,
		TraceState:  p.TraceState,
	}
	if err := r.cfg.Runs.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("atom.BackfillRunner: persist run row: %w", err)
	}
	return run, nil
}

// Execute runs the backfill and writes the TERMINAL row. It is called from a
// goroutine on a context detached from the request (context.WithoutCancel), so:
//
//   - RunTimeout is applied here — the detached context has no deadline of its
//     own and a wedged gateway call would otherwise strand the row `running`;
//   - the terminal write runs on a context of its OWN, derived with
//     WithoutCancel, so neither the dead request nor an expired RunTimeout can
//     cancel the write that records what actually happened.
//
// The row therefore ALWAYS reaches a terminal state. The returned error is for
// the caller's loud log; the row already carries the reason.
func (r *BackfillRunner) Execute(ctx context.Context, run *BackfillRun) error {
	if run == nil {
		return errors.New("atom.BackfillRunner: nil run")
	}

	execCtx, cancel := context.WithTimeout(ctx, r.cfg.RunTimeout)
	defer cancel()

	res, runErr := r.cfg.Backfill.Run(execCtx, run.Params())

	// The terminal write must survive BOTH the request's death and RunTimeout's
	// expiry — it is the only record that the mutation happened.
	termCtx, termCancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.TerminateTimeout)
	defer termCancel()

	finishedAt := r.cfg.Clock()
	run.FinishedAt = &finishedAt
	if runErr != nil {
		run.Status = BackfillFailed
		run.Error = runErr.Error()
	} else {
		run.Status = BackfillCompleted
		run.Scanned = res.Scanned
		run.Candidates = res.Candidates
		run.Tagged = res.Tagged
		run.Emitted = res.Emitted
		run.Proposed = res.Proposed
		run.Failed = res.Failed
	}

	if err := r.cfg.Runs.Finish(termCtx, run); err != nil {
		// The row is still `running`. Shout — the stale-sweep will reclaim it,
		// but an operator polling right now is looking at a stale truth.
		return fmt.Errorf("atom.BackfillRunner: terminal write for run %s failed (row left `running`; the stale-sweep will reclaim it): %w", run.RunID, err)
	}
	if runErr != nil {
		return fmt.Errorf("atom.BackfillRunner: run %s failed: %w", run.RunID, runErr)
	}
	return nil
}

// SweepStaleNow reclaims this tenant's rows still `running` past StaleCutoff —
// a runner whose pod died mid-run. Called opportunistically from the GET (the
// CHO-2138 precedent): without it a pod restart strands the row at `running`
// forever and the operator polls a lie.
//
// TENANT-SCOPED by construction — no cross-tenant scan.
func (r *BackfillRunner) SweepStaleNow(ctx context.Context, tenantID string) (int, error) {
	if strings.TrimSpace(tenantID) == "" {
		return 0, errors.New("atom.BackfillRunner: tenant_id is required for the stale-sweep (an unscoped sweep would cross tenants)")
	}
	return r.cfg.Runs.SweepStranded(ctx, tenantID, r.cfg.Clock().Add(-r.cfg.StaleCutoff))
}
