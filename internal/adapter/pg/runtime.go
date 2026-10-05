// Package pg is the pgx-backed adapter for chora-creation repository ports.
//
// Architecture:
//
//   - Domain ports (Save, Get, List, ListByCourse) defined in
//     internal/domain/atom/repository.go
//   - This package implements those ports against a *pgxpool.Pool
//   - A small `Querier` interface decouples the SQL emit-and-scan layer
//     from pgx so unit tests can stub the SQL surface without a live DB
//   - PgxPoolQuerier wraps a *pgxpool.Pool and is what cmd/server constructs
//     in production
//
// Resilience-priority directive (`feedback_resilience_priority`):
//
//   - Every read/write path that touches a tenant-scoped table runs
//     inside a transaction, with `SET LOCAL chora.tenant_id` applied
//     BEFORE the user query, so concurrent multi-tenant traffic is
//     transaction-isolated under PgBouncer transaction-pooling
//   - Soft-delete: queries default to `WHERE deleted_at IS NULL`
//   - All errors wrap, never lose context
//
// Cross-DB queries forbidden — chora-creation reads only chora_creation.
// Inter-domain side effects flow through Pub/Sub (see ../events).
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/tracing"
)

// Querier is the minimal Exec + QueryRow + Query surface this package needs
// from pgx. Production wires PgxPoolQuerier (wraps *pgxpool.Pool); tests
// inject a stub.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) error
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Row is the minimal Scan surface used by the repos.
type Row interface {
	Scan(dest ...any) error
}

// Rows is the minimal multi-row iteration surface used by the repos.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// ErrNoRows is the pg-package-local sentinel for a not-found row. Repos
// translate this to domain-level not-found.
var ErrNoRows = errors.New("pg: no rows in result set")

// PgxPoolQuerier wraps a *pgxpool.Pool with the local Querier shape.
type PgxPoolQuerier struct {
	pool *pgxpool.Pool
}

// NewPgxPoolQuerier wraps a pgxpool.Pool.
func NewPgxPoolQuerier(pool *pgxpool.Pool) *PgxPoolQuerier {
	return &PgxPoolQuerier{pool: pool}
}

// Exec runs a non-returning SQL statement.
func (q *PgxPoolQuerier) Exec(ctx context.Context, sql string, args ...any) error {
	if _, err := q.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("pg.Exec: %w", err)
	}
	return nil
}

// QueryRow runs a single-row SQL query.
func (q *PgxPoolQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &pgxPoolRow{r: q.pool.QueryRow(ctx, sql, args...)}
}

// Query runs a multi-row SQL query.
func (q *PgxPoolQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rs, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pg.Query: %w", err)
	}
	return &pgxPoolRows{r: rs}, nil
}

// Pool returns the underlying pool.
func (q *PgxPoolQuerier) Pool() *pgxpool.Pool {
	return q.pool
}

type pgxPoolRow struct {
	r pgx.Row
}

func (r *pgxPoolRow) Scan(dest ...any) error {
	err := r.r.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

type pgxPoolRows struct {
	r pgx.Rows
}

func (r *pgxPoolRows) Next() bool             { return r.r.Next() }
func (r *pgxPoolRows) Scan(dest ...any) error { return r.r.Scan(dest...) }
func (r *pgxPoolRows) Close() error           { r.r.Close(); return nil }
func (r *pgxPoolRows) Err() error             { return r.r.Err() }

// PgxTxQuerier wraps a pgx.Tx so it satisfies Querier. Used by tests +
// production handlers that wrap a request in `RunTenantTx` and want to
// pass the tenant-scoped Querier into the repository.
type PgxTxQuerier struct {
	tx pgx.Tx
}

// NewPgxTxQuerier wraps a pgx.Tx.
func NewPgxTxQuerier(tx pgx.Tx) *PgxTxQuerier {
	return &PgxTxQuerier{tx: tx}
}

// Exec runs a non-returning SQL statement against the tx.
func (q *PgxTxQuerier) Exec(ctx context.Context, sql string, args ...any) error {
	if _, err := q.tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("pg.tx.Exec: %w", err)
	}
	return nil
}

// QueryRow runs a single-row SQL query against the tx.
func (q *PgxTxQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &pgxPoolRow{r: q.tx.QueryRow(ctx, sql, args...)}
}

