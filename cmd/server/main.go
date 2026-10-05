// Package main is the chora-creation service entrypoint.
//
// Service: chora-creation (Content Creation domain, surface A+ Creator mode)
// Project: chora-content (Team 1 / Content)
// Topic prefix: chora.creation.*
//
// Wires:
//   - chora-common/observability OTLP middleware on every HTTP route
//   - chora-common/auth/servicemesh metadata propagation
//   - in-memory repos + media stub signer (production swaps for pgx + S3)
//   - in-memory event publisher (production swaps for NATS JetStream)
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	"github.com/apollo-chora/chora-common/durabilityguard"
	"github.com/apollo-chora/chora-common/eventbus"
	commonobs "github.com/apollo-chora/chora-common/observability"
	// pgx stdlib driver — registered for sql.Open("pgx", dsn) used by
	// the per-domain outbox PostgresStore in bootstrap.go.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/adapter/events"
	creationgrpc "github.com/apollo-chora/chora-creation/internal/adapter/grpc"
	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/adapter/modelbroker"
	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	creationpg "github.com/apollo-chora/chora-creation/internal/adapter/pg"
	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// buildSavedAtomFetcher wires the chora-sharing gRPC client for the question
// picker's `saved` disjunct (ux_unified_atom_picker.md). Returns nil when
// SVC_SHARING_GRPC_URL is unset — the handler then returns empty for
// `source=saved` (dev / tests without sharing wired).
func buildSavedAtomFetcher() ports.SavedAtomIDFetcher {
	sharingGRPCURL := strings.TrimSpace(os.Getenv("SVC_SHARING_GRPC_URL"))
	if sharingGRPCURL == "" {
		log.Printf("creation: SVC_SHARING_GRPC_URL unset — saved-atom picker disjunct NOT wired (source=saved returns empty)")
		return nil
	}
	return clients.NewSavedAtomClient(clients.SavedAtomClientConfig{
		GRPCAddr: sharingGRPCURL,
	})
}

// buildSharingReuseClient wires the ADR-229 reuse-consent gRPC client
// (GetReuseContext + AuthorizeAtomUse) for the WS-2 chokepoints (CHO-2133).
// Returns nil when SVC_SHARING_GRPC_URL is unset — the picker then FAILS
// LOUD on source=all/saved (500 CREATION_REUSE_CONTEXT_NOT_WIRED) and the
// snapshot gate refuses non-owner snapshots (FAILED_PRECONDITION): an
// unwired consent gate must never silently narrow or fall back wide-open.
func buildSharingReuseClient() *clients.SharingReuseClient {
	sharingGRPCURL := strings.TrimSpace(os.Getenv("SVC_SHARING_GRPC_URL"))
	if sharingGRPCURL == "" {
		log.Printf("creation: SVC_SHARING_GRPC_URL unset — ADR-229 reuse-consent gate NOT wired (consent-gated picker searches + non-owner snapshots will refuse LOUD)")
		return nil
	}
	return clients.NewSharingReuseClient(clients.SharingReuseClientConfig{
		GRPCAddr: sharingGRPCURL,
	})
}

