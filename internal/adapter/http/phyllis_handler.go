// Phyllis MVP HTTP handlers per docs/m13/phyllis-mvp-2026-05-08.md §5.3.
//
// Endpoints under /v1/atoms[*] and /v1/courses/{id}/atoms — separate from
// the legacy /api/atoms surface so the existing M10 skeleton keeps working.
//
//	POST   /v1/atoms                              create LearningAtom
//	POST   /v1/atoms:generate                     AI-Assist (Comic Ch6 P12)
//	POST   /v1/atoms/batch                        AI-Assist alias per spec §5.3
//	GET    /v1/atoms/{atom_id}                    single atom + current revision
//	PATCH  /v1/atoms/{atom_id}                    AppendRevision (audit §3.1)
//	GET    /v1/courses/{course_id}/atoms          list atoms in a course
//	GET    /v1/atoms/{atom_id}/revisions          revision history (append-only)
//	POST   /v1/atoms/{atom_id}/media              MediaAsset upload reference (audit §3.1)
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

// PhyllisDeps wires the Phyllis MVP handlers with their three core ports
// + optional media pipeline ports.
type PhyllisDeps struct {
	Repo      atom.Repository
	Broker    atom.ModelBrokerClient
	Publisher atom.EventPublisher

	// AtomWriter persists an atom and enqueues its event in ONE transaction.
	// Production wires pg.AtomTxWriter. When absent, NewPhyllisRouter falls
	// back to the NON-ATOMIC sequential writer and says so at startup, which
	// is the dev / unit-test shape only.
	AtomWriter atom.TransactionalPublisher

	// MediaRepo + MediaSigner are optional. If absent, /v1/atoms/{id}/media
	// returns 503 — the route stays wired but the pipeline is offline.
	MediaRepo   media.Repository
	MediaSigner media.SignedURLSigner
}

// PhyllisHandler exposes the MVP routes.
type PhyllisHandler struct {
	repo   atom.Repository
	ai     *atom.AIAssistService
	pub    atom.EventPublisher
	writer atom.TransactionalPublisher
	broker atom.ModelBrokerClient

	mediaRepo   media.Repository
	mediaSigner media.SignedURLSigner
}

// NewPhyllisRouter wires the Phyllis MVP handlers onto a fresh ServeMux,
// composed with the same logging + tenantContext middleware as the legacy
// router.
func NewPhyllisRouter(d PhyllisDeps) http.Handler {
	writer := d.AtomWriter
	if writer == nil {
		// Fail-loud, not silent: without a transactional seam the atom row
		// and its outbox row cannot share a commit. The sequential writer
		// still surfaces a publish failure as a 500 instead of a false 201,
		// but it cannot roll the save back.
		log.Printf("creation: PhyllisRouter AtomWriter not supplied, using the NON-ATOMIC sequential writer (dev / test wiring only)")
		writer = atom.NewSequentialPublisher(d.Repo, d.Publisher)
	}
	h := &PhyllisHandler{
		repo:        d.Repo,
		ai:          atom.NewAIAssistService(d.Broker, d.Repo, d.Publisher),
		pub:         d.Publisher,
		writer:      writer,
		broker:      d.Broker,
		mediaRepo:   d.MediaRepo,
		mediaSigner: d.MediaSigner,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/healthz/", healthz)
	mux.HandleFunc("/health", healthz)

	// Top-level dispatcher captures /v1/atoms[*] including the colon-action
	// /v1/atoms:generate path which Go's ServeMux does not match against
	// `/v1/atoms` or `/v1/atoms/` patterns.
	mux.HandleFunc("/v1/", h.handleV1)

	return logging(tenantContext(mux))
}

// -----------------------------------------------------------------------------
// /v1/* dispatcher
// -----------------------------------------------------------------------------

// handleV1 dispatches all /v1/* paths. Go's ServeMux is path-prefix only;
// we route on r.URL.Path here.
func (h *PhyllisHandler) handleV1(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/atoms":
		h.handleAtomsCollection(w, r)
	case r.URL.Path == "/v1/atoms:generate":
		h.aiAssistGenerate(w, r)
	case r.URL.Path == "/v1/atoms/batch":
		// /v1/atoms/batch is a route-level alias for /v1/atoms:generate
		// per Phyllis MVP spec §5.3. Idempotent on batch_id (the broker
		// orchestrator is the source of idempotency; this handler is a
		// thin pass-through).
		h.aiAssistGenerate(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/atoms/"):
		h.handleAtomsItem(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/courses/"):
		h.handleCoursesItem(w, r)
	default:
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown path")
	}
}

func (h *PhyllisHandler) handleAtomsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/atoms")
		return
	}
	h.createAtom(w, r)
}

