// signed_url_test.go — coverage for the S3-backed AtomMediaSigner
// adapter (ATOM Phase 1 — ADR-156).
//
// Presigning is a local operation (no network I/O), but the constructor
// requires S3 credentials from the environment. Unit tests here cover
// the pure-function concerns the adapter owns:
//   - constructor rejects empty bucket name (returns
//     ErrAtomMediaSignerNotWired so main.go can route the handler to
//     503).
//   - constructor rejects missing S3 endpoint / credentials.
//   - object-key derivation locked at
//     `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}` with the
//     extension derived from MIME (per ADR-156 Phase 1 §5).
//   - object-URL format is `s3://{bucket}/{key}`.
//   - MIME→ext mapping covers the Phase 1 enum
//     (image/jpeg | image/png | image/webp).
//   - per-tenant default cap is 2 MiB (the Phase 1 floor).
package mediastore_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Constructor — fail-loud on missing bucket
// -----------------------------------------------------------------------------

func TestNewAtomMediaSigner_RejectsEmptyBucket(t *testing.T) {

	_, err := mediastore.NewAtomMediaSigner("")
	if !errors.Is(err, ports.ErrAtomMediaSignerNotWired) {
		t.Errorf("NewAtomMediaSigner with empty bucket should return ErrAtomMediaSignerNotWired; got %v", err)
	}
}

func TestNewAtomMediaSigner_RejectsWhitespaceBucket(t *testing.T) {

	_, err := mediastore.NewAtomMediaSigner("   ")
	if !errors.Is(err, ports.ErrAtomMediaSignerNotWired) {
		t.Errorf("NewAtomMediaSigner with whitespace bucket should return ErrAtomMediaSignerNotWired; got %v", err)
	}
}

func TestNewAtomMediaSigner_RejectsMissingEndpoint(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	_, err := mediastore.NewAtomMediaSigner("some-bucket")
	if !errors.Is(err, ports.ErrAtomMediaSignerNotWired) {
		t.Errorf("NewAtomMediaSigner without S3_ENDPOINT should return ErrAtomMediaSignerNotWired; got %v", err)
	}
}

