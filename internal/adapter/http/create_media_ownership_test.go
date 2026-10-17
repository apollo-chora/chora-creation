// create_media_ownership_test.go — the tenant ownership boundary on the
// QUESTION-CREATE and SAVE-REVISION seams.
//
// Both seams let a caller supply an image ref that is persisted verbatim, and
// the read signer scopes on bucket only — so a foreign-tenant ref that reached
// persistence would be signed and served, and a durable one never expires.
// These tests wire the REAL S3 adapter as both the re-homer and the download
// signer and assert, per seam, that a foreign ref is refused before anything is
// written and that an own-tenant ref still works.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const foreignTenantKey = "tenants/someone-else/jobs/j1/secret.png"

// newOwnershipRouter wires the REAL S3 adapter as BOTH the re-homer and the
// download signer over an MCQ atom, and returns the router + question repo.
func newOwnershipRouter(t *testing.T, signer *mediastore.AtomMediaSigner) (http.Handler, *fakeQuestionRepository, string) {
	t.Helper()
	return newOwnershipRouterWith(t, signer, signer)
}

// newOwnershipRouterWith wires the given re-homer and signer over an MCQ atom.
// Passing a nil re-homer models the "copier unwired" configuration the tenant
// ownership gate must still protect.
func newOwnershipRouterWith(t *testing.T, reHomer ports.MediaReHomer, signer ports.AtomMediaDownloadSigner) (http.Handler, *fakeQuestionRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "Ownership atom", Body: "b", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("atomRepo.Save: %v", err)
	}
	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo,
		MediaReHomer: reHomer, AtomMediaDownloadSigner: signer,
	})
	return router, qRepo, a.AtomID
}

func ownershipMCQBody(imageURL string) map[string]any {
	body := map[string]any{
		"type": "mcq", "prompt": "p",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "a", "label": "A", "is_correct": true, "explainer": "y"},
			{"option_id": "b", "label": "B", "is_correct": false, "explainer": "n"},
		}},
	}
	if imageURL != "" {
		body["image_url"] = imageURL
	}
	return body
}

func postQuestion(t *testing.T, router http.Handler, atomID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	return w
}

// assertCreateRefused asserts a refused create wrote nothing at all.
func assertCreateRefused(t *testing.T, w *httptest.ResponseRecorder, qRepo *fakeQuestionRepository, atomID string) {
	t.Helper()
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	assertNoQuestionPersisted(t, qRepo, atomID)
}

// A client-supplied DURABLE ref naming another tenant's object must be refused
// before anything is written.
func TestCreateQuestion_ForeignTenantDurableRefRefused(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	foreign := "s3://" + reHomeDurableBucket + "/tenants/someone-else/atoms/a1/secret.png"
	w := postQuestion(t, router, atomID, ownershipMCQBody(foreign))
	assertCreateRefused(t, w, qRepo, atomID)
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
}

// The same for a TRANSIENT ref: the trusted origin + transient bucket check
// passes, so only the ownership check can stop it.
func TestCreateQuestion_ForeignTenantTransientRefRefused(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	foreign := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/someone-else/jobs/j1/secret.png?X-Goog-Expires=604800"
	w := postQuestion(t, router, atomID, ownershipMCQBody(foreign))
	assertCreateRefused(t, w, qRepo, atomID)
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
}

// An own-tenant DURABLE ref is persisted unchanged — no copy.
func TestCreateQuestion_OwnTenantDurableRefAccepted(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	own := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/a1/obj.png"
	w := postQuestion(t, router, atomID, ownershipMCQBody(own))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 (already durable)", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != own {
		t.Errorf("ImageURL = %v; want the unchanged durable ref %q", deref(mcq.ImageURL), own)
	}
}

