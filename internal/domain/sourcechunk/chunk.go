// Package sourcechunk — Lane 1c (CHO-1703 / ADR-180, D9) source-material
// chunk domain for chora-creation.
//
// A Chunk is one deterministic, page-referenced text extraction artifact
// from a batch-job grounding file, persisted in
// chora_creation.source_material_chunks (migration 0017). Chunks serve two
// masters:
//
//  1. The D15 citation-verification pass — the crew model-reports
//     {source_file, page, excerpt} per generated question; chora-creation
//     matches each excerpt against the job's chunks (see verify.go) and
//     stamps `verified` + `chunk_id` into candidate_questions_jsonb BEFORE
//     the FE poll surfaces candidates.
//  2. The reuse seam (owner strategy) — the chunk rows are embeddings-ready
//     (`embedding vector NULL`) for later KG / growth-edge / daily-dose
//     grounding via the existing ContentRetrieval.SearchEmbeddings gRPC.
//     v1 computes NO embeddings.
//
// Chunking policy v1 (D9, deliberately simple + citable):
//   - Paged formats (PDF): ONE chunk per extracted page (page_no 1-based).
//   - Unpaged formats (DOCX/MD/TXT): ~2-4KB splits at paragraph/line/space
//     boundaries (page_no NULL).
//   - Images: NO chunks (citations on image sources stay "AI-reported").
//
// Hexagonal: pure domain — stdlib + uuid only. Extraction (PDF/DOCX byte
// parsing) lives in internal/adapter/extraction; persistence in
// internal/adapter/pg behind ports.SourceChunkRepository.
package sourcechunk

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// File roles — mirror SourceFileRef.role (chora-contracts ai_assist.proto)
// + the migration 0017 CHECK constraint.
const (
	RoleSource = "source"
	RoleRubric = "rubric"
)

// Chunking byte targets (v1). MaxChunkBytes is a hard per-chunk ceiling;
// TargetChunkBytes is where the splitter aims before seeking a boundary.
const (
	TargetChunkBytes = 2048
	MaxChunkBytes    = 4096
)

// Chunk maps 1:1 onto a source_material_chunks row (migration 0017).
type Chunk struct {
	ChunkID    string    `json:"chunk_id"`
	JobID      string    `json:"job_id"`
	TenantID   string    `json:"tenant_id"`
	FileURI    string    `json:"file_uri"`
	FileRole   string    `json:"file_role"`
	ChunkIndex int       `json:"chunk_index"`
	PageNo     *int      `json:"page_no,omitempty"` // 1-based; nil = unpaged
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

// ExtractedPage is the extraction adapter's output unit for paged formats.
// PageNo nil means the extractor could not attribute a page (treated as
// unpaged content).
type ExtractedPage struct {
	PageNo *int
	Text   string
}

// FromPagesParams parameterises FromPages.
type FromPagesParams struct {
	JobID    string
	TenantID string
	FileURI  string
	FileRole string
	Pages    []ExtractedPage
	Now      time.Time
}

// FromTextParams parameterises FromUnpagedText.
type FromTextParams struct {
	JobID    string
	TenantID string
	FileURI  string
	FileRole string
	Text     string
	Now      time.Time
}

func validRole(role string) bool { return role == RoleSource || role == RoleRubric }

func validateIdentity(jobID, tenantID, fileURI, fileRole string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("sourcechunk: JobID required")
	}
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("sourcechunk: TenantID required")
	}
	if strings.TrimSpace(fileURI) == "" {
		return errors.New("sourcechunk: FileURI required")
	}
	if !validRole(fileRole) {
		return fmt.Errorf("sourcechunk: FileRole %q invalid (want %s|%s)", fileRole, RoleSource, RoleRubric)
	}
	return nil
}

// FromPages builds page-granular chunks (one chunk per non-blank page,
// chunk_index dense 0-based over EMITTED chunks). The v1 policy keeps a
// page whole even when large — page boundaries are the citation unit the
// crew is prompted to report.
func FromPages(p FromPagesParams) ([]Chunk, error) {
	if err := validateIdentity(p.JobID, p.TenantID, p.FileURI, p.FileRole); err != nil {
		return nil, err
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]Chunk, 0, len(p.Pages))
	for _, page := range p.Pages {
		text := strings.TrimSpace(page.Text)
		if text == "" {
			continue
		}
		out = append(out, Chunk{
			ChunkID:    newChunkID(),
			JobID:      p.JobID,
			TenantID:   p.TenantID,
			FileURI:    p.FileURI,
			FileRole:   p.FileRole,
			ChunkIndex: len(out),
			PageNo:     copyIntPtr(page.PageNo),
			Text:       text,
			CreatedAt:  now,
		})
	}
	return out, nil
}

// FromUnpagedText builds ~2-4KB split chunks for unpaged formats
// (DOCX/MD/TXT). page_no stays nil.
func FromUnpagedText(p FromTextParams) ([]Chunk, error) {
	if err := validateIdentity(p.JobID, p.TenantID, p.FileURI, p.FileRole); err != nil {
		return nil, err
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	parts := SplitText(p.Text)
	out := make([]Chunk, 0, len(parts))
	for i, part := range parts {
		out = append(out, Chunk{
			ChunkID:    newChunkID(),
			JobID:      p.JobID,
			TenantID:   p.TenantID,
			FileURI:    p.FileURI,
			FileRole:   p.FileRole,
			ChunkIndex: i,
			Text:       part,
			CreatedAt:  now,
		})
	}
	return out, nil
}

// SplitText splits text into ~TargetChunkBytes pieces never exceeding
// MaxChunkBytes, preferring paragraph (\n\n) → line (\n) → space boundaries.
// Deterministic; blank input yields no parts.
func SplitText(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) <= MaxChunkBytes {
		return []string{trimmed}
	}

	var parts []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			parts = append(parts, s)
		}
		b.Reset()
	}

	for _, para := range strings.Split(trimmed, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Oversized paragraph: hard-split at line/space boundaries first.
		for _, piece := range splitOversized(para) {
			if b.Len() > 0 && b.Len()+1+len(piece) > TargetChunkBytes {
				flush()
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(piece)
			if b.Len() >= TargetChunkBytes {
				flush()
			}
		}
	}
	flush()
	return parts
}

// splitOversized breaks one paragraph into ≤TargetChunkBytes pieces at
// line then space boundaries; degenerate unbroken runs hard-split at the
// byte boundary.
func splitOversized(para string) []string {
	if len(para) <= TargetChunkBytes {
		return []string{para}
	}
	var pieces []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			pieces = append(pieces, s)
		}
		b.Reset()
	}
	fields := strings.FieldsFunc(para, func(r rune) bool { return r == '\n' })
	for _, line := range fields {
		for _, word := range strings.Fields(line) {
			// Degenerate single token longer than the target: hard-split.
			for len(word) > TargetChunkBytes {
				flush()
				pieces = append(pieces, word[:TargetChunkBytes])
				word = word[TargetChunkBytes:]
			}
			if b.Len() > 0 && b.Len()+1+len(word) > TargetChunkBytes {
				flush()
			}
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString(word)
		}
	}
	flush()
	return pieces
}

// Normalise lowercases + collapses every whitespace run to a single space,
// trimming the ends. The shared canonical form for citation matching.
func Normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true // leading whitespace trimmed
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		lastSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

func newChunkID() string {
	return uuid.Must(uuid.NewV7()).String()
}

func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
