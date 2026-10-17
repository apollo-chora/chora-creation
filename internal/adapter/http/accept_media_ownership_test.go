// accept_media_ownership_test.go — the ownership boundary at the real accept
// seam.
//
// The accept handler PREFERS a client-supplied image_url over the stored
// draft's (question_jobs_handler.go: `if ac.ImageURL != nil { ... }`), so a
// caller can name ANY object in the transient OR durable bucket. The copy then
// runs with chora-creation's own S3 credentials, and the read signer scopes on
// bucket only — so a foreign ref that reached persistence would later be signed
// and served.
//
// These tests wire the REAL S3 adapter behind the real accept route and assert
// three things for a refusal: fail-loud 500, NO S3 copy request, and NO
// persisted question (so no revision can carry the supplied ref).
package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// newCountingS3Endpoint returns a stub S3 endpoint that answers every copy with
// a success envelope and reports how many requests it saw, so a test can prove
// no cross-tenant copy was attempted.
func newCountingS3Endpoint(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`))
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func newRealReHomer(t *testing.T, s3Endpoint string) *mediastore.AtomMediaSigner {
	t.Helper()
	t.Setenv("S3_ENDPOINT", s3Endpoint)
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")
	t.Setenv("ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN", "")
	signer, err := mediastore.NewAtomMediaSigner(reHomeDurableBucket,
		mediastore.WithTransientBucket(reHomeTransientBucket),
		mediastore.WithTrustedPublicOrigin("https://storage.googleapis.com"))
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	return signer
}

// assertNoQuestionPersisted asserts a refused accept left no question row on the
// target atom, so no revision can carry the client-supplied ref. The route atom
// itself may have been re-saved before the re-home call — that is the handler's
// pre-existing ordering, deliberately unchanged here.
func assertNoQuestionPersisted(t *testing.T, qRepo ports.QuestionRepository, atomID string) {
	t.Helper()
	q, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if !errors.Is(err, question.ErrNotFound) {
		t.Fatalf("GetByAtomID(%s) err = %v (q=%v rev=%v); want ErrNotFound — a refused accept must persist no question",
			atomID, err, q, rev)
	}
}

func assertAcceptRefused(t *testing.T, code int, body string, requests int) {
	t.Helper()
	if code != http.StatusInternalServerError {
		t.Fatalf("accept status=%d; want 500; body=%s", code, body)
	}
	if !strings.Contains(body, "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", body)
	}
	if requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 (no cross-tenant copy)", requests)
	}
}

func TestAcceptQuestionJob_ForeignTenantImageRefused_NoCopy(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	// A client-supplied URL naming ANOTHER tenant's object in the transient
	// bucket — a well-formed, correctly-origined, correctly-bucketed ref.
	foreign := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/someone-else/jobs/j1/secret.png?X-Goog-Expires=604800"
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", foreign, ""))
	assertAcceptRefused(t, w.Code, w.Body.String(), *requests)
	assertNoQuestionPersisted(t, qRepo, atomID)
}

// The DURABLE bypass at the real accept seam. ClassifyReference calls this ref
// Durable — it is well-formed and names the configured durable bucket — so
// before the ownership check covered durable refs, ReHome returned it verbatim
// and the foreign ref would have been persisted, then signed and served by the
// read path.
func TestAcceptQuestionJob_ForeignTenantDurableRefRefused(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	foreign := "s3://" + reHomeDurableBucket + "/tenants/someone-else/atoms/a1/secret.png"
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", foreign, ""))
	assertAcceptRefused(t, w.Code, w.Body.String(), *requests)
	assertNoQuestionPersisted(t, qRepo, atomID)
}

func TestAcceptQuestionJob_OwnTenantImageStillReHomes(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	own := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", own, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("accept with the tenant's own image status=%d; want 200; body=%s", w.Code, w.Body.String())
	}
	if *requests == 0 {
		t.Error("S3 endpoint received 0 requests; want the legitimate copy")
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, "s3://"+reHomeDurableBucket+"/") {
		t.Errorf("ImageURL = %v; want a durable s3:// ref", deref(mcq.ImageURL))
	}
}

// A same-tenant DURABLE ref is accepted verbatim (idempotent, no copy) — the
// ownership check must not break the already-durable fast path.
func TestAcceptQuestionJob_OwnTenantDurableRefAcceptedUnchanged(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	own := "s3://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/a1/obj.png"
	w, qRepo, atomID := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", own, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("accept with the tenant's own durable ref status=%d; want 200; body=%s", w.Code, w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 (already durable — no copy)", *requests)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != own {
		t.Errorf("ImageURL = %v; want the unchanged durable ref %q", deref(mcq.ImageURL), own)
	}
}