// An own-tenant TRANSIENT ref is re-homed to a durable ref (the preferred
// choice: otherwise it 404s once the 7-day transient TTL expires).
func TestCreateQuestion_OwnTenantTransientRefReHomed(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	own := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	w := postQuestion(t, router, atomID, ownershipMCQBody(own))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 1 {
		t.Errorf("S3 endpoint received %d requests; want 1 (the re-home copy)", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	want := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/" + atomID + "/"
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, want) {
		t.Errorf("ImageURL = %v; want a durable %s<uuid>.png ref", deref(mcq.ImageURL), want)
	}
	if strings.Contains(deref(mcq.ImageURL), "X-Goog-Expires") {
		t.Errorf("ImageURL = %q; the transient signed URL must not be persisted", deref(mcq.ImageURL))
	}
}

// -----------------------------------------------------------------------------
// SAVE-REVISION (PATCH) — the guard must run BEFORE the revision is appended,
// or a refused ref is persisted anyway and then served.
// -----------------------------------------------------------------------------

// seedQuestionNoImage creates a question with no image and returns its id.
func seedQuestionNoImage(t *testing.T, router http.Handler, atomID string) string {
	t.Helper()
	w := postQuestion(t, router, atomID, ownershipMCQBody(""))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create status=%d; body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	return created["question"].(map[string]any)["question_id"].(string)
}

func patchQuestionImage(t *testing.T, router http.Handler, atomID, qID, imageURL string) *httptest.ResponseRecorder {
	t.Helper()
	// PATCH rejects unknown fields and takes no `type` — only the payload + refs.
	body := map[string]any{
		"prompt": "edited",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "a", "label": "A", "is_correct": true, "explainer": "y"},
			{"option_id": "b", "label": "B", "is_correct": false, "explainer": "n"},
		}},
	}
	if imageURL != "" {
		body["image_url"] = imageURL
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+qID, body))
	return w
}

// assertPatchRefusedNothingAppended asserts the refused edit left the stored
// revision untouched — the foreign ref must not be in ANY persisted revision.
func assertPatchRefusedNothingAppended(t *testing.T, qRepo *fakeQuestionRepository, atomID string) {
	t.Helper()
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("latest revision = %d; want 1 — the refused edit must not be appended", rev.RevisionNumber)
	}
	if rev.MCQPayload == nil {
		t.Fatal("latest revision has nil MCQPayload")
	}
	if img := rev.MCQPayload.ImageURL; img != nil && *img != "" {
		t.Errorf("persisted image ref = %q; want none — the refused ref must not reach storage", *img)
	}
}

func TestPatchQuestion_ForeignTenantDurableRefRefusedAtomically(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)
	qID := seedQuestionNoImage(t, router, atomID)

	foreign := "s3://" + reHomeDurableBucket + "/tenants/someone-else/atoms/a1/secret.png"
	w := patchQuestionImage(t, router, atomID, qID, foreign)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("patch status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
	assertPatchRefusedNothingAppended(t, qRepo, atomID)
}

func TestPatchQuestion_ForeignTenantTransientRefRefusedAtomically(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)
	qID := seedQuestionNoImage(t, router, atomID)

	foreign := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/" + foreignTenantKey + "?X-Goog-Expires=604800"
	w := patchQuestionImage(t, router, atomID, qID, foreign)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("patch status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
	assertPatchRefusedNothingAppended(t, qRepo, atomID)
}

// An own-tenant transient ref on PATCH is re-homed in place, so the appended
// revision already carries the durable ref (one revision, not two).
func TestPatchQuestion_OwnTenantTransientRefReHomedInPlace(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)
	qID := seedQuestionNoImage(t, router, atomID)

	own := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	w := patchQuestionImage(t, router, atomID, qID, own)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status=%d; want 200; body=%s", w.Code, w.Body.String())
	}
	if *requests != 1 {
		t.Errorf("S3 endpoint received %d requests; want 1", *requests)
	}
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	want := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/" + atomID + "/"
	if rev.MCQPayload.ImageURL == nil || !strings.HasPrefix(*rev.MCQPayload.ImageURL, want) {
		t.Errorf("persisted image ref = %v; want a durable %s<uuid>.png ref", deref(rev.MCQPayload.ImageURL), want)
	}
	if rev.RevisionNumber != 2 {
		t.Errorf("latest revision = %d; want 2 (one edit revision carrying the durable ref)", rev.RevisionNumber)
	}
}

