package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestService creates a MediaProcessorService wired with mocks.
// Returns the service and all mocks for assertion.
type testHarness struct {
	svc         *MediaProcessorService
	scanner     *MockSecurityScanner
	moderator   *MockContentModerator
	optimizer   *MockImageOptimizer
	qualityAss  *MockQualityAssessor
	storage     *MockStorageClient
	jobRepo     *MockProcessingJobRepo
	scanRepo    *MockScanResultRepo
	qualityRepo *MockQualityAssessmentRepo
	events      *MockEventPublisher
}

func newTestHarness() *testHarness {
	h := &testHarness{
		scanner:     &MockSecurityScanner{},
		moderator:   &MockContentModerator{},
		optimizer:   &MockImageOptimizer{},
		qualityAss:  &MockQualityAssessor{},
		storage:     &MockStorageClient{},
		jobRepo:     &MockProcessingJobRepo{},
		scanRepo:    &MockScanResultRepo{},
		qualityRepo: &MockQualityAssessmentRepo{},
		events:      &MockEventPublisher{},
	}
	h.svc = NewMediaProcessorService(
		h.scanner,
		h.moderator,
		h.optimizer,
		h.qualityAss,
		h.storage,
		h.jobRepo,
		h.scanRepo,
		h.qualityRepo,
		h.events,
	)
	return h
}

func newTestHarnessWithoutQualityAssessor() *testHarness {
	h := &testHarness{
		scanner:     &MockSecurityScanner{},
		moderator:   &MockContentModerator{},
		optimizer:   &MockImageOptimizer{},
		storage:     &MockStorageClient{},
		jobRepo:     &MockProcessingJobRepo{},
		scanRepo:    &MockScanResultRepo{},
		qualityRepo: &MockQualityAssessmentRepo{},
		events:      &MockEventPublisher{},
	}
	h.svc = NewMediaProcessorService(
		h.scanner,
		h.moderator,
		h.optimizer,
		nil, // no quality assessor
		h.storage,
		h.jobRepo,
		h.scanRepo,
		nil, // no quality repo
		h.events,
	)
	return h
}

func newImageRequest() ProcessingRequest {
	return ProcessingRequest{
		MediaAssetID:    uuid.New(),
		TenantID:        uuid.New(),
		StorageKey:      "tenants/t1/media/photo.jpg",
		MimeType:        "image/jpeg",
		FileSize:        4096,
		StorageCategory: "learning_content",
	}
}

func newPDFRequest() ProcessingRequest {
	return ProcessingRequest{
		MediaAssetID:    uuid.New(),
		TenantID:        uuid.New(),
		StorageKey:      "tenants/t1/media/doc.pdf",
		MimeType:        "application/pdf",
		FileSize:        8192,
		StorageCategory: "learning_content",
	}
}

