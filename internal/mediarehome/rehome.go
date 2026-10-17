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
//   - foreign-tenant source     → ("", false, error) (fail-loud — no copy)
//   - unclassifiable ref        → ("", false, error) (fail-loud)
//
// tenantID + atomID scope the destination key. A publish that re-homes the
// same atom twice is a no-op the second time (the stored ref is already
// durable), so re-publish is safe.
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
	if ref.Kind == ports.MediaReferenceDurable {
		return raw, false, nil
	}

	// OWNERSHIP (defense in depth — the adapter checks this too). A trusted
	// origin + bucket does not prove the object belongs to the tenant being
	// accepted: the accept path prefers a client-supplied image_url over the
	// stored draft's, and the copy runs with the adapter's own credentials.
	// Refuse a foreign-tenant source before asking for the copy.
	if !sourceBelongsToTenant(tenantID, ref.Key) {
		return "", false, fmt.Errorf(
			"mediarehome: refusing to re-home %q — source object is outside tenant %q's namespace %q",
			ref.Key, tenantID, ports.TransientTenantPrefix(tenantID))
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

// sourceBelongsToTenant reports whether a classified transient source key sits
// under the caller's own per-tenant namespace (ports.TransientTenantPrefix).
//
// Only the TENANT is namespaced, not the job: a same-tenant key under any job
// is a legitimate re-home source, so this deliberately does not narrow to
// `jobs/{job_id}/`. ref.Key comes from ClassifyReference, which strips the
// presign query, so it is safe to name in the error.
func sourceBelongsToTenant(tenantID, key string) bool {
	return strings.HasPrefix(key, ports.TransientTenantPrefix(tenantID))
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
