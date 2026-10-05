// Package media is the MediaAsset aggregate of the Content Creation domain.
//
// MediaAsset owns the ingestion lifecycle for a LearningAtom's media payload
// (video / audio / image / document). It is a separate aggregate from
// LearningAtom (cross-aggregate reference via AtomID UUID; no FK).
//
// MVP scope per audit `docs/m13/audit-content-fillgaps.md` §3.1 row 8:
//   - aggregate skeleton + state machine
//   - upload-reference (signed URL placeholder; real GCS V4 sign deferred)
//   - content-type allow-list guard (security)
//   - soft-delete invariant (DDD #5)
//
// Out-of-scope for MVP (deferred):
//   - TranscodeJob aggregate (FFmpeg / Cloud Run Jobs)
//   - QualityScore aggregate (perceptual hash / loudness)
//
// Hexagonal: this package is dependency-free w.r.t. infrastructure; ports
// are defined in this package next to the aggregate.
package media

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Status state machine
//
//	pending_upload  → uploaded → ready
//	                ↘ uploaded → failed
//
// Soft-delete (DeletedAt) is orthogonal to status; an asset can be soft-
// deleted from any status.
// -----------------------------------------------------------------------------

type Status string

const (
	StatusPendingUpload Status = "pending_upload"
	StatusUploaded      Status = "uploaded"
	StatusReady         Status = "ready"
	StatusFailed        Status = "failed"
)

// AllowedContentTypes is the security allow-list. Anything outside this set is
// rejected at New() time. Per principle-conflict-resolution security wins —
// even if a creator wants to upload .exe, we refuse.
var AllowedContentTypes = map[string]struct{}{
	"video/mp4":       {},
	"video/webm":      {},
	"video/quicktime": {},
	"audio/mpeg":      {},
	"audio/wav":       {},
	"audio/webm":      {},
	"image/png":       {},
	"image/jpeg":      {},
	"image/webp":      {},
	"image/gif":       {},
	"application/pdf": {},
}

// MaxSizeBytes caps a single asset upload. 4 GB is the Cloud Storage single-
// object limit for resumable uploads via signed URL; the MVP rejects above
// 2 GB to keep the demo simple.
const MaxSizeBytes int64 = 2 * 1024 * 1024 * 1024

// MediaAsset is the aggregate root.
type MediaAsset struct {
	AssetID     string     `json:"asset_id"`
	TenantID    string     `json:"tenant_id"`
	Gcid        string     `json:"gcid"` // creator
	AtomID      string     `json:"atom_id"`
	Filename    string     `json:"filename"`
	ContentType string     `json:"content_type"`
	SizeBytes   int64      `json:"size_bytes"`
	Status      Status     `json:"status"`
	GCSURI      string     `json:"gcs_uri,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID    string
	Gcid        string
	AtomID      string
	Filename    string
	ContentType string
	SizeBytes   int64
}

// New constructs a MediaAsset in PENDING_UPLOAD status. Returns an error if
// the inputs violate aggregate invariants.
func New(p NewParams) (*MediaAsset, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.Gcid) == "" {
		return nil, errors.New("gcid is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("atom_id is required")
	}
	fn := strings.TrimSpace(p.Filename)
	if fn == "" {
		return nil, errors.New("filename is required")
	}
	if len(fn) > 256 {
		return nil, fmt.Errorf("filename too long: %d > 256", len(fn))
	}
	ct := strings.TrimSpace(p.ContentType)
	if ct == "" {
		return nil, errors.New("content_type is required")
	}
	if _, ok := AllowedContentTypes[strings.ToLower(ct)]; !ok {
		return nil, fmt.Errorf("content_type %q is not allowed", ct)
	}
	if p.SizeBytes <= 0 {
		return nil, errors.New("size_bytes must be > 0")
	}
	if p.SizeBytes > MaxSizeBytes {
		return nil, fmt.Errorf("size_bytes %d exceeds limit %d", p.SizeBytes, MaxSizeBytes)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}

	now := time.Now().UTC()
	return &MediaAsset{
		AssetID:     id.String(),
		TenantID:    p.TenantID,
		Gcid:        p.Gcid,
		AtomID:      p.AtomID,
		Filename:    fn,
		ContentType: strings.ToLower(ct),
		SizeBytes:   p.SizeBytes,
		Status:      StatusPendingUpload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// MarkUploaded transitions pending_upload → uploaded. Idempotent on repeat
// call with same status.
func (m *MediaAsset) MarkUploaded(gcsURI string) error {
	if m.DeletedAt != nil {
		return errors.New("cannot transition soft-deleted asset")
	}
	switch m.Status {
	case StatusPendingUpload:
		m.Status = StatusUploaded
		m.GCSURI = strings.TrimSpace(gcsURI)
		m.UpdatedAt = time.Now().UTC()
		return nil
	case StatusUploaded:
		return nil // idempotent
	default:
		return fmt.Errorf("cannot transition status %q → uploaded", m.Status)
	}
}

// MarkReady transitions uploaded → ready (post transcode/quality gate).
// Idempotent on repeat call with same status.
func (m *MediaAsset) MarkReady() error {
	if m.DeletedAt != nil {
		return errors.New("cannot transition soft-deleted asset")
	}
	switch m.Status {
	case StatusUploaded:
		m.Status = StatusReady
		m.UpdatedAt = time.Now().UTC()
		return nil
	case StatusReady:
		return nil // idempotent
	default:
		return fmt.Errorf("cannot transition status %q → ready", m.Status)
	}
}

// MarkFailed transitions any non-deleted asset to FAILED state.
func (m *MediaAsset) MarkFailed(reason string) error {
	if m.DeletedAt != nil {
		return errors.New("cannot transition soft-deleted asset")
	}
	m.Status = StatusFailed
	m.UpdatedAt = time.Now().UTC()
	_ = reason // reserved for a future field; not persisted in MVP
	return nil
}

// SoftDelete marks the asset deleted (idempotent).
func (m *MediaAsset) SoftDelete() error {
	if m.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	m.DeletedAt = &now
	m.UpdatedAt = now
	return nil
}

// IsActive reports whether the asset has not been soft-deleted.
func (m *MediaAsset) IsActive() bool { return m.DeletedAt == nil }
