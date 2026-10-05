// bootstrap_helpers_test.go — unit coverage for the pure env-parsing +
// wiring helpers in bootstrap.go, main.go, embedindex_wiring.go and
// orphan_required_wiring.go. Every case is hermetic: no secret resolver, no
// pgx pool, no event bus — the helpers are exercised on their dev
// fall-through branches (env unset ⇒ nil) plus the pure-function paths.
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/embedindex"
)

// --- parseBool (bootstrap.go) ------------------------------------------------

func TestParseBool_TruthyAndFalsy(t *testing.T) {
	truthy := []string{"true", "TRUE", "True", "1", "yes", "YES", "on", "On", " true "}
	for _, v := range truthy {
		if !parseBool(v) {
			t.Errorf("parseBool(%q) = false, want true", v)
		}
	}
	falsy := []string{"", "false", "0", "no", "off", "banana", "  "}
	for _, v := range falsy {
		if parseBool(v) {
			t.Errorf("parseBool(%q) = true, want false", v)
		}
	}
}

// --- outboxWorkerID (bootstrap.go) -------------------------------------------

func TestOutboxWorkerID_EnvPrecedence(t *testing.T) {
	t.Setenv("CHORA_OUTBOX_WORKER_ID", "worker-7")
	t.Setenv("HOSTNAME", "pod-abc")
	if got := outboxWorkerID(); got != "worker-7" {
		t.Errorf("outboxWorkerID = %q, want worker-7 (explicit env wins)", got)
	}

	t.Setenv("CHORA_OUTBOX_WORKER_ID", "")
	if got := outboxWorkerID(); got != "pod-abc" {
		t.Errorf("outboxWorkerID = %q, want pod-abc (HOSTNAME fallback)", got)
	}

	t.Setenv("HOSTNAME", "")
	if got := outboxWorkerID(); got != "chora-creation-local" {
		t.Errorf("outboxWorkerID = %q, want chora-creation-local (constant fallback)", got)
	}
}

// --- envDurationOrZero (main.go) ---------------------------------------------

func TestEnvDurationOrZero(t *testing.T) {
	const key = "CHORA_TEST_ENV_DURATION"
	t.Setenv(key, "")
	if got := envDurationOrZero(key); got != 0 {
		t.Errorf("unset: envDurationOrZero = %v, want 0 (domain default)", got)
	}

	t.Setenv(key, " 45m ")
	if got := envDurationOrZero(key); got != 45*time.Minute {
		t.Errorf("valid: envDurationOrZero = %v, want 45m", got)
	}
	// The unparseable / non-positive branches log.Fatalf — untestable
	// without a production seam (subprocess harness excluded by scope).
}

// --- splitNonEmptyCSV (embedindex_wiring.go) ---------------------------------

func TestSplitNonEmptyCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , ,b ,, ", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitNonEmptyCSV(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitNonEmptyCSV(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitNonEmptyCSV(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// --- buildSavedAtomFetcher / buildSharingReuseClient (main.go) ---------------

func TestBuildSavedAtomFetcher_EnvGate(t *testing.T) {
	t.Setenv("SVC_SHARING_GRPC_URL", "")
	if got := buildSavedAtomFetcher(); got != nil {
		t.Errorf("unset env: buildSavedAtomFetcher = %v, want nil", got)
	}

	t.Setenv("SVC_SHARING_GRPC_URL", "sharing:50051")
	if got := buildSavedAtomFetcher(); got == nil {
		t.Error("set env: buildSavedAtomFetcher = nil, want wired client")
	}
}

func TestBuildSharingReuseClient_EnvGate(t *testing.T) {
	t.Setenv("SVC_SHARING_GRPC_URL", "")
	if got := buildSharingReuseClient(); got != nil {
		t.Errorf("unset env: buildSharingReuseClient = %v, want nil", got)
	}

	t.Setenv("SVC_SHARING_GRPC_URL", "sharing:50051")
	if got := buildSharingReuseClient(); got == nil {
		t.Error("set env: buildSharingReuseClient = nil, want wired client")
	}
}

// --- buildAtomEmbedIndexer / startAtomEmbeddingBackfill (embedindex_wiring.go)

