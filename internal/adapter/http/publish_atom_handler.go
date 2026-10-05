// publish_atom_handler.go — POST /api/atoms/{atom_id}/publish handler.
//
// Implements chora-contracts/openapi/creation-admin.yaml#publishAtom per FE
// A22 ack (Phase K — atom Publish + Preview flow). Transitions an atom
// DRAFT -> PUBLISHED iff at least one published QuestionRevision exists.
//
// A22 Option α applies: question revisions are auto-published at PATCH/POST
// time (the QuestionsHandler appends an immutable revision row with no
// DRAFT/PUBLISHED state on the revision itself). The publish handler only
// flips the atom-level status; no revision-publish step is needed.
//
// Discriminated 409 sub-codes per ADR-141 D4 (safety_and_robustness +
// transparency):
//
//	CREATION_ATOM_NO_PUBLISHED_REVISION — no Question/Revision exists for the atom
//	CREATION_ATOM_ARCHIVED              — atom is soft-deleted
//
// Idempotent re-publish on an already-PUBLISHED atom returns 200 with the
// same envelope (not 409) per A22.Q3 — single-tap forgiving CTA.
//
// Response envelope on success / idempotent re-publish (200): bare
// LearningAtom with status=published + current_revision_id +
// current_revision_number populated from the latest QuestionRevision so the
// FE "Published! (rev N is live)" toast renders verbatim per A22.Q4.
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// publishAtom is the handler for POST /api/atoms/{atom_id}/publish.
//
// Flow:
//  1. Load the atom (404 if not found or soft-deleted).
//  2. If already PUBLISHED → idempotent 200 with the current envelope.
//  3. Verify at least one QuestionRevision exists (409 NO_PUBLISHED_REVISION
//     when the QuestionRepository is wired AND no question exists).
//  4. Flip status DRAFT -> PUBLISHED via domain.Publish() (409 ARCHIVED if
//     the atom is soft-deleted — defence-in-depth in case an admin-view repo
//     returns soft-deleted rows).
//  5. Persist + return the bare LearningAtom envelope with revision summary.
func (h *AtomHandler) publishAtom(w http.ResponseWriter, r *http.Request, atomID string) {
	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		writeNotFound(w, err)
		return
	}
	// CHO-2264 (SECURITY) — author-only. Publishing is irreversible in effect
	// (it fans the atom out to consumption/delivery via atom.published), so a
	// non-author force-publishing someone else's draft is worse than an edit.
	// Checked BEFORE the orphan guard and the idempotent-republish shortcut.
	if refuseNonAuthor(w, a, gcidFromContext(r.Context())) {
		return
	}

	// ADR-229 A1.2 (CHO-2132) — orphan editions are FROZEN. Guarded BEFORE
	// the idempotent-republish shortcut below: an orphan is already
	// status=published, so without this check a publish attempt would 200
	// silently instead of failing loud.
	if a.IsOrphan() {
		writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
			"orphan editions are frozen; publish is not an authoring surface for them")
		return
	}

	// Resolve the latest QuestionRevision (used by both the verification gate
	// and the response envelope's revision summary).
	var (
		latestRevisionID     string
		latestRevisionNumber int
		liveQuestion         *question.Question
		liveRevision         *question.QuestionRevision
	)
	if h.questionRepo != nil {
		q, rev, qerr := h.questionRepo.GetByAtomID(r.Context(), tenantID, atomID)
		switch {
		case qerr == nil && q != nil && rev != nil:
			liveQuestion, liveRevision = q, rev
			latestRevisionID = rev.RevisionID
			latestRevisionNumber = rev.RevisionNumber
		case errors.Is(qerr, question.ErrNotFound):
			// Only block on missing revision when we're transitioning DRAFT ->
			// PUBLISHED. Idempotent re-publish on an already-PUBLISHED atom
			// should still succeed (the revision is presumed to have been
			// verified at the original publish).
			if a.Status != atom.StatusPublished {
				writeError(w, http.StatusConflict,
					"CREATION_ATOM_NO_PUBLISHED_REVISION",
					"cannot publish atom without at least one published revision")
				return
			}
		default:
			// Transient repo error — surface 500 so the FE retries.
			log.Printf("publishAtom: question lookup error: %v", qerr)
			writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
				"failed to verify atom revisions")
			return
		}
	}

	// OT#4 — durably re-home W8 transient images BEFORE the publish
	// transition. A published atom can outlive the 7-day transient bucket
	// TTL, so its generated images are copied into chora-atom-media and the
	// payload ref rewritten to a durable gs:// (append-only new revision).
	// Idempotent (already-durable refs are no-ops) so it runs for both the
	// transition and idempotent-republish paths — a failed prior re-home is
	// repaired by re-tapping Publish. Fail-loud: a copy error aborts publish
	// rather than shipping an image that will 404 in 7 days.
	if h.reHomer != nil && liveQuestion != nil && liveRevision != nil {
		newRev, changed, rerr := h.reHomer.ReHomeRevision(r.Context(), tenantID, atomID, liveQuestion, liveRevision)
		if rerr != nil {
			log.Printf("publishAtom: media re-home failed (atom=%s): %v", atomID, rerr)
			writeError(w, http.StatusInternalServerError, "CREATION_ATOM_MEDIA_REHOME_FAILED",
				"failed to durably re-home generated images")
			return
		}
		if changed {
			latestRevisionID = newRev.RevisionID
			latestRevisionNumber = newRev.RevisionNumber
		}
	}

	// Idempotent re-publish — no domain mutation, no persist, return current
	// envelope so the FE toast still renders.
	if a.Status == atom.StatusPublished {
		writeJSON(w, http.StatusOK, publishAtomResponse(a, latestRevisionID, latestRevisionNumber))
		return
	}

	if err := a.Publish(); err != nil {
		if errors.Is(err, atom.ErrAtomArchived) {
			writeError(w, http.StatusConflict, "CREATION_ATOM_ARCHIVED",
				"cannot publish archived (soft-deleted) atom")
			return
		}
		writeError(w, http.StatusInternalServerError, "CREATION_PUBLISH_FAILED", err.Error())
		return
	}
	// Real DRAFT -> PUBLISHED transition (NOT the idempotent re-publish
	// short-circuit above): the durable chora.creation.atom.published.v1
	// outbox event carries answerability so chora-consumption can treat the
	// atom as playable. The atom.created.v1 event fires at draft-creation
	// BEFORE a question exists, so it can never carry the MCQ answer key;
	// atom.published.v1 is the authoritative source:
	//   - MCQ: correct_option_id (the is_correct option) + answer_count (#opts)
	//   - OE:  has_open_ended_question=true (answerable without an MCQ key)
	// Computed from the already-loaded liveQuestion/liveRevision, no extra
	// repo lookup.
	//
	// The event is built BEFORE the write so the status flip and the event
	// announcing it commit together: an atom that says PUBLISHED in the
	// database while no consumer was ever told is exactly the 2.68% gap
	// chaos20260807a measured on the create path.
	correctOptionID, answerCount, hasOE := computeAnswerability(liveQuestion, liveRevision)
	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")

	// Best-effort display name resolution (empty is legal on the wire).
	// Resolved before the transaction opens so a slow identity call never
	// holds one open.
	var authorDisplayName string
	if h.displayNameResolver != nil {
		name, err := h.displayNameResolver.ResolveDisplayName(r.Context(), a.Gcid)
		if err != nil {
			log.Printf("publishAtom: display name resolve failed (atom=%s gcid=%s): %v", atomID, a.Gcid, err)
		} else {
			authorDisplayName = name
		}
	}

	ev := atom.NewAtomPublishedEvent(a, latestRevisionID, latestRevisionNumber,
		correctOptionID, answerCount, hasOE, authorDisplayName, traceparent, tracestate)
	if err := h.atomWriter.SaveAndPublish(r.Context(), a, ev); err != nil {
		log.Printf("publishAtom: save+enqueue failed, nothing committed: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to persist publish transition")
		return
	}

	// Epic-1b W4 — embed the freshly-published atom into atom_embeddings so
	// SearchEmbeddings can resolve it as a Growth-Edge drill atom. Async +
	// soft-fail: publishing NEVER blocks on Vertex; a miss is caught by the
	// env-gated boot backfill.
	h.indexPublishedAtomAsync(r.Context(), tenantID, a)

	writeJSON(w, http.StatusOK, publishAtomResponse(a, latestRevisionID, latestRevisionNumber))
}

