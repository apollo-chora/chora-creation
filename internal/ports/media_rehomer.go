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
// The copy is a same-region server-side GCS object copy — no bytes transit
// the pod. The concrete GCS adapter satisfies this port.
package ports

import "context"

// CopyToDurableParams is the input to MediaReHomer.CopyToDurable. The
// destination object key is built BY the adapter (it owns the canonical
// `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}` shape via
// AtomMediaKey) — the caller supplies only the source coordinates + the
// owning atom + the file extension.
type CopyToDurableParams struct {
	// SrcBucket / SrcKey locate the source object in the transient bucket
	// (parsed by the re-home service from the stored signed URL).
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

// MediaReHomer copies a transient image object into the durable atom-media
// bucket and returns the canonical `gs://` URI of the durable copy.
type MediaReHomer interface {
	// CopyToDurable performs a same-region server-side copy of
	// SrcBucket/SrcKey into the durable bucket under a freshly-minted
	// per-atom key, returning the durable gs:// URI. Fail-loud on any
	// storage error (a partially-published atom is surfaced, not hidden).
	CopyToDurable(ctx context.Context, p CopyToDurableParams) (gsURI string, err error)

	// DurableBucket returns the destination (durable atom-media) bucket
	// name — used by the re-home service for the idempotency check (a ref
	// already in the durable bucket is left untouched).
	DurableBucket() string
}
