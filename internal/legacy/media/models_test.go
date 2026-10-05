package media

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// ProcessingJobStatus enum
// ---------------------------------------------------------------------------

func TestProcessingJobStatusValues(t *testing.T) {
	tests := []struct {
		name     string
		status   ProcessingJobStatus
		expected string
	}{
		{"Queued", ProcessingJobStatusQueued, "queued"},
		{"Processing", ProcessingJobStatusProcessing, "processing"},
		{"Completed", ProcessingJobStatusCompleted, "completed"},
		{"Failed", ProcessingJobStatusFailed, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.status))
		})
	}
}

// ---------------------------------------------------------------------------
// ScanType enum
// ---------------------------------------------------------------------------

func TestScanTypeValues(t *testing.T) {
	tests := []struct {
		name     string
		scanType ScanType
		expected string
	}{
		{"Antivirus", ScanTypeAntivirus, "antivirus"},
		{"ContentModeration", ScanTypeContentModeration, "content_moderation"},
		{"OCRPolicy", ScanTypeOCRPolicy, "ocr_policy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.scanType))
		})
	}
}

// ---------------------------------------------------------------------------
// ScanResult enum
// ---------------------------------------------------------------------------

func TestScanResultValues(t *testing.T) {
	tests := []struct {
		name       string
		scanResult ScanResult
		expected   string
	}{
		{"Clean", ScanResultClean, "clean"},
		{"Threat", ScanResultThreat, "threat"},
		{"NSFW", ScanResultNSFW, "nsfw"},
		{"PolicyViolation", ScanResultPolicyViolation, "policy_violation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.scanResult))
		})
	}
}

// ---------------------------------------------------------------------------
// VariantType enum
// ---------------------------------------------------------------------------

func TestVariantTypeValues(t *testing.T) {
	tests := []struct {
		name        string
		variantType VariantType
		expected    string
	}{
		{"Thumb", VariantTypeThumb, "thumb"},
		{"Medium", VariantTypeMedium, "medium"},
		{"Large", VariantTypeLarge, "large"},
		{"Original", VariantTypeOriginal, "original"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.variantType))
		})
	}
}

// ---------------------------------------------------------------------------
// ProcessingStatus enum
// ---------------------------------------------------------------------------

func TestProcessingStatusValues(t *testing.T) {
	tests := []struct {
		name     string
		status   ProcessingStatus
		expected string
	}{
		{"Pending", ProcessingStatusPending, "pending"},
		{"Processing", ProcessingStatusProcessing, "processing"},
		{"Ready", ProcessingStatusReady, "ready"},
		{"Quarantined", ProcessingStatusQuarantined, "quarantined"},
		{"Failed", ProcessingStatusFailed, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.status))
		})
	}
}

// ---------------------------------------------------------------------------
// Standard Variant Specifications
// ---------------------------------------------------------------------------

func TestThumbSpec(t *testing.T) {
	assert.Equal(t, VariantTypeThumb, ThumbSpec.Type)
	assert.Equal(t, 320, ThumbSpec.MaxWidth)
	assert.Equal(t, 320, ThumbSpec.MaxHeight)
	assert.Equal(t, 80, ThumbSpec.Quality)
	assert.Equal(t, "webp", ThumbSpec.Format)
}

func TestMediumSpec(t *testing.T) {
	assert.Equal(t, VariantTypeMedium, MediumSpec.Type)
	assert.Equal(t, 768, MediumSpec.MaxWidth)
	assert.Equal(t, 768, MediumSpec.MaxHeight)
	assert.Equal(t, 85, MediumSpec.Quality)
	assert.Equal(t, "webp", MediumSpec.Format)
}

func TestLargeSpec(t *testing.T) {
	assert.Equal(t, VariantTypeLarge, LargeSpec.Type)
	assert.Equal(t, 1920, LargeSpec.MaxWidth)
	assert.Equal(t, 1080, LargeSpec.MaxHeight)
	assert.Equal(t, 90, LargeSpec.Quality)
	assert.Equal(t, "webp", LargeSpec.Format)
}

// ---------------------------------------------------------------------------
// ProcessingJob struct
// ---------------------------------------------------------------------------

