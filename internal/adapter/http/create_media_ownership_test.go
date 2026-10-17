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
)

const foreignTenantKey = "tenants/someone-else/jobs/j1/secret.png"

// newOwnershipRouter wires the REAL S3 adapter as BOTH the re-homer and the
// download signer over an MCQ atom, and returns the router + question repo.
func newOwnershipRouter(t *testing.T, signer *mediastore.AtomMediaSigner) (http.Handler, *fakeQuestionRepository, string) {
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
		MediaReHomer: signer, AtomMediaDownloadSigner: signer,
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