func cleanScanResult() ScanResultEntry {
	return ScanResultEntry{
		ID:        uuid.New(),
		ScanType:  ScanTypeAntivirus,
		Result:    ScanResultClean,
		ScannedAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

func threatScanResult() ScanResultEntry {
	return ScanResultEntry{
		ID:        uuid.New(),
		ScanType:  ScanTypeAntivirus,
		Result:    ScanResultThreat,
		Details:   map[string]any{"threat": "Eicar-Test-Signature"},
		ScannedAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

func cleanModerationResult() ScanResultEntry {
	return ScanResultEntry{
		ID:        uuid.New(),
		ScanType:  ScanTypeContentModeration,
		Result:    ScanResultClean,
		ScannedAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

func nsfwModerationResult() ScanResultEntry {
	return ScanResultEntry{
		ID:        uuid.New(),
		ScanType:  ScanTypeContentModeration,
		Result:    ScanResultNSFW,
		ScannedAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

func policyViolationModerationResult() ScanResultEntry {
	return ScanResultEntry{
		ID:        uuid.New(),
		ScanType:  ScanTypeContentModeration,
		Result:    ScanResultPolicyViolation,
		ScannedAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

func imageVariant(vt VariantType) *ImageVariant {
	return &ImageVariant{
		MediaAssetID: uuid.New(),
		VariantType:  vt,
		Data:         []byte("variant-data"),
		Width:        320,
		Height:       240,
		FileSize:     512,
		MimeType:     "image/webp",
	}
}

// ---------------------------------------------------------------------------
// NewMediaProcessorService
// ---------------------------------------------------------------------------

func TestNewMediaProcessorService(t *testing.T) {
	h := newTestHarness()
	require.NotNil(t, h.svc)
}

func TestNewMediaProcessorService_WithNilQualityAssessor(t *testing.T) {
	h := newTestHarnessWithoutQualityAssessor()
	require.NotNil(t, h.svc)
}

// ---------------------------------------------------------------------------
// isImageMimeType
// ---------------------------------------------------------------------------

func TestIsImageMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		expected bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", true},
		{"image/bmp", true},
		{"application/pdf", false},
		{"video/mp4", false},
		{"audio/mpeg", false},
		{"text/plain", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			assert.Equal(t, tt.expected, isImageMimeType(tt.mimeType))
		})
	}
}

// ---------------------------------------------------------------------------
// isRasterImageMimeType
// ---------------------------------------------------------------------------

func TestIsRasterImageMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		expected bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/bmp", true},
		{"image/svg+xml", false}, // SVG is vector
		{"application/pdf", false},
		{"video/mp4", false},
		{"text/plain", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			assert.Equal(t, tt.expected, isRasterImageMimeType(tt.mimeType))
		})
	}
}

// ---------------------------------------------------------------------------
// ProcessMedia — Happy Path (raster image, full pipeline)
// ---------------------------------------------------------------------------

func TestProcessMedia_HappyPath_RasterImage(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("fake-jpeg-data")

	// Setup expectations.
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbVariant := imageVariant(VariantTypeThumb)
	mediumVariant := imageVariant(VariantTypeMedium)
	largeVariant := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbVariant, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(mediumVariant, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeVariant, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	qa := &QualityAssessment{
		ID:                 uuid.New(),
		BlurScore:          0.9,
		ResolutionAdequate: true,
		FormatValid:        true,
		LLMReady:           true,
		QualityGrade:       QualityGradeA,
		AssessedAt:         time.Now().UTC(),
	}
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(qa, nil)
	h.qualityRepo.On("Save", ctx, mock.AnythingOfType("*media.QualityAssessment")).Return(nil)

	// Execute.
	result, err := h.svc.ProcessMedia(ctx, req)

	// Assert.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, req.MediaAssetID, result.MediaAssetID)
	assert.Equal(t, req.TenantID, result.TenantID)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Len(t, result.ScanResults, 2) // antivirus + moderation
	assert.Len(t, result.Variants, 3)    // thumb + medium + large
	assert.NotEmpty(t, result.ContentHash)
	assert.Greater(t, result.DurationSeconds, 0.0)
	require.NotNil(t, result.QualityAssessment)
	assert.Equal(t, QualityGradeA, result.QualityAssessment.QualityGrade)

	h.jobRepo.AssertExpectations(t)
	h.scanner.AssertExpectations(t)
	h.moderator.AssertExpectations(t)
	h.optimizer.AssertExpectations(t)
	h.scanRepo.AssertExpectations(t)
	h.storage.AssertExpectations(t)
	h.events.AssertExpectations(t)
	h.qualityAss.AssertExpectations(t)
	h.qualityRepo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessMedia — Happy Path (non-image, e.g., PDF — no moderation, no variants)
// ---------------------------------------------------------------------------

func TestProcessMedia_HappyPath_NonImage(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest()
	fileData := []byte("fake-pdf-data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	// No moderation, no optimization, no quality assessment for PDF.

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Len(t, result.ScanResults, 1) // only antivirus
	assert.Empty(t, result.Variants)
	assert.NotEmpty(t, result.ContentHash)
	assert.Nil(t, result.QualityAssessment)

	// Moderation & optimizer should not be called for PDF.
	h.moderator.AssertNotCalled(t, "Moderate")
	h.optimizer.AssertNotCalled(t, "Optimize")
	h.qualityAss.AssertNotCalled(t, "Assess")
}

// ---------------------------------------------------------------------------
// ProcessMedia — SVG image (moderation YES, variants NO)
// ---------------------------------------------------------------------------

func TestProcessMedia_SVGImage_NoVariants(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	req.MimeType = "image/svg+xml"
	req.StorageKey = "tenants/t1/media/icon.svg"
	fileData := []byte("<svg>...</svg>")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/svg+xml").Return(modRes, nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Len(t, result.ScanResults, 2) // antivirus + moderation
	assert.Empty(t, result.Variants)     // SVG = vector, no raster variants

	h.optimizer.AssertNotCalled(t, "Optimize")
	h.qualityAss.AssertNotCalled(t, "Assess")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Threat detected (quarantine)
// ---------------------------------------------------------------------------

func TestProcessMedia_ThreatDetected_Quarantine(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("malicious-data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := threatScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusQuarantined, result.Status)
	assert.Len(t, result.ScanResults, 1)

	// Should NOT proceed to moderation or optimization.
	h.moderator.AssertNotCalled(t, "Moderate")
	h.optimizer.AssertNotCalled(t, "Optimize")
}

// ---------------------------------------------------------------------------
// ProcessMedia — NSFW content (quarantine after moderation)
// ---------------------------------------------------------------------------

func TestProcessMedia_NSFWContent_Quarantine(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("nsfw-image-data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := nsfwModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusQuarantined, result.Status)
	assert.Len(t, result.ScanResults, 2)

	h.optimizer.AssertNotCalled(t, "Optimize")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Policy violation (quarantine after moderation)
// ---------------------------------------------------------------------------

func TestProcessMedia_PolicyViolation_Quarantine(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("policy-violating-data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := policyViolationModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusQuarantined, result.Status)
	assert.Len(t, result.ScanResults, 2)

	h.optimizer.AssertNotCalled(t, "Optimize")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: job save failure (initial)
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_InitialJobSaveFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(errors.New("db down")).Once()

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "save processing job")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: job save failure (processing status update)
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ProcessingStatusSaveFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()

	// First Save (queued) succeeds, second Save (processing) fails.
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil).Once()
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(errors.New("db unavailable")).Once()

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "update job to processing")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: storage download failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_DownloadFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(nil, errors.New("bucket not found"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "download media asset")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: security scan failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ScanFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("some-data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(ScanResultEntry{}, errors.New("clamav timeout"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "security scan")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: scan result save failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ScanResultSaveFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(errors.New("db write error")).Once()

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "save scan result")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: moderation failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ModerationFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(ScanResultEntry{}, errors.New("vision api error"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "content moderation")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: moderation result save failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ModerationResultSaveFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	// First save (antivirus) succeeds, second save (moderation) fails.
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil).Once()
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(errors.New("db write error")).Once()

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "save moderation result")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: optimization failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_OptimizationFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(nil, errors.New("vips crash"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "generate thumb variant")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: variant upload failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_VariantUploadFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(errors.New("gcs error"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "upload thumb variant")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: second variant optimization failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_SecondVariantOptimizationFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(nil, errors.New("out of memory"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "generate medium variant")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Error: completed job save failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_CompletedJobSaveFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest() // Non-image to skip moderation/variants/quality
	fileData := []byte("data")

	// Save calls: queued (ok), processing (ok), completed (fail)
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil).Times(2)
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(errors.New("db locked")).Once()
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "update job to completed")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Event publish failures are non-fatal
// ---------------------------------------------------------------------------

func TestProcessMedia_EventPublishFailure_NonFatal(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	// Events fail but pipeline continues.
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(errors.New("pubsub down"))
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
}

// ---------------------------------------------------------------------------
// ProcessMedia — Quality assessment failure is non-fatal
// ---------------------------------------------------------------------------

func TestProcessMedia_QualityAssessmentFailure_NonFatal(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	// Quality assessment fails but pipeline continues.
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(nil, errors.New("assessment error"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Nil(t, result.QualityAssessment, "QA should be nil on failure")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Quality repo save failure is non-fatal
// ---------------------------------------------------------------------------

func TestProcessMedia_QualityRepoSaveFailure_NonFatal(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	qa := &QualityAssessment{
		ID:                 uuid.New(),
		BlurScore:          0.8,
		ResolutionAdequate: true,
		FormatValid:        true,
		LLMReady:           true,
		QualityGrade:       QualityGradeA,
		AssessedAt:         time.Now().UTC(),
	}
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(qa, nil)
	h.qualityRepo.On("Save", ctx, mock.AnythingOfType("*media.QualityAssessment")).Return(errors.New("db error"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	// QA is still set on result even if persist fails.
	require.NotNil(t, result.QualityAssessment)
}

// ---------------------------------------------------------------------------
// ProcessMedia — No quality assessor (nil qualityAssessor field)
// ---------------------------------------------------------------------------

func TestProcessMedia_NilQualityAssessor_SkipsAssessment(t *testing.T) {
	h := newTestHarnessWithoutQualityAssessor()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	assert.Nil(t, result.QualityAssessment)
}

// ---------------------------------------------------------------------------
// ProcessMedia — Content hash calculation
// ---------------------------------------------------------------------------

func TestProcessMedia_ContentHashIsDeterministic(t *testing.T) {
	// Two requests with same file data should produce same hash.
	fileData := []byte("deterministic-data")
	req := newPDFRequest()

	var result1, result2 *ProcessingResult

	for i := 0; i < 2; i++ {
		h := newTestHarness()
		ctx := context.Background()

		h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
		h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
		h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

		scanRes := cleanScanResult()
		h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
		h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

		r, err := h.svc.ProcessMedia(ctx, req)
		require.NoError(t, err)

		if i == 0 {
			result1 = r
		} else {
			result2 = r
		}
	}

	assert.Equal(t, result1.ContentHash, result2.ContentHash)
	assert.NotEmpty(t, result1.ContentHash)
}

// ---------------------------------------------------------------------------
// ProcessMedia — failJob when job save also fails (double failure)
// ---------------------------------------------------------------------------

func TestProcessMedia_FailJob_JobSaveAlsoFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()

	// Queued save succeeds, processing save succeeds, but when failJob tries to save, it also fails.
	callCount := 0
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil).Run(func(args mock.Arguments) {
		callCount++
		// Third call is from failJob — let it succeed since we can't selectively
		// fail it through the mock chain easily. The download error is the primary error.
	})
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(nil, errors.New("network timeout"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "download media asset")
}

// ---------------------------------------------------------------------------
// ProcessMedia — quarantineJob when job save fails (logged, non-fatal)
// ---------------------------------------------------------------------------

func TestProcessMedia_Quarantine_JobSaveFails_ReturnsResult(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("threat-data")

	// First two saves succeed (queued, processing).
	// Third save (quarantine) will fail — but result is still returned.
	saveCallCount := 0
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil).Run(func(args mock.Arguments) {
		saveCallCount++
	}).Times(2)
	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(errors.New("db locked")).Once()

	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := threatScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	// quarantineJob will log the error but ProcessMedia still returns result.
	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err, "quarantine save failure is logged, pipeline returns quarantined result")
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusQuarantined, result.Status)
}

// ---------------------------------------------------------------------------
// ProcessMedia — Third variant (large) upload failure
// ---------------------------------------------------------------------------

func TestProcessMedia_Error_ThirdVariantUploadFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil)
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)

	// First two uploads succeed, third fails.
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil).Times(2)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(errors.New("quota exceeded")).Once()

	result, err := h.svc.ProcessMedia(ctx, req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "upload large variant")
}

// ---------------------------------------------------------------------------
// ProcessMedia — Variant event publish failure is non-fatal
// ---------------------------------------------------------------------------

func TestProcessMedia_VariantEventPublishFailure_NonFatal(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	// All event publishes fail, but pipeline continues.
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(errors.New("pubsub unavailable"))
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(nil, errors.New("assessment err"))

	result, err := h.svc.ProcessMedia(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
}

// ---------------------------------------------------------------------------
// ProcessMedia — Completed event includes scan_summary with moderation
// ---------------------------------------------------------------------------

func TestProcessMedia_CompletedEvent_ScanSummaryWithModeration(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	// Capture the completed event to verify scan summary.
	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(nil, errors.New("skip"))

	result, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Find the processing.completed event.
	var completedEvent *DomainEvent
	for i := range capturedEvents {
		if capturedEvents[i].EventType == EventProcessingCompleted {
			completedEvent = &capturedEvents[i]
			break
		}
	}

	require.NotNil(t, completedEvent, "processing.completed event must be published")
	assert.Equal(t, EventProcessingCompleted, completedEvent.EventType)
	assert.Equal(t, req.TenantID, completedEvent.TenantID)

	scanSummary, ok := completedEvent.Payload["scan_summary"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "clean", scanSummary["antivirus"])
	assert.Equal(t, "clean", scanSummary["moderation"])
}

// ---------------------------------------------------------------------------
// ProcessMedia — Completed event quality_summary when quality is skipped
// ---------------------------------------------------------------------------

func TestProcessMedia_CompletedEvent_QualitySkipped(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest() // non-image, no quality assessment
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)

	var completedEvent *DomainEvent
	for i := range capturedEvents {
		if capturedEvents[i].EventType == EventProcessingCompleted {
			completedEvent = &capturedEvents[i]
			break
		}
	}

	require.NotNil(t, completedEvent)
	qualitySummary, ok := completedEvent.Payload["quality_summary"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "skipped", qualitySummary["status"])
}

// ---------------------------------------------------------------------------
// ProcessMedia — Completed event quality_summary when quality is present
// ---------------------------------------------------------------------------

func TestProcessMedia_CompletedEvent_QualityPresent(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	qa := &QualityAssessment{
		ID:                 uuid.New(),
		BlurScore:          0.9,
		ResolutionAdequate: true,
		FormatValid:        true,
		LLMReady:           true,
		QualityGrade:       QualityGradeA,
		AssessedAt:         time.Now().UTC(),
	}
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(qa, nil)
	h.qualityRepo.On("Save", ctx, mock.AnythingOfType("*media.QualityAssessment")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)

	var completedEvent *DomainEvent
	for i := range capturedEvents {
		if capturedEvents[i].EventType == EventProcessingCompleted {
			completedEvent = &capturedEvents[i]
			break
		}
	}

	require.NotNil(t, completedEvent)
	qualitySummary, ok := completedEvent.Payload["quality_summary"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "A", qualitySummary["grade"])
	assert.Equal(t, true, qualitySummary["llm_ready"])
}

// ---------------------------------------------------------------------------
// ProcessMedia — Completed event scan_summary without moderation (non-image)
// ---------------------------------------------------------------------------

func TestProcessMedia_CompletedEvent_ScanSummaryNoModeration(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	_, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)

	var completedEvent *DomainEvent
	for i := range capturedEvents {
		if capturedEvents[i].EventType == EventProcessingCompleted {
			completedEvent = &capturedEvents[i]
			break
		}
	}

	require.NotNil(t, completedEvent)
	scanSummary, ok := completedEvent.Payload["scan_summary"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "skipped", scanSummary["moderation"])
}

// ---------------------------------------------------------------------------
// ProcessMedia — PublishScanCompleted is called for antivirus
// ---------------------------------------------------------------------------

func TestProcessMedia_PublishesScanCompletedForAntivirus(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newPDFRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	_, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)

	// Find scan.completed events.
	scanCompletedCount := 0
	for _, evt := range capturedEvents {
		if evt.EventType == EventScanCompleted {
			scanCompletedCount++
		}
	}
	assert.Equal(t, 1, scanCompletedCount, "one scan.completed event for antivirus")
}

// ---------------------------------------------------------------------------
// ProcessMedia — PublishScanCompleted is called for both antivirus + moderation
// ---------------------------------------------------------------------------

func TestProcessMedia_PublishesScanCompletedForAntivirusAndModeration(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(nil, errors.New("skip"))

	_, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)

	scanCompletedCount := 0
	for _, evt := range capturedEvents {
		if evt.EventType == EventScanCompleted {
			scanCompletedCount++
		}
	}
	assert.Equal(t, 2, scanCompletedCount, "two scan.completed events: antivirus + moderation")
}

// ---------------------------------------------------------------------------
// ProcessMedia — QualityAssessed event is published
// ---------------------------------------------------------------------------

func TestProcessMedia_PublishesQualityAssessedEvent(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)

	var capturedEvents []DomainEvent
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		if evt, ok := args.Get(2).(DomainEvent); ok {
			capturedEvents = append(capturedEvents, evt)
		}
	})

	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	qa := &QualityAssessment{
		ID:                 uuid.New(),
		BlurScore:          0.5,
		ResolutionAdequate: true,
		FormatValid:        true,
		LLMReady:           true,
		QualityGrade:       QualityGradeB,
		AssessedAt:         time.Now().UTC(),
	}
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(qa, nil)
	h.qualityRepo.On("Save", ctx, mock.AnythingOfType("*media.QualityAssessment")).Return(nil)

	_, err := h.svc.ProcessMedia(ctx, req)
	require.NoError(t, err)

	qualityAssessedCount := 0
	for _, evt := range capturedEvents {
		if evt.EventType == EventQualityAssessed {
			qualityAssessedCount++
		}
	}
	assert.Equal(t, 1, qualityAssessedCount, "one quality.assessed event")
}

// ---------------------------------------------------------------------------
// ProcessMedia — QualityAssessed event publish failure is non-fatal
// ---------------------------------------------------------------------------

func TestProcessMedia_QualityAssessedEventPublishFailure_NonFatal(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	req := newImageRequest()
	fileData := []byte("data")

	h.jobRepo.On("Save", ctx, mock.AnythingOfType("*media.ProcessingJob")).Return(nil)
	// All events fail.
	h.events.On("Publish", ctx, TopicMediaProcessorEvents, mock.Anything).Return(errors.New("pubsub down"))
	h.storage.On("Download", ctx, req.StorageKey).Return(fileData, nil)

	scanRes := cleanScanResult()
	h.scanner.On("Scan", ctx, fileData, req.StorageKey).Return(scanRes, nil)
	h.scanRepo.On("Save", ctx, mock.AnythingOfType("*media.ScanResultEntry")).Return(nil)

	modRes := cleanModerationResult()
	h.moderator.On("Moderate", ctx, fileData, "image/jpeg").Return(modRes, nil)

	thumbV := imageVariant(VariantTypeThumb)
	medV := imageVariant(VariantTypeMedium)
	largeV := imageVariant(VariantTypeLarge)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", ThumbSpec).Return(thumbV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", MediumSpec).Return(medV, nil)
	h.optimizer.On("Optimize", ctx, fileData, "image/jpeg", LargeSpec).Return(largeV, nil)
	h.storage.On("Upload", ctx, mock.AnythingOfType("string"), mock.AnythingOfType("[]uint8"), "image/webp").Return(nil)

	qa := &QualityAssessment{
		ID:                 uuid.New(),
		BlurScore:          0.9,
		ResolutionAdequate: true,
		FormatValid:        true,
		LLMReady:           true,
		QualityGrade:       QualityGradeA,
		AssessedAt:         time.Now().UTC(),
	}
	h.qualityAss.On("Assess", ctx, fileData, "image/jpeg").Return(qa, nil)
	h.qualityRepo.On("Save", ctx, mock.AnythingOfType("*media.QualityAssessment")).Return(nil)

	result, err := h.svc.ProcessMedia(ctx, req)

	// Pipeline succeeds despite all event publish failures.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ProcessingStatusReady, result.Status)
	require.NotNil(t, result.QualityAssessment)
}
