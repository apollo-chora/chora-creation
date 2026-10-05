// gcs_blob_store_test.go — coverage for the S3-backed BlobStore
// adapter (P6 batch path).
//
// Network calls to S3 are not unit-testable without a fake server; this
// file covers:
//   - NewS3BlobStore returns ErrBlobStoreNotWired when bucket is "".
//   - The key derivation (`tenants/{tenant_id}/jobs/{job_id}/source`)
//     is enforced by `BlobKey()` so the URI format is locked.
//   - URI format: `s3://{bucket}/{key}`.
//
// Live S3 interaction is covered by smoke tests run against a
// MinIO/S3 endpoint.
package storage_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

func TestNewS3BlobStore_RejectsEmptyBucket(t *testing.T) {

	_, err := storage.NewS3BlobStore(storage.S3BlobStoreConfig{
		Bucket: "",
	})
	if !errors.Is(err, ports.ErrBlobStoreNotWired) {
		t.Errorf("NewS3BlobStore with empty bucket should return ErrBlobStoreNotWired; got %v", err)
	}
}

func TestBlobKey_TenantJobScheme(t *testing.T) {

	tenantID := "22222222-2222-7222-8222-222222222222"
	jobID := "33333333-3333-7333-8333-333333333333"
	got := storage.BlobKey(tenantID, jobID)
	want := "tenants/22222222-2222-7222-8222-222222222222/jobs/33333333-3333-7333-8333-333333333333/source"
	if got != want {
		t.Errorf("BlobKey = %q; want %q", got, want)
	}
}

func TestBlobURI_S3Scheme(t *testing.T) {

	bucket := "chora-creation-batch-uploads"
	key := "tenants/t1/jobs/j1/source"
	got := storage.BlobURI(bucket, key)
	want := "s3://chora-creation-batch-uploads/tenants/t1/jobs/j1/source"
	if got != want {
		t.Errorf("BlobURI = %q; want %q", got, want)
	}
}

// TestParseBlobURI verifies the inverse: s3:// URI -> (bucket, key).
// Used by Download to route a job's source_blob_uri to the right
// bucket+object.
func TestParseBlobURI_HappyPath(t *testing.T) {
	bucket, key, err := storage.ParseBlobURI("s3://chora-creation-batch-uploads/tenants/t1/jobs/j1/source")
	if err != nil {
		t.Fatalf("ParseBlobURI: %v", err)
	}
	if bucket != "chora-creation-batch-uploads" {
		t.Errorf("bucket = %q; want chora-creation-batch-uploads", bucket)
	}
	if key != "tenants/t1/jobs/j1/source" {
		t.Errorf("key = %q; want tenants/t1/jobs/j1/source", key)
	}
}

func TestParseBlobURI_RejectsBadScheme(t *testing.T) {
	if _, _, err := storage.ParseBlobURI("https://example.com/foo"); err == nil {
		t.Error("ParseBlobURI(https://...) should return error")
	}
	if _, _, err := storage.ParseBlobURI(""); err == nil {
		t.Error("ParseBlobURI(empty) should return error")
	}
	if _, _, err := storage.ParseBlobURI("s3://only-bucket"); err == nil {
		t.Error("ParseBlobURI without key should return error")
	}
}
