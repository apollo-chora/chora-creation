// question_jobs_batch_test.go — RED→GREEN coverage for the P6 batch
// upload path on POST /api/atoms/{atom_id}/question-jobs with
// Content-Type: multipart/form-data.
//
// Contract: chora-contracts/openapi/creation-questions.yaml
// (createQuestionJob — multipart/form-data variant).
//
// Mana model: 50 mana parse-step at job creation; the per-item 5 mana
// per accepted candidate is deducted on /accept (extension covered in
// the accept tests).
//
// Fail-loud: if BlobStore is not wired (env GCS_BUCKET_BATCH_UPLOADS
// unset) the handler returns 503 with CREATION_BATCH_NOT_WIRED — NO
// silent fallback.
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

	"github.com/google/uuid"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fake BlobStore for the handler-layer tests.
// -----------------------------------------------------------------------------

type fakeBlobStore struct {
	mu        sync.Mutex
	uploads   int
	lastReq   ports.UploadBlobReq
	uploadErr error
	returnURI string
	notWired  bool // when true, returns ErrBlobStoreNotWired
}

func (f *fakeBlobStore) Upload(_ context.Context, req ports.UploadBlobReq) (ports.UploadBlobResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads++
	// Drain body to record size + leave the stream empty.
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	f.lastReq = req
	if f.notWired {
		return ports.UploadBlobResp{}, ports.ErrBlobStoreNotWired
	}
	if f.uploadErr != nil {
		return ports.UploadBlobResp{}, f.uploadErr
	}
	uri := f.returnURI
	if uri == "" {
		uri = fmt.Sprintf("gs://test-bucket/tenants/%s/jobs/%s/source", req.TenantID, req.JobID)
	}
	return ports.UploadBlobResp{BlobURI: uri}, nil
}

func (f *fakeBlobStore) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("ignored")), nil
}

// -----------------------------------------------------------------------------
// Test server helper that wires the BlobStore.
// -----------------------------------------------------------------------------

func newBatchJobsServer(t *testing.T, mana *fakeManaLedger, blob ports.BlobStore) (http.Handler, *fakeJobRepo, *fakeJobPublisher, string) {
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
	})
	return router, jobRepo, pub, a.AtomID
}

// -----------------------------------------------------------------------------
// Multipart helpers
// -----------------------------------------------------------------------------

// buildMultipart returns a multipart request body + the Content-Type
// header value the handler will see.
func buildMultipart(filename, mime string, body []byte, settings string) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// settings field.
	if settings != "" {
		if err := w.WriteField("settings", settings); err != nil {
			return nil, "", err
		}
	}

	// file field.
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	h["Content-Type"] = []string{mime}
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(body); err != nil {
		return nil, "", err
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

func authedMultipartReq(method, path string, body io.Reader, contentType string) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", contentType)
	return r
}

