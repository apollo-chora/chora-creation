package creationgrpc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

type fakeDownloadSigner struct {
	globalErr error
}

func (f *fakeDownloadSigner) SignDownloadURL(_ context.Context, gsURI string) (ports.SignedDownloadURL, error) {
	if f.globalErr != nil {
		return ports.SignedDownloadURL{}, f.globalErr
	}
	if !strings.HasPrefix(gsURI, "gs://chora-atom-media-dev/") {
		return ports.SignedDownloadURL{}, fmt.Errorf("foreign bucket: %q", gsURI)
	}
	return ports.SignedDownloadURL{
		URL:       "https://signed.example/" + strings.TrimPrefix(gsURI, "gs://"),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}, nil
}

// Batch mint — all valid refs return signed URLs in request order.
func TestMintAtomMediaDownloadURL_BatchHappyPath(t *testing.T) {
	srv := NewCreationServer(Deps{DownloadSigner: &fakeDownloadSigner{}})
	in := &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: "t1",
		GsUris: []string{
			"gs://chora-atom-media-dev/tenants/t1/atoms/a/stem.png",
			"gs://chora-atom-media-dev/tenants/t1/atoms/a/answer.png",
		},
	}
	resp, err := srv.MintAtomMediaDownloadURL(context.Background(), in)
	if err != nil {
		t.Fatalf("MintAtomMediaDownloadURL: %v", err)
	}
	if len(resp.GetUrls()) != 2 {
		t.Fatalf("got %d urls; want 2", len(resp.GetUrls()))
	}
	for i, u := range resp.GetUrls() {
		if u.GetError() != "" {
			t.Errorf("url[%d] unexpected error %q", i, u.GetError())
		}
		if !strings.HasPrefix(u.GetSignedUrl(), "https://signed.example/") {
			t.Errorf("url[%d] signed_url = %q; want a signed URL", i, u.GetSignedUrl())
		}
		if u.GetGsUri() != in.GsUris[i] {
			t.Errorf("url[%d] gs_uri = %q; want echo %q", i, u.GetGsUri(), in.GsUris[i])
		}
		if u.GetExpiresAt() == nil {
			t.Errorf("url[%d] expires_at nil on success", i)
		}
	}
}

// Per-URI error isolation — one bad ref does NOT fail the batch.
func TestMintAtomMediaDownloadURL_PerURIErrorIsolation(t *testing.T) {
	srv := NewCreationServer(Deps{DownloadSigner: &fakeDownloadSigner{}})
	in := &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: "t1",
		GsUris: []string{
			"gs://chora-atom-media-dev/tenants/t1/atoms/a/ok.png",
			"gs://evil-bucket/x.png",
		},
	}
	resp, err := srv.MintAtomMediaDownloadURL(context.Background(), in)
	if err != nil {
		t.Fatalf("batch should not fail on a single bad ref: %v", err)
	}
	if len(resp.GetUrls()) != 2 {
		t.Fatalf("got %d urls; want 2", len(resp.GetUrls()))
	}
	if resp.Urls[0].GetSignedUrl() == "" || resp.Urls[0].GetError() != "" {
		t.Errorf("url[0] should succeed; got url=%q err=%q", resp.Urls[0].GetSignedUrl(), resp.Urls[0].GetError())
	}
	if resp.Urls[1].GetError() == "" || resp.Urls[1].GetSignedUrl() != "" {
		t.Errorf("url[1] should carry an error + empty signed_url; got url=%q err=%q", resp.Urls[1].GetSignedUrl(), resp.Urls[1].GetError())
	}
}

// No signer wired → FailedPrecondition (fail-loud, not a silent empty batch).
func TestMintAtomMediaDownloadURL_NoSigner_FailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{DownloadSigner: nil})
	_, err := srv.MintAtomMediaDownloadURL(context.Background(), &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: "t1", GsUris: []string{"gs://chora-atom-media-dev/x.png"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code = %v; want FailedPrecondition", status.Code(err))
	}
}

// Missing tenant_id → InvalidArgument.
func TestMintAtomMediaDownloadURL_NoTenant_InvalidArgument(t *testing.T) {
	srv := NewCreationServer(Deps{DownloadSigner: &fakeDownloadSigner{}})
	_, err := srv.MintAtomMediaDownloadURL(context.Background(), &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: "", GsUris: []string{"gs://chora-atom-media-dev/x.png"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v; want InvalidArgument", status.Code(err))
	}
}

// Empty gs_uris → empty (but non-nil) result set, no error.
func TestMintAtomMediaDownloadURL_EmptyBatch(t *testing.T) {
	srv := NewCreationServer(Deps{DownloadSigner: &fakeDownloadSigner{}})
	resp, err := srv.MintAtomMediaDownloadURL(context.Background(), &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: "t1", GsUris: nil,
	})
	if err != nil {
		t.Fatalf("empty batch err = %v", err)
	}
	if len(resp.GetUrls()) != 0 {
		t.Errorf("got %d urls; want 0", len(resp.GetUrls()))
	}
}
