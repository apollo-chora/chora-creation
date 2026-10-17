// Package mediarehome is the W8 durable image re-home application service
// (OT#4). On atom publish, the orchestrator's transient 7-day signed image
// URLs are re-homed into the durable atom-media bucket and rewritten to a
// canonical durable ref so a published atom's images survive past 7 days.
//
// This package owns the DECISION logic (idempotency, copy / no-op /
// fail-loud) and delegates the actual copy AND the storage-URL grammar to
// the ports.MediaReHomer port: the adapter (S3-compatible in production,
// GCS elsewhere) validates + classifies the reference against the buckets
// it is configured with, and this service acts on that verdict. It is
// infrastructure-free and fully unit-tested; the publish handler wires it
// over the real adapter.
package mediarehome

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// Service re-homes transient W8 image URLs into durable storage.
type Service struct {
	copier ports.MediaReHomer
}

// New builds the service over a MediaReHomer (the storage adapter in
// production).
func New(copier ports.MediaReHomer) *Service { return &Service{copier: copier} }

// ReHome ensures imageURL points at durable storage and returns the durable
// ref plus whether it changed:
//
//   - empty input              → ("", false, nil)  (no image)
//   - already-durable ref       → (ref, false, nil) (idempotent — no copy)
//   - transient ref             → (durable ref, true, nil) after copy
//   - foreign-tenant reference  → ("", false, error) (fail-loud — no copy)
//   - unclassifiable ref        → ("", false, error) (fail-loud)
//
// The ownership check covers DURABLE references too, not just copyable
// transient ones — see referenceBelongsToTenant. tenantID + atomID scope the
// destination key. A publish that re-homes the same atom twice is a no-op the
// second time (the stored ref is already durable), so re-publish is safe.
func (s *Service) ReHome(ctx context.Context, tenantID, atomID, imageURL string) (string, bool, error) {
	raw := strings.TrimSpace(imageURL)
	if raw == "" {
		return "", false, nil
	}

	// The adapter owns storage-URL grammar + validation: it decides whether
	// this ref is transient (copy it) or already durable (leave it). An
	// unknown / malformed / foreign ref is an error, so we fail loud BEFORE
	// any copy or persist.
	ref, err := s.copier.ClassifyReference(ctx, raw)
	if err != nil {
		return "", false, fmt.Errorf("mediarehome: %w", err)
	}

	// OWNERSHIP — applies to EVERY classified object reference, durable ones
	// included, and therefore runs BEFORE the durable early-return. A trusted
	// origin + bucket does not prove the object belongs to the tenant being
	// accepted: the accept path prefers a client-supplied image_url over the
	// stored draft's, and the read signer scopes on bucket only — so a foreign
	// DURABLE ref returned verbatim here would be persisted and later signed
	// and served. Refuse it exactly like a foreign transient source.
	if !referenceBelongsToTenant(tenantID, ref.Key) {
		return "", false, fmt.Errorf(
			"mediarehome: refusing to re-home %q — object is outside tenant %q's namespace %q",
			ref.Key, tenantID, ports.TenantMediaPrefix(tenantID))
	}

	if ref.Kind == ports.MediaReferenceDurable {
		return raw, false, nil
	}

	durableURI, err := s.copier.CopyToDurable(ctx, ports.CopyToDurableParams{
		SrcBucket: ref.Bucket,
		SrcKey:    ref.Key,
		TenantID:  tenantID,
		AtomID:    atomID,
		Ext:       extOf(ref.Key),
	})
	if err != nil {
		return "", false, fmt.Errorf("mediarehome: copy: %w", err)
	}
	return durableURI, true, nil
}

// referenceBelongsToTenant reports whether a classified reference's key sits
// under the caller's own per-tenant namespace (ports.TenantMediaPrefix).
//
// The boundary is TENANT-level, deliberately — see the policy note on
// ports.TenantMediaPrefix. A same-tenant key under any job or atom is accepted;
// narrowing to the originating job would need the caller's authorship of that
// job, which this namespace cannot answer. ref.Key comes from
// ClassifyReference, which strips the presign query, so it is safe to name in
// the error.
func referenceBelongsToTenant(tenantID, key string) bool {
	return strings.HasPrefix(key, ports.TenantMediaPrefix(tenantID))
}

// extOf returns the lowercased file extension of an object key, defaulting
// to "png" (the orchestrator's render default) when absent.
func extOf(key string) string {
	dot := strings.LastIndexByte(key, '.')
	slash := strings.LastIndexByte(key, '/')
	if dot <= slash || dot == len(key)-1 {
		return "png"
	}
	return strings.ToLower(key[dot+1:])
}
