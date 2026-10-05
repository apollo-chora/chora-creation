// reuse_visibility_handler.go — PATCH /api/atoms/{atom_id}/reuse-visibility.
//
// ADR-229 WS-1 (CHO-2127): the author-only reuse-consent audience mutation
// per chora-contracts/openapi/creation-admin.yaml#changeAtomReuseVisibility.
// This is the FIRST author-level authorization check in chora-creation — the
// caller's mesh-claim GCID must equal the atom's author GCID; everyone else
// (including tenant admins) receives 403 CREATION_NOT_AUTHOR. It exists
// because attribution (spec-001 R1) makes authorship valuable.
//
// Semantics:
//   - 200 + persisted + ONE chora.creation.atom.reuse_visibility_changed.v1
//     outbox emit on a real change (new + previous audience on the wire).
//   - 200 + NO event on a same-value change (domain reports changed=false) —
//     the projection never sees a phantom change.
//   - 403 CREATION_NOT_AUTHOR for a non-author; 400 for an unknown audience;
//     404 for a missing atom; 409 CREATION_ATOM_ARCHIVED for a soft-deleted
//     atom (defence-in-depth — Get usually 404s those first).
//
// Per Amendment A1.1 narrowing NEVER revokes grants here — stranded-consumer
// continuity belongs to the orphan-edition machinery (successor story).
package httpadapter

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// patchAtomReuseVisibility handles PATCH /api/atoms/{atom_id}/reuse-visibility.
func (h *AtomHandler) patchAtomReuseVisibility(w http.ResponseWriter, r *http.Request, atomID string) {
	var req struct {
		ReuseVisibility string `json:"reuse_visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_BAD_REQUEST",
			"invalid JSON body: expected {\"reuse_visibility\": \"private|friends|tenant\"}")
		return
	}
	v, err := atom.ParseReuseVisibility(req.ReuseVisibility)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_BAD_REQUEST", err.Error())
		return
	}

	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		writeNotFound(w, err)
		return
	}

	previous := a.ReuseVisibility
	changed, err := a.ChangeReuseVisibility(gcidFromContext(r.Context()), v)
	if err != nil {
		switch {
		case errors.Is(err, atom.ErrOrphanFrozen):
			// ADR-229 A1.2 — orphan editions are private FOREVER; not even
			// the author may re-widen one.
			writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
				"orphan editions are frozen (reuse_visibility=private forever); fork via clone to evolve")
		case errors.Is(err, atom.ErrNotAuthor):
			writeError(w, http.StatusForbidden, "CREATION_NOT_AUTHOR",
				"only the atom author may change reuse visibility")
		default:
			// The only other domain refusal is the soft-deleted guard
			// (defence-in-depth; repo.Get filters soft-deleted by default).
			writeError(w, http.StatusConflict, "CREATION_ATOM_ARCHIVED",
				"cannot change reuse visibility of archived atom")
		}
		return
	}

	if changed {
		// The audience change and the event announcing it are ONE transaction:
		// a narrowing that never reaches the consumers is worse than a refused
		// request, so a failed enqueue rolls the change back and 500s.
		ev := atom.NewAtomReuseVisibilityChangedEvent(a, previous,
			effectiveTraceparent(r), r.Header.Get("tracestate"))
		if err := h.atomWriter.SaveAndPublish(r.Context(), a, ev); err != nil {
			log.Printf("patchAtomReuseVisibility: save+enqueue failed, nothing committed: %v", err)
			writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
				"failed to persist reuse visibility change")
			return
		}
	}

	writeJSON(w, http.StatusOK, a)
}
