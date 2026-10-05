// backfill_topic_tags.go — the topic-tag backfill routes (CHO-2142, async per CHO-2159).
//
//	POST /api/internal/atoms/backfill-topic-tags           → 202 {run_id, poll_url}
//	GET  /api/internal/atoms/backfill-topic-tags/{run_id}  → the persisted report
//
// The durable fix for the 74 PUBLISHED atoms whose source tags are null/empty, so
// chora-consumption projects atom_index.topic_tags = {} and the topic-accuracy
// projector acks them unclassified.
//
// Direction of the fix: SOURCE-side. atom_index is a projection — creation owns
// the truth. This endpoint classifies each tagless atom via chora-model-gateway
// (the un-bypassable LLM chokepoint, ADR-163/177 — Model Armor + metering are
// central there), writes learning_atoms.tags, and re-emits atom.published.v1
// through the durable OUTBOX. That event now carries the tags on wire field 7
// (topic_node_ids), which chora-consumption projects into atom_index.topic_tags.
//
// ⚠ The re-emit only carries tags because CHO-2142 also fixed the typed-event
// path: atom.Event previously had NO tags field and payloadAsMap never wrote a
// "tags" key, so atom.published.v1 reached the wire tagless every time. Only the
// composer path (questions PATCH / batch accept) ever carried them. Without that
// fix a source-side tag write could never project.
//
// WHY 202 AND NOT 200 (CHO-2159). The run is 74 atoms x ~3.5s of gateway
// classification ≈ 280s, and chora-creation's http.Server carries
// WriteTimeout: 15s. The old synchronous handler ran to completion and then
// wrote its report to a socket the server had already closed — measured live
// 2026-07-14 as HTTP 000 / empty reply after 280s, with the pod log showing the
// run HAD completed (scanned=117 candidates=74 failed=2). On a REAL run that is
// dangerous: tags are written and events re-emitted for up to 74 atoms while the
// operator sees nothing and never learns which atoms failed. A mutating
// operation whose report cannot be delivered is unobservable by construction.
//
// So the run is now PERSISTED and executed DETACHED, and the operator polls the
// report. Both routes return in milliseconds, far inside the 15s WriteTimeout —
// which is deliberately left ALONE: it is a whole-server setting, and widening
// it to accommodate one internal endpoint would weaken every other route's
// slow-loris posture.
//
// OWNER GATE: ?dry_run=true classifies and records the PROPOSED tags per atom
// WITHOUT writing or emitting anything — the whole set can be reviewed before a
// single row is touched.
//
// Guard: cluster-INTERNAL (not exposed via the public gateway; bypasses the
// X-Tenant-Id header gate per middleware.isPublicPath), so tenant_id is REQUIRED
// on both routes and fails loud (400) when absent. It is also what scopes the
// RLS read of the run row.
package httpadapter

