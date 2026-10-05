// atom_clone_handler.go — POST /api/atoms/{atom_id}/clone (ADR-199
// clone-as-variant, Wave 2). Reads the source atom + its live question and
// persists a NEW draft atom (+ cloned question) owned by the caller, stamping
// cloned_from_atom_id provenance. The source aggregate is never mutated.
package httpadapter

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

type cloneAtomRequest struct {
	Title string `json:"title,omitempty"`
}

func (h *AtomHandler) cloneAtom(w http.ResponseWriter, r *http.Request, atomID string) {
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_TENANT", "tenant_id + gcid are required")
		return
	}

	// Body is optional ({} or absent) — only the title override is read.
	var req cloneAtomRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}

	// 1. Load the SOURCE atom. Get is tenant-scoped, so a cross-tenant atom is
	// indistinguishable from a missing one → 404.
	src, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "source atom not found")
			return
		}
		log.Printf("cloneAtom: source lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load source atom")
		return
	}

	// 2. Load the source's live question UP-FRONT (if any). The clone needs it
	// twice: to derive a canonical Stem when the atom-level Stem is empty (OE
	// atoms carry their prompt on the question, not atom.Stem — without this the
	// clone fails ValidatePhase1 "stem is required") and to copy it onto the
	// variant. A source with no question (e.g. a seed atom) clones the atom alone.
	var srcQ *question.Question
	if h.questionRepo != nil {
		q, _, qErr := h.questionRepo.GetByAtomID(r.Context(), tenantID, atomID)
		switch {
		case qErr == nil:
			srcQ = q
		case errors.Is(qErr, question.ErrNotFound):
			// no question on the source — atom-only clone
		default:
			log.Printf("cloneAtom: source question lookup: %v", qErr)
			writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load source question")
			return
		}
	}

	// 3. Build the cloned atom (new draft owned by the caller).
	clone, err := atom.Clone(atom.CloneParams{
		Source: src, TenantID: tenantID, Gcid: gcid, TitleOverride: req.Title,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ATOM", err.Error())
		return
	}
	// Stem is the canonical question prompt; an atom may legitimately carry an
	// empty Stem with its prompt on the question (OE). Derive it from the source
	// question so the clone satisfies ValidatePhase1 — they denote the same text.
	if strings.TrimSpace(clone.Stem) == "" && srcQ != nil && strings.TrimSpace(srcQ.Prompt) != "" {
		clone.Stem = srcQ.Prompt
	}
	if err := clone.ValidatePhase1(); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ATOM", err.Error())
		return
	}
	if err := h.repo.Save(r.Context(), clone); err != nil {
		log.Printf("cloneAtom: save atom: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist cloned atom")
		return
	}

	// 4. Clone the source's question onto the new atom (reusing the up-front load).
	if srcQ != nil {
		if err := h.cloneQuestionOnto(r, clone.AtomID, tenantID, gcid, srcQ); err != nil {
			log.Printf("cloneAtom: clone question: %v", err)
			writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
				"cloned atom saved but question clone failed — retry")
			return
		}
	}

	writeJSON(w, http.StatusCreated, clone)
}

// cloneQuestionOnto re-creates srcQ (Type + Prompt + payload) as a NEW question
// on newAtomID authored by the caller, persisting it + its first revision.
func (h *AtomHandler) cloneQuestionOnto(r *http.Request, newAtomID, tenantID, gcid string, srcQ *question.Question) error {
	newQ, err := question.New(question.NewParams{
		TenantID:   tenantID,
		AtomID:     newAtomID,
		AuthorGcid: gcid,
		Type:       srcQ.Type,
		Prompt:     srcQ.Prompt,
		SourceType: atom.SourceManual,
		MCQ:        srcQ.MCQ,
		OE:         srcQ.OE,
	})
	if err != nil {
		return err
	}
	rev, err := question.NewRevision(newQ, newQ.Prompt, newQ.MCQ, newQ.OE, gcid, atom.SourceManual)
	if err != nil {
		return err
	}
	newQ.LatestRevisionID = rev.RevisionID
	return h.questionRepo.Save(r.Context(), newQ, rev)
}
