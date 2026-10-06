// gcs_blob_store.go — S3-compatible BlobStore for the P6 batch upload path.
//
// Production wiring: cmd/server/main.go constructs `NewS3BlobStore` with
// the bucket name from env `GCS_BUCKET_BATCH_UPLOADS` (retained env name for
// continuity). The adapter authenticates via static credentials from the
// environment (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY) —
// works against MinIO locally and any S3-compatible endpoint in production.
//
// On boot, if env is empty the constructor returns
// ports.ErrBlobStoreNotWired so main.go can log "NOT wired" and the
// HTTP handler returns 503 with envelope `CREATION_BATCH_NOT_WIRED`
// (fail-loud per `feedback_no_stubs_real_wiring`).
//
// Key derivation is locked at `tenants/{tenant_id}/jobs/{job_id}/source`
// — see BlobKey(). URIs are `s3://{bucket}/{key}` — see BlobURI() +
// ParseBlobURI() (inverse for the worker).
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/apollo-chora/chora-common/objectstore"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// MaxBlobBytes is the 32MB upload cap per design §4 (creation-questions
// 413 response).
const MaxBlobBytes int64 = 32 * 1024 * 1024

// S3BlobStoreConfig is the constructor input.
type S3BlobStoreConfig struct {
	// Bucket is the S3 bucket name. Sourced from env
	// GCS_BUCKET_BATCH_UPLOADS via main.go. Empty value =>
	// ErrBlobStoreNotWired.
	Bucket string

	// Store is the objectstore-backed store. Use NewS3BlobStore(...) for
	// env-based construction.
	Store *objectstore.Store
}

// S3BlobStore implements ports.BlobStore against S3-compatible storage.
type S3BlobStore struct {
	bucket string
	store  *objectstore.Store
}

// NewS3BlobStore constructs an S3-compatible BlobStore from the
// environment (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY /
// S3_REGION / S3_FORCE_PATH_STYLE). Returns ErrBlobStoreNotWired when the
// bucket is empty so main.go can route the handler to 503.
func NewS3BlobStore(cfg S3BlobStoreConfig) (*S3BlobStore, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("S3 bucket not set: %w", ports.ErrBlobStoreNotWired)
	}
	if cfg.Store == nil {
		store, err := objectstore.New(objectstore.ConfigFromEnv())
		if err != nil {
			return nil, fmt.Errorf("objectstore.New: %w", err)
		}
		cfg.Store = store
	}
	return &S3BlobStore{bucket: cfg.Bucket, store: cfg.Store}, nil
}

// Compile-time check.
var _ ports.BlobStore = (*S3BlobStore)(nil)

// Upload streams the request body into an S3 object under the canonical
// per-tenant/per-job key. Returns the s3:// URI on success.
//
// Size validation: the caller MUST set req.Size (typically from
// `r.ContentLength` after the multipart parse). Sizes > MaxBlobBytes are
// rejected here as an extra defence-in-depth — the HTTP handler also
// limits.
func (s *S3BlobStore) Upload(ctx context.Context, req ports.UploadBlobReq) (ports.UploadBlobResp, error) {
	if req.TenantID == "" || req.JobID == "" {
		return ports.UploadBlobResp{}, errors.New("s3 blob: tenant_id + job_id required")
	}
	if req.Body == nil {
		return ports.UploadBlobResp{}, errors.New("s3 blob: body nil")
	}
	if req.Size > MaxBlobBytes {
		return ports.UploadBlobResp{}, fmt.Errorf("s3 blob: size %d > max %d", req.Size, MaxBlobBytes)
	}

	key := BlobKeyWithSlot(req.TenantID, req.JobID, req.Slot)

	// Buffer into a seekable reader: the S3 SDK signs PutObject by hashing
	// the payload and rewinding it, which fails on the non-seekable stream
	// io.LimitReader returns ("request stream is not seekable"). Cap the
	// read at MaxBlobBytes+1 so an oversized body trips the size check
	// rather than silently uploading.
	buf, err := io.ReadAll(io.LimitReader(req.Body, MaxBlobBytes+1))
	if err != nil {
		return ports.UploadBlobResp{}, fmt.Errorf("s3 blob: read: %w", err)
	}
	if int64(len(buf)) > MaxBlobBytes {
		return ports.UploadBlobResp{}, fmt.Errorf("s3 blob: size %d > max %d", len(buf), MaxBlobBytes)
	}
	if err := s.store.Put(ctx, key, bytes.NewReader(buf), int64(len(buf)), req.MIME); err != nil {
		return ports.UploadBlobResp{}, fmt.Errorf("s3 blob: put: %w", err)
	}

	return ports.UploadBlobResp{BlobURI: BlobURI(s.bucket, key)}, nil
}

// Download streams the blob back out. The caller MUST Close the reader.
func (s *S3BlobStore) Download(ctx context.Context, blobURI string) (io.ReadCloser, error) {
	bucket, key, err := ParseBlobURI(blobURI)
	if err != nil {
		return nil, fmt.Errorf("s3 blob: parse uri: %w", err)
	}

	// Cross-bucket safety: refuse to read from a bucket other than the
	// configured one (otherwise a stale job from a previous deployment
	// could leak through).
	if bucket != s.bucket {
		return nil, fmt.Errorf("s3 blob: uri bucket %q != configured %q", bucket, s.bucket)
	}
	rc, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("s3 blob: get: %w", err)
	}
	return rc, nil
}

// -----------------------------------------------------------------------------
// Key + URI helpers — pure functions, unit-testable.
// -----------------------------------------------------------------------------

// BlobKey returns the canonical S3 object key for a tenant + job.
// Locked at `tenants/{tenant_id}/jobs/{job_id}/source`. The trailing
// `/source` allows the worker to write per-job extracted artifacts
// (e.g. `extracted.json`, `chunks.json`) without colliding with the
// upload.
func BlobKey(tenantID, jobID string) string {
	return "tenants/" + tenantID + "/jobs/" + jobID + "/source"
}

// BlobKeyWithSlot returns the slot-suffixed object key for multi-blob jobs
// (Lane 1c D7). The empty slot is the legacy single-file `/source` key
// byte-for-byte; named slots ("source-1".."source-5", "rubric") give each
// upload its own object so N files on one job never overwrite each other.
func BlobKeyWithSlot(tenantID, jobID, slot string) string {
	if slot == "" {
		return BlobKey(tenantID, jobID)
	}
	return "tenants/" + tenantID + "/jobs/" + jobID + "/" + slot
}

// BlobURI joins bucket + key into an s3:// URI.
func BlobURI(bucket, key string) string {
	return "s3://" + bucket + "/" + key
}

// ParseBlobURI splits `s3://{bucket}/{key}` into its components. Returns
// a descriptive error on malformed input.
func ParseBlobURI(uri string) (bucket, key string, err error) {
	const prefix = "s3://"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", fmt.Errorf("blob uri must start with %q (got %q)", prefix, uri)
	}
	rest := uri[len(prefix):]
	if rest == "" {
		return "", "", errors.New("blob uri missing bucket")
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", "", errors.New("blob uri missing key")
	}
	bucket = rest[:slash]
	key = rest[slash+1:]
	if bucket == "" || key == "" {
		return "", "", errors.New("blob uri bucket/key empty")
	}
	return bucket, key, nil
}
