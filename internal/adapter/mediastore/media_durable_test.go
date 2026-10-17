// media_durable_test.go — coverage for the durable atom-media
// additions to the S3 adapter (W8 re-home OT#4):
//   - ClassifyReference validates a stored media ref against the configured
//     durable + transient buckets and reports transient vs durable, or
//     fails loud on a foreign / malformed / unconfigured one.
//   - SignDownloadURL mints a fresh presigned GET URL over an s3:// durable
//     object and REFUSES a foreign bucket / malformed URI.
//   - CopyToDurable fails loud when required params are missing and returns
//     an s3:// URI (the contract the re-home service relies on).
//
// The live cross-bucket copy + live GET signing are exercised by the
// cluster e2e verify (the copy needs a real S3 endpoint + the durable
// bucket); these unit tests cover the pure parse/guard logic the adapter
// owns before the network is touched, mirroring signed_url_test.go.
package mediastore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const (
	testDurableBucket   = "chora-atom-media"
	testTransientBucket = "chora-ai-assist-images"
)

func newTestSigner(t *testing.T) *mediastore.AtomMediaSigner {
	t.Helper()
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	// Keep classification deterministic regardless of the developer's env.
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")
	t.Setenv("ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN", "")
	signer, err := mediastore.NewAtomMediaSigner(testDurableBucket)
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	return signer
}

// newTestSignerWithTransient builds a signer whose transient bucket is
// configured — the Garage/S3 deployment shape.
func newTestSignerWithTransient(t *testing.T) *mediastore.AtomMediaSigner {
	t.Helper()
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")
	t.Setenv("ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN", "")
	signer, err := mediastore.NewAtomMediaSigner(testDurableBucket,
		mediastore.WithTransientBucket(testTransientBucket))
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	return signer
}

// SignDownloadURL happy path — mints a presigned GET URL embedding the object key.
func TestAtomMediaSigner_SignDownloadURL_HappyPath(t *testing.T) {

	signer := newTestSigner(t)

	s3URI := "s3://" + testDurableBucket + "/tenants/t1/atoms/a1/obj1.png"
	out, err := signer.SignDownloadURL(context.Background(), s3URI)
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}
	if out.URL == "" {
		t.Fatal("SignDownloadURL returned empty URL")
	}
	// The key path must appear in the presigned URL.
	if !strings.Contains(out.URL, "tenants/t1/atoms/a1/obj1.png") {
		t.Errorf("URL %q should contain the object key", out.URL)
	}
	// Bucket host/path present.
	if !strings.Contains(out.URL, testDurableBucket) {
		t.Errorf("URL %q should reference the durable bucket", out.URL)
	}
	// Short-lived, future expiry.
	if !out.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt %v should be in the future", out.ExpiresAt)
	}
	if out.ExpiresAt.After(time.Now().Add(2 * time.Hour)) {
		t.Errorf("ExpiresAt %v should be short-lived (<= ~1h)", out.ExpiresAt)
	}
}

// SignDownloadURL refuses an s3:// URI for any bucket other than the
// configured durable bucket — a caller MUST NOT coerce a URL for an
// arbitrary bucket (security).
func TestAtomMediaSigner_SignDownloadURL_RejectsForeignBucket(t *testing.T) {

	signer := newTestSigner(t)

	_, err := signer.SignDownloadURL(context.Background(), "s3://some-other-bucket/tenants/t/atoms/a/x.png")
	if err == nil {
		t.Fatal("SignDownloadURL should reject a foreign bucket; got nil")
	}
	if !strings.Contains(err.Error(), "some-other-bucket") && !strings.Contains(strings.ToLower(err.Error()), "bucket") {
		t.Errorf("error %q should mention the refused bucket", err.Error())
	}
}

// SignDownloadURL rejects malformed / non-s3:// input.
func TestAtomMediaSigner_SignDownloadURL_RejectsMalformed(t *testing.T) {

	signer := newTestSigner(t)

	for _, bad := range []string{
		"",
		"https://s3.amazonaws.com/chora-atom-media/x.png",
		"s3://",
		"s3://bucket-only",
		"s3://" + testDurableBucket + "/", // empty key
	} {
		if _, err := signer.SignDownloadURL(context.Background(), bad); err == nil {
			t.Errorf("SignDownloadURL(%q) should error; got nil", bad)
		}
	}
}

// CopyToDurable fails loud when required params are missing.
func TestAtomMediaSigner_CopyToDurable_RequiresParams(t *testing.T) {

	signer := newTestSigner(t)

	for _, tc := range []struct {
		name   string
		params ports.CopyToDurableParams
	}{
		{"missing src bucket", ports.CopyToDurableParams{SrcKey: "k", TenantID: "t", AtomID: "a"}},
		{"missing src key", ports.CopyToDurableParams{SrcBucket: "b", TenantID: "t", AtomID: "a"}},
		{"missing tenant", ports.CopyToDurableParams{SrcBucket: "b", SrcKey: "k", AtomID: "a"}},
		{"missing atom", ports.CopyToDurableParams{SrcBucket: "b", SrcKey: "k", TenantID: "t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := signer.CopyToDurable(context.Background(), tc.params)
			if err == nil {
				t.Fatal("CopyToDurable with missing params should error; got nil")
			}
		})
	}
}

