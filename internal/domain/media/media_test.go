// MediaAsset domain tests — Phyllis MVP S5 stub per audit §3.1.
//
// MediaAsset is the aggregate that owns ingestion lifecycle for an atom's
// media (video/audio/image/document). TranscodeJob + QualityScore are
// out-of-scope for the MVP stub; this domain ships the upload-reference
// state machine + content-type guard.
package media_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
	atomA   = "01970000-0000-7000-aaaa-000000000001"
)

func TestNew_HappyPath(t *testing.T) {
	t.Parallel()

	m, err := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024 * 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.AssetID == "" {
		t.Errorf("AssetID empty")
	}
	if m.Status != media.StatusPendingUpload {
		t.Errorf("Status = %q; want pending_upload", m.Status)
	}
	if m.TenantID != tenantA {
		t.Errorf("TenantID = %q", m.TenantID)
	}
	if m.AtomID != atomA {
		t.Errorf("AtomID = %q", m.AtomID)
	}
	if m.CreatedAt.IsZero() {
		t.Errorf("CreatedAt zero")
	}
	if m.DeletedAt != nil {
		t.Errorf("DeletedAt should be nil on fresh asset")
	}
}

func TestNew_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		p    media.NewParams
		want string
	}{
		{
			"missing tenant",
			media.NewParams{Gcid: gcidA, AtomID: atomA, Filename: "f.mp4", ContentType: "video/mp4", SizeBytes: 1},
			"tenant_id",
		},
		{
			"missing gcid",
			media.NewParams{TenantID: tenantA, AtomID: atomA, Filename: "f.mp4", ContentType: "video/mp4", SizeBytes: 1},
			"gcid",
		},
		{
			"missing atom_id",
			media.NewParams{TenantID: tenantA, Gcid: gcidA, Filename: "f.mp4", ContentType: "video/mp4", SizeBytes: 1},
			"atom_id",
		},
		{
			"empty filename",
			media.NewParams{TenantID: tenantA, Gcid: gcidA, AtomID: atomA, Filename: "", ContentType: "video/mp4", SizeBytes: 1},
			"filename",
		},
		{
			"disallowed content type",
			media.NewParams{TenantID: tenantA, Gcid: gcidA, AtomID: atomA, Filename: "evil.exe", ContentType: "application/x-msdownload", SizeBytes: 1},
			"content_type",
		},
		{
			"zero size",
			media.NewParams{TenantID: tenantA, Gcid: gcidA, AtomID: atomA, Filename: "f.mp4", ContentType: "video/mp4", SizeBytes: 0},
			"size_bytes",
		},
		{
			"size exceeds limit",
			media.NewParams{TenantID: tenantA, Gcid: gcidA, AtomID: atomA, Filename: "f.mp4", ContentType: "video/mp4", SizeBytes: 5_000_000_000},
			"size_bytes",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := media.New(tc.p)
			if err == nil {
				t.Fatalf("expected error mentioning %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestStatusTransitions(t *testing.T) {
	t.Parallel()

	m, err := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Status != media.StatusPendingUpload {
		t.Fatalf("initial status = %q", m.Status)
	}

	if err := m.MarkUploaded("gs://chora-content-media/asset/intro.mp4"); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	if m.Status != media.StatusUploaded {
		t.Errorf("after MarkUploaded status = %q; want uploaded", m.Status)
	}
	if m.GCSURI == "" {
		t.Errorf("GCSURI empty after MarkUploaded")
	}

	if err := m.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if m.Status != media.StatusReady {
		t.Errorf("after MarkReady status = %q; want ready", m.Status)
	}

	// Idempotent re-call → same status, no error.
	if err := m.MarkReady(); err != nil {
		t.Errorf("MarkReady idempotent: %v", err)
	}
}

func TestStatusTransitions_RejectInvalid(t *testing.T) {
	t.Parallel()

	m, _ := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	})
	// Cannot go from pending_upload → ready directly.
	if err := m.MarkReady(); err == nil {
		t.Errorf("MarkReady from pending_upload: expected error")
	}
	// Soft-deleted asset rejects all transitions.
	_ = m.SoftDelete()
	if err := m.MarkUploaded("gs://x"); err == nil {
		t.Errorf("MarkUploaded on deleted: expected error")
	}
	if err := m.MarkReady(); err == nil {
		t.Errorf("MarkReady on deleted: expected error")
	}
	if err := m.MarkFailed("test"); err == nil {
		t.Errorf("MarkFailed on deleted: expected error")
	}
}

func TestMarkFailed(t *testing.T) {
	t.Parallel()

	m, _ := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	})
	if err := m.MarkFailed("transcode timeout"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if m.Status != media.StatusFailed {
		t.Errorf("Status = %q; want failed", m.Status)
	}
}

func TestMarkUploaded_Idempotent(t *testing.T) {
	t.Parallel()

	m, _ := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	})
	if err := m.MarkUploaded("gs://b/k"); err != nil {
		t.Fatalf("first: %v", err)
	}
	gcsURI := m.GCSURI
	// Re-call returns nil; idempotent.
	if err := m.MarkUploaded("gs://b/different"); err != nil {
		t.Errorf("idempotent re-call: %v", err)
	}
	if m.GCSURI != gcsURI {
		t.Errorf("GCSURI mutated on idempotent re-call: %q vs %q", m.GCSURI, gcsURI)
	}

	// Cannot go ready→uploaded.
	_ = m.MarkReady()
	if err := m.MarkUploaded("gs://x"); err == nil {
		t.Errorf("MarkUploaded on ready: expected error")
	}
}

func TestSoftDelete(t *testing.T) {
	t.Parallel()

	m, _ := media.New(media.NewParams{
		TenantID:    tenantA,
		Gcid:        gcidA,
		AtomID:      atomA,
		Filename:    "intro.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	})
	if !m.IsActive() {
		t.Errorf("fresh asset should be active")
	}
	if err := m.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if m.IsActive() {
		t.Errorf("soft-deleted asset should NOT be active")
	}
	if m.DeletedAt == nil {
		t.Errorf("DeletedAt should be set")
	}
	// Soft-delete is idempotent.
	if err := m.SoftDelete(); err != nil {
		t.Errorf("SoftDelete idempotent: %v", err)
	}
}

func TestAllowedContentTypes(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"video/mp4", "video/webm", "video/quicktime",
		"audio/mpeg", "audio/wav", "audio/webm",
		"image/png", "image/jpeg", "image/webp", "image/gif",
		"application/pdf",
	}
	for _, ct := range allowed {
		ct := ct
		t.Run(ct, func(t *testing.T) {
			t.Parallel()
			_, err := media.New(media.NewParams{
				TenantID:    tenantA,
				Gcid:        gcidA,
				AtomID:      atomA,
				Filename:    "ok.dat",
				ContentType: ct,
				SizeBytes:   1,
			})
			if err != nil {
				t.Errorf("expected %q allowed, got %v", ct, err)
			}
		})
	}

	// Sample blocked types.
	blocked := []string{"application/x-msdownload", "application/x-sh", "text/x-script"}
	for _, ct := range blocked {
		ct := ct
		t.Run("blocked-"+ct, func(t *testing.T) {
			t.Parallel()
			_, err := media.New(media.NewParams{
				TenantID:    tenantA,
				Gcid:        gcidA,
				AtomID:      atomA,
				Filename:    "x",
				ContentType: ct,
				SizeBytes:   1,
			})
			if err == nil {
				t.Errorf("expected %q blocked, got nil", ct)
			}
		})
	}
}
