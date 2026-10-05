// chunk_test.go — RED→GREEN coverage for the Lane 1c (CHO-1703 / ADR-180,
// D9) source-chunk domain: deterministic chunk construction from extracted
// pages + the ~2-4KB unpaged split. Domain gate ≥85%.
package sourcechunk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

const (
	tnt = "22222222-2222-7222-8222-222222222222"
	job = "01970000-0000-7000-8000-00000000c001"
	uri = "gs://bucket/tenants/t/jobs/j/source-1"
)

func intp(v int) *int { return &v }

func TestFromPages_PageGranularChunks(t *testing.T) {
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	pages := []sourcechunk.ExtractedPage{
		{PageNo: intp(1), Text: "Page one text about photosynthesis."},
		{PageNo: intp(2), Text: "Page two text about chlorophyll."},
	}

	chunks, err := sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: job, TenantID: tnt, FileURI: uri, FileRole: sourcechunk.RoleSource,
		Pages: pages, Now: now,
	})
	if err != nil {
		t.Fatalf("FromPages: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d; want 2 (one per page)", len(chunks))
	}
	for i, c := range chunks {
		if c.JobID != job || c.TenantID != tnt || c.FileURI != uri {
			t.Errorf("chunk[%d] identity fields wrong: %+v", i, c)
		}
		if c.FileRole != sourcechunk.RoleSource {
			t.Errorf("chunk[%d].FileRole = %q; want source", i, c.FileRole)
		}
		if c.ChunkIndex != i {
			t.Errorf("chunk[%d].ChunkIndex = %d; want %d", i, c.ChunkIndex, i)
		}
		if c.PageNo == nil || *c.PageNo != i+1 {
			t.Errorf("chunk[%d].PageNo = %v; want %d", i, c.PageNo, i+1)
		}
		if c.CreatedAt != now {
			t.Errorf("chunk[%d].CreatedAt = %v; want %v", i, c.CreatedAt, now)
		}
		// chunk_id is a parseable UUID (v7 when the runtime can mint it).
		if _, err := uuid.Parse(c.ChunkID); err != nil {
			t.Errorf("chunk[%d].ChunkID = %q not a uuid: %v", i, c.ChunkID, err)
		}
	}
	if chunks[0].Text != "Page one text about photosynthesis." {
		t.Errorf("chunk[0].Text = %q", chunks[0].Text)
	}
}

func TestFromPages_SkipsBlankPages(t *testing.T) {
	chunks, err := sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: job, TenantID: tnt, FileURI: uri, FileRole: sourcechunk.RoleSource,
		Pages: []sourcechunk.ExtractedPage{
			{PageNo: intp(1), Text: "   \n\t  "},
			{PageNo: intp(2), Text: "Real content."},
		},
		Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("FromPages: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d; want 1 (blank page skipped)", len(chunks))
	}
	if chunks[0].PageNo == nil || *chunks[0].PageNo != 2 {
		t.Errorf("kept chunk PageNo = %v; want 2", chunks[0].PageNo)
	}
	// chunk_index stays dense (0-based) over EMITTED chunks.
	if chunks[0].ChunkIndex != 0 {
		t.Errorf("ChunkIndex = %d; want 0", chunks[0].ChunkIndex)
	}
}

func TestFromPages_ValidatesParams(t *testing.T) {
	_, err := sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: "", TenantID: tnt, FileURI: uri, FileRole: sourcechunk.RoleSource,
		Pages: []sourcechunk.ExtractedPage{{Text: "x"}}, Now: time.Now(),
	})
	if err == nil {
		t.Error("expected error for empty JobID")
	}
	_, err = sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: job, TenantID: "", FileURI: uri, FileRole: sourcechunk.RoleSource,
		Pages: []sourcechunk.ExtractedPage{{Text: "x"}}, Now: time.Now(),
	})
	if err == nil {
		t.Error("expected error for empty TenantID")
	}
	_, err = sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: job, TenantID: tnt, FileURI: "", FileRole: sourcechunk.RoleSource,
		Pages: []sourcechunk.ExtractedPage{{Text: "x"}}, Now: time.Now(),
	})
	if err == nil {
		t.Error("expected error for empty FileURI")
	}
	_, err = sourcechunk.FromPages(sourcechunk.FromPagesParams{
		JobID: job, TenantID: tnt, FileURI: uri, FileRole: "banana",
		Pages: []sourcechunk.ExtractedPage{{Text: "x"}}, Now: time.Now(),
	})
	if err == nil {
		t.Error("expected error for invalid FileRole")
	}
}