import (
	"context"
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// backfillRunBasePath is the collection route; the poll route is this + "/{run_id}".
const backfillRunBasePath = "/api/internal/atoms/backfill-topic-tags"

// backfillRunAck is the 202 body. It carries everything the operator needs to
// come back for the report — a 202 with no run_id would be as unobservable as
// the HTTP 000 it replaces.
type backfillRunAck struct {
	RunID    string `json:"run_id"`
	TenantID string `json:"tenant_id"`
	DryRun   bool   `json:"dry_run"`
	Status   string `json:"status"`
	PollURL  string `json:"poll_url"`
	Message  string `json:"message"`
}

// backfillTopicTags accepts a run, persists it, and executes it detached.
//
// 202 with {run_id, status:"running", poll_url}. A wiring/param error is a
// fail-loud 4xx/5xx BEFORE anything is persisted or detached — we never hand out
// a 202 for a run that is not going to happen, or that has no row to observe it.
func (h *AtomHandler) backfillTopicTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on "+backfillRunBasePath)
		return
	}

	tenantID := backfillTenantID(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
			"tenant_id is required (query param ?tenant_id= or JSON body {\"tenant_id\":..})")
		return
	}

	q := r.URL.Query()
	dryRun := q.Get("dry_run") == "true"

	// The salt busts the outbox UNIQUE(idempotency_key) dedup. The plain key
	// (atom:published:rev) is permanently deduped for an already-published
	// revision, so an UNSALTED re-emit would be swallowed and nothing would
	// re-project. Required for a real run; meaningless on a dry run.
	reemitSalt := strings.TrimSpace(q.Get("reemit_salt"))
	if q.Has("reemit_salt") && !reemitSaltPattern.MatchString(reemitSalt) {
		writeError(w, http.StatusBadRequest, "CREATION_REEMIT_SALT_INVALID",
			"reemit_salt must match [A-Za-z0-9._-]{1,64} (it is embedded into outbox idempotency keys)")
		return
	}
	if !dryRun && reemitSalt == "" {
		writeError(w, http.StatusBadRequest, "CREATION_REEMIT_SALT_REQUIRED",
			"reemit_salt is required for a real run: the plain idempotency key is permanently "+
				"deduped by the outbox for an already-published revision, so an unsalted re-emit "+
				"would be swallowed and topic_tags would never re-project")
		return
	}

	limit := 0
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "CREATION_LIMIT_INVALID",
				"limit must be a non-negative integer")
			return
		}
		limit = n
	}

	// no-stubs: an unwired classifier must fail loud. A run that "succeeds"
	// having classified nothing is worse than no run — it looks done.
	if h.topicClassifier == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_TOPIC_CLASSIFIER_NOT_WIRED",
			"topic classifier is not wired (set CHORA_MODEL_GATEWAY_GRPC_URL); "+
				"the backfill requires the model-gateway chokepoint")
		return
	}
	if !dryRun && h.atomEventPublisher == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_PUBLISHER_NOT_WIRED",
			"atom event publisher is not wired; source tags with no re-emit would never project")
		return
	}
	// Only a REAL run needs the question repo: it resolves the published revision
	// + answerability for the re-emit. A dry run classifies and reports; it never
	// emits, so it must not be blocked on a dep it will not use.
	if !dryRun && h.questionRepo == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_QUESTION_REPO_NOT_WIRED",
			"question repository is not wired; the re-emit cannot resolve the published revision "+
				"and would blank the MCQ answer key")
		return
	}
	// CHO-2159 — no run store, no run. The report of a ~280s mutating run cannot
	// be held in memory (it outlives a pod restart, and the poll can land on
	// another replica), so an unpersistable run is refused outright rather than
	// executed unobserved.
	if h.backfillRunner == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_BACKFILL_RUNS_NOT_WIRED",
			"the backfill run store is not wired; a run whose report cannot be persisted "+
				"would mutate atoms unobserved (that is the CHO-2159 defect) — refusing to start")
		return
	}

	params := atom.BackfillTopicTagsParams{
		TenantID:    tenantID,
		DryRun:      dryRun,
		Limit:       limit,
		ReemitSalt:  reemitSalt,
		TraceParent: effectiveTraceparent(r),
		TraceState:  r.Header.Get("tracestate"),
	}

	// Persist the `running` row BEFORE the 202. If this fails we must not detach:
	// the operator would be handed a run_id that never resolves while the
	// mutation ran unobserved.
	run, err := h.backfillRunner.Start(r.Context(), params)
	if err != nil {
		log.Printf("backfillTopicTags: could not start run (tenant=%s): %v", tenantID, err)
		writeError(w, http.StatusInternalServerError, "CREATION_BACKFILL_START_FAILED", err.Error())
		return
	}

	// Snapshot the ack BEFORE handing the run to the goroutine: Execute takes
	// ownership of `run` and mutates it (status, counts, report, finished_at), so
	// reading it after the `go` below would race the detached execution.
	// Everything sequenced before a `go` statement happens-before the goroutine,
	// so this snapshot is safe; the ack shares nothing with the live run.
	ack := backfillRunAck{
		RunID:    run.RunID,
		TenantID: run.TenantID,
		DryRun:   run.DryRun,
		Status:   string(run.Status),
		PollURL:  backfillRunBasePath + "/" + run.RunID + "?tenant_id=" + run.TenantID,
		Message: "run accepted and executing detached; poll poll_url for the report " +
			"(status, counts, proposed[], failed[])",
	}
	runID := run.RunID

	// net/http cancels r.Context() the moment this response completes, so the
	// slow phase MUST NOT ride it: WithoutCancel keeps the ctx values (trace
	// span, role claims) and drops the cancellation. The run is still bounded —
	// Execute applies the runner's RunTimeout — and the terminal write persists
	// on a context of its own, so the row always reaches a terminal state. A pod
	// death mid-run is backstopped by the stale-sweep on the GET.
	execCtx := context.WithoutCancel(r.Context())
	go func() {
		defer func() {
			// A panic in a detached goroutine would take the whole pod down and
			// with it every other in-flight request. Log it LOUD and leave the row
			// to the stale-sweep.
			if rec := recover(); rec != nil {
				log.Printf("backfillTopicTags: run %s PANIC in detached execution (left to the stale-sweep): %v\n%s",
					runID, rec, debug.Stack())
			}
		}()
		if execErr := h.backfillRunner.Execute(execCtx, run); execErr != nil {
			// The row already carries the reason; this is the operator-facing log.
			log.Printf("backfillTopicTags: run %s failed after the 202 ack: %v", runID, execErr)
			return
		}
		log.Printf("backfillTopicTags: run %s tenant=%s dry_run=%t scanned=%d candidates=%d tagged=%d emitted=%d failed=%d salt=%q",
			runID, run.TenantID, run.DryRun, run.Scanned, run.Candidates, run.Tagged, run.Emitted, len(run.Failed), run.ReemitSalt)
	}()

	writeJSON(w, http.StatusAccepted, ack)
}

