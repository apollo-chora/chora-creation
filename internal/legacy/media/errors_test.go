package media

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		expectedMsg string
	}{
		{"ErrProcessingJobNotFound", ErrProcessingJobNotFound, "MEDIA_PROCESSOR_PROCESSING_JOB_NOT_FOUND"},
		{"ErrMediaAssetNotFound", ErrMediaAssetNotFound, "MEDIA_PROCESSOR_MEDIA_ASSET_NOT_FOUND"},
		{"ErrScanFailed", ErrScanFailed, "MEDIA_PROCESSOR_SCAN_FAILED"},
		{"ErrModerationFailed", ErrModerationFailed, "MEDIA_PROCESSOR_MODERATION_FAILED"},
		{"ErrOptimizationFailed", ErrOptimizationFailed, "MEDIA_PROCESSOR_OPTIMIZATION_FAILED"},
		{"ErrStorageUploadFailed", ErrStorageUploadFailed, "MEDIA_PROCESSOR_STORAGE_UPLOAD_FAILED"},
		{"ErrStorageDownloadFailed", ErrStorageDownloadFailed, "MEDIA_PROCESSOR_STORAGE_DOWNLOAD_FAILED"},
		{"ErrMediaQuarantined", ErrMediaQuarantined, "MEDIA_PROCESSOR_MEDIA_QUARANTINED"},
		{"ErrUnsupportedMimeType", ErrUnsupportedMimeType, "MEDIA_PROCESSOR_UNSUPPORTED_MIME_TYPE"},
		{"ErrQualityAssessmentFailed", ErrQualityAssessmentFailed, "MEDIA_PROCESSOR_QUALITY_ASSESSMENT_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedMsg, tt.err.Error())
		})
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	allErrors := []error{
		ErrProcessingJobNotFound,
		ErrMediaAssetNotFound,
		ErrScanFailed,
		ErrModerationFailed,
		ErrOptimizationFailed,
		ErrStorageUploadFailed,
		ErrStorageDownloadFailed,
		ErrMediaQuarantined,
		ErrUnsupportedMimeType,
		ErrQualityAssessmentFailed,
	}

	for i := 0; i < len(allErrors); i++ {
		for j := i + 1; j < len(allErrors); j++ {
			assert.NotEqual(t, allErrors[i], allErrors[j],
				"sentinel errors %d and %d must be distinct", i, j)
		}
	}
}

func TestSentinelErrorsWorkWithErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("something happened: %w", ErrProcessingJobNotFound)
	assert.True(t, errors.Is(wrapped, ErrProcessingJobNotFound))
	assert.False(t, errors.Is(wrapped, ErrScanFailed))
}

func TestSentinelErrorsHaveMediaProcessorPrefix(t *testing.T) {
	allErrors := []error{
		ErrProcessingJobNotFound,
		ErrMediaAssetNotFound,
		ErrScanFailed,
		ErrModerationFailed,
		ErrOptimizationFailed,
		ErrStorageUploadFailed,
		ErrStorageDownloadFailed,
		ErrMediaQuarantined,
		ErrUnsupportedMimeType,
		ErrQualityAssessmentFailed,
	}

	for _, err := range allErrors {
		assert.Contains(t, err.Error(), "MEDIA_PROCESSOR_",
			"error %q should have MEDIA_PROCESSOR_ prefix", err.Error())
	}
}
