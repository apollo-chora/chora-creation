// topic_handler.go — HTTP surface for the content topic tree (CHO-2275
// Sub-phase A). READ routes are learner-safe (tenant + gcid headers required by
// the tenantContext middleware). WRITE routes are admin-gated in-handler via the
// mesh `x-mesh-user-roles` header (defence-in-depth; the authoritative admin
// gate is the gateway allowlist wired in Sub-phase B — chora-creation has no
// role middleware of its own, see question_bank_handler.go).
//
// Routes:
//
//	GET    /api/topics            list (flat slice; optional ?parent_id filter)
//	POST   /api/topics            create                                  (admin)
//	GET    /api/topics/{id}       single node
//	PUT    /api/topics/{id}       rename / reorder                        (admin)
//	DELETE /api/topics/{id}       soft-delete (refused if non-empty)      (admin)
//	POST   /api/topics/{id}/move  reparent (cycle-guarded)                (admin)
//	POST   /api/topics/{id}/atoms attach an atom by UUID (atom-centric)   (admin)
//	POST   /api/internal/topics/backfill  seed-from-atom-tags (operator; tenant in body)
package httpadapter

import (
	"errors"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

// TopicHandler serves the /api/topics surface.
type TopicHandler struct {
	repo     topic.Repository
	backfill *topic.SeedBackfill // nil ⇒ the backfill route 503s (fail-loud)
}

// NewTopicHandler wires the handler. backfill may be nil (routes still serve;
// only the internal backfill endpoint is disabled).
func NewTopicHandler(repo topic.Repository, backfill *topic.SeedBackfill) *TopicHandler {
	return &TopicHandler{repo: repo, backfill: backfill}
}

// topicNodeDTO is the learner-safe wire projection. tenant_id is NOT exposed
// (implicit in the caller's context; never leak another axis of identity).
type topicNodeDTO struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
	CreatedAt string  `json:"created_at,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

func toTopicDTO(n *topic.TopicNode) topicNodeDTO {
	return topicNodeDTO{
		ID:        n.TopicID,
		ParentID:  n.ParentID,
		Name:      n.Name,
		SortOrder: n.SortOrder,
		CreatedAt: n.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: n.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// topicAdminRoles are the mesh role tokens allowed to mutate the topic tree.
// Canonical lowercase DB-enum roles (role-capabilities.ts): the tenant-admin set.
var topicAdminRoles = map[string]struct{}{"admin": {}, "owner": {}}

// callerIsTopicAdmin reports whether x-mesh-user-roles carries an admin/owner
// token. Fail-closed: absent/empty header ⇒ false.
func callerIsTopicAdmin(r *http.Request) bool {
	raw := strings.ToLower(r.Header.Get("x-mesh-user-roles"))
	for _, part := range strings.Split(raw, ",") {
		if _, ok := topicAdminRoles[strings.TrimSpace(part)]; ok {
			return true
		}
	}
	return false
}

func (th *TopicHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if callerIsTopicAdmin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "CREATION_TOPIC_ADMIN_REQUIRED",
		"topic tree mutations require an admin or owner role")
	return false
}

// collection dispatches /api/topics (GET list, POST create).
func (th *TopicHandler) collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		th.list(w, r)
	case http.MethodPost:
		th.create(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET and POST are supported on /api/topics")
	}
}

// item dispatches /api/topics/{id}[/move|/atoms].
func (th *TopicHandler) item(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/topics/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "CREATION_TOPIC_NOT_FOUND", "topic id required")
		return
	}
	id := rest
	sub := ""
	if slash := strings.Index(rest, "/"); slash != -1 {
		id = rest[:slash]
		sub = rest[slash+1:]
	}

	switch sub {
	case "":
		switch r.Method {
		case http.MethodGet:
			th.get(w, r, id)
		case http.MethodPut:
			th.update(w, r, id)
		case http.MethodDelete:
			th.delete(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only GET, PUT, DELETE are supported on /api/topics/{id}")
		}
	case "move":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST is supported on /api/topics/{id}/move")
			return
		}
		th.move(w, r, id)
	case "atoms":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST is supported on /api/topics/{id}/atoms")
			return
		}
		th.attach(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "CREATION_TOPIC_NOT_FOUND", "unknown sub-resource")
	}
}

func (th *TopicHandler) list(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFromContext(r.Context())
	var parentPtr *string
	if p := strings.TrimSpace(r.URL.Query().Get("parent_id")); p != "" {
		parentPtr = &p
	}
	nodes, err := th.repo.GetTree(r.Context(), tenant, parentPtr)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	out := make([]topicNodeDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toTopicDTO(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (th *TopicHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	tenant := tenantFromContext(r.Context())
	n, err := th.repo.GetByID(r.Context(), tenant, id)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTopicDTO(n))
}

func (th *TopicHandler) create(w http.ResponseWriter, r *http.Request) {
	if !th.requireAdmin(w, r) {
		return
	}
	tenant := tenantFromContext(r.Context())
	var req struct {
		Name      string  `json:"name"`
		ParentID  *string `json:"parent_id"`
		SortOrder int     `json:"sort_order"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "invalid request body")
		return
	}
	node, err := topic.NewTopicNode(tenant, req.Name, req.ParentID, req.SortOrder)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	// A child must reference a parent that actually exists for this tenant, or it
	// would be orphaned (fail-loud rather than silently create a dangling child).
	if node.ParentID != nil {
		if _, err := th.repo.GetByID(r.Context(), tenant, *node.ParentID); err != nil {
			if errors.Is(err, topic.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "parent topic not found")
				return
			}
			writeTopicError(w, err)
			return
		}
	}
	if err := th.repo.Create(r.Context(), node); err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTopicDTO(node))
}