// Query runs a multi-row SQL query against the tx.
func (q *PgxTxQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rs, err := q.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pg.tx.Query: %w", err)
	}
	return &pgxPoolRows{r: rs}, nil
}

// RunTenantTx runs the supplied fn inside a transaction with
// `SET LOCAL chora.tenant_id = $1` applied first. Mandatory for any
// query against tenant-scoped tables under PgBouncer transaction-pooling.
//
// Errors are returned wrapped; rollback is automatic on error or panic.
func RunTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	if pool == nil {
		return errors.New("pg.RunTenantTx: nil pool")
	}
	if tenantID == "" {
		return errors.New("pg.RunTenantTx: tenant_id required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("pg.RunTenantTx: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()
	if _, err = tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantID)); err != nil {
		return fmt.Errorf("pg.RunTenantTx: set tenant: %w", err)
	}
	// ADR-191 O3: propagate the caller's role set (no-op when absent) so the
	// restrictive exam_content_embargo policy can match a PROCTOR claim.
	if err = applyUserRolesSession(ctx, func(sql string) error {
		_, e := tx.Exec(ctx, sql)
		return e
	}); err != nil {
		return fmt.Errorf("pg.RunTenantTx: %w", err)
	}
	return fn(ctx, tx)
}

// -----------------------------------------------------------------------------
// Tenant-scoped transaction surface (#27 — atom RLS fix)
//
// Mirrors services/chora-tenancy/internal/adapter/pg/runtime.go §"Tenant-
// scoped transaction surface". `learning_atoms` carries a tenant_isolation
// RLS policy and chora_creation_app_rw is NOBYPASSRLS, so a bare query
// against the pool silently returns 0 rows. AtomRepository methods now wrap
// their SQL in RunInTenantTx so SET LOCAL chora.tenant_id runs inside the
// same transaction.
//
// The pre-existing `RunTenantTx` free function (above) exposes the raw
// pgx.Tx — kept for callers (legacy / integration tests) that need direct
// pgx access. The TxQuerier surface below is the new seam used by the
// repository so unit tests can stub it without pgx.
// -----------------------------------------------------------------------------

// Tx is the minimal Exec + Query + QueryRow surface a repository needs
// inside an open, tenant-scoped transaction.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) error
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// TxQuerier opens a transaction with `SET LOCAL chora.tenant_id` already
// applied and runs fn against it. fn returning an error rolls the tx back;
// nil commits.
type TxQuerier interface {
	RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, Tx) error) error
}

// RunInTenantTx satisfies TxQuerier for the production pgxpool wrapper.
//
// Sequence: pool.Begin → SET LOCAL chora.tenant_id → fn(tx) → Commit
// (or Rollback when fn errors / Commit fails). tenantID is interpolated
// into the SET LOCAL statement after a UUID-shape check — Postgres parses
// SET LOCAL (not the prepared-stmt path), so a defence-in-depth identifier
// guard rejects anything that is not hex + dash before it reaches the wire.
func (q *PgxPoolQuerier) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, Tx) error) error {
	if err := validateTenantID(tenantID); err != nil {
		return err
	}
	if q.pool == nil {
		return errors.New("pg.RunInTenantTx: nil pool")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.RunInTenantTx: begin: %w", err)
	}
	// Roll back on any non-committed exit. After a successful Commit the
	// Rollback is a no-op (pgx returns ErrTxClosed, swallowed here).
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantID)); err != nil {
		return fmt.Errorf("pg.RunInTenantTx: SET LOCAL chora.tenant_id: %w", err)
	}

	// ADR-191 O3: propagate the caller's role set so the restrictive
	// exam_content_embargo policy can match a PROCTOR claim (no-op when the
	// ctx carries no roles).
	if err := applyUserRolesSession(ctx, func(sql string) error {
		_, e := tx.Exec(ctx, sql)
		return e
	}); err != nil {
		return fmt.Errorf("pg.RunInTenantTx: %w", err)
	}

	if err := fn(ctx, &pgxTx{tx: tx}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.RunInTenantTx: commit: %w", err)
	}
	committed = true
	return nil
}

