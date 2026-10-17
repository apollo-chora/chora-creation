// atom_media_download_signer.go — port for minting short-lived V4 signed
// GET URLs over DURABLE atom-media objects.
//
// W8 durable image re-home (OT#4). The orchestrator stamps a transient
// 7-day signed URL on a generated question/answer image; on atom publish
// chora-creation re-homes the bytes into the durable `chora-atom-media-{env}`
// bucket (per `MediaReHomer`) and rewrites the stored ref to a canonical
// durable object URI. Because the durable bucket is private (no CDN, no
// public access) AND an <img src> cannot carry the app bearer JWT, the
// durable image is served by minting a FRESH short-lived signed GET URL at
// each authed read:
//   - authoring preview  → chora-creation HTTP read resolves the durable ref
//     here;
//   - learner assessment → chora-delivery calls the chora-creation gRPC
//     MintAtomMediaDownloadURL RPC, which delegates to this port.
//
// The durable URI SCHEME is owned by the adapter, not by the call sites: the
// S3-compatible adapter stores and signs `s3://{bucket}/{key}`, a GCS one
// `gs://{bucket}/{key}`. Callers ask IsDurableRef rather than testing a
// scheme prefix themselves.
//
// Distinct from `AtomMediaSigner` (which mints single-use V4 PUT URLs for
// FE-driven uploads). The storage adapter satisfies BOTH ports.
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
// durable atom-media objects referenced by a durable object URI (the
// adapter's own scheme: `s3://` for the S3-compatible adapter, `gs://` for
// a GCS one).
type AtomMediaDownloadSigner interface {
	// IsDurableRef reports whether ref names a durable atom-media object
	// this signer can mint (as opposed to an already-fetchable transient
	// http(s) URL, or nothing). Implementations own the scheme grammar.
	//
	// It is bucket-scoped: a ref naming a bucket this signer is not
	// configured for is NOT durable here. False for an http(s) URL, an empty
	// string, a foreign bucket, or a malformed ref. A caller that finds a ref
	// is not durable must NOT emit it verbatim unless it is an http(s) URL
	// the browser can already fetch.
	IsDurableRef(ref string) bool

	// TenantOwnsRef reports whether ref names a media object inside tenantID's
	// OWN per-tenant namespace (ports.TenantMediaPrefix) — the ownership gate
	// every seam that persists or serves a media reference must pass.
	//
	// Implementations own the reference grammar: they classify ref and compare
	// the resulting object key against the tenant prefix. Returns (true, nil)
	// for a well-formed reference in the tenant's namespace, (false, nil) for a
	// well-formed reference belonging to a DIFFERENT tenant, and (false, err)
	// when the reference cannot be classified at all — ownership cannot be
	// established, so the caller must fail closed.
	//
	// It exists so a caller can enforce ownership WITHOUT a copier: the mint
	// RPC and the write seams whose re-homer is unwired still need the check,
	// and neither should parse storage URI grammar itself.
	TenantOwnsRef(ctx context.Context, tenantID, ref string) (bool, error)

	// SignDownloadURL parses a durable object URI (`s3://{bucket}/{key}` for
	// the S3 adapter), validates {bucket} is the configured durable
	// atom-media bucket (refuses any other bucket — a caller MUST NOT be
	// able to coerce the signer into vending a URL for an arbitrary
	// bucket), and returns a fresh signed GET URL.
	//
	// It does NOT check tenant ownership of {key} — SignDownloadURL scopes on
	// bucket only. Callers serving a tenant-scoped request MUST gate on
	// TenantOwnsRef first.
	//
	// Returns an error for a malformed URI, a foreign bucket, or when the
	// signer is not wired (ErrAtomMediaDownloadSignerNotWired).
	SignDownloadURL(ctx context.Context, gsURI string) (SignedDownloadURL, error)
}
