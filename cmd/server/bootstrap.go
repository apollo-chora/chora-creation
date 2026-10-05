// bootstrap.go — production wiring helpers for the chora-creation server.
//
// Per `feedback_resilience_priority` + secrets-and-env skill: every
// production dependency is sourced from env vars. Local dev sees nil pools /
// nil event buses so the server keeps the in-memory adapter fallback working
// out of the box.
//
// Environment contract (mirrors chora-identity):
//
//	CHORA_DB_DSN_SECRET_ID  — secret name resolving to a chora_creation
//	                          DSN (app_rw role).
//	CHORA_DB_DSN            — direct DSN (dev override; takes priority
//	                          over SECRET_ID).
//	CHORA_DB_PROJECT        — project label used for secret resolution.
//	                          Defaults to chora-local.
//	CHORA_DB_REWRITE_FROM_PORT — when set, rewrites DSN port (e.g. 6432
//	                             → 5432). Used to bypass a pooler until
//	                             the sidecar lands.
//	CHORA_DB_REWRITE_TO_PORT   — companion to FROM_PORT.
//
//	NATS_URL                — NATS JetStream broker URL for the event bus.
//	                          Unset → in-memory bus (dev fallback).
//
// All values empty in dev → fallthrough to in-memory adapters.
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	cgcdb "github.com/apollo-chora/chora-common/db"
	"github.com/apollo-chora/chora-common/eventbus"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"

	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
)

// bootstrapDBPool returns a pgxpool.Pool for chora_creation when the
// environment is configured for it; nil otherwise.
func bootstrapDBPool(ctx context.Context) (*pgxpool.Pool, func()) {
	dsn := os.Getenv("CHORA_DB_DSN")
	secretID := os.Getenv("CHORA_DB_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		log.Printf("creation: CHORA_DB_DSN / CHORA_DB_DSN_SECRET_ID unset — using in-memory repositories")
		return nil, nil
	}

	project := os.Getenv("CHORA_DB_PROJECT")
	if project == "" {
		project = "chora-local"
	}

	var fetcher cgcdb.SecretFetcher
	var sclient *cgcsecrets.Client
	if secretID != "" && dsn == "" {
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			log.Fatalf("creation: secret resolver init failed (env set, fail-loud): %v", err)
		}
		sclient = c
		fetcher = c
	}

	rewriteFrom, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_FROM_PORT"))
	rewriteTo, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_TO_PORT"))

	// Gate-7 fix: env-driven bootstrap context (default 30s). Under
	// concurrent 11-pod cold-start on GKE, Workload Identity → metadata-
	// server → sqladmin → cloudsql-proxy → Cloud SQL listener saturates
	// the metadata-server and Secret Manager fetch alone can exceed 30s.
	// Set CHORA_BOOTSTRAP_TIMEOUT_SECONDS=90 in the deployment env.
	bootstrapSecs, _ := strconv.Atoi(os.Getenv("CHORA_BOOTSTRAP_TIMEOUT_SECONDS"))
	if bootstrapSecs <= 0 {
		bootstrapSecs = 30
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, time.Duration(bootstrapSecs)*time.Second)
	defer cancel()

	pool, err := cgcdb.Bootstrap(bootstrapCtx, cgcdb.BootstrapOptions{
		DSN:             dsn,
		SecretID:        secretID,
		SecretFetcher:   fetcher,
		RewriteFromPort: rewriteFrom,
		RewriteToPort:   rewriteTo,
		AppName:         serviceName + "@" + version,
		RuntimeParams:   creationDBRuntimeParams(),
	})
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("creation: pgx pool bootstrap failed (env set, fail-loud — kubelet will CrashLoopBackOff): %v", err)
	}

	shutdown := func() {
		pool.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	}
	return pool, shutdown
}

