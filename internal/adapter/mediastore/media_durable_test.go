// media_durable_test.go — coverage for the durable atom-media
// additions to the S3 adapter (W8 re-home OT#4):
//   - SignDownloadURL mints a fresh presigned GET URL over an s3:// durable
//     object and REFUSES a foreign bucket / malformed URI.
//   - CopyToDurable fails loud when required params are missing.
//
// The live cross-bucket copy + live GET signing are exercised by the
// cluster e2e verify (the copy needs a real S3 endpoint + the durable
// bucket); these unit tests cover the pure parse/guard logic the adapter
// owns before the network is touched, mirroring signed_url_test.go.
package mediastore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const testDurableBucket = "chora-atom-media"

func newTestSigner(t *testing.T) *mediastore.AtomMediaSigner {
	t.Helper()
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	signer, err := mediastore.NewAtomMediaSigner(testDurableBucket)
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

// DurableBucket exposes the configured destination bucket for the re-home
// service idempotency check.
func TestAtomMediaSigner_DurableBucket(t *testing.T) {

	signer := newTestSigner(t)
	if got := signer.DurableBucket(); got != testDurableBucket {
		t.Errorf("DurableBucket() = %q; want %q", got, testDurableBucket)
	}
}
