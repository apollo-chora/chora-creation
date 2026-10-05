// extractor_test.go — RED→GREEN coverage for the Lane 1c (D9) deterministic
// in-service text extraction adapter: PDF (pure-Go ledongthuc/pdf, page-
// granular), DOCX (minimal w:t XML walk of word/document.xml), MD/TXT
// (direct). Images are NOT text-bearing (no chunks; citations stay
// AI-reported). Adapter gate ≥60%.
package extraction_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/extraction"
)

// -----------------------------------------------------------------------------
// Fixture builders
// -----------------------------------------------------------------------------

// buildTwoPagePDF hand-assembles a minimal valid PDF 1.4 with two pages of
// Helvetica text + a correct xref table (offsets computed, entries exactly
// 20 bytes) so the pure-Go reader parses it like a real exam paper.
func buildTwoPagePDF(page1, page2 string) []byte {
	stream1 := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", page1)
	stream2 := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", page2)

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream1), stream1),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream2), stream2),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1) // offsets[i] = object i's byte offset (1-based)
	for i, obj := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefStart)
	return buf.Bytes()
}

// buildDOCX zips a minimal word/document.xml with the supplied paragraphs.
func buildDOCX(paragraphs ...string) []byte {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t>` + p + `</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A token [Content_Types].xml keeps the archive shape honest.
	ct, _ := zw.Create("[Content_Types].xml")
	_, _ = ct.Write([]byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`))
	doc, _ := zw.Create("word/document.xml")
	_, _ = doc.Write([]byte(body.String()))
	_ = zw.Close()
	return buf.Bytes()
}

// -----------------------------------------------------------------------------
// TextBearing
// -----------------------------------------------------------------------------

func TestTextBearing(t *testing.T) {
	for mime, want := range map[string]bool{
		"application/pdf": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
		"text/markdown": true,
		"text/plain":    true,
		"image/png":     false,
		"image/jpeg":    false,
		"image/webp":    false,
		"video/mp4":     false,
	} {
		if got := extraction.TextBearing(mime); got != want {
			t.Errorf("TextBearing(%q) = %v; want %v", mime, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// PDF — page-granular
// -----------------------------------------------------------------------------

func TestExtractPages_PDF_PageGranular(t *testing.T) {
	data := buildTwoPagePDF(
		"Photosynthesis converts light energy.",
		"Chlorophyll absorbs red light.",
	)
	pages, paged, err := extraction.ExtractPages("application/pdf", data)
	if err != nil {
		t.Fatalf("ExtractPages(pdf): %v", err)
	}
	if !paged {
		t.Error("paged = false; want true for PDF")
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d; want 2", len(pages))
	}
	if pages[0].PageNo == nil || *pages[0].PageNo != 1 {
		t.Errorf("pages[0].PageNo = %v; want 1", pages[0].PageNo)
	}
	if pages[1].PageNo == nil || *pages[1].PageNo != 2 {
		t.Errorf("pages[1].PageNo = %v; want 2", pages[1].PageNo)
	}
	if !strings.Contains(pages[0].Text, "Photosynthesis") {
		t.Errorf("pages[0].Text = %q; want the page-1 sentence", pages[0].Text)
	}
	if !strings.Contains(pages[1].Text, "Chlorophyll") {
		t.Errorf("pages[1].Text = %q; want the page-2 sentence", pages[1].Text)
	}
}

func TestExtractPages_PDF_MalformedFailsSoft(t *testing.T) {
	_, _, err := extraction.ExtractPages("application/pdf", []byte("%PDF-1.4 garbage no xref"))
	if err == nil {
		t.Fatal("expected error for malformed PDF (extraction failure must surface as error, not panic)")
	}
}

// -----------------------------------------------------------------------------
// DOCX — minimal w:t walk
// -----------------------------------------------------------------------------

func TestExtractPages_DOCX_WTXMLWalk(t *testing.T) {
	data := buildDOCX("First paragraph about mitosis.", "Second paragraph about meiosis.")
	pages, paged, err := extraction.ExtractPages(
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document", data)
	if err != nil {
		t.Fatalf("ExtractPages(docx): %v", err)
	}
	if paged {
		t.Error("paged = true; want false for DOCX (unpaged splits)")
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d; want 1 unpaged blob", len(pages))
	}
	if pages[0].PageNo != nil {
		t.Errorf("PageNo = %v; want nil for DOCX", pages[0].PageNo)
	}
	if !strings.Contains(pages[0].Text, "mitosis") || !strings.Contains(pages[0].Text, "meiosis") {
		t.Errorf("Text = %q; want both paragraphs", pages[0].Text)
	}
	// Paragraph boundary surfaces as a newline so SplitText can prefer it.
	if !strings.Contains(pages[0].Text, "\n") {
		t.Errorf("Text = %q; want newline between paragraphs", pages[0].Text)
	}
}

func TestExtractPages_DOCX_NotAZipFailsSoft(t *testing.T) {
	_, _, err := extraction.ExtractPages(
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		[]byte("not a zip archive"))
	if err == nil {
		t.Fatal("expected error for non-zip DOCX bytes")
	}
}

// -----------------------------------------------------------------------------
// MD / TXT — direct
// -----------------------------------------------------------------------------

func TestExtractPages_MarkdownAndPlainText(t *testing.T) {
	for _, mime := range []string{"text/markdown", "text/plain"} {
		pages, paged, err := extraction.ExtractPages(mime, []byte("# Title\n\nBody text here."))
		if err != nil {
			t.Fatalf("ExtractPages(%s): %v", mime, err)
		}
		if paged {
			t.Errorf("%s: paged = true; want false", mime)
		}
		if len(pages) != 1 || pages[0].PageNo != nil {
			t.Fatalf("%s: pages = %+v; want 1 unpaged", mime, pages)
		}
		if !strings.Contains(pages[0].Text, "Body text here.") {
			t.Errorf("%s: Text = %q", mime, pages[0].Text)
		}
	}
}

// -----------------------------------------------------------------------------
// Unsupported MIME
// -----------------------------------------------------------------------------

func TestExtractPages_ImageUnsupported(t *testing.T) {
	_, _, err := extraction.ExtractPages("image/png", []byte{0x89, 0x50, 0x4E, 0x47})
	if err == nil {
		t.Fatal("expected ErrUnsupportedMIME for image/png (images produce NO chunks)")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("err = %v; want unsupported-mime error", err)
	}
}