// backfillTopicTagRun serves GET /api/internal/atoms/backfill-topic-tags/{run_id}
// — the persisted run report.
//
// It also runs an OPPORTUNISTIC STALE-SWEEP first (the CHO-2138 precedent):
// a row still `running` past the cutoff with no finished_at means the pod died
// mid-run. Without the sweep that row stays `running` forever and the operator
// polls a lie. Best-effort + RLS-scoped (no cross-tenant scan) — a sweep failure
// must not hide the report the operator actually came for.
func (h *AtomHandler) backfillTopicTagRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on "+backfillRunBasePath+"/{run_id}")
		return
	}

	runID := strings.Trim(strings.TrimPrefix(r.URL.Path, backfillRunBasePath), "/")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_RUN_ID_REQUIRED",
			"run_id is required: GET "+backfillRunBasePath+"/{run_id}?tenant_id=..")
		return
	}
	// A non-UUID run_id would reach Postgres and throw 22P02 — a user error
	// masquerading as a 500. Refuse it precisely.
	if _, err := uuid.Parse(runID); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_RUN_ID_INVALID",
			"run_id must be a UUID (as minted by the 202 from POST "+backfillRunBasePath+")")
		return
	}

	// The run row is RLS-scoped and this route bypasses the tenant-header gate,
	// so the tenant must come on the query string — exactly as the POST requires.
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
			"tenant_id is required (query param ?tenant_id=) — the run row is tenant-isolated")
		return
	}

	if h.backfillRunner == nil || h.backfillRuns == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_BACKFILL_RUNS_NOT_WIRED",
			"the backfill run store is not wired; no run report can be served")
		return
	}

	// Opportunistic self-heal (CHO-2138). Best-effort: a sweep error is logged,
	// never returned — it must not mask the report.
	if n, sErr := h.backfillRunner.SweepStaleNow(r.Context(), tenantID); sErr != nil {
		log.Printf("backfillTopicTags: stale-sweep (tenant=%s) non-fatal: %v", tenantID, sErr)
	} else if n > 0 {
		log.Printf("backfillTopicTags: stale-sweep reclaimed %d stranded run(s) (tenant=%s)", n, tenantID)
	}

	run, err := h.backfillRuns.Get(r.Context(), tenantID, runID)
	if err != nil {
		if errors.Is(err, atom.ErrBackfillRunNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_BACKFILL_RUN_NOT_FOUND",
				"no backfill run "+runID+" for this tenant")
			return
		}
		log.Printf("backfillTopicTags: read run %s (tenant=%s): %v", runID, tenantID, err)
		writeError(w, http.StatusInternalServerError, "CREATION_BACKFILL_RUN_READ_FAILED", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "CREATION_BACKFILL_RUN_NOT_FOUND",
			"no backfill run "+runID+" for this tenant")
		return
	}

	writeJSON(w, http.StatusOK, run)
}

// -----------------------------------------------------------------------------
// port adapters
// -----------------------------------------------------------------------------

// atomBackfillRepo adapts atom.Repository to atom.TopicTagBackfillRepo. The pg
// adapter wraps its SELECT in SET LOCAL chora.tenant_id, so the listing is
// RLS-scoped to the tenant — no new repo method needed.
type atomBackfillRepo struct{ repo atom.Repository }

func (r *atomBackfillRepo) ListPublished(ctx context.Context, tenantID string) ([]*atom.LearningAtom, error) {
	return r.repo.List(ctx, tenantID, atom.ListFilter{Status: atom.StatusPublished})
}

func (r *atomBackfillRepo) Save(ctx context.Context, a *atom.LearningAtom) error {
	return r.repo.Save(ctx, a)
}

// handlerRevisionResolver adapts the handler's existing revision resolution +
// answerability computation to atom.PublishedRevisionResolver. Keeping it here
// (not in the domain) is what lets the atom package stay free of the question
// aggregate — the same hexagonal boundary NewAtomPublishedEvent respects by
// taking answerability as scalars.
type handlerRevisionResolver struct{ h *AtomHandler }

func (rr *handlerRevisionResolver) ResolvePublishedRevision(ctx context.Context, tenantID, atomID string) (atom.PublishedRevision, bool) {
	revID, revNum, liveQuestion, liveRevision, ok := rr.h.resolvePublishedRevision(ctx, tenantID, atomID)
	if !ok {
		return atom.PublishedRevision{}, false
	}
	correctOptionID, answerCount, hasOE := computeAnswerability(liveQuestion, liveRevision)
	return atom.PublishedRevision{
		RevisionID:      revID,
		RevisionNumber:  revNum,
		CorrectOptionID: correctOptionID,
		AnswerCount:     answerCount,
		HasOpenEnded:    hasOE,
	}, true
}
