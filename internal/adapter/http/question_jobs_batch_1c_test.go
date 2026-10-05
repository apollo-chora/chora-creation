// question_jobs_batch_1c_test.go — RED→GREEN coverage for the Lane 1c
// (CHO-1703 / ADR-180 D7+D9) multi-file batch upload path:
//
//   - multipart gains `files[]` (1..5 role=source) + optional `rubric_file`
//     (≤1 role=rubric); legacy `file` kept (exactly one of file|files).
//   - image MIMEs (png/jpeg/webp) join the allowlist.
//   - per-file GCS upload (slot-keyed so files never overwrite).
//   - file roles persist in the job settings JSONB + ride the
//     generation_requested settings_json.
//   - deterministic extraction at job creation persists chunks for
//     text-bearing files (images → none); extraction/chunk failure must
//     NOT fail the job.
//   - GET poll surfaces proposed_test_set when stamped.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------------

// fakeMultiBlobStore records EVERY upload (the single-shot fakeBlobStore
// keeps only the last request).
type fakeMultiBlobStore struct {
	mu        sync.Mutex
	reqs      []ports.UploadBlobReq
	failAtN   int // 1-based upload ordinal to fail at; 0 = never
	uploadErr error
}

func (f *fakeMultiBlobStore) Upload(_ context.Context, req ports.UploadBlobReq) (ports.UploadBlobResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	f.reqs = append(f.reqs, req)
	if f.failAtN > 0 && len(f.reqs) == f.failAtN {
		err := f.uploadErr
		if err == nil {
			err = errors.New("forced upload failure")
		}
		return ports.UploadBlobResp{}, err
	}
	slot := req.Slot
	if slot == "" {
		slot = "source"
	}
	return ports.UploadBlobResp{
		BlobURI: fmt.Sprintf("gs://test-bucket/tenants/%s/jobs/%s/%s", req.TenantID, req.JobID, slot),
	}, nil
}

func (f *fakeMultiBlobStore) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("ignored")), nil
}

// fakeChunkRepo records InsertChunks batches + serves ListByJob.
type fakeChunkRepo struct {
	mu        sync.Mutex
	inserted  []sourcechunk.Chunk
	insertErr error
	listErr   error
}

func (f *fakeChunkRepo) InsertChunks(_ context.Context, chunks []sourcechunk.Chunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, chunks...)
	return nil
}

func (f *fakeChunkRepo) ListByJob(_ context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []sourcechunk.Chunk
	for _, c := range f.inserted {
		if c.TenantID == tenantID && c.JobID == jobID {
			out = append(out, c)
		}
	}
	return out, nil
}

// blockingChunkRepo blocks inside InsertChunks until released, so a test can
// prove the request returns its 202 WITHOUT waiting on extraction.
type blockingChunkRepo struct {
	entered  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	inserted []sourcechunk.Chunk
}

func (b *blockingChunkRepo) InsertChunks(_ context.Context, chunks []sourcechunk.Chunk) error {
	b.entered <- struct{}{}
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inserted = append(b.inserted, chunks...)
	return nil
}

func (b *blockingChunkRepo) ListByJob(_ context.Context, tenantID, jobID string) ([]sourcechunk.Chunk, error) {
	return nil, nil
}