// The READ paths are gated too: a stored ref this tenant does not own is
// dropped rather than signed. Defence in depth for a legacy row written before
// the write seams were guarded — SignDownloadURL scopes on bucket only, and the
// durable bucket is shared by every tenant. The gate covers BOTH projections
// (author + learner) and BOTH ref kinds: a foreign durable s3:// URI AND a
// foreign TRANSIENT http(s) URL on the trusted origin (TenantOwnsRef
// classifies both). Only the durable case can be detected without the
// ownership-first ordering; the transient case passed straight through before
// it.
func TestReadPaths_ForeignTenantRefNotSigned(t *testing.T) {
	srv, _ := newCountingS3Endpoint(t)
	signer := newRealReHomer(t, srv.URL)

	durableForeign := "s3://" + reHomeDurableBucket + "/tenants/someone-else/atoms/a1/secret.png"
	transientForeign := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/someone-else/jobs/j1/secret.png?X-Goog-Expires=604800"

	// assertForeignDropped seeds the given ref directly and asserts BOTH HTTP
	// projections drop it (never emit it raw, never sign it).
	assertForeignDropped := func(t *testing.T, router http.Handler, qRepo *fakeQuestionRepository, atomID, where string) {
		t.Helper()
		q, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
		if err != nil {
			t.Fatalf("%s: seed GetByAtomID: %v", where, err)
		}

		// Author projection.
		wa := httptest.NewRecorder()
		router.ServeHTTP(wa, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+q.QuestionID, nil))
		if wa.Code != http.StatusOK {
			t.Fatalf("%s: author get: %d %s", where, wa.Code, wa.Body.String())
		}
		var author map[string]any
		_ = json.Unmarshal(wa.Body.Bytes(), &author)
		mcq := author["question"].(map[string]any)["mcq"].(map[string]any)
		if s, _ := mcq["image_url"].(string); s != "" {
			t.Errorf("%s: author projection image_url = %q; want the foreign ref dropped", where, s)
		}

		// Learner projection.
		wl := httptest.NewRecorder()
		router.ServeHTTP(wl, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
		if wl.Code != http.StatusOK {
			t.Fatalf("%s: learner get: %d %s", where, wl.Code, wl.Body.String())
		}
		var learner map[string]any
		_ = json.Unmarshal(wl.Body.Bytes(), &learner)
		qp, ok := learner["question_payload"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected question_payload; got %s", where, wl.Body.String())
		}
		if s, _ := qp["image_url"].(string); s != "" {
			t.Errorf("%s: learner projection image_url = %q; want the foreign ref dropped", where, s)
		}
	}

	t.Run("durable s3 ref", func(t *testing.T) {
		router, qRepo, atomID := seedQuestionRefsDirectWithReHomer(t, signer, durableForeign, "")
		assertForeignDropped(t, router, qRepo, atomID, "durable")
	})

	t.Run("transient http ref on the trusted origin", func(t *testing.T) {
		router, qRepo, atomID := seedQuestionRefsDirectWithReHomer(t, signer, transientForeign, "")
		assertForeignDropped(t, router, qRepo, atomID, "transient")
	})
}

// The ACCEPT seam's ownership gate also runs with no copier wired. The re-homer
// and the download signer are derived from the same adapter in production, but
// the handler must not be fail-open if only one is wired — this pins the
// wiring (handler.go SetMediaSigner) and the shared guard.
func TestAcceptQuestionJob_OwnershipEnforcedWithoutACopier(t *testing.T) {
	srv, _ := newCountingS3Endpoint(t)
	signer := newRealReHomer(t, srv.URL)

	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	jobRepo := newFakeJobRepo()
	host, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "Host", Body: "Body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	host.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), host); err != nil {
		t.Fatalf("atomRepo.Save: %v", err)
	}
	cand := mcqCandidate("d1", "s3://"+reHomeDurableBucket+"/"+foreignTenantKey, "")
	candJSON, err := json.Marshal([]map[string]any{cand})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	job := &question.ComposeJob{
		JobID: "01970000-0000-7000-8000-0000000ee0b2", AtomID: host.AtomID,
		TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Status: question.JobStatusSucceeded,
		CandidateQuestionsJSON: candJSON,
	}
	jobRepo.jobs[job.JobID] = job

	// ONLY the download signer is wired — no MediaReHomer.
	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                    atomRepo,
		QuestionRepository:      qRepo,
		QuestionJobRepository:   jobRepo,
		ManaLedger:              &fakeManaLedger{successDebits: 10},
		JobEventPublisher:       &fakeJobPublisher{},
		AtomMediaDownloadSigner: signer,
	})
	body, _ := json.Marshal(map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}}})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/atoms/"+host.AtomID+"/question-jobs/"+job.JobID+"/accept", bytes.NewReader(body))
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("accept status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	assertNoQuestionPersisted(t, qRepo, host.AtomID)
}

