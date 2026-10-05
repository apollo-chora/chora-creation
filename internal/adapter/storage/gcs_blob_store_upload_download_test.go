// gcs_blob_store_upload_download_test.go — coverage for the pre-network
// validation branches of Upload/Download plus the cross-bucket safety guard.
//
// The happy paths that touch a live bucket are not unit-testable without an
// S3/MinIO endpoint, but every branch that returns BEFORE a network round-trip
// is pure logic and can be pinned here:
//   - Upload: missing tenant/job, nil body, declared-size cap.
//   - Download: malformed URI and the cross-bucket refusal guard.
package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

func newTestBlobStore(t *testing.T) *storage.S3BlobStore {
	t.Helper()
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("S3_BUCKET", "test-bucket")
	s, err := storage.NewS3BlobStore(storage.S3BlobStoreConfig{Bucket: "b"})
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}
	return s
}

func TestS3BlobStore_Upload_RejectsMissingTenantOrJob(t *testing.T) {
	s := newTestBlobStore(t)
	// Missing tenant.
	if _, err := s.Upload(context.Background(), ports.UploadBlobReq{JobID: "j", Body: strings.NewReader("x")}); err == nil {
		t.Error("expected error for missing tenant_id")
	}
	// Missing job.
	if _, err := s.Upload(context.Background(), ports.UploadBlobReq{TenantID: "t", Body: strings.NewReader("x")}); err == nil {
		t.Error("expected error for missing job_id")
	}
}

func TestS3BlobStore_Upload_RejectsNilBody(t *testing.T) {
	s := newTestBlobStore(t)
	if _, err := s.Upload(context.Background(), ports.UploadBlobReq{TenantID: "t", JobID: "j"}); err == nil {
		t.Error("expected error for nil body")
	}
}

func TestS3BlobStore_Upload_RejectsDeclaredOversize(t *testing.T) {
	s := newTestBlobStore(t)
	_, err := s.Upload(context.Background(), ports.UploadBlobReq{
		TenantID: "t", JobID: "j", Body: strings.NewReader("x"), Size: storage.MaxBlobBytes + 1,
	})
	if err == nil {
		t.Fatal("expected declared-oversize rejection")
	}
	if !strings.Contains(err.Error(), "max") {
		t.Errorf("error should name the max cap, got %q", err)
	}
}

// TestS3BlobStore_Download_RejectsMalformedURI exercises the ParseBlobURI
// failure branch inside Download before any bucket access.
func TestS3BlobStore_Download_RejectsMalformedURI(t *testing.T) {
	s := newTestBlobStore(t)
	if _, err := s.Download(context.Background(), "not-an-s3-uri"); err == nil {
		t.Error("expected error for non-s3 uri")
	}
}

// TestS3BlobStore_Download_RejectsCrossBucket is the cross-bucket safety
// guard: a stale job from another deployment must never be readable.
func TestS3BlobStore_Download_RejectsCrossBucket(t *testing.T) {
	s := newTestBlobStore(t)
	if _, err := s.Download(context.Background(), "s3://other-bucket/tenants/t/jobs/j/source"); err == nil {
		t.Fatal("expected cross-bucket refusal")
	}
}

// TestS3BlobStore_BlobKeyHelpers verifies the key/URI derivation helpers.
func TestS3BlobStore_BlobKeyHelpers(t *testing.T) {

	if got := storage.BlobKey("t", "j"); got != "tenants/t/jobs/j/source" {
		t.Errorf("BlobKey = %q", got)
	}
	if got := storage.BlobKeyWithSlot("t", "j", ""); got != "tenants/t/jobs/j/source" {
		t.Errorf("BlobKeyWithSlot empty = %q", got)
	}
	if got := storage.BlobKeyWithSlot("t", "j", "rubric"); got != "tenants/t/jobs/j/rubric" {
		t.Errorf("BlobKeyWithSlot rubric = %q", got)
	}
	if got := storage.BlobURI("b", "k"); got != "s3://b/k" {
		t.Errorf("BlobURI = %q", got)
	}
	bucket, key, err := storage.ParseBlobURI("s3://b/tenants/t/jobs/j/source")
	if err != nil || bucket != "b" || key != "tenants/t/jobs/j/source" {
		t.Errorf("ParseBlobURI = %q, %q, %v", bucket, key, err)
	}
}
