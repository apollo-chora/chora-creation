// Package httpadapter wires LearningAtom CRUD endpoints from the OpenAPI spec
// chora-contracts/openapi/creation-admin.yaml to the domain.
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
	"github.com/apollo-chora/chora-creation/internal/embedindex"
	"github.com/apollo-chora/chora-creation/internal/mediarehome"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// AtomHandler implements the /api/atoms* HTTP routes.
type AtomHandler struct {
	repo atom.Repository

	// atomWriter persists an atom and enqueues the event announcing it in ONE
	// transaction. Every atom write on this handler goes through it; the two
	// separate commits it replaced stranded 2.68% of atoms without an event
	// under pod death (chaos20260807a).
	atomWriter atom.TransactionalPublisher

	// P3 — Question Authoring CR (golden-hopping-owl plan). The Question
	// repository is wired through here so:
	//   1. /api/atoms/{atom_id}/questions[...] sub-routes can dispatch via
	//      the QuestionsHandler (delegated below in atomsItem).
	//   2. GET /api/atoms/{atom_id} can surface the LEARNER projection
	//      (mcq_payload / essay_payload) per A16.
	// Nil when the repository is not wired (legacy NewRouter shim path).
	questionRepo     ports.QuestionRepository
	questionsHandler *QuestionsHandler

	// P4+P5 — AI single-question async path (golden-hopping-owl plan).
	// QuestionJobsHandler exposes:
	//   - POST /api/atoms/{atom_id}/questions/{qid}/ai-model-answer-jobs
	//   - POST /api/atoms/{atom_id}/question-jobs (ai_draft only in P5)
	//   - GET  /api/atoms/{atom_id}/question-jobs/{jid}
	//   - POST /api/atoms/{atom_id}/question-jobs/{jid}/accept
	// Nil when the deps are not wired (the routes 503 in that case).
	jobsHandler *QuestionJobsHandler

	// OE-AI-ASSIST (Step 4c) — qgen crew async surface. POST /api/atoms/ai-assist
	// dispatches via outbox + the ai_assist_jobs table to the GKE qgen crew (the
	// ONLY ai-assist path; the legacy Agent Engine sync dispatch was retired in
	// CHO-1920). GET /api/atoms/ai-assist/{job_id} activates when aiAssistJobs is
	// non-nil (read-only).
	aiAssistJobs      ports.AiAssistJobsRepository
	aiAssistPublisher JobEventPublisher

	// mana meters the qgen crew authoring path (POST /api/atoms/ai-assist) via
	// the ADR-178 price-plan layer (CHO-1661/FU-4b). The crew debits the
	// configurable `atom_authoring_assist` price (units==0 → resolved
	// tenant-aware; default 0 = free). Fail-OPEN: an unpriced action or a
	// metering-infra hiccup serves free — authoring is never blocked by
	// metering — only a genuine insufficient balance 402s. Nil ⇒ crew is free
	// (metering not wired). Shares the same ports.ManaLedger as the question-jobs
	// path (deps.ManaLedger).
	mana ports.ManaLedger

	// ATOM Phase 1 (ADR-156) — atom-media V4-signed-URL minter.
	// POST /api/atoms/{atom_id}/media is dispatched in atomsItem when a
	// signer is wired; nil ⇒ the route 503s with envelope
	// CREATION_ATOM_MEDIA_NOT_WIRED (fail-loud per
	// feedback_no_stubs_real_wiring). Wired from main.go when env
	// ATOM_MEDIA_BUCKET is set.
	atomMediaSigner ports.AtomMediaSigner

	// OT#4 / ADR-210 — shared W8 durable image re-home. Transient 7-day signed
	// image URLs in the question payload are copied into the durable
	// chora-atom-media bucket and rewritten to a gs:// ref at every SAVE seam
	// (publish, save-revision, accept) so a saved-but-unpublished image
	// survives the transient TTL. Nil ⇒ re-home is skipped (images stay
	// transient). The same instance is shared with the questions + jobs handlers.
	reHomer *questionImageReHomer

	// OT#4 — authoring-read mint-on-read. When a question payload holds a
	// durable gs:// image ref, getQuestion resolves it to a fresh short-lived
	// signed GET URL so the author's <img> renders. Nil ⇒ gs:// refs are
	// returned raw.
	mediaDownloadSigner ports.AtomMediaDownloadSigner

	// Epic-1b W4 — on atom publish, asynchronously embed the atom's text into
	// atom_embeddings so ContentRetrieval.SearchEmbeddings can resolve it as a
	// Growth-Edge drill atom. Nil ⇒ skipped (no-op; the env-gated boot
	// backfill catches up later).
	embedIndexer *embedindex.Service

	// atomEventPublisher emits chora.creation.atom.published.v1 via the durable
	// outbox when publishAtom flips an atom DRAFT -> PUBLISHED (carrying
	// answerability so chora-consumption can treat the atom as playable). Nil ⇒
	// the emit is skipped (mirrors the nil-tolerant CollectionPublisher
	// pattern). The atom.created.v1 event fires at draft-creation before a
	// question exists, so atom.published.v1 is the authoritative source of the
	// MCQ answer key / OE answerability flag.
	atomEventPublisher atom.EventPublisher

	// CHO-2142 — topic classifier for POST /api/internal/atoms/backfill-topic-tags.
	// The live adapter dials chora-model-gateway (the un-bypassable LLM chokepoint,
	// ADR-163/177). Nil ⇒ the route fails loud with
	// CREATION_TOPIC_CLASSIFIER_NOT_WIRED — never a silent no-op run, which would
	// report success having classified nothing. Wired from main.go when env
	// CHORA_MODEL_GATEWAY_GRPC_URL is set.
	topicClassifier atom.TopicClassifier

	// CHO-2159 — the async 202 + poll lane for the topic-tag backfill. A 74-atom
	// run takes ~280s and the server's WriteTimeout is 15s, so the run is
	// PERSISTED and executed detached; the operator polls the report. Nil ⇒ the
	// backfill routes fail loud (CREATION_BACKFILL_RUNS_NOT_WIRED) — a run whose
	// report cannot be persisted is a mutation nobody can observe, which is the
	// whole defect.
	backfillRunner *atom.BackfillRunner
	backfillRuns   atom.BackfillRunStore
	// displayNameResolver resolves the author's display name at publish time.
	// Nil-tolerant — empty string is legal on the wire.
	displayNameResolver ports.DisplayNameResolver
}