// A question with no image at all is unaffected by the new guard.
func TestCreateQuestion_NoImageUnaffected(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	w := postQuestion(t, router, atomID, ownershipMCQBody(""))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL != nil {
		t.Errorf("ImageURL = %v; want nil", deref(mcq.ImageURL))
	}
	if _, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID); errors.Is(err, question.ErrNotFound) {
		t.Error("question was not persisted")
	} else if err != nil {
		t.Errorf("GetByAtomID: %v; want the question persisted", err)
	}
}

// -----------------------------------------------------------------------------
// EVERY reference is validated BEFORE any copy.
// -----------------------------------------------------------------------------

// A legitimate stem ref must not be copied when the SIBLING answer ref is
// foreign — otherwise the request fails having already left an unreferenced
// durable object behind (an orphan the caller cannot clean up and must pay for
// again on retry).
func TestCreateQuestion_NoCopyWhenASiblingRefIsRejected(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouter(t, reHomer)

	body := ownershipMCQBody("https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800")
	body["answer_image_url"] = "s3://" + reHomeDurableBucket + "/" + foreignTenantKey

	w := postQuestion(t, router, atomID, body)
	assertCreateRefused(t, w, qRepo, atomID)
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 — every ref must be validated BEFORE any copy", *requests)
	}
}

// -----------------------------------------------------------------------------
// The OWNERSHIP gate does not depend on the copier being wired.
// -----------------------------------------------------------------------------

// With only the download signer wired (no re-homer) a foreign-tenant ref is
// still refused: the check needs the storage adapter's grammar, not its copier.
func TestCreateQuestion_OwnershipEnforcedWithoutACopier(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	signer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouterWith(t, nil, signer)

	foreign := "s3://" + reHomeDurableBucket + "/" + foreignTenantKey
	w := postQuestion(t, router, atomID, ownershipMCQBody(foreign))
	assertCreateRefused(t, w, qRepo, atomID)
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
}

// With only the signer wired an OWN-tenant ref is still accepted — the copy is
// simply skipped (there is no copier), and the ref is persisted as supplied.
func TestCreateQuestion_OwnTenantRefAcceptedWithoutACopier(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	signer := newRealReHomer(t, srv.URL)
	router, qRepo, atomID := newOwnershipRouterWith(t, nil, signer)

	own := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/a1/obj.png"
	w := postQuestion(t, router, atomID, ownershipMCQBody(own))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 (no copier)", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != own {
		t.Errorf("ImageURL = %v; want the unchanged ref %q", deref(mcq.ImageURL), own)
	}
}

// -----------------------------------------------------------------------------
// PATCH: the payload-omitting edit carries the PREVIOUS refs forward, so the
// pre-append guard must see them too.
// -----------------------------------------------------------------------------

// A prompt-only PATCH on a question holding a legacy foreign-tenant ref must be
// refused WITHOUT appending a revision — otherwise the ref is re-persisted by
// the very edit that was supposed to be rejected.
func TestPatchQuestion_PayloadOmittedCarryForwardStillGuarded(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	// Seed a question whose STORED ref is foreign — a legacy row the write seam
	// would refuse today, written directly.
	foreign := "s3://" + reHomeDurableBucket + "/" + foreignTenantKey
	srvRouter, qRepo, atomID := seedQuestionRefsDirectWithReHomer(t, reHomer, foreign, "")
	q, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("seed GetByAtomID: %v", err)
	}

	// A prompt-only edit: mcq_payload omitted ⇒ the previous payload (and its
	// foreign ref) is carried forward.
	w := httptest.NewRecorder()
	srvRouter.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+atomID+"/questions/"+q.QuestionID, map[string]any{"prompt": "edited"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("patch status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}

	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("latest revision = %d; want 1 — the refused edit must not be appended", rev.RevisionNumber)
	}
}