const (
	serviceName = "chora-creation"
	version     = "0.1.0"

	// defaultTopicClassifierModel — the logical model the CHO-2142 topic
	// classifier requests from chora-model-gateway when
	// CHORA_TOPIC_CLASSIFIER_MODEL is unset. Topic classification is a short,
	// structured-output task; flash is sufficient and cheap (it is also the
	// gateway's DefaultGroundedModel). The gateway's policy loader may still
	// resolve a different model — it owns the final routing decision.
	defaultTopicClassifierModel = "gemini-2.5-flash"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ----------------------------------------------------------------------
	// OTLP wiring per Tier 3 D13 — direct to Cloud Trace in prod, stdout in dev.
	//
	// Per C(a).S1 path (b) — tracker #151 — OTLP init runs in its own
	// goroutine with its own (env-tunable, default 15s) deadline + fail-
	// soft semantics. Timeout / init-error degrade to a no-op shutdown,
	// so the rest of bootstrap (pgx pool, Pub/Sub clients) gets the FULL
	// CHORA_BOOTSTRAP_TIMEOUT_SECONDS budget. Previously a slow Cloud
	// Trace TLS handshake could swallow the shared budget and crash-
	// loop the pod under PgBouncer 4-container cold-start.
	otlpHandle := commonobs.InitOTLPAsync(ctx, serviceName, version)
	defer func() {
		// handle.WaitContext blocks until init settles — usually a no-op by
		// shutdown time because pgx-pool init below already gave OTLP
		// best-effort wall-clock to land.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res := otlpHandle.WaitContext(shutdownCtx)
		if err := res.Shutdown(shutdownCtx); err != nil {
			log.Printf("trace shutdown error: %v", err)
		}
	}()

	// ----------------------------------------------------------------------
	// Repository wiring — pgx-backed when CHORA_DB_DSN(_SECRET_ID) is set,
	// in-memory fallback otherwise. Per feedback_resilience_priority the
	// pgx adapter is the production primary; the in-memory adapter remains
	// available for local dev when no DB is wired.
	// ----------------------------------------------------------------------
	inmemAtoms := inmem.NewAtomRepository()
	mediaRepo := inmem.NewMediaRepository()

	var atomRepo atom.Repository = inmemAtoms
	// P3 — Question Authoring CR. The repository is wired alongside AtomRepository
	// when the pgx pool is available. No in-memory fallback for the production
	// path (per "no stubs, real wiring" — feedback_no_stubs_real_wiring): when
	// CHORA_CREATION_DB_DSN is unset the questions handler still mounts but the
	// pgx repo is nil and the routes return 503 CREATION_QUESTIONS_NOT_WIRED
	// (fail loud).
	var questionRepo ports.QuestionRepository
	pool, poolShutdown := bootstrapDBPool(ctx)
	if poolShutdown != nil {
		defer poolShutdown()
	}
	var questionJobRepo ports.QuestionJobRepository
	// OE-AI-ASSIST (Step 4c) — async jobs repo wired alongside other pg
	// repos. Nil when pool is unavailable; the handler 503s in that case.
	var aiAssistJobsRepo ports.AiAssistJobsRepository

	// WS-6a-wiring — CollectionRepository. pgx-backed when the pool is present;
	// nil otherwise, which 404s the collection routes (fail-loud).
	// CollectionPublisher is wired later, after outboxStore is constructed.
	//
	// There was a CollectionAtomLookup alongside this. It is GONE (ADR-233 D7):
	// its pg adapter read learning_atoms with no tenant filter and outside
	// RunInTenantTx, which RLS answered with 22P02 on every call. Atom existence
	// now rides the tenant-scoped ReuseFacts read below.
	var collectionRepo collection.Repository
	// collectionRepoPG is the CONCRETE pg repo, held separately because it also
	// serves collection.AtomReuseFactLookup (ADR-233 WS-4) — a port the inmem
	// fallback cannot honour (it has no learning_atoms).
	var collectionRepoPG *creationpg.CollectionRepository

	// W3.B.1-wiring — QuestionBank (reusable question-atom pool, 2026-06-28).
	// Both deps are pgx-backed and nil without a pool (routes 404 — fail-loud).
	// questionBankQuestionLookup reuses the (pgx) QuestionRepository GetByID so the
	// question-existence check runs inside the same tenant-scoped RLS tx. The
	// QuestionBank EventPublisher is UNWIRED in B.1 (event emission deferred until
	// the chora.creation.question_bank.* contracts exist).
	var questionBankRepo questionbank.Repository
	var questionBankItemPager questionbank.ItemPager
	var questionBankQuestionLookup questionbank.QuestionLookup

	// Epic-1b W4 — atom_embeddings repo (writer + semantic search). Nil
	// without a pool; the ContentRetrieval RPC then FAILED_PRECONDITIONs.
	var atomEmbeddingRepo *creationpg.AtomEmbeddingRepository

	// Lane 1c (CHO-1703 / ADR-180 D9) — source_material_chunks store. Nil
	// without a pool; the batch handler then skips extraction (best-effort).
	var sourceChunkRepo ports.SourceChunkRepository

	// ADR-229 A1 (CHO-2132) — concrete pg repos for the orphan-mint
	// subscriber (MintOrphan / GetAnyState / RepointCollections /
	// GetByAtomIDAnyState live off the ports; pg mode only).
	var (
		orphanAtoms     pubsubadapter.OrphanAtomRepository
		orphanQuestions pubsubadapter.OrphanQuestionRepository
	)

	// CHO-2159 — durable run report for the async topic-tag backfill. pg-only by
	// construction: the run is executed DETACHED and takes ~280s for 74 atoms, so
	// it outlives a pod restart and the operator's poll can land on another
	// replica. An in-memory record would reintroduce the exact unobservability
	// the story fixes, so there is deliberately NO inmem fallback — without a
	// pool the backfill routes fail loud rather than mutate atoms unobserved.
	var backfillRuns atom.BackfillRunStore

	// Topic tree (CHO-2275, Sub-phase A). pg-only by construction: topic_nodes is
	// RLS-protected and an in-memory tree would 404 the moment the pod restarts
	// and mislead the FE — so there is no inmem fallback. Nil without a pool ⇒ the
	// /api/topics routes 404 (fail-loud). topicSeedSink is the SAME concrete repo
	// (it also satisfies topic.SeedSink); topicTagSource reads learning_atoms.tags.
	var (
		topicRepo      topic.Repository
		topicSeedSink  topic.SeedSink
		topicTagSource topic.AtomTagSource
	)

	if pool != nil {
		querier := creationpg.NewPgxPoolQuerier(pool)
		backfillRuns = creationpg.NewAtomBackfillRunRepository(querier)
		pgAtomRepo := creationpg.NewAtomRepository(querier)
		pgTopicRepo := creationpg.NewTopicRepository(querier)
		topicRepo = pgTopicRepo
		topicSeedSink = pgTopicRepo
		topicTagSource = creationpg.NewAtomTagSource(querier)
		pgQuestionRepo := creationpg.NewQuestionRepository(querier)
		atomRepo = pgAtomRepo
		atomEmbeddingRepo = creationpg.NewAtomEmbeddingRepository(querier)
		questionRepo = pgQuestionRepo
		orphanAtoms = pgAtomRepo
		orphanQuestions = pgQuestionRepo
		questionJobRepo = creationpg.NewQuestionJobRepository(querier)
		aiAssistJobsRepo = creationpg.NewAiAssistJobsRepository(querier)
		sourceChunkRepo = creationpg.NewSourceChunkRepository(querier)
		// WS-6a-wiring: upgrade collection deps to pgx adapters.
		pgCollectionRepo := creationpg.NewCollectionRepository(querier)
		collectionRepo = pgCollectionRepo
		// ADR-233 WS-4: the SAME concrete repo also serves the reuse-consent
		// gate's creation-local half (ONE batch read over learning_atoms). It is
		// pg-only by construction — the inmem fallback has no learning_atoms to
		// read, and a gate that cannot read the author's audience flag must not
		// pretend to enforce it.
		collectionRepoPG = pgCollectionRepo
		// W3.B.1-wiring: QuestionBank repo + QuestionLookup (reuses questionRepo).
		qbRepo := creationpg.NewQuestionBankRepository(querier)
		questionBankRepo = qbRepo
		questionBankItemPager = qbRepo // same concrete repo implements ItemPager (JOIN)
		questionBankQuestionLookup = creationpg.NewQuestionLookupAdapter(questionRepo)
		log.Printf("creation: pgx QuestionBankRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx QuestionLookupAdapter (question bank) wired (pool=chora_creation)")
		log.Printf("creation: QuestionBank event publisher UNWIRED — emission deferred (W3.B.1)")
		log.Printf("creation: pgx AtomRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx QuestionRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx QuestionJobRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx AiAssistJobsRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx AtomBackfillRunRepository wired (pool=chora_creation) — CHO-2159 async backfill reports")
		log.Printf("creation: pgx TopicRepository + AtomTagSource wired (pool=chora_creation) — CHO-2275 topic tree")
		log.Printf("creation: pgx SourceChunkRepository wired (pool=chora_creation)")
		log.Printf("creation: pgx CollectionRepository wired (pool=chora_creation)")
	} else {
		log.Printf("creation: pool unavailable — CollectionRepository NOT wired (collection routes will 404)")
	}

	// Phyllis MVP wiring (docs/m13/phyllis-mvp-2026-05-08.md):
	//   - chora-model-broker-router URL from env (no inline config per
	//     feedback_no_inline_config memory).
	//   - Cloud Pub/Sub via outbox when DB pool + CHORA_PUBSUB_PROJECT both
	//     wired; in-memory recorder fallback for local dev.
	//   - Media-asset signer is in-memory stub for MVP — real Cloud Storage
	//     V4 signing lands at M12 alongside chora-content GCS bucket.
	brokerURL := os.Getenv("SVC_MODEL_BROKER_ROUTER_URL")
	brokerClient := modelbroker.NewClient(brokerURL)
	mediaBucket := os.Getenv("CHORA_CONTENT_MEDIA_BUCKET") // empty -> stub
	mediaSigner := storage.NewInMemorySigner(mediaBucket)

	// ----------------------------------------------------------------------
	// Event bus — NATS JetStream via chora-common/eventbus when NATS_URL is
	// set; in-memory fallback otherwise. The bus is the publisher for the
	// outbox dispatcher AND the subscriber transport for every consumer
	// wired below.
	// ----------------------------------------------------------------------
	bus, busShutdown := bootstrapBus(ctx)
	if busShutdown != nil {
		defer busShutdown()
	}
	if bus == nil {
		log.Printf("creation: NATS_URL unset — event bus NOT wired (in-memory fallback; subscribers disabled)")
	}

	// ----------------------------------------------------------------------
	// D6.2 producer-side outbox wiring (M12.3 Wave 1 + W1.5a bootstrap).
	//
	// Per `feedback_d6_resilience_first_class` + `agentic-resilience-d6`
	// skill Pillar 2. The per-domain Publisher writes atom events to
	// outbox_events; the Dispatcher drains to Cloud Pub/Sub on a background
	// goroutine. Schema: migrations/0003_outbox.sql + 0004_outbox_d6.sql.
	//
	// Fallback ladder:
	//   1. pool == nil                       → events.InMemoryPublisher
	//      (dev: tests inspect emitted events directly).
	//   2. pool != nil, outboxDB == nil      → outbox.Publisher backed by
	//      outbox.InMemoryStore (dev with pool — outbox accumulates in
	//      process memory; NOT durable across restart).
	//   3. pool != nil, outboxDB != nil      → outbox.Publisher backed by
	//      outbox.PostgresStore (production posture).
	//
	// Dispatcher goroutine starts ONLY in state 3 (durable store +
	// Cloud Pub/Sub bus both wired). State 2's rows are visible to tests
	// via the Store but never reach Pub/Sub — acceptable for dev, NOT
	// acceptable for production.
	// ----------------------------------------------------------------------
	var publisher atom.EventPublisher = events.NewInMemoryPublisher()
	var outboxStore creationoutbox.Store
	var outboxDispatcher *creationoutbox.Dispatcher
	var dispatcherDone chan struct{}

	outboxDB, outboxDBShutdown := bootstrapOutboxDB(ctx)
	if outboxDBShutdown != nil {
		defer outboxDBShutdown()
	}

	if pool != nil {
		if outboxDB != nil {
			outboxStore = creationoutbox.NewPostgresStore(
				sqlDBAdapter{db: outboxDB},
				creationoutbox.PostgresStoreOptions{WorkerID: outboxWorkerID()},
			)
			log.Printf("creation: outbox PostgresStore wired (worker_id=%s)", outboxWorkerID())
		} else {
			outboxStore = creationoutbox.NewInMemoryStore()
			log.Printf("creation: outbox InMemoryStore wired (CHORA_OUTBOX_DSN unset; NOT durable across restart)")
		}
		sourceProject := os.Getenv("CHORA_SOURCE_PROJECT")
		if sourceProject == "" {
			sourceProject = "chora-local"
		}
		publisher = creationoutbox.NewPublisher(creationoutbox.PublisherConfig{
			Store:         outboxStore,
			SourceProject: sourceProject,
			SourceService: serviceName,
		})

		// Dispatcher: drain outbox to the event bus on a background goroutine.
		// Only starts when both bus + durable PostgresStore are wired — the
		// InMemoryStore + nil bus combo would leak rows to /dev/null on
		// shutdown.
		if bus != nil && outboxDB != nil {
			outboxDispatcher = creationoutbox.NewDispatcher(creationoutbox.DispatcherConfig{
				Store:    outboxStore,
				Bus:      bus,
				WorkerID: outboxWorkerID(),
			})
			dispatcherDone = make(chan struct{})
			go func() {
				defer close(dispatcherDone)
				if err := outboxDispatcher.Run(ctx, 100); err != nil &&
					!errors.Is(err, context.Canceled) &&
					!errors.Is(err, context.DeadlineExceeded) {
					log.Printf("creation: outbox dispatcher exited: %v", err)
				}
			}()
			log.Printf("creation: outbox dispatcher goroutine started (batch=100)")
		} else if bus == nil {
			log.Printf("creation: NATS_URL unset — outbox dispatcher NOT started; rows accumulate")
		}
	}

	// ----------------------------------------------------------------------
	// Atomic state-write + event-publish (the envelope invariant in
	// CLAUDE.md), 2026-08-07.
	//
	// The atom write path used to persist the aggregate on `pool` and then
	// enqueue its outbox row on a SECOND, separate *sql.DB, logging and
	// discarding a failure on the second. Chaos run chaos20260807a killed the
	// sole pod under 64 concurrent writers and measured the result: 20 of 747
	// atoms (2.68%) committed with no event, ten of them after the handler had
	// already answered HTTP 201. Both DSNs resolve to the same secret
	// (chora-dev-cloudsql-chora_creation-app_rw-dsn), so the split bought
	// nothing and cost atomicity.
	//
	// The writer below closes it: the learning_atoms UPSERT and the
	// outbox_events INSERT run inside ONE tenant-scoped transaction on the
	// request pool. Either both commit or neither does, and the handler
	// answers 500 rather than a false 201.
	//
	// The dispatcher keeps its own connection deliberately (see
	// bootstrapOutboxDB): a background drain loop must never run inside a
	// producer's request transaction.
	// ----------------------------------------------------------------------
	var atomWriter atom.TransactionalPublisher
	if pool != nil {
		sourceProject := os.Getenv("CHORA_SOURCE_PROJECT")
		if sourceProject == "" {
			sourceProject = "chora-local"
		}
		atomWriter = creationpg.NewAtomTxWriter(
			creationpg.NewPgxPoolQuerier(pool),
			func(tx creationpg.Tx) atom.EventPublisher {
				return creationoutbox.NewPublisher(creationoutbox.PublisherConfig{
					Store:         creationoutbox.NewTxStore(tx),
					SourceProject: sourceProject,
					SourceService: serviceName,
				})
			},
		)
		log.Printf("creation: atom writer = pg.AtomTxWriter (learning_atoms + outbox_events in ONE transaction on the request pool)")
	} else {
		// No pool means no transaction to join. The sequential writer still
		// fails loud on an enqueue error instead of fabricating a success,
		// but it cannot roll the save back: dev / in-memory wiring only.
		atomWriter = atom.NewSequentialPublisher(atomRepo, publisher)
		log.Printf("creation: pool unavailable, atom writer = NON-ATOMIC sequential writer (in-memory dev wiring)")
	}

	// Federated closure-saga subscriber (CHO-1719 / Tier 3 D11): consumes
	// chora.creation.pii.pseudonymise.requested.v1, applies the per-domain
	// PII_Closure_Map.yaml duty, and acks on
	// chora.creation.account.pseudonymised.v1.
	//
	// Repo seam (CHO-2198, W0-F1 durability + W0-F5 error-honesty): pg on a
	// healthy pool (durable ack/dedup — migration 0033,
	// closure_pseudonymisation_state), in-memory ONLY when the pool is
	// absent, mirroring the chora-payments else-branch shape. Before this
	// fix the repo was UNGATED — gated on pubsubClient only, never on pool
	// health — so the ack/dedup state was lost on every pod restart even
	// with a healthy chora_creation pool (see
	// docs/references/w0-f1-inmemory-inventory.md §6 item 4). Real
	// per-table pg tokenisation (actually redacting learning_atoms /
	// atom_revisions / ... columns) remains separate, deeper M12+ debt —
	// this fix is durability of the ack/dedup SIGNAL only, not the
	// redaction itself. The durable consumer is created by the event bus;
	// override the name via env.
	//
	// closureRepo is hoisted to function scope so the W0-F1 durability guard
	// (below, after Deps) can classify it alongside the other repos. It stays
	// nil when the bus is absent (closure subscriber unwired) — the guard
	// reports nil as UNKNOWN, never a violation (mirrors chora-delivery D5).
	var closureRepo events.ClosureRepository
	if bus != nil {
		piiPath := os.Getenv("CHORA_PII_CLOSURE_MAP_PATH")
		if piiPath == "" {
			piiPath = "config/PII_Closure_Map.yaml"
		}
		closureAckPub := eventbus.NewClosureAckPublisher(
			bus,
			sourceProjectOrLocal(),
			"chora-creation",
		)
		if pool != nil {
			closureRepo = creationpg.NewClosureRepository(creationpg.NewPgxPoolQuerier(pool))
			log.Printf("creation: pg ClosureRepository wired (table=closure_pseudonymisation_state)")
		} else {
			closureRepo = events.NewInMemoryClosureRepo()
			log.Printf("creation: CHORA_DB_DSN unset — closure repo uses in-memory store (NOT durable across restart)")
		}
		if closureSub, err := events.BootstrapClosureSubscriber(piiPath, closureRepo, closureAckPub, nil); err != nil {
			log.Printf("creation: closure subscriber DISABLED (PII map load: %v)", err)
		} else {
			closureSubName := os.Getenv("CHORA_CLOSURE_SUBSCRIPTION")
			if closureSubName == "" {
				closureSubName = "chora-creation.closure-pseudonymise"
			}
			go func() {
				log.Printf("creation: closure subscriber binding %s -> %s", closureSubName, events.TopicPseudonymiseRequested)
				if err := bus.Subscribe(ctx, consumerConfig(closureSubName, events.TopicPseudonymiseRequested), events.ClosurePullHandler(closureSub)); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("creation: closure subscriber exited: %v", err)
				}
			}()
		}
	}

	// WS-6a-wiring — CollectionPublisher.
	//
	// The CollectionOutboxPublisher reuses the shared outboxStore that the
	// atom publisher already drains. No separate dispatcher goroutine is
	// needed — the existing outbox.Dispatcher handles collection event rows
	// alongside atom event rows (aggregate_type = "collection" is
	// distinguishable; the dispatcher routes by topic prefix).
	//
	// Fail-loud rule (feedback_no_stubs_real_wiring): when the collection
	// publisher cannot be constructed (outboxStore nil because pool was not
	// wired), the RouterDeps field stays nil and the handler logs a gap
	// at startup (handler.go line 244 comment). This is intentional — events
	// are nil-tolerant per collection_handler.go design, so the endpoints
	// still serve real data without event emission in dev mode.
	var collectionPublisher collection.EventPublisher
	if outboxStore != nil {
		sourceProject := os.Getenv("CHORA_SOURCE_PROJECT")
		if sourceProject == "" {
			sourceProject = "chora-local"
		}
		collectionPublisher = pubsubadapter.NewCollectionOutboxPublisher(pubsubadapter.CollectionOutboxConfig{
			Store:         outboxStore,
			SourceProject: sourceProject,
			SourceService: serviceName,
		})
		log.Printf("creation: CollectionOutboxPublisher wired (outboxStore ready)")
	} else {
		log.Printf("creation: CollectionOutboxPublisher NOT wired — outboxStore nil (collection events will be dropped)")
	}

	// Lane 1c W3 (CHO-1703 / ADR-180 D10) — question_batch.accepted.v1
	// outbox publisher. Shares the same outboxStore + Dispatcher as the
	// atom/collection rows; the payload is BINARY-encoded at insert (the
	// in_app.created dead-letter lesson). Nil ⇒ accepts carrying a test_set
	// block 503 fail-loud (the plain accept path is unaffected).
	var questionBatchPublisher ports.QuestionBatchAcceptedPublisher
	if outboxStore != nil {
		sourceProject := os.Getenv("CHORA_SOURCE_PROJECT")
		if sourceProject == "" {
			sourceProject = "chora-local"
		}
		questionBatchPublisher = pubsubadapter.NewQuestionBatchOutboxPublisher(pubsubadapter.QuestionBatchOutboxConfig{
			Store:         outboxStore,
			SourceProject: sourceProject,
			SourceService: serviceName,
		})
		log.Printf("creation: QuestionBatchOutboxPublisher wired (outboxStore ready)")
	} else {
		log.Printf("creation: QuestionBatchOutboxPublisher NOT wired — outboxStore nil (test_set accepts will 503)")
	}

	// W3.B.2 — assemble-TestSet-from-QuestionBank reuses the SAME
	// question_batch.accepted.v1 outbox publisher (no new event/topic/proto). The
	// thin adapter maps questionbank.AssembledTestSet → ports.QuestionBatchAcceptedEvent.
	// Nil when the batch publisher is nil ⇒ POST .../assemble-test-set 503s
	// (fail-loud, mirrors the test_set-accept gap).
	var questionBankTestSetPublisher questionbank.TestSetAssemblyPublisher
	if questionBatchPublisher != nil {
		questionBankTestSetPublisher = pubsubadapter.NewQuestionBankTestSetAssembler(questionBatchPublisher)
		log.Printf("creation: QuestionBank TestSetAssembler wired (reuses question_batch.accepted.v1 outbox)")
	} else {
		log.Printf("creation: QuestionBank TestSetAssembler NOT wired — questionBatchPublisher nil (assemble-test-set will 503)")
	}

	// AI Assist subscriber per docs/m13/audit-content-fillgaps.md §3.1 +
	// CHO-1465 AC #3: persists atoms emitted by chora-ai-kernel-orchestrator
	// on chora.ai_kernel.crew.atoms_ready.v1.
	//
	// Idempotent on event_id. The subscriber is constructed here so the
	// Pub/Sub Receiver wiring (M10 dependency) only needs to call
	// `aiAssistSub.Handle(ctx, ev)` once per inbound message.
	//
	// Until Cloud Pub/Sub topics are provisioned (M10 Foundation Infra),
	// this subscriber is unsubscribed (no Receive loop) — it stays callable
	// from tests + future receiver wiring.
	// nil inbox triggers the defensive MemoryStore fallback in
	// NewAtomsReadySubscriber. When the M10 Pub/Sub Receive loop lands,
	// swap this for an idempotent.PostgresStore against chora_creation
	// idempotency_keys (same pattern as the closure subscriber inbox).
	aiAssistSub := atom.NewAtomsReadySubscriber(atomRepo, publisher, nil)
	_ = aiAssistSub // silence unused-var until M10 Receive loop lands

	// POST /api/atoms/ai-assist is served exclusively by the GKE qgen crew
	// (async). The legacy Vertex AI Agent Engine sync clients (QGen + AI
	// Kernel Orchestrator, us-central1) were retired with the engine
	// decommission (ADR-169; CHO-1920) — both pointed at dead engines and
	// were unreachable behind the crew dispatch.

	// ----------------------------------------------------------------------
	// P4+P5 — AI single-question async path wiring (golden-hopping-owl plan).
	//
	// Mana ledger gRPC client → chora-identity ManaService.
	// QGen question client    → Vertex AI Agent Engine qgen_pipeline.
	// Job event publisher     → bridges to the existing outbox publisher
	//                            so generation_requested events drain to
	//                            Pub/Sub via the same pipe as atom events.
	// Question subscriber     → goroutine listening on
	//                            chora.creation.question.generation_requested.v1
	//                            (production wires a real Pub/Sub receiver;
	//                            until M10 Pub/Sub subscriptions land, the
	//                            subscriber is constructed but not driven).
	// ----------------------------------------------------------------------
	identityGRPCURL := strings.TrimSpace(os.Getenv("SVC_IDENTITY_GRPC_URL"))
	var manaClient ports.ManaLedger
	var grpcConn *grpc.ClientConn
	if identityGRPCURL != "" {
		// Dial chora-identity over the service mesh. mTLS is enforced by
		// Cloud Service Mesh + SPIFFE identity; on the dial side we use
		// insecure transport because the mesh proxy terminates TLS.
		conn, err := grpc.NewClient(identityGRPCURL,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			log.Printf("creation: chora-identity gRPC dial failed (%s): %v", identityGRPCURL, err)
		} else {
			grpcConn = conn
			manaClient = clients.NewManaClient(clients.ManaClientConfig{
				GRPCClient: identityv1.NewManaServiceClient(conn),
			})
			log.Printf("creation: ManaClient wired (chora-identity at %s)", identityGRPCURL)
		}
	} else {
		log.Printf("creation: SVC_IDENTITY_GRPC_URL unset — mana client NOT wired (question-jobs routes 503)")
	}

	// Display name resolver — best-effort identity lookup for author_display_name
	// on atom.published.v1 (field 31). Nil when identity gRPC is unwired.
	var displayNameResolver ports.DisplayNameResolver
	if grpcConn != nil {
		displayNameResolver = clients.NewIdentityDisplayNameClient(grpcConn)
		log.Printf("creation: DisplayNameResolver wired (chora-identity at %s)", identityGRPCURL)
	}
	defer func() {
		if grpcConn != nil {
			_ = grpcConn.Close()
		}
	}()

	// Unified picker (ux_unified_atom_picker.md) — chora-sharing gRPC client
	// for the `saved` disjunct. Nil when SVC_SHARING_GRPC_URL is unset (dev /
	// tests without sharing wired — `saved` source returns empty).
	savedAtomFetcher := buildSavedAtomFetcher()

	// ADR-229 WS-2 (CHO-2133) — the reuse-consent client feeding chokepoint 1
	// (picker consent disjunct) + chokepoint 2 (SnapshotQuestionByID gate +
	// D2 audit grant). Typed-nil guard: only assign the interfaces when the
	// client exists, so the handlers' `== nil` fail-loud checks stay honest.
	sharingReuseClient := buildSharingReuseClient()
	var reuseContextFetcher ports.ReuseContextFetcher
	if sharingReuseClient != nil {
		reuseContextFetcher = sharingReuseClient
	}

	// ADR-233 WS-4 — the collection reuse-consent gate (add-time + convert-time).
	// Same client, same env var (SVC_SHARING_GRPC_URL), two DOMAIN-owned ports:
	// the friend set + active grants (ConsentContextFetcher) and the
	// GRANT_SCOPE_COLLECTION audit write (CollectionUseAuthorizer).
	//
	// Typed-nil guard as above: leaving these nil when the client is absent keeps
	// the service's fail-loud `== nil` checks honest. An unwired gate REFUSES the
	// operation — it never passes silently.
	var collectionConsent collection.ConsentContextFetcher
	var collectionAuthz collection.CollectionUseAuthorizer
	if sharingReuseClient != nil {
		collectionConsent = sharingReuseClient
		collectionAuthz = sharingReuseClient
	}

	// The atom-reuse FACTS are creation-local (chora_creation owns the audience
	// bit per ADR-229 D1), so they come from the pg collection repo — ONE batch
	// read over learning_atoms, never a cross-DB query. Nil without a pool, which
	// is the same condition that already 404s the collection routes.
	var collectionFacts collection.AtomReuseFactLookup
	if collectionRepoPG != nil {
		collectionFacts = collectionRepoPG
	}
	switch {
	case collectionFacts != nil && collectionConsent != nil && collectionAuthz != nil:
		log.Printf("creation: ADR-233 collection reuse-consent gate wired (facts=pg, consent+grants=chora-sharing)")
	default:
		log.Printf("creation: ADR-233 collection reuse-consent gate NOT fully wired (facts=%t consent=%t authz=%t) — collection add + convert-to-study-list will REFUSE LOUD",
			collectionFacts != nil, collectionConsent != nil, collectionAuthz != nil)
	}

	// CHO-1658 + CHO-1920 — the Vertex AI Agent Engine clients (QGenQuestionClient,
	// QGen engine, AI Kernel Orchestrator; all us-central1, decommissioned) are
	// fully retired. The question subscriber dispatches model_answer_fill + ai_draft
	// to the GKE qgen crew (QUESTION_JOBS_QGEN_CREW_ENABLED), and /api/atoms/ai-assist
	// is served exclusively by the same crew (async).

	// JobEventPublisher bridge — wraps the event bus emitting atom
	// events to also accept question-job events with the same envelope.
	// Production wires the bus; tests inject in-memory recorders.
	jobPub := newJobEventPublisherBridge(bus, sourceProjectOrLocal())

	// ADR-195 WS7 (D7) cutover step 5 COMPLETE — v1 retired 2026-06-26. The
	// producers now publish the 3 compose events DIRECTLY to their .v2 topics
	// (operation/intent/input_kind; the legacy job_type/job_kind discriminant is
	// dropped). The transitional ComposeV2ParallelPublisher dual-publish decorator
	// + the QGEN_V2_PARALLEL_PUBLISH_ENABLED flag are removed — jobPub (the bridge)
	// is the single publisher. The in-process subscriber loop reads jobPub's
	// channel directly (the bridge fans generation_requested.v2 into it).

	// Batch source-material BlobStore wiring (handler upload path). Per
	// `feedback_no_inline_config`: the bucket name comes from env
	// GCS_BUCKET_BATCH_UPLOADS (retained env name). Empty env => the
	// handler returns 503 CREATION_BATCH_NOT_WIRED. The uploaded blob URI is
	// passed to the qgen crew, which inlines the bytes as Gemini multimodal
	// (ADR-169 / EPIC-1a) — there is no chora-doc-parser download/extract step.
	var blobStore ports.BlobStore
	batchBucket := strings.TrimSpace(os.Getenv("GCS_BUCKET_BATCH_UPLOADS"))
	if batchBucket == "" {
		log.Printf("creation: BlobStore NOT wired — GCS_BUCKET_BATCH_UPLOADS env empty (batch path will 503)")
	} else {
		bs, err := storage.NewS3BlobStore(storage.S3BlobStoreConfig{Bucket: batchBucket})
		if err != nil {
			log.Printf("creation: BlobStore NOT wired — NewS3BlobStore(%q) failed: %v (batch path will 503)", batchBucket, err)
		} else {
			blobStore = bs
			log.Printf("creation: BlobStore wired (bucket=%s)", batchBucket)
		}
	}

	// QUESTION_JOBS_QGEN_CREW_ENABLED (W2 Seam A) — when true, the ai_draft
	// path publishes chora.creation.ai_assist.started.v1 onto the GKE qgen
	// 2-agent crew (via the orchestrator) instead of calling a deleted
	// Vertex AI Agent Engine reasoning engine directly. Flag-off retains the
	// legacy direct-engine QGen branch (the dual-dispatch fallback).
	questionJobsQGenCrewEnabled := os.Getenv("QUESTION_JOBS_QGEN_CREW_ENABLED") == "true"

	// QuestionSubscriber — goroutine that processes generation_requested
	// events. Started when jobRepo + mana + jobPub are present. QGen is now
	// OPTIONAL: on the crew path (flag-on) the subscriber publishes
	// ai_assist.started.v1 via jobPub instead of calling QGen, so a nil
	// qgenQuestionClient (engine deleted) no longer blocks startup. QGen is
	// only needed for the flag-off legacy fallback.
	if questionJobRepo != nil && manaClient != nil && jobPub != nil {
		sub := pubsubadapter.NewQuestionSubscriber(pubsubadapter.QuestionSubscriberDeps{
			JobRepo:         questionJobRepo,
			Mana:            manaClient,
			Publisher:       jobPub,
			SyncPublisher:   jobPub, // load-bearing ai_assist.started.v2 publish (W2 + batch)
			QGenCrewEnabled: questionJobsQGenCrewEnabled,
			// CHO-1658 — model_answer_fill crew dispatch fetches the author's existing
			// question (stem/options/rubric) to seed the fill. A pgx QuestionRepository
			// satisfies the narrow QuestionReader; nil without a pool (the crew branch
			// then fails the model-answer job loud rather than dispatching blind).
			QuestionReader: questionRepo,
		})
		// Production Pub/Sub Receive loop lands at M10 Pub/Sub subscription
		// wiring; until then the subscriber is dispatched in-process by
		// the outbox dispatcher emitting the same event the HTTP handler
		// publishes. We start a small in-process listener that polls the
		// outbox bridge's channel.
		startQuestionSubscriberLoop(ctx, sub, jobPub)
		log.Printf("creation: question subscriber goroutine started (qgen_crew_enabled=%v)",
			questionJobsQGenCrewEnabled)
	} else {
		log.Printf("creation: question subscriber NOT started — missing deps (jobRepo=%v, mana=%v, pub=%v)",
			questionJobRepo != nil, manaClient != nil, jobPub != nil)
	}

	// OE-AI-ASSIST (Step 4c per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md)
	// — qgen 2-agent crew async surface. POST /api/atoms/ai-assist publishes
	// ai_assist.started.v1 via outbox + INSERTs an ai_assist_jobs row +
	// returns 202 + AiAssistJob envelope. The orchestrator subscriber wired in
	// services/chora-ai-kernel-orchestrator runs the qgen crew + publishes
	// ai_assist.completed.v1 / refused.v1; a subscriber here UPDATEs the row
	// terminal. This is the ONLY ai-assist path (the legacy Agent Engine sync
	// dispatch was retired in CHO-1920).

	// AI Assist terminal subscriber — bridges Cloud Pub/Sub Receive loops
	// for chora.creation.ai_assist.completed.v1 + .refused.v1 to the
	// aiAssistJobsRepo Update* methods. Required so the FE's GET
	// /api/atoms/ai-assist/{job_id} surfaces COMPLETED / REFUSED / FAILED
	// terminal state after the orchestrator's qgen 2-agent crew finishes.
	//
	// Per [[feedback-no-stubs-real-wiring]] "fail loud not crash": when a
	// subscription doesn't yet exist (Infra provisions in parallel) the
	// Receive call returns an error after dial. The goroutine logs WARN +
	// retries with backoff so the pod stays up; production reads the WARN
	// + the Infra ask remains open.
	//
	// Durable consumers (created by the event bus):
	//   - chora-creation.ai-assist-completed
	//   - chora-creation.ai-assist-refused
	if aiAssistJobsRepo != nil && bus != nil {
		termSub := pubsubadapter.NewAiAssistTerminalSubscriber(aiAssistJobsRepo)
		// W2 Seam A — dual-dispatch: the same two terminal subs also carry
		// the question-jobs ai_draft path. Wire the question-job deps so the
		// subscriber resolves assist_id against question_generation_jobs
		// first (ai_draft + running) before falling through to ai_assist_jobs.
		if questionJobRepo != nil && manaClient != nil && jobPub != nil {
			termSub = termSub.WithQuestionJobs(questionJobRepo, manaClient, jobPub)
			log.Printf("creation: ai_assist terminal subscriber dual-dispatch ENABLED (question_generation_jobs path)")
			// Lane 1c (D15) — citation verification corpus. Nil-safe: when
			// the chunk repo is unavailable citations stay AI-reported.
			if sourceChunkRepo != nil {
				termSub = termSub.WithChunkStore(sourceChunkRepo)
				log.Printf("creation: ai_assist terminal subscriber citation verification ENABLED (source_material_chunks)")
			}
		} else {
			log.Printf("creation: ai_assist terminal subscriber dual-dispatch DISABLED — missing question-job deps (jobRepo=%v, mana=%v, pub=%v); legacy-only",
				questionJobRepo != nil, manaClient != nil, jobPub != nil)
		}
		bus.Subscribe(ctx, consumerConfig("chora-creation.ai-assist-completed", pubsubadapter.TopicAiAssistCompleted), termSub.HandleCompleted)
		bus.Subscribe(ctx, consumerConfig("chora-creation.ai-assist-refused", pubsubadapter.TopicAiAssistRefused), termSub.HandleRefused)
		// CR Phase 1 — live-trace streaming. Updates ONLY a running question-job's
		// partial pipeline_trace (HandleProgress ACK-no-ops when the question-jobs
		// dual-dispatch path above is unwired, so this is safe to start always).
		bus.Subscribe(ctx, consumerConfig("chora-creation.ai-assist-progress", pubsubadapter.TopicAiAssistProgress), termSub.HandleProgress)
		// ADR-251 D5 (CHO-2398) - questions-so-far chunk persistence.
		bus.Subscribe(ctx, consumerConfig("chora-creation.ai-assist-chunk-completed", pubsubadapter.TopicAiAssistChunkCompleted), termSub.HandleChunkCompleted)
		log.Printf("creation: ai_assist terminal subscriber wired (subs=chora-creation.ai-assist-{completed,refused,progress,chunk-completed})")
	} else {
		log.Printf("creation: ai_assist terminal subscriber NOT wired — missing deps (jobsRepo=%v, bus=%v)",
			aiAssistJobsRepo != nil, bus != nil)
	}

	// ADR-172 §D8 — model_answer_amended consumer. When an instructor corrects a
	// question's canonical model_answer during OE grading, chora-delivery emits
	// chora.delivery.grading.model_answer_amended.v1; we append a grading-
	// amendment QuestionRevision (append-only, idempotent on event_id). The
	// idempotency store defaults to in-memory; a durable PostgresStore against
	// chora_creation idempotency_keys can be injected via WithInbox when wired.
	if questionRepo != nil && bus != nil {
		maSub := pubsubadapter.NewModelAnswerAmendedSubscriber(questionRepo)
		bus.Subscribe(ctx, consumerConfig("chora-creation.grading-model-answer-amended", pubsubadapter.TopicModelAnswerAmended), maSub.Handle)
		log.Printf("creation: model_answer_amended subscriber wired (sub=chora-creation.grading-model-answer-amended)")
	} else {
		log.Printf("creation: model_answer_amended subscriber NOT wired — missing deps (questionRepo=%v, bus=%v)",
			questionRepo != nil, bus != nil)
	}

	// ADR-229 Amendment A1 (CHO-2132) — orphan-edition mint. Consumes
	// chora.sharing.atom_reuse.orphan_required.v1 (sharing's grant table is
	// the authoritative stranded-consumer registry), mints the singleton
	// orphan edition + clones its question + repoints creation's own
	// non-author collection entries, then answers with
	// chora.creation.atom.orphan_created.v1 via the durable outbox.
	if orphanAtoms != nil && orphanQuestions != nil && bus != nil {
		orphanSub := pubsubadapter.NewOrphanRequiredSubscriber(pubsubadapter.OrphanRequiredConfig{
			Atoms:     orphanAtoms,
			Questions: orphanQuestions,
			Publisher: publisher,
		})
		registerOrphanRequiredSubscriber(ctx, bus, orphanSub)
	} else {
		log.Printf("creation: orphan_required subscriber NOT wired — missing deps (pgRepos=%v, bus=%v)",
			orphanAtoms != nil && orphanQuestions != nil, bus != nil)
	}

	// ATOM Phase 1 (ADR-156) — atom-media V4 signed URL minter wiring.
	// Bucket name comes from env ATOM_MEDIA_BUCKET. Per
	// feedback_no_inline_config + feedback_no_stubs_real_wiring: empty env
	// leaves atomMediaSigner nil and the handler 503s with envelope
	// CREATION_ATOM_MEDIA_NOT_WIRED. NO stub fallback.
	//
	// The adapter presigns PUT/GET URLs against S3-compatible storage
	// (MinIO / S3) using static credentials from the environment
	// (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY).
	var atomMediaSigner ports.AtomMediaSigner
	atomMediaBucket := strings.TrimSpace(os.Getenv("ATOM_MEDIA_BUCKET"))
	if atomMediaBucket == "" {
		log.Printf("creation: AtomMediaSigner NOT wired — ATOM_MEDIA_BUCKET env empty (POST /api/atoms/{id}/media will 503)")
	} else {
		options := []mediastore.AtomMediaSignerOption{}
		if envT := strings.TrimSpace(os.Getenv("ATOM_MEDIA_SIGN_TIMEOUT")); envT != "" {
			if d, parseErr := time.ParseDuration(envT); parseErr == nil && d > 0 {
				options = append(options, mediastore.WithSignTimeout(d))
				log.Printf("creation: AtomMediaSigner sign-timeout override = %s", d)
			} else {
				log.Printf("creation: ATOM_MEDIA_SIGN_TIMEOUT=%q invalid (want Go duration like 10s); using default %s", envT, mediastore.DefaultSignTimeout())
			}
		}
		s, sErr := mediastore.NewAtomMediaSigner(atomMediaBucket, options...)
		if sErr != nil {
			log.Printf("creation: AtomMediaSigner NOT wired — NewAtomMediaSigner(%q) failed: %v (POST /api/atoms/{id}/media will 503)", atomMediaBucket, sErr)
		} else {
			atomMediaSigner = s
			log.Printf("creation: AtomMediaSigner wired (bucket=%s, timeout=%s)",
				atomMediaBucket, mediastore.DefaultSignTimeout())
		}
	}

	// OT#4 — the S3 atom-media signer also satisfies MediaReHomer (publish
	// re-home) + AtomMediaDownloadSigner (mint-on-read). Derive both via
	// assertion so a nil signer stays a nil interface (avoids the
	// typed-nil-in-interface gotcha that would make != nil checks lie).
	var mediaReHomer ports.MediaReHomer
	var mediaDownloadSigner ports.AtomMediaDownloadSigner
	if atomMediaSigner != nil {
		mediaReHomer = atomMediaSigner.(ports.MediaReHomer)
		mediaDownloadSigner = atomMediaSigner.(ports.AtomMediaDownloadSigner)
	}

	// CHO-2142 — atom topic classifier for the topic-tag backfill. Dials
	// chora-model-gateway (gRPC :9090), the single un-bypassable LLM chokepoint
	// (ADR-163/177) where Cloud Model Armor screens and metering is central. The
	// 6-agent gate's Classifier is dead code in retired services (ADR-145/146,
	// mock-only, not deployed), so the role lives here on the mandated path.
	//
	// ⚠ A new gRPC method needs a mesh AuthorizationPolicy allow-list entry for
	// the CREATION principal + a caller restart before Invoke will pass.
	//
	// Unset target ⇒ classifier stays nil and ONLY
	// POST /api/internal/atoms/backfill-topic-tags fails loud
	// (CREATION_TOPIC_CLASSIFIER_NOT_WIRED). Nothing else is affected — the
	// backfill is an operator route, not a request-path dependency.
	var topicClassifier atom.TopicClassifier
	if gwTarget := strings.TrimSpace(os.Getenv("CHORA_MODEL_GATEWAY_GRPC_URL")); gwTarget != "" {
		gwModel := strings.TrimSpace(os.Getenv("CHORA_TOPIC_CLASSIFIER_MODEL"))
		if gwModel == "" {
			gwModel = defaultTopicClassifierModel
		}
		tc, tcErr := clients.NewTopicClassifierGatewayClient(gwTarget, gwModel, 0)
		if tcErr != nil {
			// Fail-loud log, nil classifier — the backfill route 500s with a
			// precise envelope rather than pretending to classify.
			log.Printf("creation: TopicClassifier NOT wired — NewTopicClassifierGatewayClient(%q, %q) failed: %v (POST /api/internal/atoms/backfill-topic-tags will 500)",
				gwTarget, gwModel, tcErr)
		} else {
			topicClassifier = tc
			log.Printf("creation: TopicClassifier wired (gateway=%s, model=%s)", gwTarget, gwModel)
		}
	} else {
		log.Printf("creation: TopicClassifier NOT wired (CHORA_MODEL_GATEWAY_GRPC_URL unset) — POST /api/internal/atoms/backfill-topic-tags will 500")
	}

	// CHO-2159 — bounds for the DETACHED topic-tag backfill run. No inline config:
	// both come from env, and zero ⇒ the domain defaults (30m run / 45m stale).
	//
	//   CHORA_BACKFILL_RUN_TIMEOUT   bounds ONE run. The detached context has no
	//     deadline of its own (it must outlive the request), so this is the only
	//     thing between a wedged model-gateway call and a row stuck `running`.
	//     Must comfortably exceed a legit full run (74 atoms x ~3.5s ≈ 280s).
	//   CHORA_BACKFILL_STALE_CUTOFF  the age past which a still-`running` row is
	//     reclaimed as stranded (its pod died mid-run). MUST exceed the run
	//     timeout, or the sweep would reclaim LIVE runs and the operator would
	//     poll `failed` while tags were still being written — NewBackfillRunner
	//     refuses that pair and the routes then fail loud.
	backfillRunTimeout := envDurationOrZero("CHORA_BACKFILL_RUN_TIMEOUT")
	backfillStaleCutoff := envDurationOrZero("CHORA_BACKFILL_STALE_CUTOFF")

	// Epic-1b W4 — publish-time atom-embedding indexer + boot backfill.
	embedIndexer := buildAtomEmbedIndexer(ctx, atomEmbeddingRepo)
	startAtomEmbeddingBackfill(ctx, embedIndexer)

	// W0-F1 durability gate (CHO-2198): classify every wired domain repo by SHAPE
	// (holds a live *pgxpool.Pool ⇒ DURABLE; a data map ⇒ IN_MEMORY) and log a
	// structured, greppable report at boot. Report-only unless
	// CHORA_DURABILITY_GUARD=enforce AND the binding is allow-listed — nil
	// allow-list matches the chora-payments / chora-identity / chora-delivery
	// wirings. `media` is the sole media.Repository impl (inmem-only, no pg) — an
	// UNGATED-DEFECT per the W0-F1 inventory §6, so it surfaces as IN_MEMORY here
	// exactly as chora-identity's un-fixed ports do; it is left un-allow-listed on
	// purpose so the report shows the real defect until a pg MediaRepository lands.
	// The nil pg-only ports (questions/jobs/collections/... in dev) report UNKNOWN,
	// never a false violation. Publishers, signers, gRPC clients, the outbox store,
	// and the GCS blob store are intentionally OMITTED (non-store deps).
	durabilityguard.Guard("chora-creation", []durabilityguard.Binding{
		{Port: "atoms", Adapter: atomRepo},
		{Port: "media", Adapter: mediaRepo},
		{Port: "questions", Adapter: questionRepo},
		{Port: "question_jobs", Adapter: questionJobRepo},
		{Port: "ai_assist_jobs", Adapter: aiAssistJobsRepo},
		{Port: "collections", Adapter: collectionRepo},
		{Port: "question_bank", Adapter: questionBankRepo},
		{Port: "atom_embeddings", Adapter: atomEmbeddingRepo},
		{Port: "source_chunks", Adapter: sourceChunkRepo},
		{Port: "backfill_runs", Adapter: backfillRuns},
		{Port: "closure", Adapter: closureRepo},
	}, nil)

	legacy := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo,
		// Atomic atom-write seam (see the AtomTxWriter block above). Every
		// atom write on this router commits its outbox row in the same
		// transaction or not at all.
		AtomWriter:            atomWriter,
		EmbedIndexer:          embedIndexer,
		QuestionRepository:    questionRepo,
		QuestionJobRepository: questionJobRepo,
		ManaLedger:            manaClient,
		JobEventPublisher:     jobPub,
		BlobStore:             blobStore, // P6 — nil ⇒ batch upload 503 CREATION_BATCH_NOT_WIRED
		// Lane 1c (D9) — chunk store for batch extraction + citation
		// verification. Nil ⇒ extraction skipped (batch path unaffected).
		SourceChunkRepository: sourceChunkRepo,
		// Lane 1c W3 (D10) — test-set accept outbox publisher. Nil ⇒
		// test_set-carrying accepts 503 CREATION_TESTSET_PUBLISH_NOT_WIRED.
		QuestionBatchAcceptedPublisher: questionBatchPublisher,
		// OE-AI-ASSIST async surface (Step 4c). POST /api/atoms/ai-assist
		// always dispatches via outbox + repo (the GKE qgen crew); the GET
		// handler activates when AiAssistJobs is non-nil (read-only).
		AiAssistJobs:      aiAssistJobsRepo,
		AiAssistPublisher: jobPub,
		// ATOM Phase 1 (ADR-156) — atom-media V4 signed URL. Nil ⇒
		// POST /api/atoms/{id}/media 503s CREATION_ATOM_MEDIA_NOT_WIRED.
		AtomMediaSigner: atomMediaSigner,
		// OT#4 — durable W8 image re-home on publish + authoring-read
		// mint-on-read. Nil ⇒ re-home no-ops + gs:// refs return raw.
		MediaReHomer:            mediaReHomer,
		AtomMediaDownloadSigner: mediaDownloadSigner,
		// WS-6a-wiring — Personal Collections (2026-05-26).
		// CollectionRepository is pgx-backed when the pool is available (routes
		// activate) and nil without it (routes 404 — fail-loud).
		// CollectionPublisher is nil-tolerant: events are dropped when the
		// outboxStore is not yet wired (gap logged at startup above).
		CollectionRepository: collectionRepo,
		CollectionPublisher:  collectionPublisher,
		// ADR-233 WS-4 — the reuse-consent gate. Facts are creation-local (pg);
		// consent + grant-write go over the chora-sharing mesh. A nil port here
		// makes the corresponding gate REFUSE loudly, never pass.
		CollectionFacts:   collectionFacts,
		CollectionConsent: collectionConsent,
		CollectionAuthz:   collectionAuthz,
		// Unified picker (ux_unified_atom_picker.md) — saved-atom-ID
		// fetcher over chora-sharing gRPC. Nil ⇒ `saved` source returns
		// empty (dev / tests without sharing wired).
		SavedAtomIDFetcher: savedAtomFetcher,
		// ADR-229 WS-2 — consent-context fetcher for the picker disjunct
		// (chokepoint 1). Nil ⇒ source=all/saved searches fail LOUD.
		ReuseContextFetcher: reuseContextFetcher,
		// W3.B.1-wiring — QuestionBank (2026-06-28). pgx-backed when the pool is
		// available (routes activate); nil without pool so routes 404
		// (fail-loud). Publisher is intentionally nil in B.1 (events deferred).
		QuestionBankRepository:     questionBankRepo,
		QuestionBankQuestionLookup: questionBankQuestionLookup,
		QuestionBankPublisher:      nil,
		// Server-side question paging — the same pg repo (implements ItemPager).
		QuestionBankItemPager: questionBankItemPager,
		// W3.B.2 — assemble-test-set publisher (reuses question_batch.accepted.v1;
		// nil without an outboxStore so the route 503s fail-loud).
		QuestionBankTestSetPublisher: questionBankTestSetPublisher,
		// atom.published.v1 durable emit on the DRAFT -> PUBLISHED transition —
		// the same outbox-backed publisher the Phyllis router drains. Carries
		// answerability (MCQ answer key / OE flag) for chora-consumption.
		AtomEventPublisher: publisher,
		// CHO-2142 — model-gateway-backed topic classifier for the topic-tag
		// backfill route. Nil ⇒ that one route fails loud.
		TopicClassifier: topicClassifier,
		// CHO-2159 — durable run reports for that backfill (202 + poll). Nil ⇒
		// the backfill routes fail loud rather than mutate atoms unobserved.
		BackfillRuns:        backfillRuns,
		BackfillRunTimeout:  backfillRunTimeout,
		BackfillStaleCutoff: backfillStaleCutoff,
		// CHO-2275 — content topic tree. pg-backed when the pool is present
		// (routes activate); nil without it so /api/topics 404s (fail-loud). The
		// seed-from-atom-tags backfill activates only with both sink + source.
		TopicRepository: topicRepo,
		TopicSeedSink:   topicSeedSink,
		TopicTagSource:  topicTagSource,
		// Author display name resolver — best-effort identity lookup via
		// chora-identity gRPC (GetMe). Nil when identity gRPC is unwired;
		// the event carries an empty author_display_name in that case.
		DisplayNameResolver: displayNameResolver,
	})
	phyllis := httpadapter.NewPhyllisRouter(httpadapter.PhyllisDeps{
		Repo:        atomRepo,
		Broker:      brokerClient,
		Publisher:   publisher,
		AtomWriter:  atomWriter,
		MediaRepo:   mediaRepo,
		MediaSigner: mediaSigner,
	})

	mux := http.NewServeMux()
	// /v1/* -> Phyllis handler
	mux.Handle("/v1/", phyllis)
	// everything else -> legacy handler (incl. /api/atoms, /, /healthz, /readyz)
	mux.Handle("/", legacy)

	// Cross-cutting middleware chain (outer-to-inner):
	//
	//   commonobs.HTTPMiddleware  — extracts/mints W3C traceparent + emits
	//                               per-request span via the lib (OTel SDK
	//                               registered above by commonobs.InitOTLPAsync).
	//   servicemesh.Middleware    — extracts mTLS-bound chora-gcid /
	//                               chora-tenant-id / chora-role-summary
	//                               headers and stamps them on context.
	//                               Co-exists with the local tenantContext
	//                               middleware (still inside Phyllis/legacy
	//                               routers) so the mesh-asserted identity
	//                               is available to downstream handlers
	//                               that prefer it over the loose
	//                               X-Tenant-Id / gcid headers.
	//
	// The Identity Platform JWT validator (libs/.../auth/identityplatform)
	// is intentionally NOT applied here in the build-out phase — the BFF
	// gateway terminates the JWT and translates it to mesh metadata before
	// calling chora-creation. When chora-creation is exposed directly to
	// the SPA in a deployment topology variant, switch back to the JWT
	// validator middleware via the IDP_ISSUER_URL + IDP_AUDIENCE env vars.
	wrapped := commonobs.HTTPMiddleware()(servicemesh.Middleware(mux))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           wrapped,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("service=%s version=%s listening on %s", serviceName, version, srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// ----------------------------------------------------------------------
	// gRPC server for Creation service (Wave-1 gRPC mass remediation, C-FULL).
	// ----------------------------------------------------------------------
	// 8 RPCs on chora.services.creation.v1.Creation per
	// chora-contracts/proto/services/creation/v1/creation.proto:
	//   - CreateAtom               — DRAFT atom creation
	//   - GenerateAtomsViaAI       — UNIMPLEMENTED (async via Pub/Sub)
	//   - GetAtom                  — single-atom resolve
	//   - ValidateAtomID           — anti-fabrication guard (Familiar cite_atom)
	//   - ListAtomsByCourse        — course-scoped atom projection
	//   - AppendRevision           — UNIMPLEMENTED (use Phyllis HTTP)
	//   - QueryKnowledgeGraph      — UNIMPLEMENTED (per-user KG in Consumption)
	//   - SnapshotQuestionByID     — chain-break close for chora-delivery
	//                                QuestionSnapshotter (E2E-INFRA-LEG3-D)
	//
	// Per `feedback_no_inline_config`: gRPC port comes from env. Default 9090
	// matches the Cloud Service Mesh canonical port the other Chora services
	// already use (chora-identity:9090, chora-tenancy:9090).
	//
	// Per `feedback_no_stubs_real_wiring`: the gRPC server registers
	// UNCONDITIONALLY. When a dependent repository is nil (e.g. pgx pool not
	// wired in dev), the affected RPC returns FAILED_PRECONDITION with a
	// fail-loud message so the caller distinguishes "method exists,
	// dependency missing" from "method not registered".
	//
	// Mirrors services/chora-identity/cmd/server/main.go:660-690 + the
	// canonical pattern in docs/m13/grpc-mass-remediation-2026-05-16.md §3.a.
	grpcPort := strings.TrimSpace(os.Getenv("CHORA_GRPC_PORT"))
	if grpcPort == "" {
		grpcPort = "9090"
	}
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("creation: gRPC net.Listen :%s: %v", grpcPort, err)
	}
	grpcSrv := grpc.NewServer()
	// ADR-229 WS-2 chokepoint 2 — the snapshot gate's consent deps (typed-nil
	// guarded like the RouterDeps wiring above).
	var snapshotAtomUse ports.AtomUseAuthorizer
	if sharingReuseClient != nil {
		snapshotAtomUse = sharingReuseClient
	}
	creationServer := creationgrpc.NewCreationServer(creationgrpc.Deps{
		Atoms:     atomRepo,
		Questions: questionRepo,
		// OT#4 — MintAtomMediaDownloadURL signs durable atom-media gs:// refs
		// for chora-delivery's learner mint-on-read. Nil ⇒ RPC 503s.
		DownloadSigner: mediaDownloadSigner,
		// ADR-229 WS-2 (CHO-2133) — SnapshotQuestionByID reuse gate. Nil ⇒
		// caller_gcid-bearing non-owner snapshots refuse FAILED_PRECONDITION
		// (fail closed); legacy callers (no caller_gcid) are unaffected.
		ReuseContext: reuseContextFetcher,
		AtomUse:      snapshotAtomUse,
	})
	creationv1.RegisterCreationServer(grpcSrv, creationServer)

	// Epic-1b W4 — ContentRetrieval.SearchEmbeddings (semantic drill-atom
	// resolution for chora-consumption's Growth Edges). Registered
	// unconditionally per feedback_no_stubs_real_wiring; nil searcher (no
	// pool) FAILED_PRECONDITIONs.
	var embeddingSearcher creationgrpc.EmbeddingSearcher
	if atomEmbeddingRepo != nil {
		embeddingSearcher = grpcEmbeddingSearcher{repo: atomEmbeddingRepo}
	}
	creationv1.RegisterContentRetrievalServer(grpcSrv, creationgrpc.NewContentRetrievalServer(creationgrpc.ContentRetrievalDeps{
		Embeddings: embeddingSearcher,
	}))

	// gRPC health server — required for Cloud Service Mesh probe routing.
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.creation.v1.Creation", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)

	go func() {
		log.Printf("service=%s grpc listening on :%s (Creation bound)", serviceName, grpcPort)
		if serr := grpcSrv.Serve(grpcLis); serr != nil && serr != grpc.ErrServerStopped {
			log.Fatalf("creation: grpc server error: %v", serr)
		}
	}()

	// Wait for SIGINT/SIGTERM, then drain.
	<-ctx.Done()
	log.Printf("shutting down...")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	// Final outbox drain — dispatcher.Run already returned on ctx
	// cancellation; synchronously drain one last batch so in-flight
	// pending rows reach Pub/Sub before the binary exits.
	if outboxDispatcher != nil {
		finalDrain, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer fcancel()
		if n, derr := outboxDispatcher.DrainOnce(finalDrain, 200); derr != nil {
			log.Printf("creation: final outbox drain error: %v (drained %d)", derr, n)
		} else {
			log.Printf("creation: final outbox drain published %d rows", n)
		}
	}

	// Wait for dispatcher goroutine to exit cleanly (5s grace) so we
	// don't leak a goroutine on shutdown.
	if dispatcherDone != nil {
		select {
		case <-dispatcherDone:
		case <-time.After(5 * time.Second):
			log.Printf("creation: outbox dispatcher shutdown timed out (5s)")
		}
	}
}

// sourceProjectOrLocal returns the CHORA_SOURCE_PROJECT env value, defaulting
// to chora-local for local dev.
func sourceProjectOrLocal() string {
	if v := os.Getenv("CHORA_SOURCE_PROJECT"); v != "" {
		return v
	}
	return "chora-local"
}

// envDurationOrZero parses a Go duration from env (CHO-2159 backfill bounds).
//
// Unset ⇒ 0, which means "use the domain default". A value that is present but
// UNPARSEABLE or non-positive is a LOUD refusal to boot: silently falling back
// to a default would substitute a bound the operator never chose (a typo'd
// "30" quietly becoming 30m instead of the intended 30s), and for the stale
// cutoff that is the difference between reclaiming dead runs and reclaiming
// LIVE ones. Fail closed at boot, never guess at runtime.
func envDurationOrZero(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Fatalf("creation: %s=%q is not a Go duration (e.g. 30m, 45m, 300s): %v", key, raw, err)
	}
	if d <= 0 {
		log.Fatalf("creation: %s=%q must be positive", key, raw)
	}
	return d
}