func TestBuildAtomEmbedIndexer_NilRepoShortCircuits(t *testing.T) {
	// Nil repo ⇒ fail-soft nil regardless of env (the pgx pool is the gate).
	if got := buildAtomEmbedIndexer(context.Background(), nil); got != nil {
		t.Errorf("buildAtomEmbedIndexer(nil repo) = %v, want nil", got)
	}
}

func TestStartAtomEmbeddingBackfill_NilServiceAndNoTenants(t *testing.T) {
	// Nil service must return without spawning anything.
	startAtomEmbeddingBackfill(context.Background(), nil)

	// Wired service but no tenants env ⇒ still returns before the sweep.
	t.Setenv("ATOM_EMBEDDING_BACKFILL_TENANTS", "")
	svc := embedindex.New(nil, nil)
	startAtomEmbeddingBackfill(context.Background(), svc)
}

// --- bootstrap dev fall-through branches (bootstrap.go) ----------------------

func TestBootstrapDBPool_UnsetEnvReturnsNil(t *testing.T) {
	t.Setenv("CHORA_DB_DSN", "")
	t.Setenv("CHORA_DB_DSN_SECRET_ID", "")
	pool, shutdown := bootstrapDBPool(context.Background())
	if pool != nil || shutdown != nil {
		t.Error("bootstrapDBPool: want (nil, nil) on unset env")
	}
}

func TestBootstrapBus_UnsetEnvReturnsNil(t *testing.T) {
	t.Setenv("NATS_URL", "")
	bus, shutdown := bootstrapBus(context.Background())
	if bus != nil || shutdown != nil {
		t.Error("bootstrapBus: want (nil, nil) on unset env")
	}
}

func TestBootstrapOutboxDB_UnsetEnvReturnsNil(t *testing.T) {
	t.Setenv("CHORA_OUTBOX_DSN", "")
	t.Setenv("CHORA_OUTBOX_DSN_SECRET_ID", "")
	db, shutdown := bootstrapOutboxDB(context.Background())
	if db != nil || shutdown != nil {
		t.Error("bootstrapOutboxDB: want (nil, nil) on unset env")
	}
}

// --- sqlDBAdapter (bootstrap.go) ---------------------------------------------

func TestSQLDBAdapter_PassthroughSurfacesDriverError(t *testing.T) {
	// A *sql.DB against an unreachable DSN: sql.Open is lazy, so Exec /
	// Query surface the dial error — proving the adapter is a thin
	// passthrough that does not swallow driver failures.
	db, err := sql.Open("pgx", "postgres://127.0.0.1:1/chora_creation?connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	a := sqlDBAdapter{db: db}
	if _, err := a.ExecContext(context.Background(), "SELECT 1"); err == nil {
		t.Error("ExecContext: expected dial error on unreachable DSN, got nil")
	}
	rows, err := a.QueryContext(context.Background(), "SELECT 1")
	if err == nil {
		if rows != nil {
			_ = rows.Close()
		}
		t.Error("QueryContext: expected dial error on unreachable DSN, got nil")
	}
}

// --- orphanRequiredSubscriptionName (orphan_required_wiring.go) --------------

func TestOrphanRequiredSubscriptionName_DefaultAndOverride(t *testing.T) {
	t.Setenv("CHORA_ORPHAN_REQUIRED_SUBSCRIPTION", "")
	if got := orphanRequiredSubscriptionName(); got != pubsubadapter.DefaultOrphanRequiredSubscription {
		t.Errorf("default = %q, want %q", got, pubsubadapter.DefaultOrphanRequiredSubscription)
	}

	t.Setenv("CHORA_ORPHAN_REQUIRED_SUBSCRIPTION", "  custom-sub ")
	if got := orphanRequiredSubscriptionName(); got != "custom-sub" {
		t.Errorf("override = %q, want custom-sub", got)
	}
}

// --- registerOrphanRequiredSubscriber (orphan_required_wiring.go) ------------

func TestRegisterOrphanRequiredSubscriber_NilGuards(t *testing.T) {
	// Nil subscriber ⇒ loud skip.
	registerOrphanRequiredSubscriber(context.Background(), nil, nil)

	// Non-nil subscriber but nil bus ⇒ loud skip (dev in-memory bus).
	sub := pubsubadapter.NewOrphanRequiredSubscriber(pubsubadapter.OrphanRequiredConfig{})
	registerOrphanRequiredSubscriber(context.Background(), nil, sub)
}
