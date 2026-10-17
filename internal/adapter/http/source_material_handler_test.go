// source_material_handler_test.go — the grounding read seam.
//
// The behaviour under test is what a generator depends on to ground on TEXT:
// tenant+job scoping, page attribution that survives to the wire, and an
// honest `complete` flag when the char budget truncates the document.

package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

func pageNo(n int) *int { return &n }

// stubChunkRepo is a minimal ports.SourceChunkRepository. This package's
// _test-package fake lives next door and is not visible here; a local stub
// keeps this a true unit test of the handler.
type stubChunkRepo struct {
	chunks  []sourcechunk.Chunk
	listErr error
}

func (s *stubChunkRepo) InsertChunks(_ context.Context, _ []sourcechunk.Chunk) error {
	return nil
}

func (s *stubChunkRepo) ListByJob(_ context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []sourcechunk.Chunk
	for _, c := range s.chunks {
		if c.TenantID == tenantID && c.JobID == jobID {
			out = append(out, c)
		}
	}
	return out, nil
}

// sourceMaterialHandler builds just enough handler to serve the route.
func sourceMaterialHandler(repo *stubChunkRepo) *QuestionJobsHandler {
	h := &QuestionJobsHandler{}
	if repo != nil {
		h.chunks = repo
	}
	return h
}