// DurableBucket exposes the configured destination bucket — the bucket the
// adapter authorises durable refs against.
func TestAtomMediaSigner_DurableBucket(t *testing.T) {

	signer := newTestSigner(t)
	if got := signer.DurableBucket(); got != testDurableBucket {
		t.Errorf("DurableBucket() = %q; want %q", got, testDurableBucket)
	}
}

// -----------------------------------------------------------------------------
// ClassifyReference — the adapter owns storage-reference grammar + validation.
// -----------------------------------------------------------------------------

// A durable ref (s3:// — what this adapter itself persists — or gs:// for a
// GCS deployment) on the durable bucket classifies Durable with its
// coordinates split out.
func TestClassifyReference_DurableRefs(t *testing.T) {
	signer := newTestSigner(t)

	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"s3 scheme", "s3://" + testDurableBucket + "/tenants/t1/atoms/a1/obj.png"},
		{"gs scheme", "gs://" + testDurableBucket + "/tenants/t1/atoms/a1/obj.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := signer.ClassifyReference(context.Background(), tc.ref)
			if err != nil {
				t.Fatalf("ClassifyReference(%q) err = %v; want nil", tc.ref, err)
			}
			if got.Kind != ports.MediaReferenceDurable {
				t.Errorf("Kind = %v; want MediaReferenceDurable", got.Kind)
			}
			if got.Bucket != testDurableBucket {
				t.Errorf("Bucket = %q; want %q", got.Bucket, testDurableBucket)
			}
			if got.Key != "tenants/t1/atoms/a1/obj.png" {
				t.Errorf("Key = %q; want tenants/t1/atoms/a1/obj.png", got.Key)
			}
		})
	}
}

// A durable ref naming a foreign bucket is refused — we never re-home an
// object we are not configured for.
func TestClassifyReference_ForeignDurableBucket_Errors(t *testing.T) {
	signer := newTestSigner(t)

	for _, ref := range []string{
		"s3://some-other-bucket/tenants/t1/atoms/a1/obj.png",
		"gs://some-other-bucket/tenants/t1/atoms/a1/obj.png",
	} {
		_, err := signer.ClassifyReference(context.Background(), ref)
		if err == nil {
			t.Errorf("ClassifyReference(%q) should error; got nil", ref)
			continue
		}
		if !strings.Contains(err.Error(), "some-other-bucket") {
			t.Errorf("error %q should name the refused bucket", err.Error())
		}
	}
}

// The production case: a Garage/S3 path-style presigned URL on the
// configured transient bucket classifies Transient, with the signature
// stripped from the key.
func TestClassifyReference_TransientGarageURL(t *testing.T) {
	signer := newTestSignerWithTransient(t)

	ref := "http://146.235.20.25:9000/" + testTransientBucket +
		"/tenants/t1/jobs/j9/abc123.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=604800&X-Amz-Signature=deadbeef"
	got, err := signer.ClassifyReference(context.Background(), ref)
	if err != nil {
		t.Fatalf("ClassifyReference(%q) err = %v; want nil", ref, err)
	}
	if got.Kind != ports.MediaReferenceTransient {
		t.Errorf("Kind = %v; want MediaReferenceTransient", got.Kind)
	}
	if got.Bucket != testTransientBucket {
		t.Errorf("Bucket = %q; want %q", got.Bucket, testTransientBucket)
	}
	if got.Key != "tenants/t1/jobs/j9/abc123.png" {
		t.Errorf("Key = %q; want tenants/t1/jobs/j9/abc123.png (signature stripped)", got.Key)
	}
}

// A GCS path-style signed URL on the transient bucket classifies Transient
// too (the same shape, a different host).
func TestClassifyReference_TransientGCSURL(t *testing.T) {
	signer := newTestSignerWithTransient(t)

	ref := "https://storage.googleapis.com/" + testTransientBucket +
		"/tenants/t1/jobs/j9/abc.png?X-Goog-Expires=604800"
	got, err := signer.ClassifyReference(context.Background(), ref)
	if err != nil {
		t.Fatalf("ClassifyReference(%q) err = %v; want nil", ref, err)
	}
	if got.Kind != ports.MediaReferenceTransient {
		t.Errorf("Kind = %v; want MediaReferenceTransient", got.Kind)
	}
	if got.Bucket != testTransientBucket || got.Key != "tenants/t1/jobs/j9/abc.png" {
		t.Errorf("(Bucket,Key) = (%q,%q); want (%q,tenants/t1/jobs/j9/abc.png)", got.Bucket, got.Key, testTransientBucket)
	}
}

// An http(s) ref on a bucket that is neither the transient nor the durable
// one is refused.
func TestClassifyReference_HTTPForeignBucket_Errors(t *testing.T) {
	signer := newTestSignerWithTransient(t)

	ref := "http://146.235.20.25:9000/some-other-bucket/tenants/t1/jobs/j9/x.png?X-Amz-Signature=deadbeef"
	_, err := signer.ClassifyReference(context.Background(), ref)
	if err == nil {
		t.Fatal("ClassifyReference(foreign http bucket) should error; got nil")
	}
	if !strings.Contains(err.Error(), "some-other-bucket") {
		t.Errorf("error %q should name the refused bucket", err.Error())
	}
}

