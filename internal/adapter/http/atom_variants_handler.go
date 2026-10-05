// Package httpadapter — Atom Variants HTTP routes.
//
// Routes (one POST per variant kind, single-shot publish):
//
//	POST /atoms/{atom_id}/variant:flashcard
//	POST /atoms/{atom_id}/variant:code-editor
//	POST /atoms/{atom_id}/variant:drag-drop-matching
//	POST /atoms/{atom_id}/variant:multimedia
//
// On success each route persists the variant via inmem.VariantRepository
// (M11 stand-in; Cloud SQL adapter in M12) and publishes a single Pub/Sub
// event on topic `chora.creation.atom.variant.published.v1` with the
// chora.common.v1 envelope mandatory fields populated. Failures to persist
// return 500; failures to publish are logged but non-fatal — the persistent
// write has already committed and downstream subscribers can be reconciled
// via the M12 outbox pattern (per data-consistency skill).
package httpadapter

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

// variantTopic is the single Pub/Sub topic for all four variant kinds —
// `chora.{domain}.{aggregate}.{event_type}.v{N}` per pub-sub-topology.
const variantTopic = "chora.creation.atom.variant.published.v1"

// VariantHandler holds the dependencies for variant authoring routes.
type VariantHandler struct {
	repo  *inmem.VariantRepository
	pub   EventPublisher
	clock func() time.Time // injectable for deterministic tests
}

// NewAtomVariantsRouter wires the atom-variants mux. Returns an http.Handler
// composed with logging + tenantContext middleware so callers receive the
// same header-enforcement behaviour as the rest of the chora-creation
// HTTP surface.
func NewAtomVariantsRouter(repo *inmem.VariantRepository, pub EventPublisher) http.Handler {
	h := &VariantHandler{repo: repo, pub: pub, clock: func() time.Time { return time.Now().UTC() }}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/healthz/", healthz)
	mux.HandleFunc("/health", healthz)

	// Single dispatcher under /atoms/ — net/http's ServeMux does not
	// support `:` path-segment patterns, so we sub-parse the trailing
	// `variant:{kind}` token in dispatchVariant.
	mux.HandleFunc("/atoms/", h.dispatchVariant)

	return logging(tenantContext(mux))
}

// dispatchVariant parses /atoms/{atom_id}/variant:{kind} and routes to the
// per-variant decoder. Anything that isn't `variant:{kind}` is 404.
func (h *VariantHandler) dispatchVariant(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/atoms/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}
	atomID := parts[0]
	subResource := parts[1]
	if !strings.HasPrefix(subResource, "variant:") {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}
	kind := strings.TrimPrefix(subResource, "variant:")
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /atoms/{id}/variant:{kind}")
		return
	}
	switch av.VariantType(kind) {
	case av.VariantTypeFlashcard:
		h.createFlashcard(w, r, atomID)
	case av.VariantTypeCodeEditor:
		h.createCodeEditor(w, r, atomID)
	case av.VariantTypeDragDrop:
		h.createDragDrop(w, r, atomID)
	case av.VariantTypeMultimedia:
		h.createMultimedia(w, r, atomID)
	default:
		writeError(w, http.StatusNotFound, "CREATION_VARIANT_UNKNOWN",
			"unknown variant kind: "+kind)
	}
}

// -----------------------------------------------------------------------------
// Flashcard
// -----------------------------------------------------------------------------

type flashcardRequest struct {
	Front string `json:"front"`
	Back  string `json:"back"`
}