func (th *TopicHandler) update(w http.ResponseWriter, r *http.Request, id string) {
	if !th.requireAdmin(w, r) {
		return
	}
	tenant := tenantFromContext(r.Context())
	var req struct {
		Name      *string `json:"name"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "invalid request body")
		return
	}
	node, err := th.repo.GetByID(r.Context(), tenant, id)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	if req.Name != nil {
		if err := node.Rename(*req.Name); err != nil {
			writeTopicError(w, err)
			return
		}
	}
	if req.SortOrder != nil {
		if err := node.SetSortOrder(*req.SortOrder); err != nil {
			writeTopicError(w, err)
			return
		}
	}
	if err := th.repo.Update(r.Context(), node); err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTopicDTO(node))
}

func (th *TopicHandler) delete(w http.ResponseWriter, r *http.Request, id string) {
	if !th.requireAdmin(w, r) {
		return
	}
	tenant := tenantFromContext(r.Context())
	if err := th.repo.SoftDelete(r.Context(), tenant, id); err != nil {
		writeTopicError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (th *TopicHandler) move(w http.ResponseWriter, r *http.Request, id string) {
	if !th.requireAdmin(w, r) {
		return
	}
	tenant := tenantFromContext(r.Context())
	var req struct {
		ParentID *string `json:"parent_id"` // null / absent ⇒ promote to root
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "invalid request body")
		return
	}
	if err := th.repo.Move(r.Context(), tenant, id, req.ParentID); err != nil {
		writeTopicError(w, err)
		return
	}
	n, err := th.repo.GetByID(r.Context(), tenant, id)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTopicDTO(n))
}

func (th *TopicHandler) attach(w http.ResponseWriter, r *http.Request, id string) {
	if !th.requireAdmin(w, r) {
		return
	}
	tenant := tenantFromContext(r.Context())
	var req struct {
		AtomID string `json:"atom_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "invalid request body")
		return
	}
	if err := th.repo.AttachAtom(r.Context(), tenant, id, req.AtomID); err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "attached", "topic_id": id, "atom_id": req.AtomID})
}

// backfillHandler runs the seed-from-atom-tags backfill for a tenant. Internal
// (bypasses the header gate); tenant carried in the body. Operator/CI use.
func (th *TopicHandler) backfillHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/internal/topics/backfill")
		return
	}
	if th.backfill == nil {
		writeError(w, http.StatusServiceUnavailable, "CREATION_TOPIC_BACKFILL_NOT_WIRED",
			"topic backfill is not wired in this deployment")
		return
	}
	var req struct {
		TenantID string `json:"tenant_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", "invalid request body")
		return
	}
	if strings.TrimSpace(req.TenantID) == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED", "tenant_id is required")
		return
	}
	rep, err := th.backfill.Seed(r.Context(), req.TenantID)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// writeTopicError maps domain sentinels to HTTP status + envelope.
func writeTopicError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, topic.ErrNotFound):
		writeError(w, http.StatusNotFound, "CREATION_TOPIC_NOT_FOUND", "topic not found")
	case errors.Is(err, topic.ErrCycle):
		writeError(w, http.StatusConflict, "CREATION_TOPIC_CYCLE",
			"move would create a cycle (a node cannot become its own descendant)")
	case errors.Is(err, topic.ErrHasChildren):
		writeError(w, http.StatusConflict, "CREATION_TOPIC_HAS_CHILDREN",
			"cannot delete a topic that still has child topics")
	case errors.Is(err, topic.ErrNameRequired),
		errors.Is(err, topic.ErrNameTooLong),
		errors.Is(err, topic.ErrInvalidParent),
		errors.Is(err, topic.ErrInvalidSortOrder),
		errors.Is(err, topic.ErrInvalidAtomID),
		errors.Is(err, topic.ErrTenantRequired):
		writeError(w, http.StatusBadRequest, "CREATION_TOPIC_INVALID", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "CREATION_TOPIC_ERROR", "internal error")
	}
}