// computeAnswerability derives the chora.creation.atom.published.v1
// answerability fields from an atom's live question + revision:
//   - MCQ: correct_option_id (the is_correct option) + answer_count (#options),
//     read off the live revision's MCQPayload.
//   - OE:  hasOE=true (answerable without an MCQ key).
//
// A nil question/revision (or any other question type) yields the zero values
// (correctOptionID="", answerCount=0, hasOE=false). Single source of truth for
// the answerability carried on atom.published.v1 — shared by publishAtom (the
// DRAFT -> PUBLISHED transition) and backfillPublished (the one-off re-emit) so
// the two paths can never drift.
func computeAnswerability(liveQuestion *question.Question, liveRevision *question.QuestionRevision) (correctOptionID string, answerCount int, hasOE bool) {
	if liveQuestion == nil || liveRevision == nil {
		return "", 0, false
	}
	switch liveQuestion.Type {
	case question.TypeMCQ:
		if liveRevision.MCQPayload != nil {
			// CHO-2272 — the grading key is the DERIVED served id (what the
			// learner is graded on), not the stored id. Fail loud: an underivable
			// payload ships NO key (atom_index.IsMCQ() then false -> non-gradable)
			// rather than a wrong one that mis-grades every take.
			if cid, cerr := liveRevision.MCQPayload.ServedCorrectOptionID(); cerr != nil {
				log.Printf("computeAnswerability: cannot derive served correct id for atom=%s (non-gradable): %v", liveQuestion.AtomID, cerr)
			} else {
				correctOptionID = cid
			}
			answerCount = liveRevision.MCQPayload.AnswerCount()
		}
	case question.TypeOpenEnded:
		hasOE = true
	}
	return correctOptionID, answerCount, hasOE
}