func TestSplitText_ShortTextSingleChunk(t *testing.T) {
	parts := sourcechunk.SplitText("Short paragraph.")
	if len(parts) != 1 || parts[0] != "Short paragraph." {
		t.Fatalf("SplitText short = %#v; want single verbatim part", parts)
	}
}

func TestSplitText_EmptyYieldsNone(t *testing.T) {
	if parts := sourcechunk.SplitText("   \n  "); len(parts) != 0 {
		t.Fatalf("SplitText blank = %#v; want empty", parts)
	}
}

func TestSplitText_SplitsAtParagraphBoundaries(t *testing.T) {
	para := strings.Repeat("alpha beta gamma delta. ", 60) // ~1.4KB
	text := para + "\n\n" + para + "\n\n" + para + "\n\n" + para
	parts := sourcechunk.SplitText(text)
	if len(parts) < 2 {
		t.Fatalf("len(parts) = %d; want ≥2 (~2-4KB splits over ~5.7KB input)", len(parts))
	}
	for i, p := range parts {
		if len(p) > sourcechunk.MaxChunkBytes {
			t.Errorf("part[%d] len %d exceeds MaxChunkBytes %d", i, len(p), sourcechunk.MaxChunkBytes)
		}
		if strings.TrimSpace(p) == "" {
			t.Errorf("part[%d] blank", i)
		}
	}
	// Joining the parts must preserve all non-whitespace content.
	joined := strings.Join(parts, " ")
	if sourcechunk.Normalise(joined) != sourcechunk.Normalise(text) {
		t.Error("split lost content (normalised join != normalised input)")
	}
}

func TestSplitText_HardSplitsOversizedParagraph(t *testing.T) {
	// One giant paragraph with no \n\n boundary — must still split ≤ max.
	text := strings.Repeat("loremipsum word ", 600) // ~9.6KB, no paragraph breaks
	parts := sourcechunk.SplitText(text)
	if len(parts) < 3 {
		t.Fatalf("len(parts) = %d; want ≥3 for ~9.6KB", len(parts))
	}
	for i, p := range parts {
		if len(p) > sourcechunk.MaxChunkBytes {
			t.Errorf("part[%d] len %d exceeds MaxChunkBytes", i, len(p))
		}
	}
}

func TestFromUnpagedText_SplitsAndIndexes(t *testing.T) {
	text := strings.Repeat("alpha beta gamma. ", 300) // ~5.4KB
	chunks, err := sourcechunk.FromUnpagedText(sourcechunk.FromTextParams{
		JobID: job, TenantID: tnt, FileURI: uri, FileRole: sourcechunk.RoleRubric,
		Text: text, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("FromUnpagedText: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d; want ≥2", len(chunks))
	}
	for i, c := range chunks {
		if c.PageNo != nil {
			t.Errorf("chunk[%d].PageNo = %v; want nil for unpaged", i, c.PageNo)
		}
		if c.ChunkIndex != i {
			t.Errorf("chunk[%d].ChunkIndex = %d; want %d", i, c.ChunkIndex, i)
		}
		if c.FileRole != sourcechunk.RoleRubric {
			t.Errorf("chunk[%d].FileRole = %q; want rubric", i, c.FileRole)
		}
	}
}

func TestNormalise_LowercasesAndCollapsesWhitespace(t *testing.T) {
	got := sourcechunk.Normalise("  The  QUICK\n\tbrown   Fox. ")
	want := "the quick brown fox."
	if got != want {
		t.Errorf("Normalise = %q; want %q", got, want)
	}
}