func TestNewAtomMediaSigner_RejectsMissingCredentials(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")

	_, err := mediastore.NewAtomMediaSigner("some-bucket")
	if !errors.Is(err, ports.ErrAtomMediaSignerNotWired) {
		t.Errorf("NewAtomMediaSigner without credentials should return ErrAtomMediaSignerNotWired; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Pure-function key + URI derivation
// -----------------------------------------------------------------------------

func TestAtomMediaKey_TenantAtomScheme(t *testing.T) {

	tenantID := "22222222-2222-7222-8222-222222222222"
	atomID := "33333333-3333-7333-8333-333333333333"
	objectID := "44444444-4444-7444-8444-444444444444"
	ext := "png"
	got := mediastore.AtomMediaKey(tenantID, atomID, objectID, ext)
	want := "tenants/22222222-2222-7222-8222-222222222222/atoms/33333333-3333-7333-8333-333333333333/44444444-4444-7444-8444-444444444444.png"
	if got != want {
		t.Errorf("AtomMediaKey = %q; want %q", got, want)
	}
}

func TestAtomMediaURI_S3Scheme(t *testing.T) {

	bucket := "chora-atom-media"
	key := "tenants/t1/atoms/a1/obj1.jpg"
	got := mediastore.AtomMediaURI(bucket, key)
	want := "s3://chora-atom-media/tenants/t1/atoms/a1/obj1.jpg"
	if got != want {
		t.Errorf("AtomMediaURI = %q; want %q", got, want)
	}
}

// -----------------------------------------------------------------------------
// MIME → extension mapping (Phase 1 enum)
// -----------------------------------------------------------------------------

func TestExtensionForMIME_HappyPath(t *testing.T) {

	cases := []struct {
		mime string
		want string
	}{
		{"image/jpeg", "jpg"},
		{"image/png", "png"},
		{"image/webp", "webp"},
	}
	for _, c := range cases {
		t.Run(c.mime, func(t *testing.T) {
			got, err := mediastore.ExtensionForMIME(c.mime)
			if err != nil {
				t.Fatalf("ExtensionForMIME(%q) err = %v; want nil", c.mime, err)
			}
			if got != c.want {
				t.Errorf("ExtensionForMIME(%q) = %q; want %q", c.mime, got, c.want)
			}
		})
	}
}

func TestExtensionForMIME_RejectsUnknown(t *testing.T) {

	cases := []string{"image/gif", "application/pdf", "text/plain", "", "image/JPEG", "  image/png  "}
	for _, mime := range cases {
		t.Run(mime, func(t *testing.T) {
			if _, err := mediastore.ExtensionForMIME(mime); err == nil {
				t.Errorf("ExtensionForMIME(%q) expected error for unsupported MIME", mime)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Default per-tenant size cap (Phase 1 floor: 2 MiB)
// -----------------------------------------------------------------------------

func TestDefaultMaxSizeBytes_Is2MiB(t *testing.T) {

	got := mediastore.DefaultMaxSizeBytes()
	const want int64 = 2 * 1024 * 1024
	if got != want {
		t.Errorf("DefaultMaxSizeBytes = %d; want %d (Phase 1 2 MiB floor)", got, want)
	}
}

// -----------------------------------------------------------------------------
// URL TTL (Phase 1: 15 minutes)
// -----------------------------------------------------------------------------

func TestDefaultSignedURLTTL_Is15Minutes(t *testing.T) {

	got := mediastore.DefaultSignedURLTTL()
	if got.Minutes() != 15 {
		t.Errorf("DefaultSignedURLTTL = %v; want 15m0s (Phase 1 default)", got)
	}
}

// -----------------------------------------------------------------------------
// Adapter exposes its bucket name (helps the HTTP handler emit ObjectURL
// for the response envelope before it dispatches to Sign).
// -----------------------------------------------------------------------------

func TestAtomMediaSigner_BucketAccessor(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	if signer == nil {
		t.Fatal("signer is nil")
	}
	if got := signer.Bucket(); got != "chora-atom-media" {
		t.Errorf("Bucket() = %q; want chora-atom-media", got)
	}
}

// -----------------------------------------------------------------------------
// Sign without S3 credentials fails loud.
// -----------------------------------------------------------------------------

func TestAtomMediaSigner_SignWithoutCredentials_ReturnsError(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")

	_, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err == nil {
		t.Fatal("NewAtomMediaSigner without credentials should fail")
	}
}

// -----------------------------------------------------------------------------
// Default sign timeout is sensible (well under the 30s gateway timeout).
// -----------------------------------------------------------------------------

func TestAtomMediaSigner_DefaultSignTimeout(t *testing.T) {

	got := mediastore.DefaultSignTimeout()
	if got < time.Second || got > 30*time.Second {
		t.Errorf("DefaultSignTimeout = %v; want 1s < d < 30s (must be < gateway 30s)", got)
	}
}

// -----------------------------------------------------------------------------
// Sign mints a presigned PUT URL when credentials are present.
// -----------------------------------------------------------------------------

func TestAtomMediaSigner_SignMintsPresignedURL(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	out, err := signer.Sign(context.Background(), "atom-id", "tenant-id", ports.SignAtomMediaInput{
		MIME:      "image/png",
		SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.HasPrefix(out.UploadURL, "http://localhost:9000/chora-atom-media/tenants/tenant-id/atoms/atom-id/") {
		t.Errorf("UploadURL = %q; want presigned PUT URL prefix", out.UploadURL)
	}
	if !strings.HasPrefix(out.ObjectURL, "s3://chora-atom-media/tenants/tenant-id/atoms/atom-id/") {
		t.Errorf("ObjectURL = %q; want s3:// prefix", out.ObjectURL)
	}
	if out.MaxSizeBytes != 2*1024*1024 {
		t.Errorf("MaxSizeBytes = %d; want 2 MiB", out.MaxSizeBytes)
	}
	if out.ExpiresAt.Before(time.Now()) {
		t.Errorf("ExpiresAt = %v; want future", out.ExpiresAt)
	}
}

// -----------------------------------------------------------------------------
// Presign host selection — S3_PUBLIC_ENDPOINT.
//
// SigV4 signs the Host header, so a presigned URL minted against the
// in-network service (e.g. garage:3900) cannot be fetched by the
// author's/learner's browser. When S3_PUBLIC_ENDPOINT is set, BOTH sign
// paths (Sign → PresignPutObject and SignDownloadURL → PresignGetObject)
// share the presign client, so both must mint URLs naming the public host.
// -----------------------------------------------------------------------------

func TestAtomMediaSigner_PublicEndpoint_PutSignNamesPublicHost(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://media.example.com")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	out, err := signer.Sign(context.Background(), "atom-id", "tenant-id", ports.SignAtomMediaInput{
		MIME:      "image/png",
		SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	u, err := url.Parse(out.UploadURL)
	if err != nil {
		t.Fatalf("UploadURL is not a valid URL: %v", err)
	}
	if u.Hostname() != "media.example.com" {
		t.Errorf("UploadURL host = %q; want the public endpoint host media.example.com (url: %q)", u.Hostname(), out.UploadURL)
	}
	if u.Hostname() == "garage" {
		t.Errorf("UploadURL still names the in-network host garage: %q", out.UploadURL)
	}
}

func TestAtomMediaSigner_PublicEndpoint_GetSignNamesPublicHost(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://media.example.com")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	out, err := signer.SignDownloadURL(context.Background(), "s3://chora-atom-media/tenants/t1/atoms/a1/obj1.png")
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}

	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatalf("download URL is not a valid URL: %v", err)
	}
	if u.Hostname() != "media.example.com" {
		t.Errorf("download URL host = %q; want the public endpoint host media.example.com (url: %q)", u.Hostname(), out.URL)
	}
}

func TestAtomMediaSigner_PublicEndpoint_UnsetSignsInternalHost(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	out, err := signer.Sign(context.Background(), "atom-id", "tenant-id", ports.SignAtomMediaInput{
		MIME:      "image/png",
		SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	u, err := url.Parse(out.UploadURL)
	if err != nil {
		t.Fatalf("UploadURL is not a valid URL: %v", err)
	}
	// Today's behaviour preserved: unset ⇒ sign with S3_ENDPOINT (the
	// documented dev / no-public-endpoint case).
	if u.Hostname() != "garage" || u.Port() != "3900" {
		t.Errorf("UploadURL host = %q; want the internal endpoint host garage:3900 (url: %q)", u.Host, out.UploadURL)
	}
}

func TestAtomMediaSigner_PublicEndpoint_WithoutSchemeStillSignsPublicHost(t *testing.T) {

	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_PUBLIC_ENDPOINT", "media.example.com")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}

	out, err := signer.Sign(context.Background(), "atom-id", "tenant-id", ports.SignAtomMediaInput{
		MIME:      "image/png",
		SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// A scheme-less public endpoint is normalised to https:// by the same
	// logic the internal endpoint uses — no construction failure.
	u, err := url.Parse(out.UploadURL)
	if err != nil {
		t.Fatalf("UploadURL is not a valid URL: %v", err)
	}
	if u.Hostname() != "media.example.com" {
		t.Errorf("UploadURL host = %q; want media.example.com (url: %q)", u.Hostname(), out.UploadURL)
	}
	if u.Scheme != "https" {
		t.Errorf("UploadURL scheme = %q; want https", u.Scheme)
	}
}