// indexPublishedAtomAsync fires the W4 embed-index step on its own goroutine
// with a detached, bounded context (the HTTP request ends immediately). The
// embedding is attributed to the atom's author (a.Gcid), the topic-classifier
// precedent; ctx values survive WithoutCancel so the traceparent rides too.
func (h *AtomHandler) indexPublishedAtomAsync(ctx context.Context, tenantID string, a *atom.LearningAtom) {
	if h.embedIndexer == nil || a == nil {
		return
	}
	indexCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	go func() {
		defer cancel()
		if err := h.embedIndexer.IndexAtom(indexCtx, tenantID, a.Gcid, a.AtomID, a.Title, a.Stem, a.Body); err != nil {
			log.Printf("publishAtom: embed-index atom %s failed (non-fatal; backfill catches up): %v", a.AtomID, err)
		}
	}()
}

// publishAtomResponse encodes the bare LearningAtom envelope plus the revision
// summary fields per A22.Q4. The atom struct already JSON-encodes status; the
// envelope merges `current_revision_id` + `current_revision_number` onto it.
//
// Implementation note: we encode the atom to JSON, decode back into a map,
// and merge the revision fields — same pattern as projectAtomWithQuestion()
// in handler.go to avoid drift between the baseline LearningAtom shape and
// the publish envelope.
func publishAtomResponse(a *atom.LearningAtom, revisionID string, revisionNumber int) map[string]any {
	envelope := map[string]any{}
	b, err := json.Marshal(a)
	if err != nil {
		// Pathological — fall back to a minimal envelope.
		envelope["atom_id"] = a.AtomID
		envelope["status"] = a.Status
	} else if uerr := json.Unmarshal(b, &envelope); uerr != nil {
		envelope["atom_id"] = a.AtomID
		envelope["status"] = a.Status
	}
	envelope["current_revision_id"] = revisionID
	envelope["current_revision_number"] = revisionNumber
	return envelope
}