// -----------------------------------------------------------------------------
// Tests — happy path
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_Batch_Returns202_WithJobID(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeBlobStore{}
	srv, jobRepo, pub, atomID := newBatchJobsServer(t, mana, blob)

	settings := `{"job_type":"batch_source_material","question_type":"mcq","count":3}`
	buf, contentType, err := buildMultipart("source.pdf", "application/pdf", []byte("%PDF-1.4 fake content"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	jid, _ := got["job_id"].(string)
	if jid == "" {
		t.Fatal("job_id missing in 202 envelope")
	}
	if got["status"] != "requested" {
		t.Errorf("status = %v; want requested", got["status"])
	}
	// U3b — batch generation (parse) is no longer charged; the author pays per
	// ACCEPTED question at accept. No upfront debit at job creation.
	if mana.deductCalls != 0 {
		t.Errorf("DeductMana called %d times; want 0 (no upfront parse charge)", mana.deductCalls)
	}
	// Blob uploaded.
	if blob.uploads != 1 {
		t.Errorf("blob uploads = %d; want 1", blob.uploads)
	}
	if blob.lastReq.MIME != "application/pdf" {
		t.Errorf("blob mime = %q; want application/pdf", blob.lastReq.MIME)
	}
	if blob.lastReq.Filename != "source.pdf" {
		t.Errorf("blob filename = %q; want source.pdf", blob.lastReq.Filename)
	}
	// Job persisted with batch type + URI.
	if len(jobRepo.jobs) != 1 {
		t.Fatalf("job not persisted")
	}
	for _, j := range jobRepo.jobs {
		if j.Input.Kind() != question.InputSourceFiles {
			t.Errorf("input kind = %q; want source_files", j.Input.Kind())
		}
		if j.SourceBlobURI == "" {
			t.Error("SourceBlobURI empty on persisted job")
		}
		if j.SourceMimeType != "application/pdf" {
			t.Errorf("SourceMimeType = %q; want application/pdf", j.SourceMimeType)
		}
	}
	// Event published on the detached finish goroutine; wait for it.
	events := pub.waitForEvents(t, 1, 2*time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	if events[0].Topic != "chora.creation.question.generation_requested.v2" {
		t.Errorf("event topic = %q", events[0].Topic)
	}
}

// -----------------------------------------------------------------------------
// 503 when BlobStore not wired (env GCS_BUCKET_BATCH_UPLOADS unset).
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_Batch_503WhenBlobNotWired(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	// Wire RouterDeps WITHOUT a BlobStore (the realistic dev-mode "not wired" path).
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	jobRepo := newFakeJobRepo()
	pub := &fakeJobPublisher{}

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo, QuestionJobRepository: jobRepo,
		ManaLedger: mana, JobEventPublisher: pub,
		// BlobStore: nil — intentional
	})

	settings := `{"job_type":"batch_source_material","question_type":"mcq","count":3}`
	buf, contentType, err := buildMultipart("source.pdf", "application/pdf", []byte("%PDF"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/question-jobs", buf, contentType))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_BATCH_NOT_WIRED") {
		t.Errorf("body should mention CREATION_BATCH_NOT_WIRED; got %s", w.Body.String())
	}
	// No mana debited on the not-wired path.
	if mana.deductCalls != 0 {
		t.Errorf("mana deduct called %d times; want 0 (no debit before wiring check)", mana.deductCalls)
	}
}

// -----------------------------------------------------------------------------
// 413 file too large.
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_Batch_413FileTooLarge(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeBlobStore{}
	srv, _, _, atomID := newBatchJobsServer(t, mana, blob)

	// 33MB sentinel — handler must reject at the boundary (32MB).
	big := bytes.Repeat([]byte{'A'}, 33*1024*1024)
	settings := `{"job_type":"batch_source_material","question_type":"mcq","count":3}`
	buf, contentType, err := buildMultipart("big.pdf", "application/pdf", big, settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d body=%s; want 413", w.Code, w.Body.String())
	}
	if blob.uploads != 0 {
		t.Errorf("blob uploaded %d times; want 0 on 413", blob.uploads)
	}
}

// -----------------------------------------------------------------------------
// 415 unsupported MIME.
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_Batch_415UnsupportedMIME(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeBlobStore{}
	srv, _, _, atomID := newBatchJobsServer(t, mana, blob)

	settings := `{"job_type":"batch_source_material","question_type":"mcq","count":3}`
	// .exe is not in the {PDF,DOCX,MD,TXT} allowlist.
	buf, contentType, err := buildMultipart("evil.exe", "application/x-msdownload", []byte("MZ\x90"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d body=%s; want 415", w.Code, w.Body.String())
	}
	if blob.uploads != 0 {
		t.Errorf("blob uploaded %d times; want 0 on 415", blob.uploads)
	}
}

// TestCreateQuestionJob_Batch_402WhenInsufficientMana was removed in U3b:
// batch generation (parse) no longer charges, so insufficient mana can only
// 402 at ACCEPT time — see TestAccept_InsufficientMana_402_NoPersist.

// -----------------------------------------------------------------------------
// Accept N>1 — unified 1q=1atom (CHO-1826 U3a, retires Debt #28).
// -----------------------------------------------------------------------------
//
// Every accepted question is its own LearningAtom (D2: 1 atom = 1 non-deleted
// question). The FIRST candidate reuses the route/parent atom (so a count=1
// author never strands an empty draft container); candidates 2..N mint fresh
// atoms cloning the route atom's metadata.
//
// Per-candidate atom metadata:
//   - atom_meta_override provided → use title/tags/difficulty from override
//   - atom_meta_override absent   → clone the route atom's title/tags/difficulty
//
// Persistence: each (atom, question) pair is persisted; per-candidate
// chora.creation.atom.created.v1 (upsert) + chora.creation.question.authored.v1
// events are emitted.
//
// Response: persisted[].atom_id surfaces each atom id so FE can navigate.

func TestAcceptBatchJob_NGreaterThan1_CreatesNewAtomsPerCandidate(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	blob := &fakeBlobStore{}
	srv, jobRepo, pub, parentAtomID := newBatchJobsServer(t, mana, blob)

	jid := uuid.NewString()
	drafts := []map[string]any{
		{
			"draft_id": "d1",
			"type":     "mcq",
			"prompt":   "MCQ #1 — what is X?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "a1", "label": "Right", "is_correct": true, "explainer": "yes"},
					{"option_id": "a2", "label": "Wrong", "is_correct": false, "explainer": "no"},
				},
			},
		},
		{
			"draft_id": "d2",
			"type":     "mcq",
			"prompt":   "MCQ #2 — what is Y?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "b1", "label": "Right", "is_correct": true, "explainer": "yes"},
					{"option_id": "b2", "label": "Wrong", "is_correct": false, "explainer": "no"},
				},
			},
		},
		{
			"draft_id": "d3",
			"type":     "mcq",
			"prompt":   "MCQ #3 — what is Z?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "c1", "label": "Right", "is_correct": true, "explainer": "yes"},
					{"option_id": "c2", "label": "Wrong", "is_correct": false, "explainer": "no"},
				},
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: parentAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}},
		Status:                 question.JobStatusSucceeded,
		ManaCharged:            0,
		CandidateQuestionsJSON: draftsJSON,
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{
				"draft_id": "d1",
				"atom_meta_override": map[string]any{
					"title":      "Custom Atom 1",
					"tags":       []string{"agile", "scrum"},
					"difficulty": 3,
				},
			},
			{"draft_id": "d2"}, // override absent → clone from parent
			{
				"draft_id": "d3",
				"atom_meta_override": map[string]any{
					"title": "Custom Atom 3",
				},
			},
		},
	}
	w := httptest.NewRecorder()
	r := authedJobJSON(http.MethodPost, "/api/atoms/"+parentAtomID+"/question-jobs/"+jid+"/accept", body)
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (no ErrAtomHasQuestion on N>1)", w.Code, w.Body.String())
	}

	// Parse response — expect 3 persisted, each on a NEW atom (not parentAtomID).
	var resp struct {
		Persisted []struct {
			QuestionID string `json:"question_id"`
			AtomID     string `json:"atom_id"`
			Prompt     string `json:"prompt"`
		} `json:"persisted"`
		ManaDebited int `json:"mana_debited"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid response json: %v body=%s", err, w.Body.String())
	}
	if len(resp.Persisted) != 3 {
		t.Fatalf("persisted len = %d; want 3 (one per candidate)", len(resp.Persisted))
	}
	// Unified model: d1 reuses the route/parent atom; d2/d3 mint fresh atoms.
	if resp.Persisted[0].AtomID != parentAtomID {
		t.Errorf("persisted[0].atom_id = %q; want route atom %q (reuse-for-first)", resp.Persisted[0].AtomID, parentAtomID)
	}
	seenAtomIDs := map[string]bool{}
	for i, p := range resp.Persisted {
		if p.AtomID == "" {
			t.Errorf("persisted[%d].atom_id empty", i)
		}
		if i > 0 && p.AtomID == parentAtomID {
			t.Errorf("persisted[%d].atom_id reused the route atom; want a fresh atom (1q=1atom)", i)
		}
		if p.QuestionID == "" {
			t.Errorf("persisted[%d].question_id empty", i)
		}
		seenAtomIDs[p.AtomID] = true
	}
	if len(seenAtomIDs) != 3 {
		t.Errorf("seen atom ids = %d; want 3 distinct (parent + 2 new) (got: %v)", len(seenAtomIDs), seenAtomIDs)
	}

	// Events emitted: 3× atom.created.v1 + 3× question.authored.v1 + 1× generation_completed.v1 = 7
	events := pub.snapshot()
	atomCreatedCount := 0
	questionAuthoredCount := 0
	for _, ev := range events {
		switch ev.Topic {
		case "chora.creation.atom.created.v1":
			atomCreatedCount++
		case "chora.creation.question.authored.v1":
			questionAuthoredCount++
		}
	}
	if atomCreatedCount != 3 {
		t.Errorf("atom.created.v1 events = %d; want 3 (one per new atom)", atomCreatedCount)
	}
	if questionAuthoredCount != 3 {
		t.Errorf("question.authored.v1 events = %d; want 3 (one per persisted question)", questionAuthoredCount)
	}

	// U3b — no upfront parse charge; 3 text MCQ accepted × 10 (generate) = 30.
	if resp.ManaDebited != 30 {
		t.Errorf("mana_debited = %d; want 30 (3 text × 10 generate)", resp.ManaDebited)
	}
	if mana.lastDeductReq.ActionCode != "question_authoring_generate" {
		t.Errorf("last debit action = %q; want question_authoring_generate", mana.lastDeductReq.ActionCode)
	}
	if mana.lastDeductReq.Units != 0 {
		t.Errorf("generate debit units = %d; want 0 (server-resolved)", mana.lastDeductReq.Units)
	}
	if mana.lastDeductReq.Context["item_count"] != "3" {
		t.Errorf("generate debit item_count = %q; want \"3\"", mana.lastDeductReq.Context["item_count"])
	}
}

func TestAcceptBatchJob_AtomMetaOverride_HonoredOnReusedAndMintedAtoms(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	blob := &fakeBlobStore{}
	srv, jobRepo, _, parentAtomID := newBatchJobsServer(t, mana, blob)

	jid := uuid.NewString()
	drafts := []map[string]any{
		mcqDraft("d1", "what is X?", "a1"),
		mcqDraft("d2", "what is Y?", "b1"),
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: parentAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		ManaCharged: 50, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	// d1 reuses the route atom (override applied to it via applyAtomMetaOverride);
	// d2 mints a fresh atom (override applied via buildBatchCandidateAtom).
	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{
				"draft_id":           "d1",
				"atom_meta_override": map[string]any{"title": "Reused Title", "tags": []string{"reuse-a"}, "difficulty": 2},
			},
			{
				"draft_id":           "d2",
				"atom_meta_override": map[string]any{"title": "Minted Title", "tags": []string{"mint-a", "mint-b"}, "difficulty": 4},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+parentAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Persisted []struct {
			AtomID string `json:"atom_id"`
		} `json:"persisted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid resp: %v", err)
	}
	if len(resp.Persisted) != 2 {
		t.Fatalf("persisted len = %d; want 2", len(resp.Persisted))
	}
	if resp.Persisted[0].AtomID != parentAtomID {
		t.Errorf("persisted[0].atom_id = %q; want reused route atom %q", resp.Persisted[0].AtomID, parentAtomID)
	}
	mintedID := resp.Persisted[1].AtomID
	if mintedID == parentAtomID {
		t.Fatalf("persisted[1] reused the route atom; want a minted atom")
	}

	assertAtomMeta := func(atomID, wantTitle string, wantTags []any, wantDiff int) {
		t.Helper()
		ww := httptest.NewRecorder()
		srv.ServeHTTP(ww, authedJobJSON(http.MethodGet, "/api/atoms/"+atomID, nil))
		if ww.Code != http.StatusOK {
			t.Fatalf("GET atom %s: status %d body=%s", atomID, ww.Code, ww.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(ww.Body.Bytes(), &got); err != nil {
			t.Fatalf("invalid atom json: %v", err)
		}
		if got["title"] != wantTitle {
			t.Errorf("atom %s title = %v; want %q", atomID, got["title"], wantTitle)
		}
		tags, _ := got["tags"].([]any)
		if len(tags) != len(wantTags) {
			t.Errorf("atom %s tags = %v; want %v", atomID, tags, wantTags)
		} else {
			for i := range wantTags {
				if tags[i] != wantTags[i] {
					t.Errorf("atom %s tags[%d] = %v; want %v", atomID, i, tags[i], wantTags[i])
				}
			}
		}
		if d, ok := got["difficulty"].(float64); !ok || int(d) != wantDiff {
			t.Errorf("atom %s difficulty = %v; want %d", atomID, got["difficulty"], wantDiff)
		}
	}
	assertAtomMeta(parentAtomID, "Reused Title", []any{"reuse-a"}, 2)
	assertAtomMeta(mintedID, "Minted Title", []any{"mint-a", "mint-b"}, 4)
}

func TestAcceptBatchJob_NoOverride_ClonesRouteAtomMeta(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	blob := &fakeBlobStore{}
	srv, jobRepo, _, parentAtomID := newBatchJobsServer(t, mana, blob)

	jid := uuid.NewString()
	drafts := []map[string]any{
		mcqDraft("d1", "what is Y?", "b1"),
		mcqDraft("d2", "what is Z?", "c1"),
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: parentAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}, Status: question.JobStatusSucceeded,
		ManaCharged: 50, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	// No overrides: d1 reuses the route atom; d2 mints a fresh atom cloning
	// the route atom's metadata.
	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{"draft_id": "d1"}, {"draft_id": "d2"},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+parentAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}

	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, authedJobJSON(http.MethodGet, "/api/atoms/"+parentAtomID, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("GET parent: status %d", w1.Code)
	}
	var parent map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &parent)
	parentTitle, _ := parent["title"].(string)

	var resp struct {
		Persisted []struct {
			AtomID string `json:"atom_id"`
		} `json:"persisted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Persisted) != 2 {
		t.Fatalf("persisted len = %d; want 2", len(resp.Persisted))
	}
	mintedID := resp.Persisted[1].AtomID
	if mintedID == parentAtomID {
		t.Fatalf("persisted[1] reused the route atom; want a minted atom to test cloning")
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJobJSON(http.MethodGet, "/api/atoms/"+mintedID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET minted atom: status %d body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["title"] != parentTitle {
		t.Errorf("minted title = %v; want clone of route atom title %q", got["title"], parentTitle)
	}
}

// -----------------------------------------------------------------------------
// Non-batch jobs (ai_draft / ai_model_answer) preserve single-atom semantics.
// -----------------------------------------------------------------------------

func TestAcceptAIDraftJob_StaysOnParentAtom_NoNewAtomCreated(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, qRepo, _, parentAtomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	drafts := []map[string]any{
		{
			"draft_id": "d1", "type": "mcq", "prompt": "what is X?",
			"mcq_payload": map[string]any{
				"options": []map[string]any{
					{"option_id": "a1", "label": "Right", "is_correct": true, "explainer": "yes"},
					{"option_id": "a2", "label": "Wrong", "is_correct": false, "explainer": "no"},
				},
			},
		},
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: parentAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{"draft_id": "d1", "atom_meta_override": map[string]any{"title": "ignored for ai_draft"}},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+parentAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	// Question must still be on the parent atom (ai_draft is single-atom).
	if len(qRepo.questions) != 1 {
		t.Fatalf("expected 1 persisted question; got %d", len(qRepo.questions))
	}
	for _, q := range qRepo.questions {
		if q.AtomID != parentAtomID {
			t.Errorf("ai_draft accept persisted on atom %q; want parent %q (D2 — single-atom path)", q.AtomID, parentAtomID)
		}
	}
}

// -----------------------------------------------------------------------------
// 500 blob upload failure refunds mana.
// -----------------------------------------------------------------------------

func TestCreateQuestionJob_Batch_5xxRefundsMana_WhenBlobUploadFails(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 1}
	blob := &fakeBlobStore{uploadErr: errors.New("gcs 500")}
	srv, jobRepo, _, atomID := newBatchJobsServer(t, mana, blob)

	settings := `{"job_type":"batch_source_material","question_type":"mcq","count":3}`
	buf, contentType, err := buildMultipart("doc.txt", "text/plain", []byte("Hello"), settings)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedMultipartReq(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", buf, contentType))

	if w.Code < 500 {
		t.Fatalf("status = %d; want 5xx after blob upload failure", w.Code)
	}
	// U3b — generation no longer charges, so there is nothing to refund.
	if mana.refundCalls != 0 {
		t.Errorf("refund called %d times; want 0 (no upfront charge in U3b)", mana.refundCalls)
	}
	if len(jobRepo.jobs) != 0 {
		t.Errorf("job should NOT be persisted on blob failure; got %d", len(jobRepo.jobs))
	}
}