// bootstrapBus returns a NATS JetStream eventbus when NATS_URL is set,
// otherwise nil (the caller falls back to the in-memory bus). The returned
// closure is the shutdown hook; caller defers it.
//
// Environment contract:
//
//	NATS_URL — JetStream broker URL (e.g. nats://127.0.0.1:4222).
//	           Unset → nil bus (in-process fallback; NOT durable).
func bootstrapBus(ctx context.Context) (eventbus.Bus, func()) {
	url := strings.TrimSpace(os.Getenv("NATS_URL"))
	if url == "" {
		return nil, nil
	}
	bus, err := eventbus.NewJetStream(eventbus.JetStreamConfig{URL: url})
	if err != nil {
		log.Printf("creation: NATS JetStream init failed: %v — falling back to in-memory bus", err)
		return nil, nil
	}
	return bus, func() { _ = bus.Close() }
}

// consumerConfig is the shared durable-consumer tuning for every
// chora-creation subscriber: at-least-once with a 30s ack window, five
// delivery attempts, and the canonical _dlq.<subject> dead-letter routing.
//
// The dotted subscription id is safe as Name — eventbus sanitises it to a
// NATS-legal durable name internally.
func consumerConfig(name, subject string) eventbus.ConsumerConfig {
	return eventbus.ConsumerConfig{
		Name:       name,
		Subject:    subject,
		MaxDeliver: 5,
		AckWait:    30 * time.Second,
		Backoff: []time.Duration{
			1 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
		},
		DLQSubject: eventbus.DLQSubject(subject),
	}
}

// -----------------------------------------------------------------------------
// M12.3 W1.5a — per-domain outbox bootstrap helpers.
//
// The per-domain outbox PostgresStore (services/chora-creation/internal/adapter/
// outbox.PostgresStore) writes / reads via the database/sql driver — distinct
// from the legacy cgcoutbox.PostgresRecorder above (which is pgxpool-bridged
// and is being phased out). Production wires both to the same outbox_events
// table in chora_creation (D6 columns added by migration 0004_outbox_d6.sql).
//
// Env contract additions:
//
//   CHORA_OUTBOX_DSN        — direct DSN to chora_creation for the per-domain
//                             outbox PostgresStore. Empty → in-memory store
//                             (dev fallback).
//   CHORA_OUTBOX_WORKER_ID  — dispatcher worker_id stamped onto deadletter
//                             rows; defaults to HOSTNAME, then a chora-creation
//                             constant.
// -----------------------------------------------------------------------------

// bootstrapOutboxDB opens a *sql.DB connection to chora_creation backing the
// outbox Dispatcher + the non-transactional producers (M12.3 W1.5a). Returns
// (nil, nil) when CHORA_OUTBOX_DSN is unset — main() then falls back to the
// in-memory store.
//
// ⚠ What this connection is NOT, since 2026-08-07: it is no longer on the atom
// request path. The atom handlers write their outbox row through the request
// pool inside the atom's own transaction (creationpg.AtomTxWriter +
// creationoutbox.TxStore), because writing it here made atomicity structurally
// impossible: two pools cannot share a commit. Chaos run chaos20260807a
// measured the cost of that split at 20 of 747 atoms (2.68%) committed with no
// event. Do not route a producer that HAS a transaction back onto this
// connection.
//
// It survives because two callers legitimately have no transaction to join:
//
//  1. the Dispatcher, a background drain loop whose claim / publish / complete
//     statements must never run inside a producer's request transaction; and
//  2. the non-transactional producers (collection + question-batch publishers,
//     the orphan and atoms-ready subscribers, the operator re-emit backfills).
//
// Two-step Open keeps the binary driver-agnostic (pgx vs lib/pq) — whichever
// driver is registered at compile time wins.
func bootstrapOutboxDB(ctx context.Context) (*sql.DB, func()) {
	dsn := os.Getenv("CHORA_OUTBOX_DSN")
	secretID := os.Getenv("CHORA_OUTBOX_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		return nil, nil
	}

	var sclient *cgcsecrets.Client
	if dsn == "" {
		project := os.Getenv("CHORA_DB_PROJECT")
		if project == "" {
			project = "chora-local"
		}
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			log.Fatalf("creation: outbox secret resolver init failed (env set, fail-loud): %v", err)
		}
		sclient = c
		bootstrapSecs, _ := strconv.Atoi(os.Getenv("CHORA_BOOTSTRAP_TIMEOUT_SECONDS"))
		if bootstrapSecs <= 0 {
			bootstrapSecs = 30
		}
		resolveCtx, cancel := context.WithTimeout(ctx, time.Duration(bootstrapSecs)*time.Second)
		defer cancel()
		resolved, err := c.GetSecret(resolveCtx, secretID)
		if err != nil {
			_ = c.Close()
			log.Fatalf("creation: outbox secret fetch %q failed (env set, fail-loud): %v", secretID, err)
		}
		dsn = resolved
	}

	if rewriteFrom, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_FROM_PORT")); rewriteFrom != 0 {
		if rewriteTo, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_TO_PORT")); rewriteTo != 0 {
			rewritten, err := cgcdb.RewriteDSNPort(dsn, rewriteFrom, rewriteTo)
			if err != nil {
				if sclient != nil {
					_ = sclient.Close()
				}
				log.Fatalf("creation: outbox DSN port rewrite failed (env set, fail-loud): %v", err)
			}
			dsn = rewritten
		}
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		db, err = sql.Open("postgres", dsn)
	}
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("creation: outbox sql.Open failed (env set, fail-loud): %v", err)
	}
	if pingErr := db.PingContext(ctx); pingErr != nil {
		_ = db.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("creation: outbox db.Ping failed (env set, fail-loud): %v", pingErr)
	}
	return db, func() {
		_ = db.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	}
}