// TestBatch1c_202ReturnsBeforeExtractionCompletes proves the fix: the heavy PDF
// extraction runs on a detached goroutine, so the upload's 202 is not blocked
// on it, and the generation_requested publish still follows extraction (order
// preserved).
func TestBatch1c_202ReturnsBeforeExtractionCompletes(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	chunks := &blockingChunkRepo{entered: make(chan struct{}, 1), release: make(chan struct{})}
	srv, _, pub, atomID := newMultiBatchServer(t, mana, &fakeMultiBlobStore{}, chunks)

	buf, ct, err := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "notes.md", "text/markdown", []byte("# Topic\n\nA paragraph of body text that will extract to at least one chunk.")},
	)
	if err != nil {
		t.Fatalf("buildMultipartFiles: %v", err)
	}

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
		close(done)
	}()

	// The request must return promptly even though extraction is still blocked.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return; the request is blocked on extraction (async move failed)")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}

	// The detached goroutine must be inside InsertChunks now, and the publish
	// must NOT have happened yet (extract before publish).
	select {
	case <-chunks.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("extraction goroutine never reached InsertChunks")
	}
	if got := len(pub.snapshot()); got != 0 {
		t.Fatalf("event published before extraction completed (%d events); ordering violated", got)
	}

	// Release extraction; the publish follows.
	close(chunks.release)
	pub.waitForEvents(t, 1, 2*time.Second)
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

type mpFile struct {
	field    string
	filename string
	mime     string
	body     []byte
}