// get issues the request and returns the recorder.
func get(t *testing.T, h *QuestionJobsHandler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.getSourceMaterial(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) sourceMaterialResponse {
	t.Helper()
	var out sourceMaterialResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestGetSourceMaterial_PagedChunksKeepPageAndRole(t *testing.T) {
	repo := &stubChunkRepo{chunks: []sourcechunk.Chunk{
		{JobID: "job-1", TenantID: "t-1", FileURI: "s3://b/paper.pdf", FileRole: "source", ChunkIndex: 0, PageNo: pageNo(1), Text: "Page one text."},
		{JobID: "job-1", TenantID: "t-1", FileURI: "s3://b/marks.pdf", FileRole: "rubric", ChunkIndex: 0, PageNo: pageNo(3), Text: "Mark scheme text."},
	}}
	rec := get(t, sourceMaterialHandler(repo), "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if !body.Complete {
		t.Error("complete = false; want true (no budget pressure)")
	}
	if len(body.Chunks) != 2 {
		t.Fatalf("chunks = %d; want 2", len(body.Chunks))
	}
	if body.Chunks[0].PageNo == nil || *body.Chunks[0].PageNo != 1 {
		t.Errorf("chunk[0].PageNo = %v; want 1", body.Chunks[0].PageNo)
	}
	// The rubric is a DISTINCT role: a mark scheme grades the material, it is
	// not question material. Callers must be able to tell them apart.
	if body.Chunks[1].FileRole != "rubric" {
		t.Errorf("chunk[1].FileRole = %q; want rubric", body.Chunks[1].FileRole)
	}
	if body.Chunks[0].FileURI != "s3://b/paper.pdf" {
		t.Errorf("chunk[0].FileURI = %q; want the recorded object URI", body.Chunks[0].FileURI)
	}
}

func TestGetSourceMaterial_TenantScopingHidesOtherTenants(t *testing.T) {
	repo := &stubChunkRepo{chunks: []sourcechunk.Chunk{
		{JobID: "job-1", TenantID: "t-2", FileURI: "f", FileRole: "source", Text: "someone else's material"},
	}}
	rec := get(t, sourceMaterialHandler(repo), "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if body := decodeBody(t, rec); len(body.Chunks) != 0 {
		t.Fatalf("chunks = %d; want 0 — a tenant must not read another tenant's job", len(body.Chunks))
	}
}

func TestGetSourceMaterial_UnpagedOmitsPageNo(t *testing.T) {
	repo := &stubChunkRepo{chunks: []sourcechunk.Chunk{
		{JobID: "job-2", TenantID: "t-1", FileURI: "s3://b/notes.md", FileRole: "source", ChunkIndex: 0, Text: "Unpaged text."},
	}}
	rec := get(t, sourceMaterialHandler(repo), "/api/internal/question-jobs/job-2/source-material?tenant_id=t-1")

	var raw struct {
		Chunks []map[string]any `json:"chunks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Chunks) != 1 {
		t.Fatalf("chunks = %d; want 1", len(raw.Chunks))
	}
	// A zero page number would be a lie dressed as data: unpaged must be absent.
	if _, present := raw.Chunks[0]["page_no"]; present {
		t.Errorf("page_no present for an unpaged chunk: %v", raw.Chunks[0]["page_no"])
	}
}

func TestGetSourceMaterial_TruncationStopsBeforeCrossingChunk(t *testing.T) {
	// Two 10-char chunks; a 15-char budget fits the first only.
	repo := &stubChunkRepo{chunks: []sourcechunk.Chunk{
		{JobID: "job-3", TenantID: "t-1", FileURI: "f", FileRole: "source", ChunkIndex: 0, PageNo: pageNo(1), Text: "0123456789"},
		{JobID: "job-3", TenantID: "t-1", FileURI: "f", FileRole: "source", ChunkIndex: 1, PageNo: pageNo(2), Text: "abcdefghij"},
	}}
	rec := get(t, sourceMaterialHandler(repo), "/api/internal/question-jobs/job-3/source-material?tenant_id=t-1&max_chars=15")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := decodeBody(t, rec)
	if body.Complete {
		t.Error("complete = true; want false — the budget truncated the document")
	}
	if len(body.Chunks) != 1 {
		t.Fatalf("chunks = %d; want 1 (must not cut a chunk in half)", len(body.Chunks))
	}
	if body.Chunks[0].Text != "0123456789" {
		t.Errorf("chunk text = %q; want the intact first chunk", body.Chunks[0].Text)
	}
}

func TestGetSourceMaterial_RequiresTenant(t *testing.T) {
	rec := get(t, sourceMaterialHandler(&stubChunkRepo{}), "/api/internal/question-jobs/job-1/source-material")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 without tenant_id", rec.Code)
	}
}

func TestGetSourceMaterial_UnknownPathIs404(t *testing.T) {
	h := sourceMaterialHandler(&stubChunkRepo{})
	for _, p := range []string{
		"/api/internal/question-jobs/job-1/other-thing",
		"/api/internal/question-jobs//source-material",
		"/api/internal/question-jobs/job-1/extra/source-material",
	} {
		rec := get(t, h, p+"?tenant_id=t-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d; want 404", p, rec.Code)
		}
	}
}

func TestGetSourceMaterial_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	sourceMaterialHandler(&stubChunkRepo{}).getSourceMaterial(rec,
		httptest.NewRequest(http.MethodPost, "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("Allow = %q; want GET", rec.Header().Get("Allow"))
	}
}

func TestGetSourceMaterial_StoreErrorIs500Not404(t *testing.T) {
	repo := &stubChunkRepo{listErr: errors.New("db down")}
	rec := get(t, sourceMaterialHandler(repo), "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1")
	// Distinguishable from "this job has no text": an operator must be able to
	// tell a broken store from an image-only batch.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rec.Code)
	}
}

func TestGetSourceMaterial_UnwiredStoreIs503(t *testing.T) {
	rec := get(t, sourceMaterialHandler(nil), "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503 when the chunk store is unwired", rec.Code)
	}
}

func TestGetSourceMaterial_EmptyIsSuccess(t *testing.T) {
	// Image-only batches legitimately have no text layer, and a job whose
	// extraction failed stores nothing. Neither is an error.
	rec := get(t, sourceMaterialHandler(&stubChunkRepo{}), "/api/internal/question-jobs/job-x/source-material?tenant_id=t-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := decodeBody(t, rec)
	if !body.Complete {
		t.Error("complete = false; want true for a legitimately empty job")
	}
	if len(body.Chunks) != 0 {
		t.Errorf("chunks = %d; want 0", len(body.Chunks))
	}
	if got := rec.Body.String(); !strings.Contains(got, `"chunks":[]`) {
		t.Errorf("body %q; want an empty JSON array, not null", got)
	}
}

func TestGetSourceMaterial_BadMaxCharsIs400(t *testing.T) {
	rec := get(t, sourceMaterialHandler(&stubChunkRepo{}), "/api/internal/question-jobs/job-1/source-material?tenant_id=t-1&max_chars=lots")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

func TestSourceMaterialMaxChars_DefaultCeilingAndFloor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", sourceMaterialDefaultMaxChars},
		{"0", sourceMaterialDefaultMaxChars},
		{"-5", sourceMaterialDefaultMaxChars},
		{"500", 500},
		{"999999999", sourceMaterialMaxCharsCeiling},
	}
	for _, c := range cases {
		got, err := sourceMaterialMaxChars(c.in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("%q: got %d; want %d", c.in, got, c.want)
		}
	}
	if _, err := sourceMaterialMaxChars("abc"); err == nil {
		t.Error("expected an error for a non-integer budget")
	}
}
