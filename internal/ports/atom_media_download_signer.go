// atom_media_download_signer.go — port for minting short-lived V4 signed
// GET URLs over DURABLE atom-media objects.
//
// W8 durable image re-home (OT#4). The orchestrator stamps a transient
// 7-day signed URL on a generated question/answer image; on atom publish
// chora-creation re-homes the bytes into the durable `chora-atom-media-{env}`
// bucket (per `MediaReHomer`) and rewrites the stored ref to a canonical
// `gs://` URI. Because the durable bucket has `public_access_prevention =
// enforced` (no CDN) AND an <img src> cannot carry the app bearer JWT, the
// durable image is served by minting a FRESH short-lived signed GET URL at
// each authed read:
//   - authoring preview  → chora-creation HTTP read resolves gs:// here;
//   - learner assessment → chora-delivery calls the chora-creation gRPC
//     MintAtomMediaDownloadURL RPC, which delegates to this port.
//
// Distinct from `AtomMediaSigner` (which mints single-use V4 PUT URLs for
// FE-driven uploads). The concrete GCS adapter satisfies BOTH ports.
package ports

import (
	"context"
	"errors"
	"time"
)

// ErrAtomMediaDownloadSignerNotWired is the canonical sentinel signalling
// the download signer is unavailable on this deployment (env unset or
// constructor failed). Callers map it to a fail-loud 503 / gRPC Unavailable
// rather than serving a broken image (per `feedback_no_stubs_real_wiring`).
var ErrAtomMediaDownloadSignerNotWired = errors.New("atom media download signer not wired")

// SignedDownloadURL is the output of AtomMediaDownloadSigner.SignDownloadURL.
type SignedDownloadURL struct {
	// URL is the V4 signed GET URL the FE renders directly in an <img src>.
	// Short-lived (the signer's TTL) — re-minted on each authed read, so a
	// frozen snapshot ref never goes stale.
	URL string

	// ExpiresAt is the UTC time the signed URL stops serving bytes.
	ExpiresAt time.Time
}

// AtomMediaDownloadSigner mints fresh short-lived V4 signed GET URLs for
// durable atom-media objects referenced by `gs://` URI.
type AtomMediaDownloadSigner interface {
	// SignDownloadURL parses a gs://{bucket}/{key} URI, validates {bucket}
	// is the configured durable atom-media bucket (refuses any other bucket
	// — a caller MUST NOT be able to coerce the signer into vending a URL
	// for an arbitrary bucket), and returns a fresh signed GET URL.
	//
	// Returns an error for a malformed URI, a foreign bucket, or when the
	// signer is not wired (ErrAtomMediaDownloadSignerNotWired).
	SignDownloadURL(ctx context.Context, gsURI string) (SignedDownloadURL, error)
}
