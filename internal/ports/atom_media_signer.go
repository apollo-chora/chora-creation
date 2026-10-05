// AtomMediaSigner — port for minting V4 signed URLs the FE uses to PUT
// atom-media images directly to Google Cloud Storage.
//
// Hex architecture: the chora-creation HTTP handler depends on THIS
// interface (the domain-side port), not on the concrete GCS adapter at
// `internal/adapter/mediastore`. Per ADR-156 Phase 1 (atom redesign — locked
// 2026-05-17 in `docs/m13/atom-phase1-execution-plan-2026-05-17.md`).
//
// Wiring:
//   - Adapter at `internal/adapter/mediastore.NewAtomMediaSigner(...)` provides
//     the production implementation against the `chora-atom-media-dev`
//     bucket (provisioned by Infra commit `4df66f51`, env
//     `ATOM_MEDIA_BUCKET`).
//   - If env `ATOM_MEDIA_BUCKET` is unset on the chora-creation
//     deployment, `cmd/server/main.go` leaves the handler's signer
//     reference nil and the HTTP handler returns 503 with envelope
//     `CREATION_ATOM_MEDIA_NOT_WIRED` (fail-loud per
//     `feedback_no_stubs_real_wiring`).
//
// Bucket policy:
//   - URL expiry 15 minutes (per `secrets-and-env` skill short-lived
//     signed-URL default).
//   - Object key shape: `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}`
//     where `object_id` is a freshly-minted UUIDv7 and `ext` is derived
//     from MIME.
//   - Per-tenant cap via `MaxSizeBytes` on the response — Phase 1 ships
//     a 2 MiB default; the per-tenant `TenantEntitlement.atom_media_max_bytes`
//     hook is future work (see ADR-156 Decision #5).
package ports

import (
	"context"
	"errors"
	"time"
)

// ErrAtomMediaSignerNotWired is the canonical sentinel signalling the
// signer adapter is not available on this deployment (env var unset or
// constructor failed). The HTTP handler maps it to 503 with envelope
// `CREATION_ATOM_MEDIA_NOT_WIRED`.
var ErrAtomMediaSignerNotWired = errors.New("atom media signer not wired")

// AtomMediaSigner mints V4 signed URLs for atom-media image upload.
type AtomMediaSigner interface {
	// Sign mints a single-use V4 signed URL the FE will PUT bytes to.
	// Returns the upload URL, expiry, the canonical `gs://` object URL
	// (FE persists this on `media_assets[].url` via subsequent
	// `PATCH /atoms/{atom_id}`), and the actual size cap the GCS upload
	// will enforce (may be tighter than the request `SizeBytes`).
	Sign(ctx context.Context, atomID, tenantID string, in SignAtomMediaInput) (SignAtomMediaOutput, error)
}

// SignAtomMediaInput is the input to AtomMediaSigner.Sign.
type SignAtomMediaInput struct {
	// MIME is the image MIME type. Phase 1 enum:
	// image/jpeg | image/png | image/webp. The signer caller (the HTTP
	// handler) validates the enum at the wire layer; the adapter
	// re-checks defence-in-depth.
	MIME string

	// SizeBytes is the declared object size. Used to choose / report
	// the per-tenant cap on the response envelope. The signed URL itself
	// does NOT encode the size — GCS enforces by Content-Length on the
	// PUT request.
	SizeBytes int64

	// AltText is optional. When non-empty the adapter stamps it on the
	// object metadata as `x-goog-meta-alt-text` for accessibility audit
	// trail (per ADR-156 Decision #5 + OpenAPI
	// MintAtomMediaSignedUrlRequest schema).
	AltText string
}

// SignAtomMediaOutput is the response from AtomMediaSigner.Sign.
type SignAtomMediaOutput struct {
	// UploadURL is the V4 signed URL — single-use, expires per
	// ExpiresAt. The FE PUTs the image bytes here.
	UploadURL string

	// ExpiresAt is the UTC time the signed URL stops accepting uploads.
	// Per `secrets-and-env` short-lived default, 15 minutes from mint.
	ExpiresAt time.Time

	// ObjectURL is the canonical `gs://{bucket}/{key}` URL the FE
	// persists on `media_assets[].url` after the upload completes.
	// Distinct from UploadURL (which carries the time-limited
	// signature query params).
	ObjectURL string

	// MaxSizeBytes is the actual server-enforced size cap for this
	// tenant — Phase 1 default 2 MiB. May be tighter than the request
	// `SizeBytes` if the latter slipped past the outer 10 MiB schema
	// bound. FE MUST honour this on the PUT.
	MaxSizeBytes int64
}
