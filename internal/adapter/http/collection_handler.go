// collection_handler.go — Personal Collection CRUD endpoints (WS-6a, 2026-05-26).
//
// Routes (sole-owner of this file per WS-6a workstream scope):
//
//	POST   /api/v1/collections                       — create collection
//	GET    /api/v1/me/collections                    — list caller's collections
//	GET    /api/v1/collections/{id}                  — get detail (visibility-gated)
//	PATCH  /api/v1/collections/{id}                  — update title/desc/visibility
//	DELETE /api/v1/collections/{id}                  — soft delete
//	POST   /api/v1/collections/{id}/atoms            — add atom (consent-gated)
//	DELETE /api/v1/collections/{id}/atoms/{atomId}   — remove atom
//	POST   /api/v1/collections/{id}/convert-to-study-list — ADR-233 / US5
//
// All endpoints require X-Tenant-Id + gcid headers (enforced by the shared
// tenantContext middleware in middleware.go). The handler delegates to
// collection.Service, which composes Repository + EventPublisher + the ADR-229
// reuse-consent gate. (There is no AtomLookup: ADR-233 D7 deleted it — atom
// existence now rides the gate's tenant-scoped read. See collection/service.go.)
//
// Error envelope shapes match the existing CREATION_* convention (see
// handler.go writeError). Per chora-contracts/openapi/creation-admin.yaml
// §components/schemas/Error.
package httpadapter

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

// CollectionHandler implements the /api/v1/collections* routes. Wired by
// NewRouterWithDeps when CollectionRepository is non-nil. CollectionPublisher is
// optional — without it create/update/etc still succeed but no Pub/Sub events
// flow (matches the `feedback_no_stubs_real_wiring` pattern of failing only when
// a required port is missing; publisher gap is logged at boot).
type CollectionHandler struct {
	service *collection.Service
}

// NewCollectionHandler wires the handler. service must be non-nil; the
// caller (NewRouterWithDeps) gates on the repository before constructing the
// service.
func NewCollectionHandler(service *collection.Service) *CollectionHandler {
	return &CollectionHandler{service: service}
}

// mountCollectionRoutes registers the collection routes on the supplied
// mux. Called from handler.go NewRouterWithDeps when the deps are wired.
func mountCollectionRoutes(mux *http.ServeMux, h *CollectionHandler) {
	mux.HandleFunc("/api/v1/collections", h.collectionsCollection)
	mux.HandleFunc("/api/v1/collections/", h.collectionsItem)
	mux.HandleFunc("/api/v1/me/collections", h.listMyCollections)
}

// -----------------------------------------------------------------------------
// /api/v1/collections (collection root)
// -----------------------------------------------------------------------------

func (h *CollectionHandler) collectionsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/v1/collections")
		return
	}
	h.createCollection(w, r)
}

// -----------------------------------------------------------------------------
// /api/v1/collections/{id}[/atoms[/{atom_id}]]
// -----------------------------------------------------------------------------

func (h *CollectionHandler) collectionsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/collections/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "missing collection id")
		return
	}
	// Parse {id}[/atoms[/{atom_id}]].
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "missing collection id")
		return
	}
	// CHO-2175 — every caller-supplied id is parsed HERE, before any query is
	// built from it. Postgres is not our input validator: an unparseable id used
	// to reach pg, die there on `invalid input syntax for type uuid (SQLSTATE
	// 22P02)`, and come back as a repository failure — so we blamed OURSELVES for
	// the caller's typo. (Before the default arm was inverted it came back as a
	// 400 carrying the raw SQLSTATE, which is how a 22P02 became an API contract
	// and how CHO-2173 stayed hidden.) Letting the database's type parser reject
	// our inputs is the disease; this is the cure.
	//
	// 400, not 404: a 404 claims we looked. We did not — there was nothing to
	// look for.
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "CREATION_COLLECTION_INVALID",
			"collection id must be a UUID")
		return
	}
	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			h.getCollection(w, r, id)
		case http.MethodPatch:
			h.patchCollection(w, r, id)
		case http.MethodDelete:
			h.deleteCollection(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only GET, PATCH, DELETE on /api/v1/collections/{id}")
		}
	case len(parts) == 2 && parts[1] == "atoms":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST on /api/v1/collections/{id}/atoms")
			return
		}
		h.addAtom(w, r, id)
	case len(parts) == 2 && parts[1] == "convert-to-study-list":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST on /api/v1/collections/{id}/convert-to-study-list")
			return
		}
		h.convertToStudyList(w, r, id)
	case len(parts) == 3 && parts[1] == "atoms":
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only DELETE on /api/v1/collections/{id}/atoms/{atom_id}")
			return
		}
		if !isUUID(parts[2]) {
			writeError(w, http.StatusBadRequest, "CREATION_COLLECTION_INVALID",
				"atom_id must be a UUID")
			return
		}
		h.removeAtom(w, r, id, parts[2])
	default:
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
	}
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

type createCollectionRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

func (h *CollectionHandler) createCollection(w http.ResponseWriter, r *http.Request) {
	var req createCollectionRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")

	visibility := audience.Audience(req.Visibility)
	c, err := h.service.Create(r.Context(), collection.CreateInput{
		TenantID:    tenantID,
		OwnerGcid:   gcid,
		Title:       req.Title,
		Description: req.Description,
		Visibility:  visibility,
		TraceParent: traceparent,
		TraceState:  tracestate,
	})
	if err != nil {
		writeCollectionError(w, err, "create")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

// getCollection reads one collection.
//
// 🔴 ADR-233 D8: the caller's GCID is now passed to the service, which enforces
// owner ∪ tenant ∪ friends via Collection.VisibleTo. Before this, the GCID was
// never passed AT ALL — so nothing downstream could enforce visibility even in
// principle, and any authenticated tenant member could read any collection in
// the tenant by ID, including another learner's PRIVATE one.
//
// A non-entitled read surfaces as 404 (ErrNotFound), never 403.
func (h *CollectionHandler) getCollection(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	c, err := h.service.GetVisible(r.Context(), tenantID, gcid, id)
	if err != nil {
		writeCollectionError(w, err, "get")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// -----------------------------------------------------------------------------
// List (caller's collections)
// -----------------------------------------------------------------------------

type listCollectionsResponse struct {
	Items []*collection.Collection `json:"items"`
	Total int                      `json:"total"`
}

func (h *CollectionHandler) listMyCollections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET on /api/v1/me/collections")
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	items, err := h.service.List(r.Context(), tenantID, collection.ListFilter{
		OwnerGcid: gcid,
	})
	if err != nil {
		log.Printf("listMyCollections: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to list collections")
		return
	}
	writeJSON(w, http.StatusOK, listCollectionsResponse{Items: items, Total: len(items)})
}

// -----------------------------------------------------------------------------
// Patch
// -----------------------------------------------------------------------------

type patchCollectionRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Visibility  *string `json:"visibility,omitempty"`
}

func (h *CollectionHandler) patchCollection(w http.ResponseWriter, r *http.Request, id string) {
	var req patchCollectionRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	params := collection.UpdateParams{
		Title:       req.Title,
		Description: req.Description,
	}
	if req.Visibility != nil {
		v := audience.Audience(*req.Visibility)
		params.Visibility = &v
	}
	c, err := h.service.Update(r.Context(), collection.UpdateInput{
		TenantID:     tenantID,
		CollectionID: id,
		OwnerGcid:    gcid,
		Params:       params,
		TraceParent:  effectiveTraceparent(r),
		TraceState:   r.Header.Get("tracestate"),
	})
	if err != nil {
		writeCollectionError(w, err, "patch")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// -----------------------------------------------------------------------------
// Delete
// -----------------------------------------------------------------------------

func (h *CollectionHandler) deleteCollection(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	err := h.service.Delete(r.Context(), collection.DeleteInput{
		TenantID:      tenantID,
		CollectionID:  id,
		OwnerGcid:     gcid,
		DeletedByGcid: gcid,
		TraceParent:   effectiveTraceparent(r),
		TraceState:    r.Header.Get("tracestate"),
	})
	if err != nil {
		writeCollectionError(w, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// AddAtom
// -----------------------------------------------------------------------------

type addAtomRequest struct {
	AtomID string `json:"atom_id"`
}

func (h *CollectionHandler) addAtom(w http.ResponseWriter, r *http.Request, id string) {
	var req addAtomRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	if strings.TrimSpace(req.AtomID) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "atom_id is required")
		return
	}
	// Parsed HERE, not by Postgres (CHO-2175) — see collectionsItem.
	if !isUUID(strings.TrimSpace(req.AtomID)) {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "atom_id must be a UUID")
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	c, err := h.service.AddAtom(r.Context(), collection.AddAtomInput{
		TenantID:     tenantID,
		CollectionID: id,
		OwnerGcid:    gcid,
		AtomID:       req.AtomID,
		TraceParent:  effectiveTraceparent(r),
		TraceState:   r.Header.Get("tracestate"),
	})
	if err != nil {
		writeCollectionError(w, err, "add_atom")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// -----------------------------------------------------------------------------
// RemoveAtom
// -----------------------------------------------------------------------------

func (h *CollectionHandler) removeAtom(w http.ResponseWriter, r *http.Request, id, atomID string) {
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	_, err := h.service.RemoveAtom(r.Context(), collection.RemoveAtomInput{
		TenantID:     tenantID,
		CollectionID: id,
		OwnerGcid:    gcid,
		AtomID:       atomID,
		TraceParent:  effectiveTraceparent(r),
		TraceState:   r.Header.Get("tracestate"),
	})
	if err != nil {
		writeCollectionError(w, err, "remove_atom")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// ConvertToStudyList — ADR-233 / spec-001 US5
// -----------------------------------------------------------------------------

// convertExclusion names an atom left out of the study list, with the machine-
// readable D4.2 reason. ADR-233 D11 forbids dropping an atom silently — A+
// renders these verbatim so the learner is told what was left out and why.
type convertExclusion struct {
	AtomID string `json:"atom_id"`
	Reason string `json:"reason"`
}

type convertToStudyListResponse struct {
	StudyListEventID string             `json:"study_list_event_id"`
	AtomCount        int                `json:"atom_count"`
	Excluded         []convertExclusion `json:"excluded"`
}

// convertToStudyList publishes chora.creation.collection.converted_to_study_list.v1;
// chora-consumption builds the LearningPath from it (no cross-DB read — the
// atom_ids ride the payload).
//
// Partial success is the norm (D11): entitlement is per atom, so a mixed
// collection converts with the excluded atoms NAMED. Zero survivors is a 409 —
// emitting an empty study list would be fabricating a success.
func (h *CollectionHandler) convertToStudyList(w http.ResponseWriter, r *http.Request, id string) {
	res, err := h.service.ConvertToStudyList(r.Context(), collection.ConvertToStudyListInput{
		TenantID:     tenantFromContext(r.Context()),
		CollectionID: id,
		ActorGCID:    gcidFromContext(r.Context()),
		// effectiveTraceparent(r), NOT r.Header.Get("traceparent"): the raw read
		// yields "" for a request the mesh did not decorate, and the outbox
		// publisher rejects an empty traceparent — stranding the event as
		// `failed` while the FE has already been told 201.
		TraceParent: effectiveTraceparent(r),
		TraceState:  r.Header.Get("tracestate"),
	})
	if err != nil {
		writeCollectionError(w, err, "convert_to_study_list")
		return
	}

	excluded := make([]convertExclusion, 0, len(res.Excluded))
	for _, d := range res.Excluded {
		excluded = append(excluded, convertExclusion{AtomID: d.AtomID, Reason: string(d.Reason)})
	}
	writeJSON(w, http.StatusCreated, convertToStudyListResponse{
		StudyListEventID: res.StudyListEventID,
		AtomCount:        res.AtomCount,
		Excluded:         excluded,
	})
}

// -----------------------------------------------------------------------------
// Error mapping
// -----------------------------------------------------------------------------

// writeCollectionError translates domain errors → HTTP status + envelope.
// Per CLAUDE.md §1 + ddd-enforcement.md error-envelope conventions.
func writeCollectionError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, collection.ErrNotFound):
		writeError(w, http.StatusNotFound, "CREATION_COLLECTION_NOT_FOUND",
			"collection not found")
	case errors.Is(err, collection.ErrForbidden):
		writeError(w, http.StatusForbidden, "CREATION_COLLECTION_FORBIDDEN",
			"caller is not the collection owner")
	case errors.Is(err, collection.ErrAtomDoesNotExist):
		writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND",
			"referenced atom does not exist")
	case errors.Is(err, collection.ErrCrossTenantAtomNotPermitted):
		writeError(w, http.StatusForbidden, "CREATION_COLLECTION_CROSS_TENANT_FORBIDDEN",
			err.Error())
	case errors.Is(err, collection.ErrDuplicateAtom):
		writeError(w, http.StatusConflict, "CREATION_COLLECTION_DUPLICATE_ATOM",
			"atom is already in the collection")
	case errors.Is(err, collection.ErrAtomCapExceeded):
		writeError(w, http.StatusUnprocessableEntity, "CREATION_COLLECTION_ATOM_CAP_EXCEEDED",
			err.Error())
	case errors.Is(err, collection.ErrAtomNotInCollection):
		writeError(w, http.StatusNotFound, "CREATION_COLLECTION_ATOM_NOT_FOUND",
			"atom is not in the collection")
	case errors.Is(err, collection.ErrCollectionDeleted):
		writeError(w, http.StatusGone, "CREATION_COLLECTION_DELETED",
			"collection is soft-deleted")
	case errors.Is(err, collection.ErrNoEntitledAtoms):
		// 409, not 200-with-nothing: never emit an empty study list (ADR-233 D11).
		writeError(w, http.StatusConflict, "CREATION_COLLECTION_NO_ENTITLED_ATOMS",
			"no atoms in this collection are reusable by you; nothing to convert")
	case errors.Is(err, collection.ErrAtomNotReusable):
		writeError(w, http.StatusForbidden, "CREATION_ATOM_NOT_REUSABLE",
			err.Error())

	// ── CHO-2174: chora-sharing's verdicts, translated at the client adapter ──
	//
	// These four used to fall through to the default arm and reach A+ as
	// 400 CREATION_COLLECTION_INVALID with the whole internal call chain
	// (`… rpc error: code = FailedPrecondition desc = …`) pasted into the
	// message. Each now gets its true status and an ACTIONABLE code.
	//
	// NB the messages are HUMAN and SELF-CONTAINED — no gRPC text, no call
	// chain, no atom UUID (per-atom detail belongs in `excluded[]`). The
	// upstream detail is not lost: it goes to the server log, loudly, and
	// stays out of the caller's response.
	case errors.Is(err, collection.ErrAtomNotShareable):
		// The atom has no reusable projection in chora-sharing (unpublished,
		// withdrawn, or narrowed), so no reuse grant can be minted for it. A
		// consent VERDICT — not a malformed request. Terminal: retrying changes
		// nothing until the author re-publishes or re-widens.
		log.Printf("collection.%s: atom not shareable (chora-sharing holds no reusable projection): %v", op, err)
		writeError(w, http.StatusConflict, "CREATION_ATOM_NOT_SHAREABLE",
			"an atom in this collection is not available for reuse — it is not published for sharing, or its author has since withdrawn it")
	case errors.Is(err, collection.ErrAtomReuseDenied):
		// The upstream authority refused THIS actor's reuse of the atom.
		log.Printf("collection.%s: reuse denied by chora-sharing: %v", op, err)
		writeError(w, http.StatusForbidden, "CREATION_ATOM_REUSE_DENIED",
			"the author of an atom in this collection has not given you permission to reuse it")
	case errors.Is(err, collection.ErrSharingUnavailable):
		// FAIL LOUD. The consent gate could not be evaluated (GetReuseContext) or
		// the D2 audit grant could not be recorded (AuthorizeAtomUse), so the
		// operation was REFUSED — never quietly completed without consent. 502:
		// an outage is OUR fault, not the caller's, and it is retryable.
		log.Printf("collection.%s: chora-sharing UNAVAILABLE — operation REFUSED (gate unevaluated / grant unminted; the gate must never degrade open): %v", op, err)
		writeError(w, http.StatusBadGateway, "CREATION_SHARING_UNAVAILABLE",
			"we could not check sharing permissions just now, so nothing was changed; please try again in a moment")
	case errors.Is(err, collection.ErrSharingRejectedRequest):
		// chora-sharing rejected the request chora-creation constructed. A
		// creation-side defect: never a 4xx blaming the learner, and — unlike an
		// outage — retrying it will fail identically, so it gets its own code.
		log.Printf("collection.%s: chora-sharing REJECTED the reuse-gate request we constructed (creation-side defect) — operation REFUSED: %v", op, err)
		writeError(w, http.StatusBadGateway, "CREATION_SHARING_GATE_ERROR",
			"we could not check sharing permissions just now, so nothing was changed; please try again in a moment")

	// ── CHO-2175: whose fault is it? ────────────────────────────────────────────
	//
	// These three used to share the default arm below, which answered 400
	// CREATION_COLLECTION_INVALID — "your request was malformed" — with the raw
	// internal error pasted into the message. For a database fault and an unwired
	// port alike. That is how CHO-2173 hid for months: AddAtom had NEVER worked in
	// prod, every call returned a 400 carrying a raw `SQLSTATE 22P02`, and because
	// 4xx means "client error" no alert fired and no dashboard reddened. The status
	// code told everyone the users were holding it wrong.
	//
	// A 4xx is a promise that the caller can fix it by sending something different.
	// We do not make that promise on their behalf.
	case errors.Is(err, collection.ErrInvalid):
		// The one class that IS theirs to fix. Keep the human message — the author
		// cannot correct what we will not name.
		writeError(w, http.StatusBadRequest, "CREATION_COLLECTION_INVALID",
			err.Error())
	case errors.Is(err, collection.ErrGateNotWired):
		// A DEPLOYMENT defect. The service already refused ("an unwired gate must
		// never pass"); this makes the refusal legible instead of libelling the
		// caller. Loudest log in the file: nothing else here means "this rollout is
		// broken and every request through this path is failing".
		log.Printf("collection.%s: 🔴 CONSENT GATE NOT WIRED — every request through this path is "+
			"REFUSED until the deployment is fixed. This is OUR misconfiguration, not the caller's "+
			"request: %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_GATE_NOT_WIRED",
			"we could not check sharing permissions just now, so nothing was changed; please try again in a moment")
	case errors.Is(err, collection.ErrRepository):
		// OUR datastore failed. 500, not 502: 502 is reserved for a failing UPSTREAM
		// SERVICE (CREATION_SHARING_UNAVAILABLE above), and conflating the two would
		// send an operator to debug chora-sharing while our own database is on fire.
		// The gate stays SHUT — a gate that cannot be evaluated never degrades open.
		log.Printf("collection.%s: local datastore FAILURE — operation REFUSED (the gate is not "+
			"evaluable, and must never degrade open): %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_COLLECTION_REPO_ERROR",
			"something went wrong on our side, so nothing was changed; please try again in a moment")

	default:
		// THE CATCH-ALL IS OURS, NOT THEIRS. Anything reaching here is an error we
		// failed to classify — which makes it a bug in this switch, and bugs are
		// 5xx. Never `err.Error()`: an unclassified error is exactly the one whose
		// contents we have not vetted for leaks (CHO-2174a closed this on the gRPC
		// path; it stayed wide open on every other path until CHO-2175).
		log.Printf("collection.%s: UNCLASSIFIED error reached the default arm — this is a bug in "+
			"writeCollectionError; give it a sentinel: %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_INTERNAL",
			"something went wrong on our side, so nothing was changed; please try again in a moment")
	}
}

// isUUID reports whether s parses as a UUID.
//
// CHO-2175 — the boundary guard. Every id a caller supplies is checked with this
// BEFORE it is put into a query, because the alternative is letting Postgres's
// type parser do our validation for us: an unparseable id reaches pg, dies on
// `invalid input syntax for type uuid (SQLSTATE 22P02)`, and surfaces as a
// repository failure we then blame ourselves for — or, before the default arm
// was inverted, as a 400 with the raw SQLSTATE pasted in, which is precisely how
// CHO-2173 hid in production for months.
//
// Two ways to be wrong about whose fault an error is, and this closes the second
// one: a 5xx must never be charged to the caller's typo, and a 4xx must never be
// charged to our outage.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
