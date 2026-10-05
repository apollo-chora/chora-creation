// Package mediarehome is the W8 durable image re-home application service
// (OT#4). On atom publish, the orchestrator's transient 7-day signed image
// URLs are re-homed into the durable atom-media bucket and rewritten to a
// canonical gs:// ref so a published atom's images survive past 7 days.
//
// This package owns the DECISION logic (is the ref transient? what are the
// source coordinates? idempotency) and delegates the actual GCS copy to the
// ports.MediaReHomer port (the GCS adapter). It is infrastructure-free and
// fully unit-tested; the publish handler wires it over the real adapter.
package mediarehome

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// Service re-homes transient W8 image URLs into durable storage.
type Service struct {
	copier ports.MediaReHomer
}

// New builds the service over a MediaReHomer (the GCS adapter in production).
func New(copier ports.MediaReHomer) *Service { return &Service{copier: copier} }

// ReHome ensures imageURL points at durable storage and returns the durable
// gs:// ref plus whether it changed:
//
//   - empty input                → ("", false, nil)  (no image)
//   - already-durable gs:// ref   → (ref, false, nil) (idempotent — no copy)
//   - transient GCS signed URL    → (durable gs://, true, nil) after copy
//   - foreign gs:// / non-GCS URL → ("", false, error) (fail-loud)
//
// tenantID + atomID scope the destination key. A publish that re-homes the
// same atom twice is a no-op the second time (the stored ref is already a
// durable gs://), so re-publish is safe.
func (s *Service) ReHome(ctx context.Context, tenantID, atomID, imageURL string) (string, bool, error) {
	raw := strings.TrimSpace(imageURL)
	if raw == "" {
		return "", false, nil
	}

	// Already a gs:// ref — idempotent when it's the durable bucket.
	if strings.HasPrefix(raw, "gs://") {
		bucket, _, err := splitGSURI(raw)
		if err != nil {
			return "", false, fmt.Errorf("mediarehome: %w", err)
		}
		if bucket == s.copier.DurableBucket() {
			return raw, false, nil
		}
		return "", false, fmt.Errorf(
			"mediarehome: stored gs:// bucket %q is neither transient nor the durable bucket %q",
			bucket, s.copier.DurableBucket())
	}

	// Transient HTTPS signed URL — parse the source object coordinates.
	srcBucket, srcKey, err := parseGCSHTTPURL(raw)
	if err != nil {
		return "", false, fmt.Errorf("mediarehome: %w", err)
	}

	gsURI, err := s.copier.CopyToDurable(ctx, ports.CopyToDurableParams{
		SrcBucket: srcBucket,
		SrcKey:    srcKey,
		TenantID:  tenantID,
		AtomID:    atomID,
		Ext:       extOf(srcKey),
	})
	if err != nil {
		return "", false, fmt.Errorf("mediarehome: copy: %w", err)
	}
	return gsURI, true, nil
}

// splitGSURI splits gs://{bucket}/{key}.
func splitGSURI(gsURI string) (bucket, key string, err error) {
	const scheme = "gs://"
	rest := strings.TrimPrefix(gsURI, scheme)
	if rest == gsURI {
		return "", "", fmt.Errorf("not a gs:// URI: %q", gsURI)
	}
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i >= len(rest)-1 {
		return "", "", fmt.Errorf("malformed gs:// URI: %q", gsURI)
	}
	return rest[:i], rest[i+1:], nil
}

// parseGCSHTTPURL extracts (bucket, object-key) from a Google Cloud Storage
// HTTPS URL — both path-style (storage.googleapis.com/{bucket}/{key}, the
// orchestrator's V4 default) and virtual-hosted ({bucket}.storage.googleapis
// .com/{key}). The query string (the V4 signature) is stripped. Any other
// host is rejected — we only re-home GCS objects.
func parseGCSHTTPURL(raw string) (bucket, key string, err error) {
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("parse image URL: %w", perr)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", "", fmt.Errorf("unrecognized image URL scheme %q in %q", u.Scheme, raw)
	}
	host := u.Host
	path := strings.TrimPrefix(u.Path, "/") // decoded object name; query already excluded

	switch {
	case host == "storage.googleapis.com":
		i := strings.IndexByte(path, '/')
		if i <= 0 || i >= len(path)-1 {
			return "", "", fmt.Errorf("malformed GCS path-style URL: %q", raw)
		}
		return path[:i], path[i+1:], nil
	case strings.HasSuffix(host, ".storage.googleapis.com"):
		bucket = strings.TrimSuffix(host, ".storage.googleapis.com")
		if bucket == "" || path == "" {
			return "", "", fmt.Errorf("malformed GCS virtual-hosted URL: %q", raw)
		}
		return bucket, path, nil
	default:
		return "", "", fmt.Errorf("not a GCS URL (host %q): %q", host, raw)
	}
}

// extOf returns the lowercased file extension of a GCS object key, defaulting
// to "png" (the orchestrator's render default) when absent.
func extOf(key string) string {
	dot := strings.LastIndexByte(key, '.')
	slash := strings.LastIndexByte(key, '/')
	if dot <= slash || dot == len(key)-1 {
		return "png"
	}
	return strings.ToLower(key[dot+1:])
}