// RouterDeps bundles boot-time wiring for NewRouterWithDeps.
type RouterDeps struct {
	Repo         atom.Repository
	EmbedIndexer *embedindex.Service // Epic-1b W4; nil ⇒ publish skips embedding

	// P3 — Question Authoring CR. When wired, activates:
	//   - POST/PATCH/GET /api/atoms/{atom_id}/questions[...] routes
	//   - mcq_payload / essay_payload A16 projection on GET /api/atoms/{id}
	// nil disables the routes and the projection (handler is still mounted).
	QuestionRepository ports.QuestionRepository

	// P4+P5 — AI single-question async path. When all three deps are wired,
	// activates the question-jobs handler (ai-model-answer + ai_draft +
	// poll + accept). nil disables only those routes.
	QuestionJobRepository ports.QuestionJobRepository
	ManaLedger            ports.ManaLedger
	JobEventPublisher     JobEventPublisher

	// P6 — batch source-material upload. The BlobStore is the GCS-backed
	// artefact store (env GCS_BUCKET_BATCH_UPLOADS sourced from Terraform
	// per `feedback_no_inline_config`). Nil ⇒ the multipart batch path on
	// POST /api/atoms/{atom_id}/question-jobs returns 503 with envelope
	// CREATION_BATCH_NOT_WIRED (fail-loud per `feedback_no_stubs_real_wiring`).
	// JSON ai_draft path is unaffected by this dep.
	BlobStore ports.BlobStore

	// Lane 1c (CHO-1703 / ADR-180 D9) — source_material_chunks store for
	// the deterministic extraction at batch-job creation + the D15 citation
	// verification corpus. Nil skips extraction (batch path unaffected;
	// citations surface as AI-reported).
	SourceChunkRepository ports.SourceChunkRepository

	// Lane 1c W3 (D10) — outbox publisher for
	// chora.creation.question_batch.accepted.v1. Nil ⇒ accept requests
	// carrying a test_set block 503 CREATION_TESTSET_PUBLISH_NOT_WIRED
	// (fail loud BEFORE persistence); the plain accept path is unaffected.
	QuestionBatchAcceptedPublisher ports.QuestionBatchAcceptedPublisher

	// OE-AI-ASSIST (Step 4c) — qgen crew async surface. POST /api/atoms/ai-assist
	// dispatches via outbox (202 + AiAssistJob) to the GKE qgen crew — the ONLY
	// ai-assist path (the legacy Agent Engine sync dispatch was retired in
	// CHO-1920). GET /api/atoms/ai-assist/{job_id} activates when AiAssistJobs is
	// non-nil (read-only).
	AiAssistJobs      ports.AiAssistJobsRepository
	AiAssistPublisher JobEventPublisher

	// ATOM Phase 1 (ADR-156) — atom-media V4-signed-URL minter for
	// POST /api/atoms/{atom_id}/media. Wired from cmd/server/main.go
	// when env ATOM_MEDIA_BUCKET is set (bucket provisioned by Infra at
	// commit 4df66f51 against chora-atom-media-dev). Nil ⇒ the route
	// 503s with envelope CREATION_ATOM_MEDIA_NOT_WIRED per
	// feedback_no_stubs_real_wiring.
	AtomMediaSigner ports.AtomMediaSigner

	// OT#4 — W8 durable image re-home + authoring-read download signer.
	// MediaReHomer copies transient images into chora-atom-media on atom
	// publish; AtomMediaDownloadSigner mints fresh GET URLs over the durable
	// gs:// refs at authoring read-time. Both are satisfied by the same GCS
	// adapter (the *gcs.AtomMediaSigner). Nil ⇒ the respective behaviour is
	// skipped (re-home no-ops; gs:// refs return raw).
	MediaReHomer            ports.MediaReHomer
	AtomMediaDownloadSigner ports.AtomMediaDownloadSigner

	// WS-6a — Personal Collections (2026-05-26). CollectionRepository is the
	// gate: non-nil ⇒ the /api/v1/collections + /api/v1/me/collections routes
	// activate; nil ⇒ they 404 from the default mux. See collection_handler.go
	// for the route table.
	//
	// There was a CollectionAtomLookup here. It is GONE (ADR-233 D7) — its pg
	// adapter issued an untenanted read of the RLS-protected learning_atoms and
	// 500'd every add with 22P02. Atom existence is now decided by the
	// tenant-scoped CollectionFacts read below.
	CollectionRepository collection.Repository
	CollectionPublisher  collection.EventPublisher

	// ADR-229 / ADR-233 D10 reuse-consent gate (WS-4). CollectionFacts is the
	// pg collection repo (an intra-DB batch read over learning_atoms);
	// CollectionConsent + CollectionAuthz are the chora-sharing gRPC client.
	//
	// These are NOT nil-tolerant in the way CollectionPublisher is: the service
	// FAILS LOUD the moment a gate is needed and its port is unwired. A
	// silently-skipped consent gate would admit atoms the author never consented
	// to — the exact failure ADR-229 exists to prevent.
	CollectionFacts   collection.AtomReuseFactLookup
	CollectionConsent collection.ConsentContextFetcher
	CollectionAuthz   collection.CollectionUseAuthorizer

	// SavedAtomIDFetcher — fetches the caller's bookmarked atom IDs from
	// chora-sharing for the question picker's `saved` disjunct
	// (ux_unified_atom_picker.md). Nil ⇒ `saved` source returns empty
	// (dev / tests without sharing wired).
	SavedAtomIDFetcher ports.SavedAtomIDFetcher

	// ReuseContextFetcher — reads the caller's ADR-229 consent context
	// (active-grant atom ids + friend set) from chora-sharing once per
	// picker search (WS-2 chokepoint 1, CHO-2133). REQUIRED for source=all/
	// saved searches: nil ⇒ those searches fail loud with 500
	// CREATION_REUSE_CONTEXT_NOT_WIRED (a misconfigured gate must never
	// silently narrow to mine-only, and never fall back wide-open).
	ReuseContextFetcher ports.ReuseContextFetcher

	// W3.B.1 — QuestionBank (reusable question-atom pool, 2026-06-28). When
	// QuestionBankRepository + QuestionBankQuestionLookup are both non-nil the
	// /api/v1/question-banks + /api/v1/me/question-banks routes activate; missing
	// either disables them (404 from the default mux — fail-loud). See
	// question_bank_handler.go for the route table. QuestionBankPublisher is the
	// forward seam for Pub/Sub emission and is UNWIRED (nil) in B.1.
	QuestionBankRepository     questionbank.Repository
	QuestionBankQuestionLookup questionbank.QuestionLookup
	QuestionBankPublisher      questionbank.EventPublisher

	// W3.B.2 — assemble-test-set publisher (the thin adapter over the EXISTING
	// QuestionBatchAcceptedPublisher; reuses chora.creation.question_batch.accepted.v1,
	// no new contract). Nil ⇒ POST /api/v1/question-banks/{id}/assemble-test-set
	// returns 503 CREATION_TESTSET_PUBLISH_NOT_WIRED (fail-loud, never a silent
	// no-op). The CRUD routes are unaffected by this dep.
	QuestionBankTestSetPublisher questionbank.TestSetAssemblyPublisher

	// QuestionBankItemPager resolves a filtered/sorted/paginated page of a bank's
	// questions in SQL (huge-bank-safe; the pg QuestionBankRepository implements
	// it). Nil ⇒ GET /question-banks/{id}/questions fails loud. Production passes
	// the same *pg.QuestionBankRepository; tests inject a fake.
	QuestionBankItemPager questionbank.ItemPager

	// AtomEventPublisher emits chora.creation.atom.published.v1 via the durable
	// outbox on the DRAFT -> PUBLISHED transition (POST /api/atoms/{id}/publish).
	// Production passes the outbox-backed publisher; nil-tolerant so legacy /
	// test wirings skip the emit (mirrors CollectionPublisher).
	AtomEventPublisher atom.EventPublisher

	// AtomWriter persists an atom and enqueues its event in ONE transaction
	// (production wires pg.AtomTxWriter). Absent, NewRouterWithDeps falls
	// back to the NON-ATOMIC sequential writer and says so at startup, which
	// is the dev / unit-test shape only.
	AtomWriter atom.TransactionalPublisher

	// TopicClassifier backs POST /api/internal/atoms/backfill-topic-tags
	// (CHO-2142). Production passes the model-gateway-backed classifier (the
	// un-bypassable LLM chokepoint, ADR-163/177). Nil ⇒ that ONE route fails loud;
	// nothing else is affected.
	TopicClassifier atom.TopicClassifier

	// BackfillRuns persists topic-tag backfill run reports (CHO-2159). Production
	// passes the pg-backed store; the run is written `running` before the 202 and
	// terminal when the detached execution ends, so a 280s run survives a pod
	// restart and a poll can land on any replica. Nil ⇒ the backfill routes fail
	// loud; nothing else is affected.
	BackfillRuns atom.BackfillRunStore

	// BackfillRunTimeout bounds one detached backfill run; BackfillStaleCutoff is
	// the age past which a still-`running` row is reclaimed as stranded (a pod
	// died mid-run). Both come from env at the composition root (no inline
	// config); zero ⇒ the domain defaults (30m / 45m). StaleCutoff MUST exceed
	// RunTimeout or the sweep would reclaim LIVE runs — NewBackfillRunner refuses
	// the pair, and the routes then fail loud rather than lie to the operator.
	BackfillRunTimeout  time.Duration
	BackfillStaleCutoff time.Duration

	// TopicRepository backs the content topic-tree routes (CHO-2275, Sub-phase
	// A). Non-nil ⇒ the /api/topics[...] routes activate; nil ⇒ they 404 from the
	// default mux (fail-loud). pgx-backed in production; nil without a pool.
	TopicRepository topic.Repository

	// TopicSeedSink + TopicTagSource gate the seed-from-atom-tags backfill
	// (/api/internal/topics/backfill). In production both are the pgx adapters
	// (the SAME *pg.TopicRepository satisfies TopicSeedSink; a *pg.AtomTagSource
	// reads learning_atoms.tags). Either nil ⇒ that ONE internal route 503s; the
	// CRUD routes are unaffected.
	TopicSeedSink  topic.SeedSink
	TopicTagSource topic.AtomTagSource
	// DisplayNameResolver resolves the author's display name at publish time
	// so it can be denormalised onto atom.published.v1 (field 31). Nil ⇒
	// the event carries an empty author_display_name; consumers fall back
	// to a short GCID label.
	DisplayNameResolver ports.DisplayNameResolver
}

