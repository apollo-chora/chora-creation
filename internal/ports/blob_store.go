// BlobStore — port for the batch-upload artefact store (P6 path).
//
// The chora-creation batch path streams the author's source-material
// file (PDF/DOCX/MD/TXT, ≤32MB) into a GCS bucket keyed by
// `tenants/{tenant_id}/jobs/{job_id}/source`. The HTTP handler uploads
// the blob then persists the `gs://...` URI on the ComposeJob
// (column source_blob_uri). The `gs://...` URI is forwarded to the qgen
// crew (via ai_assist.started.v1, job_kind=batch); the crew inlines the
// bytes as Gemini multimodal (ADR-169 / EPIC-1a) — chora-creation does not
// download or extract the blob itself (no chora-doc-parser step).
//
// Per `feedback_no_inline_config`: the bucket name MUST come from the
// `GCS_BUCKET_BATCH_UPLOADS` env var sourced from Terraform; if unset
// the adapter constructor returns ErrBlobStoreNotWired so the boot path
// can log "NOT wired" and the HTTP handler returns 503 with the
// `CREATION_BATCH_NOT_WIRED` envelope code (fail-loud per
// `feedback_no_stubs_real_wiring`).
package ports

import (
	"context"
	"errors"
	"io"
)

// ErrBlobStoreNotWired signals the BlobStore is not available (env var
// unset / bucket missing / IAM blocked).
var ErrBlobStoreNotWired = errors.New("blob store not wired")

// BlobStore uploads + downloads blobs for the batch path.
type BlobStore interface {
	// Upload writes `body` (≤MaxBytes) to the configured bucket at the
	// given key under the tenant prefix. Returns a `gs://bucket/key` URI.
	// MIME is recorded as Content-Type on the GCS object (so a downstream
	// reader can detect type without re-introspection).
	Upload(ctx context.Context, req UploadBlobReq) (UploadBlobResp, error)

	// Download streams the blob back out. The reader is owned by the
	// caller and MUST be Closed.
	Download(ctx context.Context, blobURI string) (io.ReadCloser, error)
}

// UploadBlobReq is the input for BlobStore.Upload.
type UploadBlobReq struct {
	TenantID string // tenant_id used for the key prefix (NOT for IAM scoping —
	// IAM is bucket-wide).
	JobID    string // job_id used in the key (1 blob = 1 job).
	MIME     string // detected MIME (e.g. application/pdf).
	Filename string // original filename, recorded as object metadata "x-goog-meta-original-filename".
	Body     io.Reader
	Size     int64 // bytes (for content-length header + size validation).
	// Slot distinguishes multiple blobs on one job (Lane 1c D7 multi-file):
	// "" keeps the legacy `/source` key byte-identical; multi-file uploads
	// use "source-1".."source-5" and the dedicated rubric uses "rubric" so
	// files never overwrite each other.
	Slot string
}

// UploadBlobResp is the output of BlobStore.Upload.
type UploadBlobResp struct {
	BlobURI string // gs://bucket/tenants/{tenant_id}/jobs/{job_id}/source
}