// A prompt-only PATCH on a question holding an own-tenant transient ref
// re-homes it: the carried-forward ref is durabilised rather than expiring.
func TestPatchQuestion_PayloadOmittedCarryForwardReHomed(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	own := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	srvRouter, qRepo, atomID := seedQuestionRefsDirectWithReHomer(t, reHomer, own, "")
	q, _, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("seed GetByAtomID: %v", err)
	}

	w := httptest.NewRecorder()
	srvRouter.ServeHTTP(w, authedJSON(http.MethodPatch,
		"/api/atoms/"+atomID+"/questions/"+q.QuestionID, map[string]any{"prompt": "edited"}))
	if w.Code != http.StatusOK {
		t.Fatalf("patch status=%d; want 200; body=%s", w.Code, w.Body.String())
	}
	if *requests != 1 {
		t.Errorf("S3 endpoint received %d requests; want 1 (the re-home copy)", *requests)
	}
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	want := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/" + atomID + "/"
	if rev.MCQPayload.ImageURL == nil || !strings.HasPrefix(*rev.MCQPayload.ImageURL, want) {
		t.Errorf("persisted image ref = %v; want a durable %s<uuid>.png ref", deref(rev.MCQPayload.ImageURL), want)
	}
}

// -----------------------------------------------------------------------------
// CLONE is a write seam.
// -----------------------------------------------------------------------------

// seedQuestionRefsDirectWithReHomer seeds an MCQ atom + question carrying the
// given refs directly into the repo, and wires the given re-homer as both the
// copier and the download signer.
func seedQuestionRefsDirectWithReHomer(t *testing.T, signer *mediastore.AtomMediaSigner, img, ans string) (http.Handler, *fakeQuestionRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "Clone source", Body: "b", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save atom: %v", err)
	}
	seedMCQWithImages(t, qRepo, a.AtomID, img, ans)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo,
		MediaReHomer: signer, AtomMediaDownloadSigner: signer,
	})
	return srv, qRepo, a.AtomID
}

func cloneAtom(t *testing.T, router http.Handler, srcAtomID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+srcAtomID+"/clone", map[string]any{}))
	return w
}

