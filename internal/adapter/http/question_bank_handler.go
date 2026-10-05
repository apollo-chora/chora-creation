// question_bank_handler.go — QuestionBank CRUD endpoints (W3.B.1, 2026-06-28).
//
// Routes (sole-owner of this file):
//
//	POST   /api/v1/question-banks                            — create bank
//	GET    /api/v1/me/question-banks                         — list caller's banks
//	GET    /api/v1/question-banks/{id}                       — get detail
//	PATCH  /api/v1/question-banks/{id}                       — update name/desc/visibility/tags
//	DELETE /api/v1/question-banks/{id}                       — soft delete
//	POST   /api/v1/question-banks/{id}/questions             — add question
//	GET    /api/v1/question-banks/{id}/questions             — list questions
//	DELETE /api/v1/question-banks/{id}/questions/{qid}       — remove question
//
// The /api/v1/* prefix matches the sibling curation aggregate (Collection at
// /api/v1/collections) — the newest chora-creation REST convention. All
// endpoints require X-Tenant-Id + gcid headers (enforced by the shared
// tenantContext middleware). Mutations are owner-gated by the domain Service
// (ErrForbidden → 403); there is no separate role middleware in this service.
//
// Error envelope shapes match the existing CREATION_* convention (see
// handler.go writeError). The handler delegates to questionbank.Service which
// composes Repository + QuestionLookup (+ a B.1-unwired EventPublisher).
package httpadapter

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// QuestionBankHandler implements the /api/v1/question-banks* routes. Wired by
// NewRouterWithDeps when QuestionBankRepository + QuestionBankQuestionLookup are
// non-nil.
type QuestionBankHandler struct {
	service *questionbank.Service
}

// NewQuestionBankHandler wires the handler. service must be non-nil.
func NewQuestionBankHandler(service *questionbank.Service) *QuestionBankHandler {
	return &QuestionBankHandler{service: service}
}

// mountQuestionBankRoutes registers the question-bank routes on the supplied mux.
func mountQuestionBankRoutes(mux *http.ServeMux, h *QuestionBankHandler) {
	mux.HandleFunc("/api/v1/question-banks", h.questionBanksCollection)
	mux.HandleFunc("/api/v1/question-banks/", h.questionBanksItem)
	mux.HandleFunc("/api/v1/me/question-banks", h.listMyQuestionBanks)
}

// -----------------------------------------------------------------------------
// /api/v1/question-banks (collection root)
// -----------------------------------------------------------------------------

func (h *QuestionBankHandler) questionBanksCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/v1/question-banks")
		return
	}
	h.createQuestionBank(w, r)
}

// -----------------------------------------------------------------------------
// /api/v1/question-banks/{id}[/questions[/{question_id}]]
// -----------------------------------------------------------------------------

func (h *QuestionBankHandler) questionBanksItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/question-banks/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "missing question bank id")
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "missing question bank id")
		return
	}
	// CHO-2175 — parsed HERE, not by Postgres. See collection_handler.go isUUID.
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_INVALID",
			"question bank id must be a UUID")
		return
	}
	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			h.getQuestionBank(w, r, id)
		case http.MethodPatch:
			h.patchQuestionBank(w, r, id)
		case http.MethodDelete:
			h.deleteQuestionBank(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only GET, PATCH, DELETE on /api/v1/question-banks/{id}")
		}
	case len(parts) == 2 && parts[1] == "questions":
		switch r.Method {
		case http.MethodPost:
			h.addQuestion(w, r, id)
		case http.MethodGet:
			h.listQuestions(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only GET, POST on /api/v1/question-banks/{id}/questions")
		}
	case len(parts) == 2 && parts[1] == "assemble-test-set":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST on /api/v1/question-banks/{id}/assemble-test-set")
			return
		}
		h.assembleTestSet(w, r, id)
	case len(parts) == 2 && parts[1] == "reorder":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST on /api/v1/question-banks/{id}/reorder")
			return
		}
		h.reorderItems(w, r, id)
	case len(parts) == 3 && parts[1] == "questions":
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only DELETE on /api/v1/question-banks/{id}/questions/{question_id}")
			return
		}
		if !isUUID(parts[2]) {
			writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_INVALID",
				"question_id must be a UUID")
			return
		}
		h.removeQuestion(w, r, id, parts[2])
	default:
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
	}
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

type createQuestionBankRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