// NewRouter wires the public mux with default deps (no QGen wiring).
// Retained for backwards compatibility with existing callers; the
// /api/atoms/ai-assist endpoint will 503 until callers migrate to
// NewRouterWithDeps and pass QGenEngineResource.
func NewRouter(repo atom.Repository) http.Handler {
	return NewRouterWithDeps(RouterDeps{Repo: repo})
}

// NewRouterWithDeps wires the public mux with explicit deps. Preferred
// constructor for new call sites. Returns an http.Handler ready for
// ServeMux embedding or direct ListenAndServe.
func NewRouterWithDeps(deps RouterDeps) http.Handler {
	atomWriter := deps.AtomWriter
	if atomWriter == nil {
		// Fail-loud, not silent: without a transactional seam the atom row
		// and its outbox row cannot share a commit. The sequential writer
		// still surfaces an enqueue failure to the caller instead of
		// fabricating a success, but it cannot roll the save back.
		log.Printf("creation: RouterDeps.AtomWriter not supplied, using the NON-ATOMIC sequential writer (dev / test wiring only)")
		atomWriter = atom.NewSequentialPublisher(deps.Repo, deps.AtomEventPublisher)
	}
	h := &AtomHandler{
		repo:                deps.Repo,
		atomWriter:          atomWriter,
		embedIndexer:        deps.EmbedIndexer,
		questionRepo:        deps.QuestionRepository,
		aiAssistJobs:        deps.AiAssistJobs,
		aiAssistPublisher:   deps.AiAssistPublisher,
		mana:                deps.ManaLedger,
		atomMediaSigner:     deps.AtomMediaSigner,
		mediaDownloadSigner: deps.AtomMediaDownloadSigner,
		atomEventPublisher:  deps.AtomEventPublisher,
		topicClassifier:     deps.TopicClassifier,
		backfillRuns:        deps.BackfillRuns,
		displayNameResolver: deps.DisplayNameResolver,
	}
	// CHO-2159 — the async backfill lane. Built once here (the collaborators are
	// all fixed at wiring time) so a bad duration pair fails at BOOT with a loud
	// log rather than per-request. A nil runner makes the backfill routes fail
	// loud; it never degrades to the old synchronous behaviour, whose report
	// could not be returned at all.
	if deps.BackfillRuns != nil {
		runner, err := atom.NewBackfillRunner(atom.BackfillRunnerConfig{
			Backfill: atom.NewTopicTagBackfillService(
				&atomBackfillRepo{repo: deps.Repo},
				deps.TopicClassifier,
				deps.AtomEventPublisher,
				&handlerRevisionResolver{h: h},
			),
			Runs:        deps.BackfillRuns,
			RunTimeout:  deps.BackfillRunTimeout,
			StaleCutoff: deps.BackfillStaleCutoff,
		})
		if err != nil {
			log.Printf("creation: topic-tag backfill runner NOT wired: %v (POST/GET /api/internal/atoms/backfill-topic-tags will fail loud)", err)
		} else {
			h.backfillRunner = runner
		}

	}
	// OT#4 / ADR-210 — build the shared durable image re-home service over the
	// GCS copier + question repo, and wire it into all three SAVE seams
	// (publish here on AtomHandler, plus the questions + jobs handlers below).
	// Nil when either dep is absent (each seam then no-ops; images stay
	// transient).
	var reHomer *questionImageReHomer
	if deps.MediaReHomer != nil && deps.QuestionRepository != nil {
		reHomer = newQuestionImageReHomer(mediarehome.New(deps.MediaReHomer), deps.QuestionRepository)
	}
	h.reHomer = reHomer
	if deps.QuestionRepository != nil {
		h.questionsHandler = NewQuestionsHandler(deps.QuestionRepository, deps.Repo, deps.AtomMediaDownloadSigner, deps.JobEventPublisher, deps.SavedAtomIDFetcher, deps.ReuseContextFetcher)
		h.questionsHandler.reHomer = reHomer
	}
	// P4+P5 — wire the question-jobs handler when ALL three deps are
	// non-nil. Missing any of jobRepo / manaLedger / publisher disables
	// the routes (handler falls through to 404 / 503).
	if deps.QuestionJobRepository != nil && deps.ManaLedger != nil && deps.JobEventPublisher != nil && deps.QuestionRepository != nil {
		h.jobsHandler = NewQuestionJobsHandler(
			deps.QuestionJobRepository,
			deps.QuestionRepository,
			deps.Repo,
			deps.ManaLedger,
			deps.JobEventPublisher,
		)
		// ADR-210 — durably re-home a candidate's transient images at accept.
		h.jobsHandler.reHomer = reHomer
		// P6 — BlobStore is optional. When nil the multipart batch path
		// 503s; the JSON ai_draft path stays available.
		h.jobsHandler.SetBlobStore(deps.BlobStore)
		// Lane 1c — chunk store is optional best-effort (nil skips the
		// extraction step; the batch path itself stays available).
		h.jobsHandler.SetChunkStore(deps.SourceChunkRepository)
		// Lane 1c W3 — test-set accept publisher (nil ⇒ test_set accepts 503).
		h.jobsHandler.SetBatchAcceptedPublisher(deps.QuestionBatchAcceptedPublisher)
	}

	mux := http.NewServeMux()
	// Health + readiness — public, no tenant context required.
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/healthz/", healthz)
	mux.HandleFunc("/health", healthz)
	mux.HandleFunc("/readyz", h.readyz)
	mux.HandleFunc("/", h.indexHandler)

	// M14.iter5 — AI Assist drawer (Phyllis Step 4). MUST register BEFORE
	// the /api/atoms/ prefix below so ServeMux longest-match wins.
	mux.HandleFunc("/api/atoms/ai-assist", h.aiAssist)

	// OE-AI-ASSIST (Step 4c) — async status subtree. Registers the
	// prefix-match `/api/atoms/ai-assist/` so GET /api/atoms/ai-assist/
	// {job_id} routes here instead of falling through to atomsItem (which
	// would mis-parse `ai-assist` as an atom_id). MUST register BEFORE
	// the /api/atoms/ catch-all below.
	mux.HandleFunc("/api/atoms/ai-assist/", h.aiAssistSubtree)

	// P7 — 16-enum QuestionType registry (FE Phase H blocker). MUST
	// register BEFORE the /api/atoms/ prefix so ServeMux longest-match
	// wins (otherwise atomsItem treats "question-types" as an atom id
	// and 404s).
	mux.HandleFunc("/api/atoms/question-types", h.GetQuestionTypes)

	// FE B-FE-X5 (2026-05-16) — cross-atom question picker for the A+ X.2
	// test-set editor (ADR-155 D1). Static exact path MUST register BEFORE
	// the /api/atoms/ subtree so ServeMux longest-match wins (otherwise
	// atomsItem treats the literal "questions" as an atom_id and 404s).
	// Disabled when the question repo isn't wired — falls through to
	// atomsItem's 404 path so the gap is fail-loud.
	if h.questionsHandler != nil {
		mux.HandleFunc("/api/atoms/questions/search", h.questionsHandler.HandleSearchQuestions)
	}

	// One-off cluster-INTERNAL backfill — POST /api/internal/atoms/backfill-published.
	// Re-emits chora.creation.atom.published.v1 for every currently-published
	// atom of a tenant so chora-consumption's AtomPublishedSubscriber can flip
	// pre-existing atoms playable (the consumption migration defaulted atom_index
	// rows to draft; only NEW publishes emit atom.published). NOT exposed via the
	// public gateway: the /api/internal/* prefix bypasses the tenant-header gate
	// (middleware.isPublicPath) and the handler reads a REQUIRED tenant from the
	// request. Distinct prefix from /api/atoms, so registration order is
	// immaterial. The emit is a fail-loud 500 when the publisher is unwired.
	mux.HandleFunc("/api/internal/atoms/backfill-published", h.backfillPublished)

	// CHO-2273 follow-up — POST /api/internal/atoms/backfill-atom-index re-emits
	// atom.created.v1 (the UPSERT that CREATES the atom_index row with the derived
	// key) for every published, non-orphan atom, seeding rows that
	// backfill-published (UPDATE-only) cannot. Reuses emitAtomUpsert on the
	// questions handler (same proven wire shape). Registered only when the
	// questions handler is wired (production always wires it).
	if h.questionsHandler != nil {
		mux.HandleFunc("/api/internal/atoms/backfill-atom-index", h.questionsHandler.backfillAtomIndex)
	}

	// CHO-2142 — POST /api/internal/atoms/backfill-topic-tags. Classifies the
	// tenant's tagless PUBLISHED atoms via the model-gateway chokepoint, writes
	// learning_atoms.tags AT SOURCE, and re-emits atom.published.v1 (which now
	// carries field 7 topic_node_ids) through the outbox so chora-consumption
	// re-projects atom_index.topic_tags. ?dry_run=true previews the proposed tags
	// without touching a single row — the owner's gate.
	//
	// CHO-2159 — the POST now 202s and runs DETACHED (74 atoms x ~3.5s of gateway
	// classification is ~280s, and http.Server.WriteTimeout is 15s: the old
	// synchronous handler wrote its report to a closed socket). The report is
	// polled from the subtree route below. Distinct paths, so mux registration
	// order is immaterial: the exact path takes the POST, the subtree takes
	// /{run_id}.
	mux.HandleFunc("/api/internal/atoms/backfill-topic-tags", h.backfillTopicTags)
	mux.HandleFunc("/api/internal/atoms/backfill-topic-tags/", h.backfillTopicTagRun)

	// Protected /api/atoms[/...] dispatcher.
	mux.HandleFunc("/api/atoms", h.atomsCollection)
	mux.HandleFunc("/api/atoms/", h.atomsItem)

	// Content topic tree (CHO-2275, Sub-phase A). Gated on the repository: nil ⇒
	// the routes 404 from the default mux (fail-loud). The seed-from-atom-tags
	// backfill activates only when BOTH the sink + tag source are wired; absent
	// either, that ONE internal route 503s (the CRUD routes are unaffected).
	if deps.TopicRepository != nil {
		var topicBackfill *topic.SeedBackfill
		if deps.TopicSeedSink != nil && deps.TopicTagSource != nil {
			topicBackfill = topic.NewSeedBackfill(deps.TopicTagSource, deps.TopicSeedSink)
		} else {
			log.Printf("creation: topic backfill NOT wired (POST /api/internal/topics/backfill will 503)")
		}
		th := NewTopicHandler(deps.TopicRepository, topicBackfill)
		mux.HandleFunc("/api/topics", th.collection)
		mux.HandleFunc("/api/topics/", th.item)
		mux.HandleFunc("/api/internal/topics/backfill", th.backfillHandler)
		log.Printf("creation: topic-tree routes wired (/api/topics[/...] + /api/internal/topics/backfill)")
	} else {
		log.Printf("creation: TopicRepository NOT wired — /api/topics routes will 404")
	}

	// WS-6a — Personal Collections routes (2026-05-26). Gated on the repository
	// alone; without it the routes 404 from the default mux (fail-loud per
	// feedback_no_stubs_real_wiring). The gate ports are NOT part of this
	// condition on purpose: an unwired gate must REFUSE loudly at call time
	// (ADR-229), not silently un-mount the whole route table.
	if deps.CollectionRepository != nil {
		collectionSvc := collection.NewService(
			deps.CollectionRepository,
			deps.CollectionPublisher, // nil-tolerant: events are dropped if unwired (boot logs the gap)
			deps.CollectionFacts,     // ADR-233: fail-loud when a gate needs it
			deps.CollectionConsent,
			deps.CollectionAuthz,
		)
		mountCollectionRoutes(mux, NewCollectionHandler(collectionSvc))
	}

	// W3.B.1 — QuestionBank routes (2026-06-28). Only wired when both the
	// repository and the question lookup are non-nil; otherwise the routes
	// 404 from the default mux (fail-loud per feedback_no_stubs_real_wiring).
	// QuestionBankPublisher is nil-tolerant: B.1 ships it unwired (no events).
	if deps.QuestionBankRepository != nil && deps.QuestionBankQuestionLookup != nil {
		questionBankSvc := questionbank.NewService(
			deps.QuestionBankRepository,
			deps.QuestionBankQuestionLookup,
			deps.QuestionBankPublisher, // nil in B.1 — generic question_bank.* emission deferred
		).SetTestSetAssembler(deps.QuestionBankTestSetPublisher). // W3.B.2 (nil ⇒ assemble-test-set 503s fail-loud)
										SetItemPager(deps.QuestionBankItemPager) // server-side question paging (nil ⇒ list fails loud)
		mountQuestionBankRoutes(mux, NewQuestionBankHandler(questionBankSvc))
	}

	// Compose middleware: logging -> tenantContext -> mux.
	return logging(tenantContext(mux))
}

