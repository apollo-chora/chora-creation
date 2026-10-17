// accept_media_ownership_test.go — the P1 scenario at the real accept seam.
//
// The accept handler PREFERS a client-supplied image_url over the stored
// draft's (question_jobs_handler.go: `if ac.ImageURL != nil { ... }`), so a
// caller can name ANY object in the transient bucket. The copy then runs with
// chora-creation's own S3 credentials. A trusted origin + bucket does not prove
// ownership, so both the re-home service and the adapter refuse a source key
// outside the accepting tenant's namespace.
//
// These tests wire the REAL S3 adapter behind the real accept route: a
// foreign-tenant source must fail loud with NO copy attempted, and the tenant's
// own object must still re-home.
package httpadapter_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
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

func TestAcceptQuestionJob_ForeignTenantImageRefused_NoCopy(t *testing.T) {
	srv, requests := newCountingS3Endpoint(t)
	reHomer := newRealReHomer(t, srv.URL)

	// A client-supplied URL naming ANOTHER tenant's object in the transient
	// bucket — a well-formed, correctly-origined, correctly-bucketed ref.
	foreign := "https://storage.googleapis.com/" + reHomeTransientBucket +
		"/tenants/someone-else/jobs/j1/secret.png?X-Goog-Expires=604800"
	w, _, _ := acceptCandWithRehome(t, reHomer, mcqCandidate("d1", foreign, ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("accept with a foreign-tenant image status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
	if *requests != 0 {
		t.Errorf("S3 endpoint received %d requests; want 0 (no cross-tenant copy)", *requests)
	}
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