// /v1/atoms/{atom_id} | /v1/atoms/{atom_id}/revisions | /v1/atoms/{atom_id}/media
func (h *PhyllisHandler) handleAtomsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/atoms/")
	rest = strings.TrimSuffix(rest, "/")

	if rest == "" {
		// /v1/atoms/ trailing slash with no item
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown path")
		return
	}

	if strings.Contains(rest, "/") {
		// /v1/atoms/{id}/{sub}
		parts := strings.SplitN(rest, "/", 2)
		atomID := parts[0]
		sub := parts[1]
		switch {
		case sub == "revisions" && r.Method == http.MethodGet:
			h.listRevisions(w, r, atomID)
			return
		case sub == "media" && r.Method == http.MethodPost:
			h.createAtomMedia(w, r, atomID)
			return
		}
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}

	// /v1/atoms/{id}
	switch r.Method {
	case http.MethodGet:
		h.getAtom(w, r, rest)
	case http.MethodPatch:
		h.patchAtom(w, r, rest)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET and PATCH are supported on /v1/atoms/{id}")
	}
}

// /v1/courses/{course_id}/atoms
func (h *PhyllisHandler) handleCoursesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/courses/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "atoms" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown path")
		return
	}
	courseID := parts[0]
	if courseID == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "course_id required")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /v1/courses/{id}/atoms")
		return
	}
	h.listCourseAtoms(w, r, courseID)
}

// -----------------------------------------------------------------------------
// POST /v1/atoms — create + POST /v1/atoms:generate routing
// -----------------------------------------------------------------------------