// -----------------------------------------------------------------------------
// Health + readiness + index
// -----------------------------------------------------------------------------

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *AtomHandler) readyz(w http.ResponseWriter, _ *http.Request) {
	if h.repo == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "repo-uninitialised"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (h *AtomHandler) indexHandler(w http.ResponseWriter, r *http.Request) {
	// Only return the service banner on the bare "/" path; everything else 404s.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "chora-creation",
		"surface": "A+ Creator mode",
		"domain":  "Content Creation",
		"project": "chora-content",
	})
}

// -----------------------------------------------------------------------------
// Collection: /api/atoms
// -----------------------------------------------------------------------------

func (h *AtomHandler) atomsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listAtoms(w, r)
	case http.MethodPost:
		h.createAtom(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET and POST are supported on /api/atoms")
	}
}

// -----------------------------------------------------------------------------
// Item: /api/atoms/{atom_id}
// -----------------------------------------------------------------------------

func (h *AtomHandler) atomsItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/atoms/")
	id = strings.TrimSuffix(id, "/")

	// P3 + P4+P5 — /api/atoms/{atom_id}/...:
	//   - /questions[...]                  → P3 QuestionsHandler (manual CRUD)
	//   - /questions/{qid}/ai-model-answer-jobs → P5 QuestionJobsHandler
	//   - /question-jobs[...]              → P5 QuestionJobsHandler
	//   - /publish                         → A22 publishAtom handler (Phase K)
	if slash := strings.Index(id, "/"); slash != -1 {
		atomID := id[:slash]
		rest := id[slash:] // includes leading "/"

		// First check P5 question-jobs routes (these are higher-priority because
		// they share a /questions/ prefix with the P3 routes).
		if strings.HasPrefix(rest, "/question-jobs") {
			if h.jobsHandler == nil {
				writeError(w, http.StatusServiceUnavailable, "CREATION_JOBS_NOT_WIRED",
					"question-jobs is not wired in this deployment")
				return
			}
			h.jobsHandler.dispatch(w, r, atomID, rest)
			return
		}
		// /questions/{qid}/ai-model-answer-jobs — let the jobs handler try
		// first; if it doesn't claim the path, fall back to the P3 handler.
		if strings.HasPrefix(rest, "/questions") {
			if h.jobsHandler != nil {
				if h.jobsHandler.dispatch(w, r, atomID, rest) {
					return
				}
			}
			if h.questionsHandler == nil {
				writeError(w, http.StatusServiceUnavailable, "CREATION_QUESTIONS_NOT_WIRED",
					"question authoring is not wired in this deployment")
				return
			}
			h.questionsHandler.dispatch(w, r, atomID, strings.TrimPrefix(rest, "/questions"))
			return
		}
		// A22 Phase K — POST /api/atoms/{atom_id}/publish.
		if rest == "/publish" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only POST is supported on /api/atoms/{id}/publish")
				return
			}
			h.publishAtom(w, r, atomID)
			return
		}
		// ATOM Phase 1 (ADR-156) — POST /api/atoms/{atom_id}/media mints a
		// V4 signed URL for atom-media image upload (B2 lane in plan
		// docs/m13/atom-phase1-execution-plan-2026-05-17.md §2). Handler
		// body lives in atom_media_handler.go.
		if rest == "/media" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only POST is supported on /api/atoms/{id}/media")
				return
			}
			h.mintAtomMediaSignedUrl(w, r, atomID)
			return
		}
		// ADR-199 Wave 2 — POST /api/atoms/{atom_id}/clone clones the source
		// atom + its live question into a NEW draft variant owned by the caller.
		if rest == "/clone" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only POST is supported on /api/atoms/{id}/clone")
				return
			}
			h.cloneAtom(w, r, atomID)
			return
		}
		// ADR-229 WS-1 (CHO-2127) — PATCH /api/atoms/{atom_id}/reuse-visibility
		// sets the author-only reuse-consent audience. Handler body lives in
		// reuse_visibility_handler.go.
		if rest == "/reuse-visibility" {
			if r.Method != http.MethodPatch {
				writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
					"only PATCH is supported on /api/atoms/{id}/reuse-visibility")
				return
			}
			h.patchAtomReuseVisibility(w, r, atomID)
			return
		}
		// Other nested sub-resources are not yet supported.
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}

	if id == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}

	// F2 (Phyllis demo blocker) — GET /api/atoms/new returns an empty
	// AtomDraft template per the chora-web AtomAuthoringService contract
	// (atom-authoring.service.ts:36-40, atom-authoring.model.ts §AtomDraft).
	// The literal "new" path segment is the FE's signal that the user is
	// authoring a fresh atom, NOT looking up an existing one by that
	// (unlikely) id. No DB write — pure response template.
	if id == "new" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only GET is supported on /api/atoms/new")
			return
		}
		h.handleAtomDraftTemplate(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getAtom(w, r, id)
	case http.MethodPatch:
		h.patchAtom(w, r, id)
	case http.MethodDelete:
		h.deleteAtom(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET, PATCH, DELETE are supported on /api/atoms/{id}")
	}
}

