package cms

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Enum validators: StorageCategory, ProcessingStatus, ScanStatus, StorageTier,
// VariantType, ProcessingJobStatus, ScanType, ScanResult
// These validators have valid-path coverage but invalid-path missing.
// ---------------------------------------------------------------------------

func TestValidStorageCategory(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidStorageCategory(StorageCategoryAssignmentSubmission))
	assert.True(t, ValidStorageCategory(StorageCategoryExamEvidence))
	assert.True(t, ValidStorageCategory(StorageCategoryCredentialEvidence))
	assert.True(t, ValidStorageCategory(StorageCategoryAtomContent))
	assert.True(t, ValidStorageCategory(StorageCategorySocialContent))
	assert.True(t, ValidStorageCategory(StorageCategoryCMSAuthored))
	assert.False(t, ValidStorageCategory(StorageCategory("invalid")))
	assert.False(t, ValidStorageCategory(StorageCategory("")))
}

func TestValidProcessingStatus(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidProcessingStatus(ProcessingStatusPending))
	assert.True(t, ValidProcessingStatus(ProcessingStatusProcessing))
	assert.True(t, ValidProcessingStatus(ProcessingStatusReady))
	assert.True(t, ValidProcessingStatus(ProcessingStatusQuarantined))
	assert.True(t, ValidProcessingStatus(ProcessingStatusFailed))
	assert.False(t, ValidProcessingStatus(ProcessingStatus("invalid")))
	assert.False(t, ValidProcessingStatus(ProcessingStatus("")))
}

func TestValidScanStatus(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidScanStatus(ScanStatusPending))
	assert.True(t, ValidScanStatus(ScanStatusClean))
	assert.True(t, ValidScanStatus(ScanStatusThreat))
	assert.True(t, ValidScanStatus(ScanStatusFlagged))
	assert.False(t, ValidScanStatus(ScanStatus("invalid")))
}

func TestValidStorageTier(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidStorageTier(StorageTierStandard))
	assert.True(t, ValidStorageTier(StorageTierNearline))
	assert.True(t, ValidStorageTier(StorageTierColdline))
	assert.True(t, ValidStorageTier(StorageTierArchive))
	assert.False(t, ValidStorageTier(StorageTier("invalid")))
}

func TestValidVariantType(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidVariantType(VariantTypeThumb))
	assert.True(t, ValidVariantType(VariantTypeMedium))
	assert.True(t, ValidVariantType(VariantTypeLarge))
	assert.True(t, ValidVariantType(VariantTypeOriginal))
	assert.False(t, ValidVariantType(VariantType("invalid")))
}

func TestValidProcessingJobStatus(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidProcessingJobStatus(ProcessingJobStatusQueued))
	assert.True(t, ValidProcessingJobStatus(ProcessingJobStatusProcessing))
	assert.True(t, ValidProcessingJobStatus(ProcessingJobStatusCompleted))
	assert.True(t, ValidProcessingJobStatus(ProcessingJobStatusFailed))
	assert.False(t, ValidProcessingJobStatus(ProcessingJobStatus("invalid")))
}

func TestValidScanType(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidScanType(ScanTypeAntivirus))
	assert.True(t, ValidScanType(ScanTypeContentModeration))
	assert.True(t, ValidScanType(ScanTypeOCRPolicy))
	assert.False(t, ValidScanType(ScanType("invalid")))
}

func TestValidScanResult(t *testing.T) {
	t.Parallel()

	assert.True(t, ValidScanResult(ScanResultClean))
	assert.True(t, ValidScanResult(ScanResultThreat))
	assert.True(t, ValidScanResult(ScanResultNSFW))
	assert.True(t, ValidScanResult(ScanResultPolicyViolation))
	assert.False(t, ValidScanResult(ScanResult("invalid")))
}