func buildMultipartFiles(settings string, files ...mpFile) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if settings != "" {
		if err := w.WriteField("settings", settings); err != nil {
			return nil, "", err
		}
	}
	for _, f := range files {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name=%q; filename=%q`, f.field, f.filename)}
		h["Content-Type"] = []string{f.mime}
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(f.body); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

func newMultiBatchServer(t *testing.T, mana *fakeManaLedger, blob ports.BlobStore, chunks ports.SourceChunkRepository) (http.Handler, *fakeJobRepo, *fakeJobPublisher, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	jobRepo := newFakeJobRepo()
	pub := &fakeJobPublisher{}

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "MCQ", Body: "Body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}

	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                  atomRepo,
		QuestionRepository:    qRepo,
		QuestionJobRepository: jobRepo,
		ManaLedger:            mana,
		JobEventPublisher:     pub,
		BlobStore:             blob,
		SourceChunkRepository: chunks,
	})
	return router, jobRepo, pub, a.AtomID
}

const batchSettings1c = `{"job_type":"batch_source_material","question_type":"mcq","count":3}`

// settingsSourceFiles decodes settings JSON → source_files entries.
func settingsSourceFilesFromJob(t *testing.T, j *question.ComposeJob) []map[string]any {
	t.Helper()
	if len(j.SettingsJSON) == 0 {
		t.Fatalf("job.SettingsJSON empty")
	}
	var settings map[string]any
	if err := json.Unmarshal(j.SettingsJSON, &settings); err != nil {
		t.Fatalf("settings unmarshal: %v", err)
	}
	raw, ok := settings["source_files"].([]any)
	if !ok {
		t.Fatalf("settings.source_files missing: %s", j.SettingsJSON)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("source_files element not a map: %T", e)
		}
		out = append(out, m)
	}
	return out
}

// -----------------------------------------------------------------------------
// Happy path — 2 sources (md + png) + rubric (txt)
// -----------------------------------------------------------------------------

func TestBatch1c_MultiFilePlusRubric_202_UploadsSlotsRolesChunks(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeMultiBlobStore{}
	chunks := &fakeChunkRepo{}
	srv, jobRepo, pub, atomID := newMultiBatchServer(t, mana, blob, chunks)

	buf, ct, err := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "notes.md", "text/markdown", []byte("# Mitosis\n\nCells divide via mitosis in somatic tissue.")},
		mpFile{"files", "diagram.png", "image/png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}},
		mpFile{"rubric_file", "marks.txt", "text/plain", []byte("Question 1 [5 marks]. Question 2 [10 marks].")},
	)
	if err != nil {
		t.Fatalf("buildMultipartFiles: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}

	// 3 uploads, slot-keyed (source-1, source-2, rubric).
	if len(blob.reqs) != 3 {
		t.Fatalf("uploads = %d; want 3", len(blob.reqs))
	}
	if blob.reqs[0].Slot != "source-1" || blob.reqs[0].MIME != "text/markdown" {
		t.Errorf("upload[0] = slot %q mime %q; want source-1 text/markdown", blob.reqs[0].Slot, blob.reqs[0].MIME)
	}
	if blob.reqs[1].Slot != "source-2" || blob.reqs[1].MIME != "image/png" {
		t.Errorf("upload[1] = slot %q mime %q; want source-2 image/png", blob.reqs[1].Slot, blob.reqs[1].MIME)
	}
	if blob.reqs[2].Slot != "rubric" || blob.reqs[2].MIME != "text/plain" {
		t.Errorf("upload[2] = slot %q mime %q; want rubric text/plain", blob.reqs[2].Slot, blob.reqs[2].MIME)
	}

	// Job: legacy mirror columns point at source_files[0]; settings carry roles.
	if len(jobRepo.jobs) != 1 {
		t.Fatalf("jobs persisted = %d; want 1", len(jobRepo.jobs))
	}
	var job *question.ComposeJob
	for _, j := range jobRepo.jobs {
		job = j
	}
	if !strings.HasSuffix(job.SourceBlobURI, "/source-1") {
		t.Errorf("SourceBlobURI = %q; want .../source-1 (mirror of source_files[0])", job.SourceBlobURI)
	}
	if job.SourceMimeType != "text/markdown" {
		t.Errorf("SourceMimeType = %q; want text/markdown", job.SourceMimeType)
	}
	sf := settingsSourceFilesFromJob(t, job)
	if len(sf) != 3 {
		t.Fatalf("settings.source_files len = %d; want 3", len(sf))
	}
	if sf[0]["role"] != "source" || sf[1]["role"] != "source" || sf[2]["role"] != "rubric" {
		t.Errorf("roles = %v %v %v; want source source rubric", sf[0]["role"], sf[1]["role"], sf[2]["role"])
	}
	if sf[2]["mime_type"] != "text/plain" {
		t.Errorf("rubric mime = %v", sf[2]["mime_type"])
	}

	// Extraction + publish run on the detached finish goroutine; wait for the
	// event (its last step) so the chunk assertions below see the result.
	pub.waitForEvents(t, 1, 2*time.Second)

	// Chunks extracted for text-bearing files only (md + txt; png none).
	if len(chunks.inserted) == 0 {
		t.Fatal("no chunks persisted; want md + rubric chunks")
	}
	uris := map[string]bool{}
	for _, c := range chunks.inserted {
		uris[c.FileURI] = true
		if c.JobID != job.JobID || c.TenantID != tenantA {
			t.Errorf("chunk identity wrong: %+v", c)
		}
	}
	if len(uris) != 2 {
		t.Errorf("chunked files = %v; want exactly the md + rubric URIs", uris)
	}
	for uri := range uris {
		if strings.HasSuffix(uri, "/source-2") {
			t.Errorf("image file %q must produce NO chunks", uri)
		}
	}
	rubricSeen := false
	for _, c := range chunks.inserted {
		if c.FileRole == sourcechunk.RoleRubric {
			rubricSeen = true
		}
	}
	if !rubricSeen {
		t.Error("rubric chunks missing role=rubric")
	}

	// Event settings_json carries source_files.
	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d; want 1", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", events[0].Payload)
	}
	settingsJSON, _ := payload["settings_json"].(string)
	if !strings.Contains(settingsJSON, `"source_files"`) || !strings.Contains(settingsJSON, `"rubric"`) {
		t.Errorf("event settings_json = %q; want source_files with rubric role", settingsJSON)
	}
	if payload["source_blob_uri"] != job.SourceBlobURI {
		t.Errorf("event source_blob_uri = %v; want mirror %q", payload["source_blob_uri"], job.SourceBlobURI)
	}
}

// -----------------------------------------------------------------------------
// Legacy single `file` part — bit-identical surface
// -----------------------------------------------------------------------------

func TestBatch1c_LegacySingleFile_KeepsLegacySlotAndRecordsRole(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeMultiBlobStore{}
	chunks := &fakeChunkRepo{}
	srv, jobRepo, _, atomID := newMultiBatchServer(t, mana, blob, chunks)

	buf, ct, err := buildMultipartFiles(batchSettings1c,
		mpFile{"file", "exam.md", "text/markdown", []byte("Legacy single source.")},
	)
	if err != nil {
		t.Fatalf("buildMultipartFiles: %v", err)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	if len(blob.reqs) != 1 || blob.reqs[0].Slot != "" {
		t.Fatalf("legacy upload must use the empty slot (locked /source key); got %+v", blob.reqs)
	}
	for _, j := range jobRepo.jobs {
		sf := settingsSourceFilesFromJob(t, j)
		if len(sf) != 1 || sf[0]["role"] != "source" {
			t.Errorf("legacy settings.source_files = %v; want 1 source entry", sf)
		}
	}
}

// -----------------------------------------------------------------------------
// Validation failures
// -----------------------------------------------------------------------------

func TestBatch1c_BothFileAndFiles_400(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, _, _, atomID := newMultiBatchServer(t, mana, &fakeMultiBlobStore{}, &fakeChunkRepo{})

	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"file", "a.md", "text/markdown", []byte("a")},
		mpFile{"files", "b.md", "text/markdown", []byte("b")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (exactly one of file|files)", w.Code)
	}
	if mana.deductCalls != 0 {
		t.Errorf("mana debited on invalid request")
	}
}

func TestBatch1c_NoFiles_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
	buf, ct, _ := buildMultipartFiles(batchSettings1c)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (missing file)", w.Code)
	}
}

func TestBatch1c_SixSourceFiles_400(t *testing.T) {
	t.Parallel()
	files := make([]mpFile, 0, 6)
	for i := 0; i < 6; i++ {
		files = append(files, mpFile{"files", fmt.Sprintf("f%d.md", i), "text/markdown", []byte("x")})
	}
	srv, _, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
	buf, ct, _ := buildMultipartFiles(batchSettings1c, files...)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (max 5 source files)", w.Code)
	}
}

func TestBatch1c_TwoRubricFiles_400(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "a.md", "text/markdown", []byte("a")},
		mpFile{"rubric_file", "r1.txt", "text/plain", []byte("r1")},
		mpFile{"rubric_file", "r2.txt", "text/plain", []byte("r2")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (≤1 rubric file)", w.Code)
	}
}

func TestBatch1c_UnsupportedMIMEInFiles_415(t *testing.T) {
	t.Parallel()
	srv, _, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "movie.mp4", "video/mp4", []byte("xx")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d; want 415", w.Code)
	}
}

func TestBatch1c_ImageMIMEsNowAllowed(t *testing.T) {
	t.Parallel()
	for _, mime := range []string{"image/png", "image/jpeg", "image/webp"} {
		srv, _, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
		buf, ct, _ := buildMultipartFiles(batchSettings1c,
			mpFile{"files", "img", mime, []byte{0x89, 0x50}},
		)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
		if w.Code != http.StatusAccepted {
			t.Errorf("%s: status = %d body=%s; want 202 (D7 image allowlist)", mime, w.Code, w.Body.String())
		}
	}
}

// -----------------------------------------------------------------------------
// Failure postures
// -----------------------------------------------------------------------------

func TestBatch1c_SecondUploadFails_502(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeMultiBlobStore{failAtN: 2}
	srv, jobRepo, _, atomID := newMultiBatchServer(t, mana, blob, &fakeChunkRepo{})

	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "a.md", "text/markdown", []byte("a")},
		mpFile{"files", "b.md", "text/markdown", []byte("b")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s; want 502", w.Code, w.Body.String())
	}
	// U3b — generation no longer charges, so there is nothing to refund.
	if mana.refundCalls != 0 {
		t.Errorf("refunds = %d; want 0 (no upfront charge in U3b)", mana.refundCalls)
	}
	if len(jobRepo.jobs) != 0 {
		t.Errorf("job persisted despite upload failure")
	}
}

func TestBatch1c_ExtractionFailure_DoesNotFailJob(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	chunks := &fakeChunkRepo{}
	srv, jobRepo, pub, atomID := newMultiBatchServer(t, mana, &fakeMultiBlobStore{}, chunks)

	// DOCX MIME with non-zip bytes → extraction errors → log + continue.
	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "broken.docx",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			[]byte("definitely not a zip")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202 (extraction failure must NOT fail the job)", w.Code, w.Body.String())
	}
	// Wait for the publish (the finish goroutine's last step) so the assertion
	// below sees the completed extraction attempt, not a not-yet-run one.
	pub.waitForEvents(t, 1, 2*time.Second)
	if len(chunks.inserted) != 0 {
		t.Errorf("chunks inserted = %d; want 0 for failed extraction", len(chunks.inserted))
	}
	if len(jobRepo.jobs) != 1 {
		t.Errorf("job not persisted")
	}
}

func TestBatch1c_ChunkInsertFailure_DoesNotFailJob(t *testing.T) {
	t.Parallel()
	chunks := &fakeChunkRepo{insertErr: errors.New("db down")}
	srv, jobRepo, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, chunks)

	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "a.md", "text/markdown", []byte("Some text.")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want 202 (chunk persistence is best-effort)", w.Code)
	}
	if len(jobRepo.jobs) != 1 {
		t.Errorf("job not persisted")
	}
}

func TestBatch1c_NilChunkRepo_StillAccepts(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, nil)
	buf, ct, _ := buildMultipartFiles(batchSettings1c,
		mpFile{"files", "a.md", "text/markdown", []byte("Some text.")},
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, ct))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want 202 (nil chunk repo skips extraction)", w.Code)
	}
	if len(jobRepo.jobs) != 1 {
		t.Errorf("job not persisted")
	}
}

// -----------------------------------------------------------------------------
// GET poll — proposed_test_set projection
// -----------------------------------------------------------------------------

func TestBatch1c_GetJob_SurfacesProposedTestSet(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	srv, jobRepo, _, atomID := newMultiBatchServer(t, mana, &fakeMultiBlobStore{}, &fakeChunkRepo{})

	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-00000000aa01", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		CandidateQuestionsJSON: []byte(`[{"draft_id":"d1","type":"mcq","prompt":"p","citations":[{"source_file":"gs://b/k","page":1,"excerpt":"x","verified":true,"chunk_id":"c1"}]}]`),
		ProposedTestSetJSON:    []byte(`{"title":"Cell Biology Quiz","order":["d1"],"points":{"d1":5}}`),
	}
	jobRepo.jobs[job.JobID] = job

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs/"+job.JobID, nil)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	pts, ok := resp["proposed_test_set"].(map[string]any)
	if !ok {
		t.Fatalf("proposed_test_set missing: %s", w.Body.String())
	}
	if pts["title"] != "Cell Biology Quiz" {
		t.Errorf("proposal title = %v", pts["title"])
	}
	// Candidates pass through verbatim — citations included.
	if !strings.Contains(w.Body.String(), `"verified":true`) {
		t.Errorf("citations must ride the candidates verbatim; body=%s", w.Body.String())
	}
}

func TestBatch1c_GetJob_NoProposal_FieldAbsent(t *testing.T) {
	t.Parallel()
	srv, jobRepo, _, atomID := newMultiBatchServer(t, &fakeManaLedger{successDebits: 1}, &fakeMultiBlobStore{}, &fakeChunkRepo{})
	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-00000000aa02", AtomID: atomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
	}
	jobRepo.jobs[job.JobID] = job

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/atoms/"+atomID+"/question-jobs/"+job.JobID, nil)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "proposed_test_set") {
		t.Errorf("proposed_test_set must be omitted when absent; body=%s", w.Body.String())
	}
}