// An http(s) ref cannot be classified when no transient bucket is
// configured — fail loud, never guess a bucket.
func TestClassifyReference_HTTPWithoutTransientBucket_Errors(t *testing.T) {
	signer := newTestSigner(t) // no transient bucket configured

	ref := "http://146.235.20.25:9000/" + testTransientBucket + "/tenants/t1/jobs/j9/x.png?X-Amz-Signature=deadbeef"
	_, err := signer.ClassifyReference(context.Background(), ref)
	if err == nil {
		t.Fatal("ClassifyReference(http) without a transient bucket should error; got nil")
	}
	if !strings.Contains(err.Error(), "transient bucket is not configured") {
		t.Errorf("error %q should say the transient bucket is not configured", err.Error())
	}
}

// A configured trusted public origin pins the host an http(s) ref may come
// from; an unconfigured one accepts any host.
func TestClassifyReference_TrustedPublicOrigin(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")

	signer, err := mediastore.NewAtomMediaSigner(testDurableBucket,
		mediastore.WithTransientBucket(testTransientBucket),
		mediastore.WithTrustedPublicOrigin("http://146.235.20.25:9000"))
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	ok := "http://146.235.20.25:9000/" + testTransientBucket + "/tenants/t1/jobs/j9/x.png?sig=1"
	if _, err := signer.ClassifyReference(context.Background(), ok); err != nil {
		t.Errorf("ClassifyReference(trusted host) err = %v; want nil", err)
	}

	bad := "http://evil.example.com/" + testTransientBucket + "/tenants/t1/jobs/j9/x.png?sig=1"
	_, err = signer.ClassifyReference(context.Background(), bad)
	if err == nil {
		t.Fatal("ClassifyReference(untrusted host) should error; got nil")
	}
	if !strings.Contains(err.Error(), "evil.example.com") {
		t.Errorf("error %q should name the untrusted host", err.Error())
	}
}

// Malformed / unrecognized refs are errors, never a silent pass-through.
func TestClassifyReference_Malformed_Errors(t *testing.T) {
	signer := newTestSignerWithTransient(t)

	for _, ref := range []string{
		"",
		"   ",
		"s3://",
		"s3://" + testDurableBucket,
		"s3://" + testDurableBucket + "/",
		"gs://",
		"gs://" + testDurableBucket + "/",
		"http://146.235.20.25:9000/" + testTransientBucket,
		"http://146.235.20.25:9000/" + testTransientBucket + "/",
		"ftp://146.235.20.25:9000/" + testTransientBucket + "/k.png",
		"not a url at all ::::",
	} {
		if _, err := signer.ClassifyReference(context.Background(), ref); err == nil {
			t.Errorf("ClassifyReference(%q) should error; got nil", ref)
		}
	}
}

// -----------------------------------------------------------------------------
// CopyToDurable — pins the s3:// URI contract the re-home service persists.
// -----------------------------------------------------------------------------

// CopyToDurable issues the server-side copy against the durable bucket and
// returns an s3:// URI (not gs://) — the contract the S3 deployment's
// stored refs and the idempotency check depend on.
func TestAtomMediaSigner_CopyToDurable_ReturnsS3URI(t *testing.T) {
	var gotCopySource, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCopySource = r.Header.Get("X-Amz-Copy-Source")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"etag"</ETag></CopyObjectResult>`))
	}))
	defer srv.Close()

	t.Setenv("S3_ENDPOINT", srv.URL)
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")

	signer, err := mediastore.NewAtomMediaSigner(testDurableBucket)
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	uri, err := signer.CopyToDurable(context.Background(), ports.CopyToDurableParams{
		SrcBucket: testTransientBucket,
		SrcKey:    "tenants/t1/jobs/j9/x.png",
		TenantID:  "t1",
		AtomID:    "a1",
		Ext:       "png",
	})
	if err != nil {
		t.Fatalf("CopyToDurable: %v", err)
	}
	if !strings.HasPrefix(uri, "s3://"+testDurableBucket+"/tenants/t1/atoms/a1/") {
		t.Errorf("URI = %q; want prefix s3://%s/tenants/t1/atoms/a1/", uri, testDurableBucket)
	}
	if !strings.HasSuffix(uri, ".png") {
		t.Errorf("URI = %q; want a .png key", uri)
	}
	if !strings.HasPrefix(gotPath, "/"+testDurableBucket+"/tenants/t1/atoms/a1/") {
		t.Errorf("copy PUT path = %q; want the durable bucket + per-atom key", gotPath)
	}
	if gotCopySource != testTransientBucket+"/tenants/t1/jobs/j9/x.png" {
		t.Errorf("X-Amz-Copy-Source = %q; want %s/tenants/t1/jobs/j9/x.png", gotCopySource, testTransientBucket)
	}
}