// validateTenantID rejects values unsafe to interpolate into a SET LOCAL
// statement: anything other than hex digits + dash (UUID shape). Mirrors
// libs/chora-go-common/rls.ValidateTenantID without pulling that package
// into the adapter import graph for one helper.
func validateTenantID(id string) error {
	if id == "" {
		return errors.New("pg.RunInTenantTx: empty tenant_id")
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
			continue
		default:
			return fmt.Errorf("pg.RunInTenantTx: tenant_id %q has forbidden characters", id)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Role-claim GUC propagation (ADR-191 O3)
//
// chora_creation's `exam_content_embargo` RESTRICTIVE RLS policy (mig 0028)
// excludes a PROCTOR-claimed caller from SELECTing learning_atoms /
// atom_revisions by matching on `current_setting('chora.user_roles', ...)`.
// That GUC keys ONLY on the tenant today — so, mirroring
// libs/chora-go-common/rls.ApplySession's user_roles branch + chora-delivery's
// state-aware `courses` RLS, the tenant-tx seam ALSO emits
// `SET LOCAL chora.user_roles` from the caller's roles on ctx (bridged from
// `x-mesh-user-roles` by the tenantContext HTTP middleware via
// tracing.WithUserRoles). Backward-compatible: a ctx with no roles leaves the
// GUC unset (existing reads are unaffected; the restrictive policy COALESCEs
// unset → non-proctor → visible).
// -----------------------------------------------------------------------------

// userRolesSetLocalStmt returns the `SET LOCAL chora.user_roles = '<roles>'`
// statement for the caller's role set on ctx, and whether to emit it. Empty
// role set → ("", false, nil). Fails loud on an unsafe role string — SET LOCAL
// is parsed by Postgres (not the prepared-stmt path), so an injection-shaped
// value must never reach the wire.
func userRolesSetLocalStmt(ctx context.Context) (string, bool, error) {
	roles := tracing.UserRolesFromContext(ctx)
	if roles == "" {
		return "", false, nil
	}
	if err := validateUserRoles(roles); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("SET LOCAL chora.user_roles = '%s'", roles), true, nil
}

// applyUserRolesSession emits SET LOCAL chora.user_roles on the tx when the ctx
// carries a (validated) role set. No-op when no roles are present.
func applyUserRolesSession(ctx context.Context, exec func(sql string) error) error {
	stmt, emit, err := userRolesSetLocalStmt(ctx)
	if err != nil {
		return fmt.Errorf("invalid chora.user_roles: %w", err)
	}
	if !emit {
		return nil
	}
	if err := exec(stmt); err != nil {
		return fmt.Errorf("SET LOCAL chora.user_roles: %w", err)
	}
	return nil
}

// validateUserRoles rejects a role list containing characters unsafe to
// interpolate into a SET LOCAL statement. Mirrors
// libs/chora-go-common/rls.ValidateUserRoles (kept local to avoid pulling the
// rls package into the adapter import graph, matching the validateTenantID
// idiom above): the mesh role list is a comma-joined set of tokens (e.g.
// "instructor,proctor"); allowed characters are alphanumerics, dash,
// underscore, comma. Empty / whitespace-bearing values are rejected (the caller
// only emits the GUC for a non-empty, pre-sanitised list).
func validateUserRoles(roles string) error {
	if roles == "" {
		return errors.New("pg: empty user_roles")
	}
	for _, r := range roles {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r == '-', r == '_', r == ',':
			continue
		default:
			return fmt.Errorf("pg: user_roles %q has forbidden characters", roles)
		}
	}
	return nil
}

// pgxTx adapts pgx.Tx to the local Tx surface.
type pgxTx struct {
	tx pgx.Tx
}

func (t *pgxTx) Exec(ctx context.Context, sql string, args ...any) error {
	if _, err := t.tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("pg.Tx.Exec: %w", err)
	}
	return nil
}

func (t *pgxTx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rs, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pg.Tx.Query: %w", err)
	}
	return &pgxPoolRows{r: rs}, nil
}

func (t *pgxTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &pgxPoolRow{r: t.tx.QueryRow(ctx, sql, args...)}
}

// Compile-time check: PgxPoolQuerier satisfies TxQuerier.
var _ TxQuerier = (*PgxPoolQuerier)(nil)
