// backfill_published.go — POST /api/internal/atoms/backfill-published.
//
// One-off, cluster-INTERNAL re-emit endpoint. The publish handler (②.a) emits
// chora.creation.atom.published.v1 (carrying answerability) on the DRAFT ->
// PUBLISHED transition so chora-consumption can flip the atom playable. A
// migration in chora-consumption defaulted every PRE-EXISTING atom_index row to
// status='draft', so the daily dose is empty until those atoms are flipped — but
// only NEW publishes emit atom.published.v1. This endpoint re-emits
// atom.published.v1 for EVERY currently-published, non-deleted atom of a tenant,
// reusing the EXACT emit + answerability logic the publish handler uses (NOT a
// new event shape), so consumption's AtomPublishedSubscriber flips them playable.
//
// Idempotent: NewAtomPublishedEvent stamps a DETERMINISTIC idempotency_key
// (atom_id + ":published:" + revision_id), so the outbox UNIQUE index dedupes a
// re-run — running the backfill repeatedly is safe.
//
// Guard: this path is NOT exposed via the public gateway. It bypasses the
// X-Tenant-Id / gcid header gate (see middleware.isPublicPath) and instead reads
// a REQUIRED tenant_id from the request (query param first, then JSON body) —
// fail-loud 400 when absent. The atom event publisher is REQUIRED (500 when
// unwired — the backfill is meaningless without it).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// reemitSaltPattern bounds the operator-supplied ?reemit_salt= token: it is
// appended verbatim into outbox idempotency keys, so keep it a short, plain
// token (no whitespace/colons — ":" is the key's own separator).
var reemitSaltPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// backfillPublished re-emits chora.creation.atom.published.v1 for every
// currently-published, non-deleted atom of a tenant. Responds 200 with
// {"tenant_id":..,"emitted":N,"skipped":M}. Per-atom failures (no resolvable
// published revision, or a publish error) are logged + skipped — the run does
// NOT abort. A list/DB error aborts the run with a fail-loud 500.
func (h *AtomHandler) backfillPublished(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/internal/atoms/backfill-published")
		return
	}

	tenantID := backfillTenantID(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
			"tenant_id is required (query param ?tenant_id= or JSON body {\"tenant_id\":..})")
		return
	}
	// CHO-2128 F1 — optional ?reemit_salt= flips the run into re-emit mode:
	// the salt is appended into each event's deterministic idempotency key so
	// the outbox UNIQUE index admits events it already dedupes (projection
	// seeding for atoms whose original publish pre-dates a subscriber). A
	// PRESENT-but-invalid salt is an operator error — fail-loud 400.
	reemitSalt := strings.TrimSpace(r.URL.Query().Get("reemit_salt"))
	if r.URL.Query().Has("reemit_salt") && !reemitSaltPattern.MatchString(reemitSalt) {
		writeError(w, http.StatusBadRequest, "CREATION_REEMIT_SALT_INVALID",
			"reemit_salt must match [A-Za-z0-9._-]{1,64} (it is embedded into outbox idempotency keys)")
		return
	}
	// The backfill's whole purpose is to emit; an unwired publisher is a
	// fail-loud 500, never a silent no-op (feedback_no_stubs_real_wiring).
	if h.atomEventPublisher == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_PUBLISHER_NOT_WIRED",
			"atom event publisher is not wired; backfill requires the outbox publisher")
		return
	}

	ctx := r.Context()
	// Reuse the existing list-by-status query — returns full, non-deleted
	// PUBLISHED atoms for the tenant under its RLS context (the pg adapter wraps
	// the SELECT in SET LOCAL chora.tenant_id). No new repo method needed.
	atoms, err := h.repo.List(ctx, tenantID, atom.ListFilter{Status: atom.StatusPublished})
	if err != nil {
		log.Printf("backfillPublished: list published atoms (tenant=%s): %v", tenantID, err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to list published atoms")
		return
	}

	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")

	emitted, skipped := 0, 0
	for _, a := range atoms {
		// ADR-229 A1 (CHO-2132) — orphan editions NEVER emit
		// atom.published.v1: a projection row in chora-sharing would make
		// them re-sharable, violating "not discoverable, reachable only via
		// repointed grants". They are status=published so List returns them
		// — skip explicitly.
		if a.IsOrphan() {
			skipped++
			continue
		}
		// A missing QUESTION revision is NOT a reason to withhold the event.
		//
		// atom.published.v1 carries two separable things: the atom's CONSENT
		// FACTS (owner, reuse_visibility, published) — which every published
		// atom has — and its QUESTION enrichment (revision id, answerability).
		// Skipping the whole emit when only the latter is missing left 19 of
		// 118 published atoms with NO row in chora-sharing's atom_projections,
		// and AuthorizeAtomUse 412s an atom it cannot see. Those atoms were
		// therefore un-reusable by anyone but their author — including
		// un-convertible into a study list, which ADR-233 made the primary way
		// atoms reach the daily dose.
		//
		// They are not defective: they carry question_type (mcq/essay/outline)
		// on the learning_atoms row and are revisioned in atom_revisions; they
		// simply have no row in question_revisions. chora-sharing migration 0036
		// makes revision_id/stem/question_type NULLABLE precisely so the consent
		// facts can be projected without them.
		//
		// So: emit regardless, with an empty revision when there is no question
		// revision to pin. The ORPHAN skip above stands — orphan editions must
		// never gain a projection row (ADR-229 A1).
		revID, revNum, liveQuestion, liveRevision, _ := h.resolvePublishedRevision(ctx, tenantID, a.AtomID)
		correctOptionID, answerCount, hasOE := computeAnswerability(liveQuestion, liveRevision)

		var authorDisplayName string
		if h.displayNameResolver != nil {
			name, err := h.displayNameResolver.ResolveDisplayName(ctx, a.Gcid)
			if err == nil {
				authorDisplayName = name
			}
		}

		var ev atom.Event
		if reemitSalt != "" {
			ev = atom.NewAtomPublishedReemitEvent(a, revID, revNum, correctOptionID, answerCount, hasOE, authorDisplayName, traceparent, tracestate, reemitSalt)
		} else {
			ev = atom.NewAtomPublishedEvent(a, revID, revNum, correctOptionID, answerCount, hasOE, authorDisplayName, traceparent, tracestate)
		}
		if perr := h.atomEventPublisher.Publish(ctx, ev); perr != nil {
			log.Printf("backfillPublished: emit atom.published failed (atom=%s, not queued): %v", a.AtomID, perr)
			skipped++
			continue
		}
		emitted++
	}

	log.Printf("backfillPublished: tenant=%s published=%d emitted=%d skipped=%d reemit_salt=%q",
		tenantID, len(atoms), emitted, skipped, reemitSalt)
	resp := map[string]any{
		"tenant_id": tenantID,
		"emitted":   emitted,
		"skipped":   skipped,
	}
	if reemitSalt != "" {
		resp["reemit_salt"] = reemitSalt
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolvePublishedRevision resolves an atom's live question + latest revision
// for the backfill emit. Mirrors the publishAtom revision-resolution block
// (load via QuestionRepository.GetByAtomID) but returns ok=false — instead of
// writing an HTTP error — when the atom has no resolvable published revision or
// the question repo is unwired, so the backfill skips the atom rather than
// failing the whole run. A transient (non ErrNotFound) repo error is logged.
func (h *AtomHandler) resolvePublishedRevision(ctx context.Context, tenantID, atomID string) (revID string, revNum int, q *question.Question, rev *question.QuestionRevision, ok bool) {
	if h.questionRepo == nil {
		return "", 0, nil, nil, false
	}
	gotQ, gotRev, err := h.questionRepo.GetByAtomID(ctx, tenantID, atomID)
	if err != nil {
		if !errors.Is(err, question.ErrNotFound) {
			log.Printf("backfillPublished: question lookup error (atom=%s): %v", atomID, err)
		}
		return "", 0, nil, nil, false
	}
	if gotQ == nil || gotRev == nil {
		return "", 0, nil, nil, false
	}
	return gotRev.RevisionID, gotRev.RevisionNumber, gotQ, gotRev, true
}

// backfillTenantID reads the REQUIRED tenant_id from the request — query param
// ?tenant_id= first, then a JSON body {"tenant_id":".."}. Returns "" when
// neither supplies a non-blank value (the handler maps that to a 400).
func backfillTenantID(r *http.Request) string {
	if t := strings.TrimSpace(r.URL.Query().Get("tenant_id")); t != "" {
		return t
	}
	if r.Body == nil {
		return ""
	}
	var body struct {
		TenantID string `json:"tenant_id"`
	}
	// Best-effort decode — a missing/empty/invalid body just yields "".
	_ = json.NewDecoder(r.Body).Decode(&body)
	return strings.TrimSpace(body.TenantID)
}