func TestProcessingJobFields(t *testing.T) {
	id := uuid.New()
	tenantID := uuid.New()
	assetID := uuid.New()
	now := time.Now().UTC()

	job := ProcessingJob{
		ID:           id,
		TenantID:     tenantID,
		MediaAssetID: assetID,
		Status:       ProcessingJobStatusQueued,
		JobType:      "full_pipeline",
		StartedAt:    &now,
		CompletedAt:  nil,
		ErrorMessage: "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	assert.Equal(t, id, job.ID)
	assert.Equal(t, tenantID, job.TenantID)
	assert.Equal(t, assetID, job.MediaAssetID)
	assert.Equal(t, ProcessingJobStatusQueued, job.Status)
	assert.Equal(t, "full_pipeline", job.JobType)
	assert.NotNil(t, job.StartedAt)
	assert.Nil(t, job.CompletedAt)
	assert.Empty(t, job.ErrorMessage)
}

// ---------------------------------------------------------------------------
// ScanResultEntry struct
// ---------------------------------------------------------------------------

func TestScanResultEntryFields(t *testing.T) {
	id := uuid.New()
	tenantID := uuid.New()
	assetID := uuid.New()
	now := time.Now().UTC()

	entry := ScanResultEntry{
		ID:           id,
		TenantID:     tenantID,
		MediaAssetID: assetID,
		ScanType:     ScanTypeAntivirus,
		Result:       ScanResultClean,
		Details:      map[string]any{"engine": "ClamAV"},
		ScannedAt:    now,
		CreatedAt:    now,
	}

	assert.Equal(t, id, entry.ID)
	assert.Equal(t, tenantID, entry.TenantID)
	assert.Equal(t, assetID, entry.MediaAssetID)
	assert.Equal(t, ScanTypeAntivirus, entry.ScanType)
	assert.Equal(t, ScanResultClean, entry.Result)
	assert.Equal(t, "ClamAV", entry.Details["engine"])
}

func TestScanResultEntryNilDetails(t *testing.T) {
	entry := ScanResultEntry{
		Details: nil,
	}
	assert.Nil(t, entry.Details)
}

// ---------------------------------------------------------------------------
// ImageVariant struct
// ---------------------------------------------------------------------------

func TestImageVariantFields(t *testing.T) {
	assetID := uuid.New()

	variant := ImageVariant{
		MediaAssetID: assetID,
		VariantType:  VariantTypeThumb,
		Data:         []byte("image-data"),
		Width:        320,
		Height:       240,
		FileSize:     1024,
		MimeType:     "image/webp",
	}

	assert.Equal(t, assetID, variant.MediaAssetID)
	assert.Equal(t, VariantTypeThumb, variant.VariantType)
	assert.Equal(t, 320, variant.Width)
	assert.Equal(t, 240, variant.Height)
	assert.Equal(t, int64(1024), variant.FileSize)
	assert.Equal(t, "image/webp", variant.MimeType)
	assert.Len(t, variant.Data, 10)
}

// ---------------------------------------------------------------------------
// ProcessingRequest struct
// ---------------------------------------------------------------------------

func TestProcessingRequestFields(t *testing.T) {
	assetID := uuid.New()
	tenantID := uuid.New()

	req := ProcessingRequest{
		MediaAssetID:    assetID,
		TenantID:        tenantID,
		StorageKey:      "tenants/abc/media/file.jpg",
		MimeType:        "image/jpeg",
		FileSize:        2048,
		StorageCategory: "learning_content",
	}

	assert.Equal(t, assetID, req.MediaAssetID)
	assert.Equal(t, tenantID, req.TenantID)
	assert.Equal(t, "tenants/abc/media/file.jpg", req.StorageKey)
	assert.Equal(t, "image/jpeg", req.MimeType)
	assert.Equal(t, int64(2048), req.FileSize)
	assert.Equal(t, "learning_content", req.StorageCategory)
}

// ---------------------------------------------------------------------------
// ProcessingResult struct
// ---------------------------------------------------------------------------

func TestProcessingResultFields(t *testing.T) {
	assetID := uuid.New()
	tenantID := uuid.New()

	result := ProcessingResult{
		MediaAssetID:    assetID,
		TenantID:        tenantID,
		Status:          ProcessingStatusReady,
		ScanResults:     []ScanResultEntry{},
		Variants:        []ImageVariant{},
		Width:           1920,
		Height:          1080,
		DurationSeconds: 1.5,
		ContentHash:     "abc123",
	}

	assert.Equal(t, assetID, result.MediaAssetID)
	assert.Equal(t, tenantID, result.TenantID)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Empty(t, result.ScanResults)
	assert.Empty(t, result.Variants)
	assert.Nil(t, result.QualityAssessment)
	assert.Equal(t, 1920, result.Width)
	assert.Equal(t, 1080, result.Height)
	assert.Equal(t, 1.5, result.DurationSeconds)
	assert.Equal(t, "abc123", result.ContentHash)
}

// ---------------------------------------------------------------------------
// VariantSpec struct
// ---------------------------------------------------------------------------

func TestVariantSpecFields(t *testing.T) {
	spec := VariantSpec{
		Type:      VariantTypeLarge,
		MaxWidth:  1920,
		MaxHeight: 1080,
		Quality:   90,
		Format:    "webp",
	}

	assert.Equal(t, VariantTypeLarge, spec.Type)
	assert.Equal(t, 1920, spec.MaxWidth)
	assert.Equal(t, 1080, spec.MaxHeight)
	assert.Equal(t, 90, spec.Quality)
	assert.Equal(t, "webp", spec.Format)
}
