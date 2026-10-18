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
// instead: tenant- and job-scoped, bounded, and never a cross-DB query
// (ddd-enforcement #3). Chunks are the same rows the D15 citation-verification
// pass reads, so grounding and citation attribution see one document.
//
// SECURITY MODEL — read this before trusting the name "internal"
//
// There is NO service authentication here. middleware.isPublicPath treats every
// /api/internal/* request as cluster-internal, so the X-Tenant-Id / gcid header
// gate is bypassed and the tenant arrives in the query string. That gives
// TENANT SCOPING, not caller identity: any workload able to reach creation's
// internal port can name another tenant.
//
// What this handler does add is object-level consistency: when jobRepo is wired
// it validates that the (tenant, job) pair actually exists before reading any
// text, and rejects a mismatched author_gcid when the caller supplies one. That
// is defence in depth against a wrong or forged job id — it is NOT
// authentication, and must not be described as such. Real service identity has
// to come from the transport, not from a query parameter.
//
// Route: GET /api/internal/question-jobs/{job_id}/source-material
//   ?tenant_id=<required>&author_gcid=<optional>&max_bytes=<optional>

package httpadapter

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

const (
	// sourceMaterialDefaultMaxBytes bounds the response when the caller does
	// not ask for a budget. Approximately 30k tokens of English — a plausible
	// recovery default, NOT a number derived from any model's context window.
	sourceMaterialDefaultMaxBytes = 120_000

	// sourceMaterialMaxBytesCeiling clamps an over-large request. The caller is
	// never the only bound: creation must not return an arbitrarily large
	// extracted document merely because another service asked for one.
	sourceMaterialMaxBytesCeiling = 400_000

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

// sourceMaterialResponse is the GET body.
//
// ChunksComplete is deliberately NOT called "complete". It means ONLY that
// every chunk that exists in source_material_chunks fitted inside this
// response's byte budget. It does NOT mean the source material is fully
// represented: an image-only upload has no text layer by design, and a PDF
// whose extraction failed stores nothing at all (extraction never fails the
// job). A caller that reads ChunksComplete as "I have the whole corpus" is
// making an error this field cannot detect.
//
// Chunk ORDER is deterministic storage order ((file_uri, chunk_index) in the
// repository), NOT semantic priority. A rubric can therefore sort before the
// material it grades. Callers that need role priority must apply it
// themselves; do not infer importance from array position.
type sourceMaterialResponse struct {
	Chunks         []sourceMaterialChunk `json:"chunks"`
	ChunksComplete bool                  `json:"chunks_complete"`
}

// getSourceMaterial serves GET
// /api/internal/question-jobs/{job_id}/source-material.
//
// An EMPTY result is a success, not an error: image-only batches have no text
// layer by design, and a job whose extraction failed stores nothing.
func (h *QuestionJobsHandler) getSourceMaterial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "CREATION_METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	q := r.URL.Query()
	tenantID := strings.TrimSpace(q.Get("tenant_id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED", "tenant_id required")
		return
	}

	jobID, ok := jobIDFromSourceMaterialPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "not found")
		return
	}

	maxBytes, err := sourceMaterialMaxBytes(q.Get("max_bytes"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_BAD_MAX_BYTES", "max_bytes must be an integer")
		return
	}

	// Object-level consistency. A random (tenant, job) pair must not look
	// identical to a legitimate job that simply has no extractable text.
	if h.jobRepo != nil {
		job, err := h.jobRepo.Get(r.Context(), tenantID, jobID)
		if err != nil {
			if errors.Is(err, question.ErrNotFound) {
				writeError(w, http.StatusNotFound, "CREATION_JOB_NOT_FOUND", "job not found")
				return
			}
			log.Printf("source-material: job lookup tenant=%s job=%s: %v", tenantID, jobID, err)
			writeError(w, http.StatusInternalServerError, "CREATION_JOB_READ_FAILED", "failed to load job")
			return
		}
		if authorGCID := strings.TrimSpace(q.Get("author_gcid")); authorGCID != "" && job.AuthorGCID != authorGCID {
			writeError(w, http.StatusForbidden, "CREATION_JOB_NOT_OWNED", "job does not belong to this author")
			return
		}
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

	writeJSON(w, http.StatusOK, buildSourceMaterialResponse(chunks, maxBytes))
}

// buildSourceMaterialResponse applies the byte budget in chunk order.
//
// The LAST chunk is included PARTIALLY when it does not fit, rather than being
// dropped whole. Dropping it was a real bug: a paged PDF keeps every page as
// one chunk with no size cap (FromPages "keeps a page whole even when large";
// only FromUnpagedText splits at MaxChunkBytes), so one oversized page could
// exhaust the budget on the very first iteration and return NO grounding text
// for a perfectly valid document.
//
// A prefix of page 17 is still text from page 17 — the attribution this
// endpoint preserves stays truthful — and the D15 verifier compares generated
// excerpts against the FULL stored chunk, so grounding from a prefix does not
// weaken later citation checks.
func buildSourceMaterialResponse(chunks []sourcechunk.Chunk, maxBytes int) sourceMaterialResponse {
	out := sourceMaterialResponse{
		Chunks:         make([]sourceMaterialChunk, 0, len(chunks)),
		ChunksComplete: true,
	}
	used := 0
	for _, c := range chunks {
		remaining := maxBytes - used
		if remaining <= 0 {
			out.ChunksComplete = false
			break
		}
		text := c.Text
		partial := len(text) > remaining
		if partial {
			// Never split a rune: a half-encoded rune would emit invalid UTF-8
			// into the JSON body.
			text = trimToRuneBoundary(text[:remaining])
		}
		used += len(text)
		out.Chunks = append(out.Chunks, sourceMaterialChunk{
			FileURI:    c.FileURI,
			FileRole:   c.FileRole,
			ChunkIndex: c.ChunkIndex,
			PageNo:     c.PageNo,
			Text:       text,
		})
		if partial {
			out.ChunksComplete = false
			break
		}
	}
	return out
}

// trimToRuneBoundary drops trailing bytes that do not form a complete rune.
func trimToRuneBoundary(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}

// sourceMaterialMaxBytes resolves the budget: the caller's value when valid,
// otherwise the default, always clamped to the ceiling.
func sourceMaterialMaxBytes(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sourceMaterialDefaultMaxBytes, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return sourceMaterialDefaultMaxBytes, nil
	}
	if n > sourceMaterialMaxBytesCeiling {
		return sourceMaterialMaxBytesCeiling, nil
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