func (h *QuestionBankHandler) createQuestionBank(w http.ResponseWriter, r *http.Request) {
	var req createQuestionBankRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	b, err := h.service.Create(r.Context(), questionbank.CreateInput{
		TenantID:    tenantFromContext(r.Context()),
		OwnerGCID:   gcidFromContext(r.Context()),
		Name:        req.Name,
		Description: req.Description,
		Visibility:  questionbank.Visibility(req.Visibility),
		Tags:        req.Tags,
	})
	if err != nil {
		writeQuestionBankError(w, err, "create")
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

func (h *QuestionBankHandler) getQuestionBank(w http.ResponseWriter, r *http.Request, id string) {
	b, err := h.service.GetVisible(r.Context(), tenantFromContext(r.Context()), id)
	if err != nil {
		writeQuestionBankError(w, err, "get")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// -----------------------------------------------------------------------------
// List (caller's banks)
// -----------------------------------------------------------------------------

type listQuestionBanksResponse struct {
	Items []*questionbank.QuestionBank `json:"items"`
	Total int                          `json:"total"`
}

func (h *QuestionBankHandler) listMyQuestionBanks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET on /api/v1/me/question-banks")
		return
	}
	// Facets (CHO-1899): name-search (q) + tag-filter (tag[], OR-any) + sort.
	qq := r.URL.Query()
	sorts, serr := questionbank.ParseSorts(qq.Get("sort"))
	if serr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_SEARCH_INVALID_SORT", serr.Error())
		return
	}
	items, err := h.service.List(r.Context(), tenantFromContext(r.Context()), questionbank.ListFilter{
		OwnerGCID: gcidFromContext(r.Context()),
		Q:         strings.TrimSpace(qq.Get("q")),
		Tags:      qq["tag"],
		Sorts:     sorts,
	})
	if err != nil {
		log.Printf("listMyQuestionBanks: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to list question banks")
		return
	}
	writeJSON(w, http.StatusOK, listQuestionBanksResponse{Items: items, Total: len(items)})
}

// -----------------------------------------------------------------------------
// Patch (update metadata)
// -----------------------------------------------------------------------------

type patchQuestionBankRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Visibility  *string   `json:"visibility,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
}

func (h *QuestionBankHandler) patchQuestionBank(w http.ResponseWriter, r *http.Request, id string) {
	var req patchQuestionBankRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	params := questionbank.UpdateParams{
		Name:        req.Name,
		Description: req.Description,
		Tags:        req.Tags,
	}
	if req.Visibility != nil {
		v := questionbank.Visibility(*req.Visibility)
		params.Visibility = &v
	}
	b, err := h.service.UpdateMetadata(r.Context(), questionbank.UpdateMetadataInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
		Params:         params,
	})
	if err != nil {
		writeQuestionBankError(w, err, "patch")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// -----------------------------------------------------------------------------
// Delete
// -----------------------------------------------------------------------------

func (h *QuestionBankHandler) deleteQuestionBank(w http.ResponseWriter, r *http.Request, id string) {
	err := h.service.Delete(r.Context(), questionbank.DeleteInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
	})
	if err != nil {
		writeQuestionBankError(w, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// AddQuestion
// -----------------------------------------------------------------------------

type addQuestionRequest struct {
	QuestionID string `json:"question_id"`
}

func (h *QuestionBankHandler) addQuestion(w http.ResponseWriter, r *http.Request, id string) {
	var req addQuestionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	if strings.TrimSpace(req.QuestionID) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "question_id is required")
		return
	}
	// CHO-2175 — parsed HERE, not by Postgres. See collection_handler.go isUUID.
	if !isUUID(strings.TrimSpace(req.QuestionID)) {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "question_id must be a UUID")
		return
	}
	b, err := h.service.AddQuestion(r.Context(), questionbank.AddQuestionInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
		QuestionID:     req.QuestionID,
	})
	if err != nil {
		writeQuestionBankError(w, err, "add_question")
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// -----------------------------------------------------------------------------
// ListQuestions
// -----------------------------------------------------------------------------

const (
	defaultQuestionsPageSize = 20
	maxQuestionsPageSize     = 100
)

type listQuestionsResponse struct {
	Items    []questionbank.EnrichedQuestionBankItem `json:"items"`
	Total    int                                     `json:"total"`
	Page     int                                     `json:"page"`
	PageSize int                                     `json:"page_size"`
}

// listQuestions serves ONE filtered/sorted page of the bank's questions, each
// row ENRICHED with host atom_id + question_type + prompt. Optional query
// params: q (keyword on the prompt), question_type (repeatable mcq/oe), sort
// (field:dir over position|added_at|prompt), page, page_size. Resolved
// server-side via an intra-DB JOIN so a HUGE bank never loads in full.
func (h *QuestionBankHandler) listQuestions(w http.ResponseWriter, r *http.Request, id string) {
	f, page, pageSize := parseItemPageFilter(r)
	items, total, err := h.service.ListItemsEnrichedPage(r.Context(), tenantFromContext(r.Context()), id, f)
	if err != nil {
		writeQuestionBankError(w, err, "list_questions")
		return
	}
	if items == nil {
		items = []questionbank.EnrichedQuestionBankItem{}
	}
	writeJSON(w, http.StatusOK, listQuestionsResponse{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// parseItemPageFilter reads the optional q / question_type / sort / page /
// page_size query params into a tenant-safe ItemPageFilter (+ the resolved
// page/page_size echoed back). page_size is clamped; an unknown sort token
// falls back to position.
func parseItemPageFilter(r *http.Request) (questionbank.ItemPageFilter, int, int) {
	qv := r.URL.Query()
	page := qbAtoiDefault(qv.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := qbAtoiDefault(qv.Get("page_size"), defaultQuestionsPageSize)
	if pageSize < 1 {
		pageSize = defaultQuestionsPageSize
	}
	if pageSize > maxQuestionsPageSize {
		pageSize = maxQuestionsPageSize
	}
	sortKey, sortDesc := parseItemSort(qv.Get("sort"))
	return questionbank.ItemPageFilter{
		Query:    strings.TrimSpace(qv.Get("q")),
		Types:    qv["question_type"], // repeated param → []string
		SortKey:  sortKey,
		SortDesc: sortDesc,
		Limit:    pageSize,
		Offset:   (page - 1) * pageSize,
	}, page, pageSize
}

// parseItemSort parses a `field:dir` token against the ItemSortKeys whitelist;
// an unknown field yields ("", false) → the adapter defaults to position ASC.
func parseItemSort(s string) (key string, desc bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	field, dir, _ := strings.Cut(s, ":")
	field = strings.TrimSpace(field)
	if _, ok := questionbank.ItemSortKeys[field]; !ok {
		return "", false
	}
	return field, strings.EqualFold(strings.TrimSpace(dir), "desc")
}

func qbAtoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// -----------------------------------------------------------------------------
// RemoveQuestion
// -----------------------------------------------------------------------------

func (h *QuestionBankHandler) removeQuestion(w http.ResponseWriter, r *http.Request, id, questionID string) {
	_, err := h.service.RemoveQuestion(r.Context(), questionbank.RemoveQuestionInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
		QuestionID:     questionID,
	})
	if err != nil {
		writeQuestionBankError(w, err, "remove_question")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// ReorderItems (Wave 2) — POST /api/v1/question-banks/{id}/reorder
// -----------------------------------------------------------------------------

type reorderQuestionBankRequest struct {
	QuestionIDs []string `json:"question_ids"`
}

func (h *QuestionBankHandler) reorderItems(w http.ResponseWriter, r *http.Request, id string) {
	var req reorderQuestionBankRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	if len(req.QuestionIDs) == 0 {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "question_ids is required")
		return
	}
	// CHO-2175 — parsed HERE, not by Postgres. See collection_handler.go isUUID.
	for _, qid := range req.QuestionIDs {
		if !isUUID(strings.TrimSpace(qid)) {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY",
				"every entry in question_ids must be a UUID")
			return
		}
	}
	b, err := h.service.ReorderItems(r.Context(), questionbank.ReorderItemsInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
		QuestionIDs:    req.QuestionIDs,
	})
	if err != nil {
		writeQuestionBankError(w, err, "reorder_items")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// -----------------------------------------------------------------------------
// AssembleTestSet (W3.B.2) — POST /api/v1/question-banks/{id}/assemble-test-set
// -----------------------------------------------------------------------------

type assembleTestSetRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type assembleTestSetResponse struct {
	JobID         string `json:"job_id"`
	TestSetStatus string `json:"test_set_status"`
}

// assembleTestSet assembles a DRAFT TestSet from ALL active questions in the
// bank by publishing the EXISTING chora.creation.question_batch.accepted.v1
// event (the chora-delivery batch_testset_inbox subscriber assembles the DRAFT).
// Returns 202 {job_id, test_set_status:"assembling"}. Owner-gated by the Service.
func (h *QuestionBankHandler) assembleTestSet(w http.ResponseWriter, r *http.Request, id string) {
	var req assembleTestSetRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
			return
		}
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "title is required")
		return
	}
	res, err := h.service.AssembleTestSet(r.Context(), questionbank.AssembleTestSetInput{
		TenantID:       tenantFromContext(r.Context()),
		QuestionBankID: id,
		OwnerGCID:      gcidFromContext(r.Context()),
		Title:          req.Title,
		Description:    req.Description,
		Traceparent:    tracing.TraceparentFromContext(r.Context()),
	})
	if err != nil {
		writeQuestionBankError(w, err, "assemble_test_set")
		return
	}
	writeJSON(w, http.StatusAccepted, assembleTestSetResponse{
		JobID:         res.JobID,
		TestSetStatus: res.Status,
	})
}

// -----------------------------------------------------------------------------
// Error mapping
// -----------------------------------------------------------------------------

// writeQuestionBankError translates domain errors → HTTP status + envelope.
func writeQuestionBankError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, questionbank.ErrNotFound):
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_BANK_NOT_FOUND",
			"question bank not found")
	case errors.Is(err, questionbank.ErrForbidden):
		writeError(w, http.StatusForbidden, "CREATION_QUESTION_BANK_FORBIDDEN",
			"caller is not the question bank owner")
	case errors.Is(err, questionbank.ErrQuestionDoesNotExist):
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
			"referenced question does not exist")
	case errors.Is(err, questionbank.ErrDuplicateQuestion):
		writeError(w, http.StatusConflict, "CREATION_QUESTION_BANK_DUPLICATE_QUESTION",
			"question is already in the question bank")
	case errors.Is(err, questionbank.ErrItemCapExceeded):
		writeError(w, http.StatusUnprocessableEntity, "CREATION_QUESTION_BANK_CAP_EXCEEDED",
			err.Error())
	case errors.Is(err, questionbank.ErrQuestionNotInBank):
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_BANK_QUESTION_NOT_FOUND",
			"question is not in the question bank")
	case errors.Is(err, questionbank.ErrReorderMismatch):
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_REORDER_MISMATCH",
			err.Error())
	case errors.Is(err, questionbank.ErrQuestionBankDeleted):
		writeError(w, http.StatusGone, "CREATION_QUESTION_BANK_DELETED",
			"question bank is soft-deleted")
	case errors.Is(err, questionbank.ErrTitleRequired):
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_INVALID", err.Error())
	case errors.Is(err, questionbank.ErrEmptyBank):
		writeError(w, http.StatusUnprocessableEntity, "CREATION_QUESTION_BANK_EMPTY",
			"cannot assemble a test set from an empty question bank")
	case errors.Is(err, questionbank.ErrAssemblerNotWired):
		writeError(w, http.StatusServiceUnavailable, "CREATION_TESTSET_PUBLISH_NOT_WIRED",
			"test set assembly is not available (publisher not wired)")
	// ── CHO-2175: whose fault is it? (twin of writeCollectionError) ─────────────
	case errors.Is(err, questionbank.ErrInvalid):
		// The one class that IS the caller's to fix. Keep the human message.
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_INVALID", err.Error())
	case errors.Is(err, questionbank.ErrGateNotWired):
		log.Printf("question_bank.%s: 🔴 REQUIRED PORT NOT WIRED — every request through this path "+
			"is REFUSED until the deployment is fixed. OUR misconfiguration, not the caller's "+
			"request: %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_QUESTION_BANK_NOT_WIRED",
			"something went wrong on our side, so nothing was changed; please try again in a moment")
	case errors.Is(err, questionbank.ErrRepository):
		// OUR datastore failed. Never a 400: the author cannot fix our database by
		// retyping their request, and a 4xx tells our monitoring nothing is wrong.
		log.Printf("question_bank.%s: local datastore FAILURE — operation REFUSED: %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_QUESTION_BANK_REPO_ERROR",
			"something went wrong on our side, so nothing was changed; please try again in a moment")

	default:
		// THE CATCH-ALL IS OURS, NOT THEIRS. Anything here is an error we failed to
		// classify — a bug in this switch, and bugs are 5xx. Never `err.Error()`: an
		// unclassified error is precisely the one whose contents we have not vetted
		// for leaks.
		log.Printf("question_bank.%s: UNCLASSIFIED error reached the default arm — this is a bug "+
			"in writeQuestionBankError; give it a sentinel: %v", op, err)
		writeError(w, http.StatusInternalServerError, "CREATION_INTERNAL",
			"something went wrong on our side, so nothing was changed; please try again in a moment")
	}
}