func (h *VariantHandler) createFlashcard(w http.ResponseWriter, r *http.Request, atomID string) {
	var req flashcardRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	v, err := av.NewFlashcard(av.FlashcardParams{
		AtomID: atomID, Front: req.Front, Back: req.Back,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_VARIANT", err.Error())
		return
	}
	h.persistAndPublish(w, r, v)
}

// -----------------------------------------------------------------------------
// Code editor
// -----------------------------------------------------------------------------

type codeEditorRequest struct {
	Language    string            `json:"language"`
	StarterCode string            `json:"starter_code"`
	TestCases   []av.CodeTestCase `json:"test_cases"`
	Solution    string            `json:"solution"`
}

func (h *VariantHandler) createCodeEditor(w http.ResponseWriter, r *http.Request, atomID string) {
	var req codeEditorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	v, err := av.NewCodeEditor(av.CodeEditorParams{
		AtomID: atomID, Language: req.Language, StarterCode: req.StarterCode,
		TestCases: req.TestCases, Solution: req.Solution,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_VARIANT", err.Error())
		return
	}
	h.persistAndPublish(w, r, v)
}

// -----------------------------------------------------------------------------
// Drag-drop matching
// -----------------------------------------------------------------------------

type dragDropRequest struct {
	LeftItems  []av.DragDropItem    `json:"left_items"`
	RightItems []av.DragDropItem    `json:"right_items"`
	Mappings   []av.DragDropMapping `json:"mappings"`
}

func (h *VariantHandler) createDragDrop(w http.ResponseWriter, r *http.Request, atomID string) {
	var req dragDropRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	v, err := av.NewDragDrop(av.DragDropParams{
		AtomID: atomID, LeftItems: req.LeftItems, RightItems: req.RightItems,
		Mappings: req.Mappings,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_VARIANT", err.Error())
		return
	}
	h.persistAndPublish(w, r, v)
}

// -----------------------------------------------------------------------------
// Multimedia
// -----------------------------------------------------------------------------

type multimediaRequest struct {
	VideoURL      string `json:"video_url"`
	Question      string `json:"question"`
	CorrectAnswer string `json:"correct_answer"`
	ThumbnailURL  string `json:"thumbnail_url"`
}

func (h *VariantHandler) createMultimedia(w http.ResponseWriter, r *http.Request, atomID string) {
	var req multimediaRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	v, err := av.NewMultimedia(av.MultimediaParams{
		AtomID: atomID, VideoURL: req.VideoURL, Question: req.Question,
		CorrectAnswer: req.CorrectAnswer, ThumbnailURL: req.ThumbnailURL,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_VARIANT", err.Error())
		return
	}
	h.persistAndPublish(w, r, v)
}

// -----------------------------------------------------------------------------
// persistAndPublish — shared trailing path: save via the variant repo and
// publish one envelope on the variant topic. Save failure → 500. Publish
// failure → log only (per knowledge_graph_handler precedent).
// -----------------------------------------------------------------------------

func (h *VariantHandler) persistAndPublish(w http.ResponseWriter, r *http.Request, v av.Variant) {
	if err := h.repo.Save(r.Context(), v); err != nil {
		log.Printf("variant save error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist variant")
		return
	}
	if err := h.publishVariantPublished(r.Context(), v); err != nil {
		log.Printf("variant publish error: %v", err) // non-fatal
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *VariantHandler) publishVariantPublished(ctx context.Context, v av.Variant) error {
	if h.pub == nil {
		return nil
	}
	tenantID := tenantFromContext(ctx)
	gcid := gcidFromContext(ctx)
	now := h.clock()
	env := inmem.Envelope{
		EventID:        mustUUIDv7(),
		IdempotencyKey: variantIdempotencyKey(v),
		TenantID:       tenantID,
		Gcid:           gcid,
		OccurredAt:     v.GetPublishedAt(),
		PublishedAt:    now,
		Traceparent:    traceparentFromContext(ctx),
		SourceProject:  "chora-content",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
	return h.pub.Publish(ctx, variantTopic, env, v)
}

// variantIdempotencyKey returns the per-variant dedup key used by Pub/Sub
// at-least-once subscribers. We key on VariantID where the concrete type
// exposes one; otherwise fall back to atom-id + variant-type.
func variantIdempotencyKey(v av.Variant) string {
	switch typed := v.(type) {
	case *av.Flashcard:
		return typed.VariantID
	case *av.CodeEditor:
		return typed.VariantID
	case *av.DragDrop:
		return typed.VariantID
	case *av.Multimedia:
		return typed.VariantID
	}
	return v.GetAtomID() + ":" + string(v.Type())
}