// handleAtomDraftTemplate returns the empty AtomDraft envelope the FE
// binds its authoring form to. Shape matches
// chora-web/src/app/features/surfaces/aplus/atom-authoring/atom-authoring.model.ts
// — defaults to cognitiveLevel="remembering" (the easiest Bloom rung) and
// state="DRAFT".
//
// Per `feedback_no_inline_config` — no env-driven values here; the template
// is pure FE contract glue, not deployment config.
func (h *AtomHandler) handleAtomDraftTemplate(w http.ResponseWriter, _ *http.Request) {
	type fdTag struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type fdPrereq struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type fdObjective struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	type feAtomDraft struct {
		AtomID         *string       `json:"atomId"`
		Title          string        `json:"title"`
		Body           string        `json:"body"`
		CourseCode     string        `json:"courseCode"`
		Topic          string        `json:"topic"`
		CognitiveLevel string        `json:"cognitiveLevel"`
		Tags           []fdTag       `json:"tags"`
		Prerequisites  []fdPrereq    `json:"prerequisites"`
		Objectives     []fdObjective `json:"objectives"`
		State          string        `json:"state"`
	}
	writeJSON(w, http.StatusOK, feAtomDraft{
		AtomID:         nil,
		Title:          "",
		Body:           "",
		CourseCode:     "",
		Topic:          "",
		CognitiveLevel: "remembering",
		Tags:           []fdTag{},
		Prerequisites:  []fdPrereq{},
		Objectives:     []fdObjective{},
		State:          "DRAFT",
	})
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

// createAtomRequest accepts the ADR-156 Phase 1 envelope per
// chora-contracts/openapi/creation-admin.yaml §CreateAtomRequest. The
// canonical shape is:
//
//	POST /api/atoms
//	  {"question_type":"mcq","stem":"What is photosynthesis?",
//	   "title":"Biology","subject":"biology","cognitive_level":"comprehension",
//	   "imda_dimension_tags":["transparency"],"author_note":"demo",
//	   "media_assets":[],"body":"","mode":"straight-up","difficulty":1,
//	   "locale":"en-SG"}
//
// Backwards-compat shim per plan §8 (`docs/m13/atom-phase1-execution-plan-2026-05-17.md`)
// + ADR-156 Decision #2: the legacy `atom_type` request field is accepted as
// a synonym for `question_type` for one release cycle. When both are present,
// `question_type` wins.
//
// Unknown fields are tolerated (no DisallowUnknownFields) so FE/BE can evolve
// the envelope independently.
type createAtomRequest struct {
	// Legacy fields (kept for the existing skeleton tests + Phyllis path).
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
	Mode  string   `json:"mode"`

	// OpenAPI-canonical fields per ADR-156 Phase 1.
	// QuestionType is the canonical question-type discriminator. AtomType is
	// the deprecated one-release-cycle synonym (§8). When both are supplied,
	// QuestionType wins.
	QuestionType      string            `json:"question_type,omitempty"`
	AtomType          string            `json:"atom_type,omitempty"`
	Stem              string            `json:"stem,omitempty"`
	Subject           string            `json:"subject,omitempty"`
	CognitiveLevel    string            `json:"cognitive_level,omitempty"`
	ImdaDimensionTags []string          `json:"imda_dimension_tags,omitempty"`
	AuthorNote        string            `json:"author_note,omitempty"`
	MediaAssets       []atom.MediaAsset `json:"media_assets,omitempty"`
	Locale            string            `json:"locale,omitempty"`
	TopicNodeIDs      []string          `json:"topic_node_ids,omitempty"`
	Difficulty        int               `json:"difficulty,omitempty"`
}

// resolveQuestionType maps the wire question_type / atom_type to the
// canonical atom.AtomType (mcq | flashcard | video | essay | outline).
// Per ADR-156 §8: question_type wins when both are supplied; atom_type is
// the deprecated one-release-cycle synonym. Returns empty when neither is
// supplied (legacy {title,body,tags,mode} shape — type tag stays unset).
// resolveQuestionType maps the wire question_type / atom_type onto the domain's
// canonical flavour, and REFUSES anything outside it (CHO-2178).
//
// The old version ended `default: return atom.AtomType(strings.ToLower(raw))` —
// "unknown strings are lowercased and passed through (the type tag is
// informational at this layer)". It was not informational. That string lands in
// a free-text varchar with no CHECK, and the outbox then has to encode it as a
// closed proto enum. Anything the enum cannot name fails the marshal, so the
// atom NEVER emits atom.published.v1 and no other domain ever learns it exists
// — while this handler answers 201. `banana` minted such an atom. So did every
// TRUE_FALSE / MATCHING / ORDERING / CODE / SIMULATION the old OpenAPI enum
// advertised, none of which the domain calls valid.
//
// A flavour that cannot survive the wire is refused HERE, where the author can
// still be told, instead of being stranded silently at publish time.
func resolveQuestionType(questionType, atomType string) (atom.AtomType, error) {
	raw := questionType
	if raw == "" {
		raw = atomType
	}
	if raw == "" {
		// Unset is not invalid — an atom may be typed later.
		return "", nil
	}
	// FE-facing OpenAPI aliases -> the canonical vocabulary (atom/phyllis.go
	// §AllAtomTypes: mcq | flashcard | video | essay | outline).
	var t atom.AtomType
	switch raw {
	case "MULTIPLE_CHOICE", "mcq", "MCQ":
		t = atom.TypeMCQ
	case "FILL_BLANK", "flashcard", "FLASHCARD":
		t = atom.TypeFlashcard
	case "MULTIMEDIA", "video", "VIDEO":
		t = atom.TypeVideo
	case "ESSAY", "SHORT_ANSWER", "essay":
		t = atom.TypeEssay
	case "OUTLINE", "outline":
		t = atom.TypeOutline
	default:
		t = atom.AtomType(strings.ToLower(raw))
	}
	if !t.Valid() {
		return "", fmt.Errorf(
			"question_type %q is not a supported atom flavour — supported: %v (or the aliases "+
				"MULTIPLE_CHOICE, SHORT_ANSWER, ESSAY, FILL_BLANK, MULTIMEDIA, OUTLINE)",
			raw, atom.AllAtomTypes)
	}
	return t, nil
}

func (h *AtomHandler) createAtom(w http.ResponseWriter, r *http.Request) {
	var req createAtomRequest
	// Per A20 — accept unknown fields (e.g., future contract extensions) so
	// FE/BE can evolve the envelope independently. DisallowUnknownFields is
	// inappropriate for a public mint endpoint.
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	if strings.TrimSpace(tenantID) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_TENANT", "tenant_id is required")
		return
	}
	if strings.TrimSpace(gcid) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_GCID", "gcid is required")
		return
	}

	mode := atom.Mode(req.Mode)
	if mode == "" {
		mode = atom.ModeStraightUp // sensible default per Tier 1: linear cert path
	}
	if !mode.Valid() {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ATOM", fmt.Sprintf("invalid mode: %q", string(mode)))
		return
	}

	// Construct the LearningAtom directly per ADR-156 Phase 1 §1: title is
	// optional (relaxed from NOT NULL per Decision #1), stem is required.
	// Bypass atom.New (which still enforces the legacy title-required gate)
	// and use ValidatePhase1 below as the canonical aggregate validator.
	id, err := uuid.NewV7()
	if err != nil {
		log.Printf("uuidv7 error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to mint atom_id")
		return
	}
	now := time.Now().UTC()
	tags := append([]string(nil), req.Tags...) // defensive copy

	a := &atom.LearningAtom{
		AtomID:    id.String(),
		TenantID:  tenantID,
		Gcid:      gcid,
		Title:     strings.TrimSpace(req.Title),
		Body:      strings.TrimSpace(req.Body),
		Tags:      tags,
		Mode:      mode,
		Status:    atom.StatusDraft,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Phase 1 fields.
	a.Stem = strings.TrimSpace(req.Stem)
	a.Subject = strings.TrimSpace(req.Subject)
	a.AuthorNote = req.AuthorNote
	if req.CognitiveLevel != "" {
		a.CognitiveLevel = atom.CognitiveLevel(req.CognitiveLevel)
	}
	if len(req.ImdaDimensionTags) > 0 {
		tags := make([]atom.ImdaDimTag, 0, len(req.ImdaDimensionTags))
		for _, t := range req.ImdaDimensionTags {
			tags = append(tags, atom.ImdaDimTag(t))
		}
		a.ImdaDimensionTags = tags
	}
	if len(req.MediaAssets) > 0 {
		// Defensive copy of the slice header so callers cannot mutate the
		// persisted assets via the request reference.
		assets := make([]atom.MediaAsset, len(req.MediaAssets))
		copy(assets, req.MediaAssets)
		a.MediaAssets = assets
	}
	// Resolve question_type with atom_type fallback per ADR-156 §8
	// backwards-compat shim. A flavour the domain cannot represent is refused
	// here (CHO-2178) — accepting it would mint an atom that can never emit
	// atom.published.v1 and so can never be seen outside chora-creation.
	questionType, err := resolveQuestionType(req.QuestionType, req.AtomType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_ATOM_TYPE_UNSUPPORTED", err.Error())
		return
	}
	a.QuestionType = questionType
	if req.Difficulty > 0 {
		a.Difficulty = req.Difficulty
	}

	// Aggregate-level validation per ADR-156 Phase 1 invariants. Stem
	// required, title optional ≤256, subject optional ≤64, cognitive_level
	// optional but must be Bloom 6-enum when present, imda_dimension_tags
	// ≤4 with canonical labels, media_assets ≤1 image entry per Decision #5.
	if err := a.ValidatePhase1(); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ATOM", err.Error())
		return
	}

	// Emit atom.created.v1 (ADR-217 Debt 3) so the tenancy atom-count projection
	// counts atoms minted via the bare create surface — parity with the Phyllis /
	// AI-assist / batch create paths. The atom row and the outbox row are ONE
	// transaction: a failed enqueue rolls the atom back and 500s, rather than
	// handing the author a 201 for an atom nothing downstream will hear about.
	ev := atom.NewAtomCreatedEvent(a, effectiveTraceparent(r), r.Header.Get("tracestate"))
	if err := h.atomWriter.SaveAndPublish(r.Context(), a, ev); err != nil {
		log.Printf("createAtom: save+enqueue failed, nothing committed: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist atom")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

func (h *AtomHandler) getAtom(w http.ResponseWriter, r *http.Request, id string) {
	// ADR-191 D3 layer-2 — a PROCTOR-claimed caller is embargoed from atom
	// content (403 + audit), before any content is read or projected.
	if enforceExamContentEmbargo(w, r) {
		return
	}
	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, id)
	if err != nil {
		writeNotFound(w, err)
		return
	}
	// A16 read-side projection — surface mcq_payload / essay_payload AND the
	// WS-0b unified question_payload on the atom envelope when a Question
	// exists for the atom and a repository is wired. The projection is
	// LEARNER-safe (no is_correct, no explainer, no model_answer; rubric IS
	// surfaced for OE so learners understand grading criteria). Author-facing
	// detail lives on GET /api/atoms/{atom_id}/questions/{question_id} which
	// returns the FULL question shape via the QuestionsHandler.
	if h.questionRepo == nil {
		writeJSON(w, http.StatusOK, a)
		return
	}
	q, _, qerr := h.questionRepo.GetByAtomID(r.Context(), tenantID, id)
	if qerr != nil && !errors.Is(qerr, question.ErrNotFound) {
		// Atom load already succeeded; surface the atom even if the question
		// lookup blew up so the FE renders the editor scaffolding. Log for
		// triage.
		log.Printf("getAtom: question lookup error: %v", qerr)
		writeJSON(w, http.StatusOK, a)
		return
	}
	if q == nil {
		writeJSON(w, http.StatusOK, a)
		return
	}
	// Fail loud per `feedback_no_stubs_real_wiring`: a question with type=mcq
	// but no MCQ payload (or empty options[]) is a domain-invariant violation
	// that must NOT result in a silent empty payload. Surface 500 so the
	// upstream BFF can map to 502 and the FE renders an explicit error
	// rather than blanking the question.
	if err := validateProjectableQuestion(q); err != nil {
		log.Printf("getAtom: malformed question for atom=%s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "CREATION_QUESTION_MALFORMED", err.Error())
		return
	}
	envelope, perr := h.projectAtomWithQuestion(r.Context(), a, q)
	if perr != nil {
		// CHO-2272 — a question that cannot be projected to a learner-safe,
		// unambiguously-keyed shape must fail loud, never serve a partial or
		// answer-leaking payload.
		log.Printf("getAtom: unprojectable question for atom=%s: %v", id, perr)
		writeError(w, http.StatusInternalServerError, "CREATION_QUESTION_MALFORMED", perr.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope)
}

// validateProjectableQuestion checks that the question carries a payload
// matching its Type. Domain invariant violations (mcq with no options,
// mcq with no MCQ payload, oe with no OE payload) trigger a 500 so the
// projection never silently emits an empty envelope.
func validateProjectableQuestion(q *question.Question) error {
	if q == nil {
		return nil
	}
	switch q.Type {
	case question.TypeMCQ:
		if q.MCQ == nil {
			return fmt.Errorf("question type=mcq requires mcq payload; got nil")
		}
		if len(q.MCQ.Options) == 0 {
			return fmt.Errorf("question type=mcq requires non-empty options[]; got 0")
		}
		// CHO-2272 (SECURITY) — the learner projection derives served option ids
		// from display content; refuse to serve an atom whose id-space is
		// underivable (an option with no display content) or ambiguous (two
		// options with identical display text would grade identically).
		if _, err := q.MCQ.ServedLearnerOptions(); err != nil {
			return fmt.Errorf("question type=mcq is not servable: %w", err)
		}
	case question.TypeOpenEnded:
		if q.OE == nil {
			return fmt.Errorf("question type=oe requires oe payload; got nil")
		}
	}
	return nil
}

// projectAtomWithQuestion produces the merged response shape — the baseline
// atom JSON plus the LEARNER-safe payload projection. Three layered keys:
//
//	mcq_payload     — legacy field shim (kept for test-set-editor + existing FE)
//	essay_payload   — legacy field shim
//	question_payload — WS-0b A16 unified discriminated envelope (MCQ | OE)
//
// All three are learner-safe — IsCorrect / Explainer / ModelAnswer are stripped.
// `correct_option_id` is NEVER emitted by chora-creation (the BFF gates it
// behind ?mode=author + tenant role per gateway.yaml §LearningAtomEnvelope).
// The original Question is NOT serialised verbatim; fields are filtered
// through Question.MCQ.LearnerProjection() / OE.LearnerProjection().
func (h *AtomHandler) projectAtomWithQuestion(ctx context.Context, a *atom.LearningAtom, q *question.Question) (map[string]any, error) {
	// Encode the atom as JSON, then decode back to a map so we can append
	// projection fields without redeclaring the atom shape. Avoids drift
	// between the baseline LearningAtom encoding and the projection envelope.
	atomBytes, err := json.Marshal(a)
	if err != nil {
		// Marshal failure on a domain struct is pathological; fall back to
		// an empty envelope so the FE gets a deterministic shape.
		return map[string]any{}, nil
	}
	envelope := map[string]any{}
	if err := json.Unmarshal(atomBytes, &envelope); err != nil {
		return map[string]any{}, nil
	}
	switch q.Type {
	case question.TypeMCQ:
		if q.MCQ == nil {
			break
		}
		proj := q.MCQ.LearnerProjection()
		// CHO-2272 (SECURITY) — derive the learner-facing option ids + order from
		// display content, so no stored id (opt_1 / opt_correct) and no stored
		// order (correct-first) reaches a learner. Fail loud rather than serve an
		// atom whose id-space is ambiguous/underivable (validateProjectableQuestion
		// pre-checks this too; this propagation is the belt to that suspenders).
		servedOpts, serr := q.MCQ.ServedLearnerOptions()
		if serr != nil {
			return nil, fmt.Errorf("mcq is not servable: %w", serr)
		}
		// CHO-1638 — mint the QUESTION illustration's durable gs:// ref to a
		// fresh signed GET URL for the learner-safe projection. The question
		// image is safe on every learner surface (atomic-session take, active
		// duel); the model-answer illustration (q.MCQ.AnswerImageURL) is NEVER
		// read here, so it can never leak to a learner pre-grade (Security-wins).
		signedQImg := h.signQuestionImage(ctx, q.MCQ.ImageURL)
		mcqEnvelope := map[string]any{
			"question_id": q.QuestionID,
			"prompt":      q.Prompt,
			"options":     learnerSafeMCQOptions(servedOpts),
		}
		if proj.XPOnCorrect > 0 {
			mcqEnvelope["xp_on_correct"] = proj.XPOnCorrect
		}
		if proj.TimerSeconds > 0 {
			mcqEnvelope["timer_seconds"] = proj.TimerSeconds
		}
		if signedQImg != nil {
			mcqEnvelope["image_url"] = *signedQImg
		}
		envelope["mcq_payload"] = mcqEnvelope
		// WS-0b A16 — unified question_payload (discriminated `type: mcq`).
		// Same projection content as mcq_payload + a `type` discriminator.
		// xp_on_correct is REQUIRED on the envelope per OpenAPI spec;
		// surface 0 when the author didn't set one.
		qpMCQ := map[string]any{
			"type":          "mcq",
			"question_id":   q.QuestionID,
			"prompt":        q.Prompt,
			"options":       learnerSafeMCQOptions(servedOpts),
			"xp_on_correct": proj.XPOnCorrect,
		}
		if proj.TimerSeconds > 0 {
			qpMCQ["timer_seconds"] = proj.TimerSeconds
		}
		if signedQImg != nil {
			qpMCQ["image_url"] = *signedQImg
		}
		envelope["question_payload"] = qpMCQ
	case question.TypeOpenEnded:
		if q.OE == nil {
			break
		}
		// LearnerProjection strips ModelAnswer + WeightedRubric — kept for
		// the legacy essay_payload shim. The new question_payload envelope
		// surfaces the rubric to learners so they understand the grading
		// criteria (per WS-0b A16 OE projection — model_answer remains
		// stripped, but rubric is visible).
		envelope["essay_payload"] = map[string]any{
			"question_id": q.QuestionID,
			"prompt":      q.Prompt,
		}
		// WS-0b A16 — unified question_payload (discriminated `type: oe`).
		// Surface rubric criteria (learner-visible) + max_score (derived
		// from rubric weight totals; defaults to 100 per OE invariant). The
		// model_answer is NEVER projected.
		qpOE := map[string]any{
			"type":        "oe",
			"question_id": q.QuestionID,
			"prompt":      q.Prompt,
			"max_score":   oeMaxScore(q.OE),
		}
		if rubric := learnerSafeOERubric(q.OE); rubric != nil {
			qpOE["rubric"] = rubric
		}
		envelope["question_payload"] = qpOE
	}
	return envelope, nil
}

// signQuestionImage mints a durable gs:// QUESTION illustration ref to a fresh
// short-lived signed GET URL for the LEARNER-safe projection. Mirrors the
// author getQuestion mint (questions_handler.mintMediaRef) but is ONLY ever
// called with the QUESTION image — the model-answer illustration is never
// projected to learners (Security-wins). A transient https ref passes through;
// a nil/empty ref, an unwired signer, or a mint failure drops the ref to nil so
// a raw gs:// is NEVER emitted to the wire.
func (h *AtomHandler) signQuestionImage(ctx context.Context, ref *string) *string {
	if ref == nil || *ref == "" {
		return nil
	}
	if !strings.HasPrefix(*ref, "gs://") {
		// Transient https ref (draft, pre-publish) — already fetchable.
		return ref
	}
	if h.mediaDownloadSigner == nil {
		// No signer wired — never emit a raw gs:// to a learner.
		return nil
	}
	signed, err := h.mediaDownloadSigner.SignDownloadURL(ctx, *ref)
	if err != nil {
		log.Printf("getAtom: mint learner question-image gs-uri=%s: %v (dropping)", *ref, err)
		return nil
	}
	url := signed.URL
	return &url
}

// oeMaxScore returns the OE max achievable score. Derived from the
// WeightedRubric weights (which sum to 100 per the OE domain invariant).
// When no weighted rubric is set, the canonical default is 100 (the
// implicit "everything-or-nothing" weighting).
func oeMaxScore(p *question.OEPayload) int {
	if p == nil || p.WeightedRubric == nil || len(p.WeightedRubric.Criteria) == 0 {
		return 100
	}
	sum := 0
	for _, c := range p.WeightedRubric.Criteria {
		sum += c.WeightPercent
	}
	if sum <= 0 {
		return 100
	}
	return sum
}

// learnerSafeOERubric returns the learner-visible rubric structure
// (criterion_id + description + weight_percent). model_answer is NEVER
// included. Returns nil when no weighted rubric is set (so the JSON
// envelope omits the field).
func learnerSafeOERubric(p *question.OEPayload) map[string]any {
	if p == nil || p.WeightedRubric == nil || len(p.WeightedRubric.Criteria) == 0 {
		return nil
	}
	crits := make([]map[string]any, len(p.WeightedRubric.Criteria))
	for i, c := range p.WeightedRubric.Criteria {
		crits[i] = map[string]any{
			"criterion_id":   c.CriterionID,
			"description":    c.Description,
			"weight_percent": c.WeightPercent,
		}
	}
	return map[string]any{"criteria": crits}
}

// learnerSafeMCQOptions strips IsCorrect + Explainer at the wire layer (the
// MCQOption struct still carries the fields for the adapter to encode; the
// projection emits a leaner map with only learner-visible columns).
func learnerSafeMCQOptions(opts []question.MCQOption) []map[string]any {
	out := make([]map[string]any, len(opts))
	for i, o := range opts {
		out[i] = map[string]any{
			"option_id": o.OptionID,
			"label":     o.Label,
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

type listAtomsResponse struct {
	Items []*atom.LearningAtom `json:"items"`
	Total int                  `json:"total"`
}

func (h *AtomHandler) listAtoms(w http.ResponseWriter, r *http.Request) {
	// ADR-191 D3 layer-2 — a PROCTOR-claimed caller is embargoed from atom
	// content (403 + audit), before any atoms are listed.
	if enforceExamContentEmbargo(w, r) {
		return
	}
	tenantID := tenantFromContext(r.Context())
	q := r.URL.Query()
	filter := atom.ListFilter{
		Status: atom.Status(q.Get("status")),
		Query:  strings.TrimSpace(q.Get("q")),
	}
	if filter.Status != "" &&
		filter.Status != atom.StatusDraft &&
		filter.Status != atom.StatusPublished &&
		filter.Status != atom.StatusArchived {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_STATUS",
			"status must be one of draft|published|archived")
		return
	}

	items, err := h.repo.List(r.Context(), tenantID, filter)
	if err != nil {
		log.Printf("list error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to list atoms")
		return
	}
	writeJSON(w, http.StatusOK, listAtomsResponse{Items: items, Total: len(items)})
}

// -----------------------------------------------------------------------------
// Patch
// -----------------------------------------------------------------------------

// patchAtomRequest is the partial-update payload per ADR-156 Phase 1 +
// chora-contracts/openapi/creation-admin.yaml §UpdateAtomRequest. All fields
// are optional; only fields present in the body are applied (pointer types
// distinguish "field omitted" from "field set to empty/zero"). Per ADR-156
// Decision #2 + plan §1, `question_type` is IMMUTABLE on PATCH — supplying
// it returns 400 with envelope CREATION_QUESTION_TYPE_IMMUTABLE.
type patchAtomRequest struct {
	// Legacy fields.
	Title *string   `json:"title,omitempty"`
	Body  *string   `json:"body,omitempty"`
	Tags  *[]string `json:"tags,omitempty"`
	Mode  *string   `json:"mode,omitempty"`

	// ADR-156 Phase 1 fields (all optional on PATCH).
	Stem              *string            `json:"stem,omitempty"`
	Subject           *string            `json:"subject,omitempty"`
	CognitiveLevel    *string            `json:"cognitive_level,omitempty"`
	ImdaDimensionTags *[]string          `json:"imda_dimension_tags,omitempty"`
	AuthorNote        *string            `json:"author_note,omitempty"`
	MediaAssets       *[]atom.MediaAsset `json:"media_assets,omitempty"`
	Difficulty        *int               `json:"difficulty,omitempty"`

	// Immutable on PATCH per ADR-156 Decision #2 — surfaced so we can
	// detect + reject the field at the handler.
	QuestionType *string `json:"question_type,omitempty"`
	AtomType     *string `json:"atom_type,omitempty"`
}

func (h *AtomHandler) patchAtom(w http.ResponseWriter, r *http.Request, id string) {
	var req patchAtomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}

	// Reject question_type / atom_type mutation per ADR-156 Decision #2 +
	// plan §1 immutability. Fail loud per `feedback_no_stubs_real_wiring`
	// rather than silently dropping the field.
	if req.QuestionType != nil || req.AtomType != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_TYPE_IMMUTABLE",
			"question_type (and the deprecated atom_type synonym) is immutable on PATCH per ADR-156 Decision #2")
		return
	}

	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, id)
	if err != nil {
		writeNotFound(w, err)
		return
	}
	if a.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
		return
	}
	// CHO-2264 (SECURITY) — author-only, checked the moment the atom is known
	// and BEFORE any business rule: a non-author must not learn whether an atom
	// is orphan-frozen, and must not reach ApplyUpdate at all.
	if refuseNonAuthor(w, a, gcidFromContext(r.Context())) {
		return
	}
	if a.IsOrphan() {
		// ADR-229 A1.2 (CHO-2132) — orphan editions are immutable. Guarded
		// here BEFORE the direct Phase-1 field mutations below (ApplyUpdate's
		// domain guard alone would fire too late to protect them).
		writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
			"orphan editions are frozen; fork via clone to evolve")
		return
	}

	// ADR-244 D5 - capture the pre-patch metadata BEFORE any field lands, so
	// the event below can name exactly what moved (changed_fields) instead of
	// echoing back whatever the request happened to mention.
	before := snapshotAtomMetadata(a)

	// Apply Phase 1 fields directly on the aggregate. ApplyUpdate is the
	// legacy entry point and only covers title/body/tags/mode; Phase 1
	// additions are intentionally outside the ApplyUpdate surface so the
	// domain stays additive-only per `ddd-enforcement.md` aggregate
	// invariant #4 (append-only revision semantics) — handler is the
	// composition site.
	if req.Stem != nil {
		a.Stem = strings.TrimSpace(*req.Stem)
	}
	if req.Subject != nil {
		a.Subject = strings.TrimSpace(*req.Subject)
	}
	if req.AuthorNote != nil {
		a.AuthorNote = *req.AuthorNote
	}
	if req.CognitiveLevel != nil {
		if *req.CognitiveLevel == "" {
			a.CognitiveLevel = ""
		} else {
			a.CognitiveLevel = atom.CognitiveLevel(*req.CognitiveLevel)
		}
	}
	if req.ImdaDimensionTags != nil {
		tags := make([]atom.ImdaDimTag, 0, len(*req.ImdaDimensionTags))
		for _, t := range *req.ImdaDimensionTags {
			tags = append(tags, atom.ImdaDimTag(t))
		}
		a.ImdaDimensionTags = tags
	}
	if req.MediaAssets != nil {
		assets := make([]atom.MediaAsset, len(*req.MediaAssets))
		copy(assets, *req.MediaAssets)
		a.MediaAssets = assets
	}
	if req.Difficulty != nil {
		a.Difficulty = *req.Difficulty
	}

	// Apply the legacy patch fields via ApplyUpdate (which also bumps
	// revision + updates UpdatedAt). When the legacy patch fields are all
	// nil, ApplyUpdate is a no-op except for revision/timestamp — but PATCH
	// semantics demand a revision bump even for Phase-1-only PATCHes, so
	// invoke it unconditionally for the side effect.
	var modePtr *atom.Mode
	if req.Mode != nil {
		m := atom.Mode(*req.Mode)
		modePtr = &m
	}
	if err := a.ApplyUpdate(atom.UpdateParams{
		Title: req.Title,
		Body:  req.Body,
		Tags:  req.Tags,
		Mode:  modePtr,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_UPDATE", err.Error())
		return
	}

	// Aggregate-level validation per ADR-156 Phase 1 invariants. Catches
	// Phase 1 PATCH errors that ApplyUpdate doesn't know about (invalid
	// cognitive_level, oversize subject, bad IMDA tag, >1 media asset, etc.).
	if err := a.ValidatePhase1(); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_UPDATE", err.Error())
		return
	}

	// ADR-244 D5 - a metadata edit on an already-PUBLISHED atom is the standing
	// trigger: other domains are projecting this atom right now, so the edit
	// and the event announcing it commit as ONE unit of work. A DRAFT has no
	// audience yet, and a patch that moved nothing has nothing to announce:
	// both keep the plain save. Before this the PATCH path emitted nothing at
	// all, so every published-atom edit reached the rest of the platform as
	// silence.
	changed := changedMetadataFields(req, before, a)
	if a.Status == atom.StatusPublished && len(changed) > 0 {
		ev := atom.NewAtomUpdatedEvent(a, changed, effectiveTraceparent(r), r.Header.Get("tracestate"))
		if err := h.atomWriter.SaveAndPublish(r.Context(), a, ev); err != nil {
			log.Printf("patchAtom: save+enqueue failed, nothing committed: %v", err)
			writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist atom update")
			return
		}
		writeJSON(w, http.StatusOK, a)
		return
	}

	if err := h.repo.Save(r.Context(), a); err != nil {
		log.Printf("save error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to save")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// -----------------------------------------------------------------------------
// Delete (soft)
// -----------------------------------------------------------------------------

func (h *AtomHandler) deleteAtom(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, id)
	if err != nil {
		writeNotFound(w, err)
		return
	}
	// CHO-2264 (SECURITY) — until now `gcid` was read here purely to STAMP the
	// archived event below; it was never compared to the atom's author, so any
	// caller sharing the tenant could archive any atom (and SoftDelete cascades
	// to the attached question). Attribution is not authorization. Refuse before
	// the mutation and before any emit.
	if refuseNonAuthor(w, a, gcid) {
		return
	}
	if err := a.SoftDelete(); err != nil {
		if errors.Is(err, atom.ErrOrphanFrozen) {
			// ADR-229 A1.2 (CHO-2132) — consumed-continuity is absolute:
			// an orphan edition cannot be archived out from under the
			// consumers repointed onto it.
			writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
				"orphan editions cannot be deleted (consumers rely on them); fork via clone to evolve")
			return
		}
		writeError(w, http.StatusInternalServerError, "CREATION_DELETE_FAILED", err.Error())
		return
	}
	if err := h.repo.Save(r.Context(), a); err != nil {
		log.Printf("save error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to save")
		return
	}
	// Cascade soft-delete to the attached Question per
	// .claude/rules/ddd-enforcement.md Aggregate Invariant #8 — "Cascade
	// soft-delete within same aggregate (parent → children)". LearningAtom
	// + Question are 1:1 by design (D2 invariant on the production
	// QuestionRepo); orphan question rows must not survive the parent
	// atom's deletion. Best-effort: if questionRepo is unwired in this
	// deployment (legacy path), or no question is attached, or the
	// question is already soft-deleted, the cascade is a no-op + we
	// still return 204 from the atom delete.
	if h.questionRepo != nil {
		q, _, qerr := h.questionRepo.GetByAtomID(r.Context(), tenantID, id)
		if qerr == nil && q != nil && q.DeletedAt == nil {
			if derr := h.questionRepo.SoftDelete(r.Context(), tenantID, q.QuestionID); derr != nil {
				// Fail loud per `feedback_no_stubs_real_wiring` — but
				// surface to the caller so they can retry. The atom
				// soft-delete already landed; cascade gap means the
				// question is orphaned, which violates
				// ddd-enforcement #8.
				log.Printf("delete-atom: cascade soft-delete question %s for atom %s: %v", q.QuestionID, id, derr)
				writeError(w, http.StatusInternalServerError, "CREATION_CASCADE_FAILED",
					"atom soft-deleted but cascade to attached question failed: "+derr.Error())
				return
			}
		}
	}
	// Emit atom.archived.v1 (ADR-217 Debt 3) so the tenancy atom-count projection
	// decrements on soft-delete. This is the ONLY atom soft-delete path in the
	// service. Outbox-durable + nil-safe; the atom is already soft-deleted (+ the
	// question cascade done), so a queue failure is logged, not fatal to the 204.
	if h.atomEventPublisher != nil {
		ev := atom.NewAtomArchivedEvent(a, gcid, effectiveTraceparent(r), r.Header.Get("tracestate"))
		if perr := h.atomEventPublisher.Publish(r.Context(), ev); perr != nil {
			log.Printf("deleteAtom: emit atom.archived failed (row not queued): %v", perr)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errEnvelope mirrors components.schemas.Error in creation-admin.yaml.
type errEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errEnvelope{Code: code, Message: msg})
}

func writeNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, atom.ErrNotFound) {
		writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
		return
	}
	log.Printf("get error: %v", err)
	writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
}

// _ keeps context import used for tooling parity even when no helper uses it.
var _ = context.TODO
