package media

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Mock: SecurityScanner
// ---------------------------------------------------------------------------

type MockSecurityScanner struct {
	mock.Mock
}

func (m *MockSecurityScanner) Scan(ctx context.Context, data []byte, fileName string) (ScanResultEntry, error) {
	args := m.Called(ctx, data, fileName)
	return args.Get(0).(ScanResultEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: ContentModerator
// ---------------------------------------------------------------------------

type MockContentModerator struct {
	mock.Mock
}

func (m *MockContentModerator) Moderate(ctx context.Context, data []byte, mimeType string) (ScanResultEntry, error) {
	args := m.Called(ctx, data, mimeType)
	return args.Get(0).(ScanResultEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: ImageOptimizer
// ---------------------------------------------------------------------------

type MockImageOptimizer struct {
	mock.Mock
}

func (m *MockImageOptimizer) Optimize(ctx context.Context, data []byte, mimeType string, spec VariantSpec) (*ImageVariant, error) {
	args := m.Called(ctx, data, mimeType, spec)
	if v := args.Get(0); v != nil {
		return v.(*ImageVariant), args.Error(1)
	}
	return nil, args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: QualityAssessor
// ---------------------------------------------------------------------------

type MockQualityAssessor struct {
	mock.Mock
}

func (m *MockQualityAssessor) Assess(ctx context.Context, imageData []byte, mimeType string) (*QualityAssessment, error) {
	args := m.Called(ctx, imageData, mimeType)
	if v := args.Get(0); v != nil {
		return v.(*QualityAssessment), args.Error(1)
	}
	return nil, args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: StorageClient
// ---------------------------------------------------------------------------

type MockStorageClient struct {
	mock.Mock
}

func (m *MockStorageClient) Download(ctx context.Context, storageKey string) ([]byte, error) {
	args := m.Called(ctx, storageKey)
	if v := args.Get(0); v != nil {
		return v.([]byte), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockStorageClient) Upload(ctx context.Context, storageKey string, data []byte, mimeType string) error {
	args := m.Called(ctx, storageKey, data, mimeType)
	return args.Error(0)
}

func (m *MockStorageClient) GenerateSignedUploadURL(ctx context.Context, storageKey string, ttlSeconds int) (string, time.Time, error) {
	args := m.Called(ctx, storageKey, ttlSeconds)
	return args.String(0), args.Get(1).(time.Time), args.Error(2)
}

func (m *MockStorageClient) GenerateSignedDownloadURL(ctx context.Context, storageKey string, ttlSeconds int) (string, time.Time, error) {
	args := m.Called(ctx, storageKey, ttlSeconds)
	return args.String(0), args.Get(1).(time.Time), args.Error(2)
}

// ---------------------------------------------------------------------------
// Mock: ProcessingJobRepository
// ---------------------------------------------------------------------------

type MockProcessingJobRepo struct {
	mock.Mock
}

func (m *MockProcessingJobRepo) Save(ctx context.Context, job *ProcessingJob) error {
	args := m.Called(ctx, job)
	return args.Error(0)
}

func (m *MockProcessingJobRepo) FindByID(ctx context.Context, id uuid.UUID) (*ProcessingJob, error) {
	args := m.Called(ctx, id)
	if v := args.Get(0); v != nil {
		return v.(*ProcessingJob), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockProcessingJobRepo) FindByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) (*ProcessingJob, error) {
	args := m.Called(ctx, mediaAssetID)
	if v := args.Get(0); v != nil {
		return v.(*ProcessingJob), args.Error(1)
	}
	return nil, args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: ScanResultRepository
// ---------------------------------------------------------------------------

type MockScanResultRepo struct {
	mock.Mock
}

func (m *MockScanResultRepo) Save(ctx context.Context, result *ScanResultEntry) error {
	args := m.Called(ctx, result)
	return args.Error(0)
}

func (m *MockScanResultRepo) ListByMediaAssetID(ctx context.Context, mediaAssetID uuid.UUID) ([]ScanResultEntry, error) {
	args := m.Called(ctx, mediaAssetID)
	if v := args.Get(0); v != nil {
		return v.([]ScanResultEntry), args.Error(1)
	}
	return nil, args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: QualityAssessmentRepository
// ---------------------------------------------------------------------------

type MockQualityAssessmentRepo struct {
	mock.Mock
}

func (m *MockQualityAssessmentRepo) Save(ctx context.Context, assessment *QualityAssessment) error {
	args := m.Called(ctx, assessment)
	return args.Error(0)
}

func (m *MockQualityAssessmentRepo) GetByJobID(ctx context.Context, jobID uuid.UUID) (*QualityAssessment, error) {
	args := m.Called(ctx, jobID)
	if v := args.Get(0); v != nil {
		return v.(*QualityAssessment), args.Error(1)
	}
	return nil, args.Error(1)
}

// ---------------------------------------------------------------------------
// Mock: EventPublisher
// ---------------------------------------------------------------------------

type MockEventPublisher struct {
	mock.Mock
}

func (m *MockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	args := m.Called(ctx, topic, event)
	return args.Error(0)
}
