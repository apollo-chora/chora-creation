// rehome_integration_test.go — the REAL mediarehome.Service over the REAL
// S3 adapter (a stub S3 endpoint), fed the production incident's URL shape.
//
// This is the seam the unit fakes hid: the service-level fake returned gs://
// while the deployed adapter returns s3://, and only the S3 adapter knows
// Garage/S3 URL grammar. Re-homing a generated image on accept must succeed
// (it 500'd) and re-homing the durable ref it produced must be a no-op (it
// 500'd too, because the old idempotency check only recognised gs://).
package mediastore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/mediarehome"
)

func TestReHome_ProductionGarageURL_EndToEnd(t *testing.T) {
	var copySource, putPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		putPath = r.URL.Path
		copySource = r.Header.Get("X-Amz-Copy-Source")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`))
	}))
	defer srv.Close()

	t.Setenv("S3_ENDPOINT", srv.URL)
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "chora-ai-assist-images")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	svc := mediarehome.New(signer)

	// Verbatim shape from the production accept-image failure.
	raw := "http://146.235.20.25:9000/chora-ai-assist-images/tenants/0f1e/atoms/aa/jobs/jj/file.png" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=x&X-Amz-Signature=deadbeef"
	got, changed, err := svc.ReHome(context.Background(), "0f1e", "aa", raw)
	if err != nil {
		t.Fatalf("ReHome(transient Garage URL) err = %v; want nil (this was the production 500)", err)
	}
	if !changed {
		t.Fatal("changed=false; want true")
	}
	if !strings.HasPrefix(got, "s3://chora-atom-media/tenants/0f1e/atoms/aa/") || !strings.HasSuffix(got, ".png") {
		t.Errorf("durable ref = %q; want s3://chora-atom-media/tenants/0f1e/atoms/aa/<uuid>.png", got)
	}
	if copySource != "chora-ai-assist-images/tenants/0f1e/atoms/aa/jobs/jj/file.png" {
		t.Errorf("X-Amz-Copy-Source = %q; want the transient object with the signature stripped", copySource)
	}
	if !strings.HasPrefix(putPath, "/chora-atom-media/tenants/0f1e/atoms/aa/") {
		t.Errorf("copy PUT path = %q; want the durable bucket + per-atom key", putPath)
	}

	// Idempotency: re-homing the durable s3:// ref just produced is a no-op.
	again, changed2, err := svc.ReHome(context.Background(), "0f1e", "aa", got)
	if err != nil {
		t.Fatalf("ReHome(durable s3://) err = %v; want nil (re-publish must not 500)", err)
	}
	if changed2 || again != got {
		t.Errorf("ReHome(durable) = (%q,%v); want unchanged %q,false", again, changed2, got)
	}
}