type phyllisCreateAtomRequest struct {
	CourseID   string   `json:"course_id"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Type       string   `json:"type"`
	Difficulty int      `json:"difficulty"`
	Tags       []string `json:"tags"`
}

func (h *PhyllisHandler) createAtom(w http.ResponseWriter, r *http.Request) {
	var req phyllisCreateAtomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	a, err := atom.NewBound(atom.NewBoundParams{
		TenantID:   tenantID,
		Gcid:       gcid,
		CourseID:   req.CourseID,
		Title:      req.Title,
		Body:       req.Body,
		AtomType:   atom.AtomType(req.Type),
		Difficulty: req.Difficulty,
		Tags:       req.Tags,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ATOM", err.Error())
		return
	}
	// The atom row and the outbox row announcing it are ONE transaction. A
	// failure on either rolls both back and the author is told so: a 201 for
	// an atom no downstream domain will ever hear about is a fabricated
	// success (chaos20260807a measured 10 of them).
	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")
	ev := atom.NewAtomCreatedEvent(a, traceparent, tracestate)
	if err := h.writer.SaveAndPublish(r.Context(), a, ev); err != nil {
		log.Printf("createAtom: save+enqueue failed, nothing committed: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist atom")
		return
	}

	writeJSON(w, http.StatusCreated, atomResponse(a))
}

// -----------------------------------------------------------------------------
// POST /v1/atoms:generate — AI Assist (Comic Ch6 P12)
// -----------------------------------------------------------------------------

type phyllisGenerateRequest struct {
	CourseID    string `json:"course_id"`
	Prompt      string `json:"prompt"`
	ContentType string `json:"content_type"`
	Difficulty  int    `json:"difficulty"`
	Count       int    `json:"count"`
}

func (h *PhyllisHandler) aiAssistGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/atoms:generate")
		return
	}
	var req phyllisGenerateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")

	// 30 s timeout per spec §AI-Assist HTTP integration. Caller's request
	// context can shorten further; we tighten only if it is unbounded.
	ctx := r.Context()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	resp, err := h.ai.Generate(ctx, atom.AIAssistRequest{
		TenantID:    tenantID,
		Gcid:        gcid,
		CourseID:    req.CourseID,
		Prompt:      req.Prompt,
		ContentType: atom.AtomType(req.ContentType),
		Difficulty:  req.Difficulty,
		Count:       req.Count,
		TraceParent: traceparent,
		TraceState:  tracestate,
	})
	if err != nil {
		// Distinguish input-validation errors (400) from broker/persist errors (502/500).
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_ASSIST", err.Error())
			return
		}
		log.Printf("ai-assist error: %v", err)
		writeError(w, http.StatusBadGateway, "CREATION_AI_BROKER_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// isValidationError returns true if err looks like a domain-validation error
// (vs an upstream broker / persistence failure). The domain returns plain
// errors with descriptive messages so we string-match a known prefix set.
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "invalid") ||
		strings.Contains(s, "is required") ||
		strings.Contains(s, "out of range") ||
		strings.Contains(s, "must be > 0") ||
		strings.Contains(s, "count too high")
}

// -----------------------------------------------------------------------------
// GET /v1/atoms/{atom_id}
// -----------------------------------------------------------------------------

func (h *PhyllisHandler) getAtom(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
			return
		}
		log.Printf("get error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, atomResponse(a))
}

// -----------------------------------------------------------------------------
// GET /v1/courses/{course_id}/atoms
// -----------------------------------------------------------------------------

func (h *PhyllisHandler) listCourseAtoms(w http.ResponseWriter, r *http.Request, courseID string) {
	tenantID := tenantFromContext(r.Context())
	atoms, err := h.repo.ListByCourse(r.Context(), tenantID, courseID)
	if err != nil {
		log.Printf("list-by-course error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to list")
		return
	}
	out := struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}{Total: len(atoms), Items: make([]map[string]any, 0, len(atoms))}
	for _, a := range atoms {
		out.Items = append(out.Items, atomResponse(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// -----------------------------------------------------------------------------
// PATCH /v1/atoms/{atom_id} — append a new revision (audit §3.1)
//
// Per ddd-enforcement aggregate invariant #4 AtomRevision is APPEND-ONLY.
// PATCH does NOT update the atom in place; it appends a new
// AppendOnlyRevision to the history and bumps the parent's UpdatedAt.
// -----------------------------------------------------------------------------

type phyllisPatchAtomRequest struct {
	Body       string `json:"body"`
	SourceType string `json:"source_type"` // manual | ai_assist | community_atom_bank
	AuthoredBy string `json:"authored_by,omitempty"`
}

func (h *PhyllisHandler) patchAtom(w http.ResponseWriter, r *http.Request, id string) {
	var req phyllisPatchAtomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	authoredBy := strings.TrimSpace(req.AuthoredBy)
	if authoredBy == "" {
		authoredBy = gcid
	}
	srcType := atom.SourceType(req.SourceType)
	if srcType == "" {
		srcType = atom.SourceManual
	}

	a, err := h.repo.Get(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
			return
		}
		log.Printf("get error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	// CHO-2264 (SECURITY) — author-only, same as the legacy twin. `gcid` was
	// read above ONLY as revision attribution (authoredBy); it never gated
	// anything, so any caller in the tenant could append a revision to any atom
	// — attributed to themselves.
	if refuseNonAuthor(w, a, gcid) {
		return
	}
	rev, err := a.AppendRevision(atom.AppendRevisionParams{
		Body:       req.Body,
		AuthoredBy: authoredBy,
		SourceType: srcType,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_REVISION", err.Error())
		return
	}
	// One transaction for the appended revision and the event announcing it
	// (see createAtom above).
	traceparent := effectiveTraceparent(r)
	tracestate := r.Header.Get("tracestate")
	ev := atom.NewAtomRevisedEvent(a, rev, traceparent, tracestate)
	if srcType == atom.SourceAIAssist {
		ev.ChoraImdaDimension = "accountability"
		ev.ImdaLifecycleStage = "runtime"
	}
	if err := h.writer.SaveAndPublish(r.Context(), a, ev); err != nil {
		log.Printf("patchAtom: save+enqueue failed, nothing committed: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist revision")
		return
	}

	writeJSON(w, http.StatusOK, atomResponse(a))
}

// -----------------------------------------------------------------------------
// POST /v1/atoms/{atom_id}/media — MediaAsset upload reference
//
// Returns a signed URL the caller can PUT the binary to. The atom must exist
// + belong to the requesting tenant. MVP: signed URL is a stub; M12 swaps in
// the real Cloud Storage V4 signer.
// -----------------------------------------------------------------------------

type phyllisCreateMediaRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

func (h *PhyllisHandler) createAtomMedia(w http.ResponseWriter, r *http.Request, atomID string) {
	if h.mediaRepo == nil || h.mediaSigner == nil {
		writeError(w, http.StatusServiceUnavailable, "CREATION_MEDIA_OFFLINE",
			"media pipeline not wired in this deployment")
		return
	}
	var req phyllisCreateMediaRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	// Parent atom must exist + belong to tenant, AND be authored by the caller.
	//
	// CHO-2264 (SECURITY) — this load DISCARDED the atom (`if _, err := ...`),
	// so it could not have checked ownership even in principle, while the legacy
	// twin POST /api/atoms/{id}/media has enforced `a.Gcid != gcid` all along.
	// Same capability, two doors, one locked. Bind the atom and gate it.
	parent, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
			return
		}
		log.Printf("media: parent atom get error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	if refuseNonAuthor(w, parent, gcid) {
		return
	}

	asset, err := media.New(media.NewParams{
		TenantID:    tenantID,
		Gcid:        gcid,
		AtomID:      atomID,
		Filename:    req.Filename,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_MEDIA", err.Error())
		return
	}
	if err := h.mediaRepo.Save(r.Context(), asset); err != nil {
		log.Printf("media save error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist asset")
		return
	}
	uploadURL, err := h.mediaSigner.UploadURL(r.Context(), asset, 600)
	if err != nil {
		log.Printf("media sign error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_SIGNER_ERROR", "failed to mint upload URL")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"asset_id":     asset.AssetID,
		"atom_id":      asset.AtomID,
		"tenant_id":    asset.TenantID,
		"filename":     asset.Filename,
		"content_type": asset.ContentType,
		"size_bytes":   asset.SizeBytes,
		"status":       string(asset.Status),
		"upload_url":   uploadURL,
		"upload_ttl_s": 600,
		"created_at":   asset.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

// -----------------------------------------------------------------------------
// GET /v1/atoms/{atom_id}/revisions
// -----------------------------------------------------------------------------

func (h *PhyllisHandler) listRevisions(w http.ResponseWriter, r *http.Request, atomID string) {
	tenantID := tenantFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
			return
		}
		log.Printf("get error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	hist := a.RevisionHistory()
	items := make([]map[string]any, 0, len(hist))
	for _, rev := range hist {
		items = append(items, map[string]any{
			"revision_id":     rev.RevisionID,
			"atom_id":         rev.AtomID,
			"revision_number": rev.RevisionNumber,
			"body":            rev.Body,
			"authored_by":     rev.AuthoredBy,
			"authored_at":     rev.AuthoredAt.UTC().Format(time.RFC3339Nano),
			"source_type":     string(rev.SourceType),
			"source_metadata": rev.SourceMetadata,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}{Items: items, Total: len(items)})
}

// -----------------------------------------------------------------------------
// Response shaping
// -----------------------------------------------------------------------------

// atomResponse projects the LearningAtom aggregate to the wire shape used
// across all Phyllis MVP endpoints. The current revision is embedded so
// callers don't need a follow-up GET.
func atomResponse(a *atom.LearningAtom) map[string]any {
	out := map[string]any{
		"atom_id":    a.AtomID,
		"tenant_id":  a.TenantID,
		"gcid":       a.Gcid,
		"course_id":  a.CourseID,
		"title":      a.Title,
		"atom_type":  string(a.QuestionType),
		"difficulty": a.Difficulty,
		"tags":       a.Tags,
		"status":     string(a.Status),
		"revision":   a.Revision,
		"created_at": a.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": a.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if a.DeletedAt != nil {
		out["deleted_at"] = a.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	if cur := a.CurrentRevision(); cur != nil {
		out["current_revision"] = map[string]any{
			"revision_id":     cur.RevisionID,
			"atom_id":         cur.AtomID,
			"revision_number": cur.RevisionNumber,
			"body":            cur.Body,
			"authored_by":     cur.AuthoredBy,
			"authored_at":     cur.AuthoredAt.UTC().Format(time.RFC3339Nano),
			"source_type":     string(cur.SourceType),
			"source_metadata": cur.SourceMetadata,
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// JSON decode helper variant — must be used; existing decodeJSON uses
// DisallowUnknownFields which would reject Phyllis-MVP-style payloads with
// optional fields. We keep the strict decoder for legacy /api/atoms but
// allow the Phyllis surface to be tolerant of additional fields by reusing
// the same helper here. (Currently identical; kept in case we diverge.)
// -----------------------------------------------------------------------------

var _ = json.NewDecoder // keep encoding/json imported
