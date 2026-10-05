// atom_backfill_run_repository.go — persistence for the async topic-tag backfill
// run report (CHO-2159).
//
// WHY A TABLE AND NOT A MAP. The run is executed DETACHED from the request and
// takes ~280s for 74 atoms. It outlives a pod restart, and the operator's poll
// can land on a different replica. An in-memory run record would reintroduce the
// very unobservability CHO-2159 exists to remove — the report would be lost
// again, just at a different layer.
//
// ⚠ RLS (CHO-2170). atom_backfill_runs carries FORCE ROW LEVEL SECURITY with a
// tenant_isolation policy on current_setting('chora.tenant_id'). A statement
// that reaches it without that GUC set IN THE SAME TRANSACTION silently matches
// nothing (fresh connection) or throws 22P02 (pooled connection — a custom GUC
// reverts to the EMPTY STRING, not unset, when its SET LOCAL transaction ends).
// The explicit `tenant_id = $n` predicates below are defence in depth; they are
// NOT a substitute for the GUC.
//
// This repository therefore holds a TxQuerier and NOTHING else: every read and
// every write goes through RunInTenantTx, which does `SET LOCAL chora.tenant_id`
// and only then invokes the closure. It is structurally incapable of emitting a
// statement outside that seam.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// SQL
// -----------------------------------------------------------------------------

const insertBackfillRunSQL = `
INSERT INTO atom_backfill_runs (
    run_id, tenant_id, dry_run, status, limit_n, reemit_salt,
    scanned, candidates, tagged, emitted, report, error,
    started_at, finished_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11::jsonb, $12,
    $13, $14
)`

const finishBackfillRunSQL = `
UPDATE atom_backfill_runs
   SET status      = $3,
       scanned     = $4,
       candidates  = $5,
       tagged      = $6,
       emitted     = $7,
       report      = $8::jsonb,
       error       = $9,
       finished_at = $10
 WHERE run_id    = $1
   AND tenant_id = $2
   AND deleted_at IS NULL`

const getBackfillRunSQL = `
SELECT run_id, tenant_id, dry_run, status, limit_n, reemit_salt,
       scanned, candidates, tagged, emitted, report, error,
       started_at, finished_at
  FROM atom_backfill_runs
 WHERE run_id    = $1
   AND tenant_id = $2
   AND deleted_at IS NULL`

// sweepStrandedBackfillRunsSQL reclaims runs whose pod died mid-flight. Only
// rows that are STILL `running`, never finished, and older than the cutoff — a
// live run must never be reclaimed out from under itself (StaleCutoff is
// validated to exceed RunTimeout for exactly this reason).
const sweepStrandedBackfillRunsSQL = `
UPDATE atom_backfill_runs
   SET status      = 'failed',
       error       = $3,
       finished_at = now()
 WHERE tenant_id   = $1
   AND status      = 'running'
   AND finished_at IS NULL
   AND started_at  < $2
   AND deleted_at IS NULL
RETURNING run_id`

// strandedReason is what an operator polling a reclaimed row reads. It says what
// happened and what to do — never a bare "failed".
const strandedReason = "stranded: the run's pod died mid-flight (no terminal write landed before the stale cutoff). " +
	"Any tags written before the death ARE at source; re-run the backfill (it selects only tagless atoms, so it retries exactly what did not land)."

// -----------------------------------------------------------------------------
// Repository
// -----------------------------------------------------------------------------

// AtomBackfillRunRepository is the pg-backed atom.BackfillRunStore.
type AtomBackfillRunRepository struct {
	tx TxQuerier
}

// NewAtomBackfillRunRepository constructs the repo around the tenant-tx seam.
// A nil seam is tolerated at construction but FAILS LOUD on every call — never a
// repo that silently pretends to persist (a 202 handed out for a row that does
// not exist would leave the operator polling a run_id that never resolves while
// the mutation ran unobserved).
func NewAtomBackfillRunRepository(tx TxQuerier) *AtomBackfillRunRepository {
	return &AtomBackfillRunRepository{tx: tx}
}

// Compile-time check: the adapter satisfies the domain port.
var _ atom.BackfillRunStore = (*AtomBackfillRunRepository)(nil)

// backfillReport is the JSONB body — the proposals and, crucially, the per-atom
// failure reasons that were undiagnosable while the report could not be
// returned.
type backfillReport struct {
	Proposed []atom.ProposedTopicTags `json:"proposed,omitempty"`
	Failed   []atom.TopicTagFailure   `json:"failed,omitempty"`
}

func (r *AtomBackfillRunRepository) seam() error {
	if r == nil || r.tx == nil {
		return errors.New("pg.AtomBackfillRunRepository: tenant-tx seam not wired — refusing to run a backfill whose report cannot be persisted")
	}
	return nil
}

func marshalBackfillReport(run *atom.BackfillRun) ([]byte, error) {
	b, err := json.Marshal(backfillReport{Proposed: run.Proposed, Failed: run.Failed})
	if err != nil {
		return nil, fmt.Errorf("marshal report: %w", err)
	}
	return b, nil
}