// A source question carrying a FOREIGN ref must not be copied into a clone: the
// source lookup is tenant-scoped, but that says nothing about the media refs the
// stored row carries.
func TestCloneAtom_ForeignTenantRefRefused(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	foreign := "s3://" + reHomeDurableBucket + "/" + foreignTenantKey
	router, qRepo, srcAtomID := seedQuestionRefsDirectWithReHomer(t, reHomer, foreign, "")

	w := cloneAtom(t, router, srcAtomID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("clone status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
	// The refusal happens BEFORE the clone atom is saved, so nothing new exists.
	if _, _, err := qRepo.GetByAtomID(context.Background(), tenantA, srcAtomID); err != nil {
		t.Errorf("source question lookup: %v", err)
	}
}

// A source question carrying an own-tenant TRANSIENT ref is cloned with a
// DURABLE ref — the clone must not inherit an image that expires with the
// transient TTL.
func TestCloneAtom_OwnTenantTransientRefBecomesDurable(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	own := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	router, qRepo, srcAtomID := seedQuestionRefsDirectWithReHomer(t, reHomer, own, "")

	w := cloneAtom(t, router, srcAtomID)
	if w.Code != http.StatusCreated {
		t.Fatalf("clone status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 1 {
		t.Errorf("S3 endpoint received %d requests; want 1 (the clone's re-home copy)", *requests)
	}
	var cloned map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &cloned)
	cloneID, _ := cloned["atom_id"].(string)
	if cloneID == "" || cloneID == srcAtomID {
		t.Fatalf("cloned atom id = %q; want a new atom", cloneID)
	}
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, cloneID)
	if err != nil {
		t.Fatalf("cloned question lookup: %v", err)
	}
	want := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/" + cloneID + "/"
	if rev.MCQPayload.ImageURL == nil || !strings.HasPrefix(*rev.MCQPayload.ImageURL, want) {
		t.Errorf("cloned image ref = %v; want a durable %s<uuid>.png ref", deref(rev.MCQPayload.ImageURL), want)
	}

	// The SOURCE question is never mutated by the clone's re-home.
	_, srcRev, err := qRepo.GetByAtomID(context.Background(), tenantA, srcAtomID)
	if err != nil {
		t.Fatalf("source question lookup: %v", err)
	}
	if srcRev.MCQPayload.ImageURL == nil || *srcRev.MCQPayload.ImageURL != own {
		t.Errorf("source ref = %v; want the ORIGINAL %q untouched", deref(srcRev.MCQPayload.ImageURL), own)
	}
}

// -----------------------------------------------------------------------------
// EXTERNAL-IMAGE MODE (neither re-homer nor download signer wired).
//
// With no storage adapter at all there is no grammar to classify a reference
// with, so the write seam persists refs verbatim WITHOUT any ownership
// classification. This is a documented deployment shape that does NOT enforce
// the tenant-isolation invariant — these tests pin exactly what the mode does
// and does not guarantee, so nobody mistakes it for an ownership pass.
// -----------------------------------------------------------------------------

// In external-image mode a foreign-tenant DURABLE ref is persisted verbatim —
// no ownership classification is possible without a storage adapter. The
// mode's one positive guarantee: with no signer nothing can mint the ref, so
// the read projection drops it (the image never renders).
func TestCreateQuestion_ExternalImageMode_ForeignDurableRefPersistedVerbatim(t *testing.T) {
	_, requests := newCountingS3Endpoint(t)
	router, qRepo, atomID := newOwnershipRouterWith(t, nil, nil)

	foreign := "s3://" + reHomeDurableBucket + "/" + foreignTenantKey
	w := postQuestion(t, router, atomID, ownershipMCQBody(foreign))
	if w.Code != http.StatusCreated {
		t.Fatalf("external-image mode must persist the ref verbatim: create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != foreign {
		t.Errorf("ImageURL = %v; want the verbatim ref %q (external-image mode classifies nothing)", deref(mcq.ImageURL), foreign)
	}

	// The mode's positive guarantee: with no signer the durable ref can never
	// be served, so the learner projection drops it.
	wl := httptest.NewRecorder()
	router.ServeHTTP(wl, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("learner get: %d %s", wl.Code, wl.Body.String())
	}
	var learner map[string]any
	_ = json.Unmarshal(wl.Body.Bytes(), &learner)
	if qp, ok := learner["question_payload"].(map[string]any); ok {
		if s, _ := qp["image_url"].(string); s != "" {
			t.Errorf("learner projection image_url = %q; want the unmintable durable ref dropped", s)
		}
	}
}

// In external-image mode an http(s) ref is persisted verbatim AND served as
// supplied — it is a URL its holder can already fetch, and with no signer
// nothing rewrites or drops it. The mode documents this behaviour explicitly;
// it is NOT an ownership pass (nothing classified the ref).
func TestCreateQuestion_ExternalImageMode_TransientRefServedVerbatim(t *testing.T) {
	router, qRepo, atomID := newOwnershipRouterWith(t, nil, nil)

	transient := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/someone-else/jobs/j1/secret.png?X-Goog-Expires=604800"
	w := postQuestion(t, router, atomID, ownershipMCQBody(transient))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d; want 201; body=%s", w.Code, w.Body.String())
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != transient {
		t.Errorf("ImageURL = %v; want the verbatim ref %q", deref(mcq.ImageURL), transient)
	}

	// Served as supplied (no signer ⇒ nothing mints or drops it).
	wl := httptest.NewRecorder()
	router.ServeHTTP(wl, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("learner get: %d %s", wl.Code, wl.Body.String())
	}
	var learner map[string]any
	_ = json.Unmarshal(wl.Body.Bytes(), &learner)
	qp, ok := learner["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload; got %s", wl.Body.String())
	}
	if qp["image_url"] != transient {
		t.Errorf("image_url = %v; want the transient ref served verbatim in external-image mode", qp["image_url"])
	}
}