// parseBool returns true when v is a truthy string ("true", "1",
// "yes", "on"); otherwise false. Used for the
// AI_KERNEL_ORCHESTRATOR_ENABLED feature flag (CHO-1533) so operators
// can flip the orchestrator path via ConfigMap without a code change.
// Defaults to false on empty / unrecognised input, preserving the
// QGen-direct path as the production default.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// outboxWorkerID derives the dispatcher worker_id from CHORA_OUTBOX_WORKER_ID
// or HOSTNAME. Returns the chora-creation-local fallback when neither is set
// (the dispatcher requires a non-empty worker_id at construction).
func outboxWorkerID() string {
	if v := os.Getenv("CHORA_OUTBOX_WORKER_ID"); v != "" {
		return v
	}
	if v := os.Getenv("HOSTNAME"); v != "" {
		return v
	}
	return "chora-creation-local"
}

// sqlDBAdapter bridges *sql.DB (driver-typed *sql.Rows) to the per-domain
// creationoutbox.SQLDB interface (which uses creationoutbox.SQLRows so tests
// can stub). *sql.Rows already satisfies SQLRows's method set (Next + Scan
// + Close + Err) so QueryContext is a thin passthrough.
type sqlDBAdapter struct {
	db *sql.DB
}

func (a sqlDBAdapter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return a.db.ExecContext(ctx, query, args...)
}

func (a sqlDBAdapter) QueryContext(ctx context.Context, query string, args ...any) (creationoutbox.SQLRows, error) {
	return a.db.QueryContext(ctx, query, args...)
}

// Compile-time check: sqlDBAdapter satisfies creationoutbox.SQLDB.
var _ creationoutbox.SQLDB = sqlDBAdapter{}

// creationDBRuntimeParams returns the per-connection Postgres GUCs that keep a
// DB write from hanging forever (fail-loud). Platform-wide rollout of the
// CHO-2005 fix (2026-07-04); mirrors chora-consumption's
// consumptionDBRuntimeParams. Set on every pooled connection via
// BootstrapOptions.RuntimeParams.
//
//   - lock_timeout=3s: a statement blocked on a row lock ERRORs ("canceling
//     statement due to lock timeout") instead of waiting indefinitely and
//     leaking the request goroutine — under the gateway's 6s per-call
//     timeout, so this service fails loud (500) before the gateway 504s.
//   - idle_in_transaction_session_timeout=60s: reaps a leaked open
//     transaction so its row locks release.
//
// No statement_timeout (owner steer): long read paths must not be capped.
func creationDBRuntimeParams() map[string]string {
	return map[string]string{
		"lock_timeout":                        "3s",
		"idle_in_transaction_session_timeout": "60s",
	}
}