// Create persists the `running` row before the 202 goes out.
func (r *AtomBackfillRunRepository) Create(ctx context.Context, run *atom.BackfillRun) error {
	if err := r.seam(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("pg.AtomBackfillRunRepository.Create: nil run")
	}
	if strings.TrimSpace(run.TenantID) == "" {
		return errors.New("pg.AtomBackfillRunRepository.Create: tenant_id required (the row is RLS-scoped)")
	}
	report, err := marshalBackfillReport(run)
	if err != nil {
		return fmt.Errorf("pg.AtomBackfillRunRepository.Create: %w", err)
	}

	// RLS: RunInTenantTx applies SET LOCAL chora.tenant_id BEFORE this INSERT.
	// Without it the FORCE-RLS WITH CHECK would reject the row (or, on a pooled
	// connection, throw 22P02) — CHO-2170.
	return r.tx.RunInTenantTx(ctx, run.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, insertBackfillRunSQL,
			run.RunID, run.TenantID, run.DryRun, string(run.Status), run.LimitN, run.ReemitSalt,
			run.Scanned, run.Candidates, run.Tagged, run.Emitted, report, run.Error,
			run.StartedAt, run.FinishedAt,
		); err != nil {
			return fmt.Errorf("pg.AtomBackfillRunRepository.Create: %w", err)
		}
		return nil
	})
}

// Finish writes the terminal row: status + counts + report + finished_at (or
// status=failed + error). This is the write that makes the mutation observable.
func (r *AtomBackfillRunRepository) Finish(ctx context.Context, run *atom.BackfillRun) error {
	if err := r.seam(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("pg.AtomBackfillRunRepository.Finish: nil run")
	}
	if strings.TrimSpace(run.TenantID) == "" {
		return errors.New("pg.AtomBackfillRunRepository.Finish: tenant_id required (the row is RLS-scoped)")
	}
	report, err := marshalBackfillReport(run)
	if err != nil {
		return fmt.Errorf("pg.AtomBackfillRunRepository.Finish: %w", err)
	}

	// RLS: SET LOCAL chora.tenant_id fires inside the seam, before the UPDATE.
	return r.tx.RunInTenantTx(ctx, run.TenantID, func(ctx context.Context, tx Tx) error {
		if err := tx.Exec(ctx, finishBackfillRunSQL,
			run.RunID, run.TenantID, string(run.Status),
			run.Scanned, run.Candidates, run.Tagged, run.Emitted,
			report, run.Error, run.FinishedAt,
		); err != nil {
			return fmt.Errorf("pg.AtomBackfillRunRepository.Finish: %w", err)
		}
		return nil
	})
}

// Get returns the persisted run, or atom.ErrBackfillRunNotFound.
func (r *AtomBackfillRunRepository) Get(ctx context.Context, tenantID, runID string) (*atom.BackfillRun, error) {
	if err := r.seam(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("pg.AtomBackfillRunRepository.Get: tenant_id required (the row is RLS-scoped)")
	}

	var out *atom.BackfillRun
	// RLS: SET LOCAL chora.tenant_id fires inside the seam, before the SELECT.
	// A read that skipped it would return ZERO ROWS with no error — the operator
	// would see a 404 for a run that exists (CHO-2170's silent-nothing).
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		var (
			run        atom.BackfillRun
			status     string
			report     []byte
			finishedAt *time.Time
		)
		row := tx.QueryRow(ctx, getBackfillRunSQL, runID, tenantID)
		if err := row.Scan(
			&run.RunID, &run.TenantID, &run.DryRun, &status, &run.LimitN, &run.ReemitSalt,
			&run.Scanned, &run.Candidates, &run.Tagged, &run.Emitted, &report, &run.Error,
			&run.StartedAt, &finishedAt,
		); err != nil {
			if errors.Is(err, ErrNoRows) {
				return atom.ErrBackfillRunNotFound
			}
			return fmt.Errorf("pg.AtomBackfillRunRepository.Get: scan: %w", err)
		}
		run.Status = atom.BackfillRunStatus(status)
		run.FinishedAt = finishedAt

		if len(report) > 0 {
			var body backfillReport
			if err := json.Unmarshal(report, &body); err != nil {
				// The report is the deliverable. A body we cannot decode must be a
				// loud error, never a run silently reported with an empty failed[].
				return fmt.Errorf("pg.AtomBackfillRunRepository.Get: decode report for run %s: %w", runID, err)
			}
			run.Proposed = body.Proposed
			run.Failed = body.Failed
		}
		out = &run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SweepStranded reclaims this tenant's rows still `running` past the cutoff — a
// runner whose pod died mid-flight. Without it a pod restart strands the row at
// `running` forever and the operator polls a lie.
func (r *AtomBackfillRunRepository) SweepStranded(ctx context.Context, tenantID string, olderThan time.Time) (int, error) {
	if err := r.seam(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(tenantID) == "" {
		return 0, errors.New("pg.AtomBackfillRunRepository.SweepStranded: tenant_id required (an unscoped sweep would cross tenants)")
	}

	n := 0
	// RLS: SET LOCAL chora.tenant_id fires inside the seam, before the UPDATE.
	// This is the exact shape that bit CHO-2170 — a FORCE-RLS mutation with an
	// explicit tenant predicate but no GUC updates NOTHING and returns (0, nil),
	// which reads as "nothing was stranded" instead of "the sweep never ran".
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		rows, err := tx.Query(ctx, sweepStrandedBackfillRunsSQL, tenantID, olderThan, strandedReason)
		if err != nil {
			return fmt.Errorf("pg.AtomBackfillRunRepository.SweepStranded: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var runID string
			if err := rows.Scan(&runID); err != nil {
				return fmt.Errorf("pg.AtomBackfillRunRepository.SweepStranded: scan: %w", err)
			}
			n++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg.AtomBackfillRunRepository.SweepStranded: rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
