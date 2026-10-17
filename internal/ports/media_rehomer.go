// media_rehomer.go — port for server-side copy of a transient W8 image
// object into the durable atom-media bucket.
//
// W8 durable image re-home (OT#4). The orchestrator writes the rendered
// question/answer image to the TRANSIENT `chora-ai-assist-images-{env}`
// bucket (7-day delete lifecycle) and stamps a 7-day signed URL on the
// candidate. On atom publish chora-creation copies those bytes into the
// DURABLE `chora-atom-media-{env}` bucket (365-day → Coldline lifecycle,
// no delete) so a published atom's images survive past 7 days.
//
// The copy is a same-region server-side object copy — no bytes transit the
// pod. The concrete storage adapter (S3-compatible: MinIO / Garage / S3)
// satisfies this port and owns the storage-URL grammar + validation for the
// buckets it is configured with.
package ports

import "context"

// TenantMediaPrefix returns the per-tenant namespace prefix that every
// atom-media object key lives under — `tenants/{tenant_id}/`.
//
// It is the OWNERSHIP boundary for a re-home. Two writers put objects under
// it: the orchestrator (qgen) writes each rendered question/answer image into
// the transient generated-image bucket at
// `tenants/{tenant_id}/jobs/{job_id}/{file}.{ext}`, and this service writes the
// durable copy at `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}`.
// Because the copy runs with the ADAPTER's own credentials, a trusted host and
// bucket do not prove the object belongs to the caller — a client-supplied
// image_url can name any object in the transient bucket — so both the service
// and the adapter refuse a reference outside the caller's own prefix. The
// service applies this to EVERY classified reference, durable ones included:
// an already-durable foreign ref must not be accepted verbatim either, since
// the read signer scopes on bucket only and would later serve it.
//
// POLICY — the boundary is TENANT-level, deliberately. A same-tenant key under
// ANY job or atom is accepted, because job-level (or author-level) scoping
// would additionally require proving the caller authored the originating job,
// and that is a product authorization question (are transient generated images
// private to the job/author that produced them?) rather than something this
// namespace can answer. Tightening it later means narrowing this helper, and
// the callers need no change.
//
// The trailing slash is part of the prefix: `tenants/abc/` must never match
// `tenants/abcd/`.
func TenantMediaPrefix(tenantID string) string {
	return "tenants/" + tenantID + "/"
}

// CopyToDurableParams is the input to MediaReHomer.CopyToDurable. The
// destination object key is built BY the adapter (it owns the canonical
// `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}` shape via
// AtomMediaKey) — the caller supplies only the source coordinates + the
// owning atom + the file extension.
type CopyToDurableParams struct {
	// SrcBucket / SrcKey locate the source object in the transient bucket
	// (supplied by the re-home service from ClassifyReference). SrcKey MUST
	// sit under TenantMediaPrefix(TenantID): the adapter refuses to copy a
	// source object belonging to another tenant.
	SrcBucket string
	SrcKey    string

	// TenantID / AtomID scope the destination key (per-tenant IAM audit
	// prefix + per-atom grouping).
	TenantID string
	AtomID   string

	// Ext is the destination file extension (e.g. "png"), derived from the
	// source object key so a downstream consumer can probe MIME without
	// re-introspection.
	Ext string
}

// MediaReferenceKind discriminates a classified storage reference: a
// TRANSIENT object (still in the short-lived generated-image bucket — copy
// it) from an already-DURABLE one (in the atom-media bucket — leave it).
type MediaReferenceKind int

const (
	// MediaReferenceTransient names an object in the transient
	// generated-image bucket. The service copies it to durable storage.
	MediaReferenceTransient MediaReferenceKind = iota

	// MediaReferenceDurable names an object already in the durable
	// atom-media bucket. The service leaves the ref untouched.
	MediaReferenceDurable
)

// MediaReference is a storage reference the adapter has validated against
// its configured origins + buckets, split into the coordinates a copy needs.
type MediaReference struct {
	Kind   MediaReferenceKind
	Bucket string
	Key    string
}

// MediaReHomer copies a transient image object into the durable atom-media
// bucket and returns the canonical durable URI of the copy.
type MediaReHomer interface {
	// ClassifyReference validates raw against the configured storage
	// origins and buckets and reports whether it names a transient object
	// (copy it) or an already-durable one (leave it). An unknown,
	// malformed, foreign or otherwise unauthorised reference is an error.
	ClassifyReference(ctx context.Context, raw string) (MediaReference, error)

	// CopyToDurable performs a same-region server-side copy of
	// SrcBucket/SrcKey into the durable bucket under a freshly-minted
	// per-atom key, returning the durable URI (s3:// for the S3 adapter,
	// gs:// for a GCS one). Fail-loud on any storage error (a
	// partially-published atom is surfaced, not hidden).
	CopyToDurable(ctx context.Context, p CopyToDurableParams) (gsURI string, err error)

	// DurableBucket returns the destination (durable atom-media) bucket
	// name — the bucket ClassifyReference authorises durable refs against.
	DurableBucket() string
}
