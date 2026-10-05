// Package extraction — Lane 1c (CHO-1703 / ADR-180, D9) deterministic
// in-service text extraction for batch-job grounding files.
//
// chora-creation extracts text-bearing uploads into page-referenced chunks
// AT JOB CREATION (the chunk store feeds the D15 citation-verification pass
// + the KG/daily-dose reuse seam). There is NO separate doc-parser service
// and NO LLM call here — extraction is pure deterministic parsing:
//
//   - PDF  — pure-Go github.com/ledongthuc/pdf, ONE page per ExtractedPage
//     (1-based PageNo; the citation unit the crew reports against).
//   - DOCX — minimal `w:t` XML walk of word/document.xml inside the zip
//     (paragraph ends surface as newlines so the domain splitter can prefer
//     them). Unpaged.
//   - MD / TXT — direct bytes. Unpaged.
//   - Images (png/jpeg/webp) — NOT text-bearing: no chunks in v1; their
//     citations stay "AI-reported" (verified == nil).
//
// Failure posture: extraction failure must NOT fail the batch job (the
// crew still grounds multimodally on the raw blob). Callers log + continue;
// this package converts library panics into errors so a hostile upload
// can't crash the pod.
package extraction

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

// ErrUnsupportedMIME is returned for MIMEs with no text layer to extract
// (images in v1) or unknown types.
var ErrUnsupportedMIME = errors.New("extraction: unsupported mime (no text layer)")

// MIME constants matching the batch-upload allowlist.
const (
	mimePDF      = "application/pdf"
	mimeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeMarkdown = "text/markdown"
	mimePlain    = "text/plain"
)

// TextBearing reports whether the MIME carries an extractable text layer.
// Images return false — they ground the crew multimodally but produce NO
// chunks (D9: image citations stay AI-reported).
func TextBearing(mime string) bool {
	switch mime {
	case mimePDF, mimeDOCX, mimeMarkdown, mimePlain:
		return true
	}
	return false
}

// ExtractPages extracts deterministic text from data per MIME.
//
// Returns (pages, paged, err):
//   - PDF: one ExtractedPage per page, PageNo 1-based, paged=true.
//   - DOCX/MD/TXT: a single unpaged ExtractedPage (PageNo nil), paged=false
//     — the domain's SplitText turns it into ~2-4KB chunks.
//   - other MIMEs: ErrUnsupportedMIME.
func ExtractPages(mime string, data []byte) (pages []sourcechunk.ExtractedPage, paged bool, err error) {
	switch mime {
	case mimePDF:
		pages, err = extractPDF(data)
		return pages, true, err
	case mimeDOCX:
		text, derr := extractDOCX(data)
		if derr != nil {
			return nil, false, derr
		}
		return []sourcechunk.ExtractedPage{{Text: text}}, false, nil
	case mimeMarkdown, mimePlain:
		return []sourcechunk.ExtractedPage{{Text: string(data)}}, false, nil
	default:
		return nil, false, fmt.Errorf("%w: %q", ErrUnsupportedMIME, mime)
	}
}

// extractPDF walks every page and collects its plain text. The pdf library
// panics on some malformed inputs — recovered into an error (fail-soft:
// the batch job continues without chunks for this file).
func extractPDF(data []byte) (pages []sourcechunk.ExtractedPage, err error) {
	defer func() {
		if r := recover(); r != nil {
			pages = nil
			err = fmt.Errorf("extraction: pdf parse panic: %v", r)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("extraction: pdf open: %w", err)
	}
	n := reader.NumPage()
	pages = make([]sourcechunk.ExtractedPage, 0, n)
	for i := 1; i <= n; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, perr := page.GetPlainText(nil)
		if perr != nil {
			// Per-page failure: skip the page, keep the rest (deterministic
			// best-effort; the verification matcher treats absent pages as
			// unverifiable for that page only).
			continue
		}
		no := i
		pages = append(pages, sourcechunk.ExtractedPage{PageNo: &no, Text: text})
	}
	return pages, nil
}

// extractDOCX opens the zip, finds word/document.xml and walks the XML
// stream collecting `w:t` character data. Paragraph ends (`w:p`) and line
// breaks (`w:br`) emit newlines; tabs (`w:tab`) emit a space.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("extraction: docx zip open: %w", err)
	}
	var docXML io.ReadCloser
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, oerr := f.Open()
			if oerr != nil {
				return "", fmt.Errorf("extraction: docx document.xml open: %w", oerr)
			}
			docXML = rc
			break
		}
	}
	if docXML == nil {
		return "", errors.New("extraction: docx has no word/document.xml")
	}
	defer docXML.Close()

	dec := xml.NewDecoder(docXML)
	var b strings.Builder
	inText := false
	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return "", fmt.Errorf("extraction: docx xml walk: %w", terr)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br":
				b.WriteString("\n")
			case "tab":
				b.WriteString(" ")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				b.Write([]byte(t))
			}
		}
	}
	return b.String(), nil
}
