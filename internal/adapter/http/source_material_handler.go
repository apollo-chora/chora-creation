// source_material_handler.go — the cluster-internal read seam for a job's
// already-persisted source material.
//
// WHY THIS EXISTS
//
// A batch upload is parsed deterministically at job creation (ADR-180 D9,
// extraction.ExtractPages) into page-referenced chunks. The generator crew is
// supposed to ground on that source material, but the only grounding shape it
// ever had was a genai FileData part carrying the object-store URI "by
// reference" — a contract that assumed Vertex Gemini would dereference a gs://
// object itself. The Python model gateway's canonical contract admits only
// text | image | refusal parts, so the client refuses that part outright and
// every file-based generation fails before a model call.
//
// This endpoint is the sanctioned way for a caller to read the extracted TEXT
// instead: tenant-scoped, job-scoped, bounded, and never a cross-DB query
// (ddd-enforcement #3). Chunks are the same rows the D15 citation-verification
// pass reads, so grounding and citation attribution see one document.
//
// Route: GET /api/internal/question-jobs/{job_id}/source-material
//   ?tenant_id=<required>&max_chars=<optional>
//
// /api/internal/* bypasses the public tenant-header gate (middleware
// isPublicPath) because it is not routed through the public gateway; per that
// convention the handler itself REQUIRES the tenant and fails loud without it.

package httpadapter

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

const (
	// sourceMaterialDefaultMaxChars is the text budget when the caller does
	// not ask for one. Roughly 30k tokens — comfortable for a question
	// generator, far below any provider context limit.
	sourceMaterialDefaultMaxChars = 120_000

	// sourceMaterialMaxCharsCeiling clamps an over-large request. A caller
	// cannot ask this endpoint for an unbounded document: grounding a prompt
	// on a whole textbook would be silently expensive and mostly irrelevant.
	sourceMaterialMaxCharsCeiling = 400_000

	// sourceMaterialRoutePrefix is the registered prefix. The handler parses
	// the {job_id} segment out of the remainder.
	sourceMaterialRoutePrefix = "/api/internal/question-jobs/"

	// sourceMaterialRouteSuffix completes the path template.
	sourceMaterialRouteSuffix = "source-material"
)

// sourceMaterialChunk is the wire shape of one chunk. PageNo is omitted for
// unpaged formats (DOCX/MD/TXT) rather than sent as a misleading 0.
type sourceMaterialChunk struct {
	FileURI    string `json:"file_uri"`
	FileRole   string `json:"file_role"`
	ChunkIndex int    `json:"chunk_index"`
	PageNo     *int   `json:"page_no,omitempty"`
	Text       string `json:"text"`
}

// sourceMaterialResponse is the GET body. Complete=false means the char
// budget truncated the material; callers MUST surface that rather than
// pretend a partial document is the whole thing.
type sourceMaterialResponse struct {
	Chunks   []sourceMaterialChunk `json:"chunks"`
	Complete bool                  `json:"complete"`
}

// getSourceMaterial serves GET
// /api/internal/question-jobs/{job_id}/source-material.
//
// An EMPTY result is a success, not an error: image-only batches have no text
// layer by design, and a job whose extraction failed stores nothing
// (extraction never fails the job).
func (h *QuestionJobsHandler) getSourceMaterial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "CREATION_METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED", "tenant_id required")
		return
	}

	jobID, ok := jobIDFromSourceMaterialPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "not found")
		return
	}

	maxChars, err := sourceMaterialMaxChars(r.URL.Query().Get("max_chars"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_BAD_MAX_CHARS", "max_chars must be an integer")
		return
	}

	if h.chunks == nil {
		writeError(w, http.StatusServiceUnavailable,
			"CREATION_CHUNK_STORE_UNAVAILABLE", "source chunk store not wired")
		return
	}

	chunks, err := h.chunks.ListByJob(r.Context(), tenantID, jobID)
	if err != nil {
		// Not a 404: an unwired/broken store must be distinguishable from a
		// job that legitimately has no text.
		log.Printf("source-material: ListByJob tenant=%s job=%s: %v", tenantID, jobID, err)
		writeError(w, http.StatusInternalServerError, "CREATION_CHUNK_READ_FAILED", "failed to read source material")
		return
	}

	writeJSON(w, http.StatusOK, buildSourceMaterialResponse(chunks, maxChars))
}

// buildSourceMaterialResponse applies the char budget in chunk order.
//
// It stops BEFORE the chunk that would cross the budget rather than cutting a
// chunk in half: a half-chunk would carry a page attribution that no longer
// matches its text, and grounding on a truncated sentence is worse than
// grounding on one page fewer.
func buildSourceMaterialResponse(chunks []sourcechunk.Chunk, maxChars int) sourceMaterialResponse {
	out := sourceMaterialResponse{
		Chunks:   make([]sourceMaterialChunk, 0, len(chunks)),
		Complete: true,
	}
	used := 0
	for _, c := range chunks {
		if used+len(c.Text) > maxChars {
			out.Complete = false
			break
		}
		used += len(c.Text)
		out.Chunks = append(out.Chunks, sourceMaterialChunk{
			FileURI:    c.FileURI,
			FileRole:   c.FileRole,
			ChunkIndex: c.ChunkIndex,
			PageNo:     c.PageNo,
			Text:       c.Text,
		})
	}
	return out
}

// sourceMaterialMaxChars resolves the budget: the caller's value when valid,
// otherwise the default, always clamped to the ceiling.
func sourceMaterialMaxChars(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sourceMaterialDefaultMaxChars, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return sourceMaterialDefaultMaxChars, nil
	}
	if n > sourceMaterialMaxCharsCeiling {
		return sourceMaterialMaxCharsCeiling, nil
	}
	return n, nil
}

// jobIDFromSourceMaterialPath extracts {job_id} from
// /api/internal/question-jobs/{job_id}/source-material. Any other shape is
// NOT FOUND so a typo cannot silently read the wrong job.
func jobIDFromSourceMaterialPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, sourceMaterialRoutePrefix)
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != sourceMaterialRouteSuffix {
		return "", false
	}
	jobID := strings.TrimSpace(parts[0])
	if jobID == "" {
		return "", false
	}
	return jobID, true
}
