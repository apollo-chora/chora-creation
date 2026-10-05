package media

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MediaProcessorService orchestrates the media processing pipeline:
// scan, moderate, optimize, assess quality, and publish domain events.
type MediaProcessorService struct {
	scanner         SecurityScanner
	moderator       ContentModerator
	optimizer       ImageOptimizer
	qualityAssessor QualityAssessor
	storage         StorageClient
	jobRepo         ProcessingJobRepository
	scanRepo        ScanResultRepository
	qualityRepo     QualityAssessmentRepository
	events          EventPublisher
}

// NewMediaProcessorService creates a MediaProcessorService with injected dependencies.
func NewMediaProcessorService(
	scanner SecurityScanner,
	moderator ContentModerator,
	optimizer ImageOptimizer,
	qualityAssessor QualityAssessor,
	storage StorageClient,
	jobRepo ProcessingJobRepository,
	scanRepo ScanResultRepository,
	qualityRepo QualityAssessmentRepository,
	events EventPublisher,
) *MediaProcessorService {
	return &MediaProcessorService{
		scanner:         scanner,
		moderator:       moderator,
		optimizer:       optimizer,
		qualityAssessor: qualityAssessor,
		storage:         storage,
		jobRepo:         jobRepo,
		scanRepo:        scanRepo,
		qualityRepo:     qualityRepo,
		events:          events,
	}
}

// ProcessMedia executes the full processing pipeline for a media asset.
// Pipeline: create job -> download -> scan -> moderate -> hash -> optimize -> publish.
func (s *MediaProcessorService) ProcessMedia(ctx context.Context, request ProcessingRequest) (*ProcessingResult, error) {
	started := time.Now()

	// 1. Create ProcessingJob (status=queued).
	job := &ProcessingJob{
		ID:           uuid.Must(uuid.NewV7()),
		TenantID:     request.TenantID,
		MediaAssetID: request.MediaAssetID,
		Status:       ProcessingJobStatusQueued,
		JobType:      "full_pipeline",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.jobRepo.Save(ctx, job); err != nil {
		return nil, fmt.Errorf("save processing job: %w", err)
	}

	// Publish EventProcessingStarted.
	startedEvent := NewDomainEvent(
		EventProcessingStarted,
		request.TenantID,
		nil,
		job.ID,
		"MediaProcessingJob",
		map[string]interface{}{
			"processing_job_id": job.ID.String(),
			"media_asset_id":    request.MediaAssetID.String(),
			"tenant_id":         request.TenantID.String(),
			"job_type":          job.JobType,
		},
	)
	if err := s.events.Publish(ctx, TopicMediaProcessorEvents, startedEvent); err != nil {
		slog.ErrorContext(ctx, "failed to publish processing.started event", "error", err)
	}

	// 2. Update job status to processing.
	now := time.Now().UTC()
	job.Status = ProcessingJobStatusProcessing
	job.StartedAt = &now
	job.UpdatedAt = now
	if err := s.jobRepo.Save(ctx, job); err != nil {
		return nil, fmt.Errorf("update job to processing: %w", err)
	}

	result := &ProcessingResult{
		MediaAssetID: request.MediaAssetID,
		TenantID:     request.TenantID,
	}

	// 3. Download file from storage.
	data, err := s.storage.Download(ctx, request.StorageKey)
	if err != nil {
		s.failJob(ctx, job, "download", "failed to download media asset", err)
		return nil, fmt.Errorf("download media asset: %w", err)
	}

	// 4. Run security scan.
	scanResult, err := s.scanner.Scan(ctx, data, request.StorageKey)
	if err != nil {
		s.failJob(ctx, job, "scan", "security scan failed", err)
		return nil, fmt.Errorf("security scan: %w", err)
	}
	scanResult.TenantID = request.TenantID
	scanResult.MediaAssetID = request.MediaAssetID
	if err := s.scanRepo.Save(ctx, &scanResult); err != nil {
		return nil, fmt.Errorf("save scan result: %w", err)
	}
	result.ScanResults = append(result.ScanResults, scanResult)

	// Publish EventScanCompleted for antivirus.
	s.publishScanCompleted(ctx, job.ID, request, scanResult)

	// 5. If scan result = threat, quarantine.
	if scanResult.Result == ScanResultThreat {
		result.Status = ProcessingStatusQuarantined
		s.quarantineJob(ctx, job, "threat detected by antivirus scan")
		return result, nil
	}

	// 6. If image MIME type, run content moderation.
	if isImageMimeType(request.MimeType) {
		modResult, err := s.moderator.Moderate(ctx, data, request.MimeType)
		if err != nil {
			s.failJob(ctx, job, "moderation", "content moderation failed", err)
			return nil, fmt.Errorf("content moderation: %w", err)
		}
		modResult.TenantID = request.TenantID
		modResult.MediaAssetID = request.MediaAssetID
		if err := s.scanRepo.Save(ctx, &modResult); err != nil {
			return nil, fmt.Errorf("save moderation result: %w", err)
		}
		result.ScanResults = append(result.ScanResults, modResult)

		// Publish EventScanCompleted for moderation.
		s.publishScanCompleted(ctx, job.ID, request, modResult)

		// 7. If moderation result = nsfw or policy_violation, quarantine.
		if modResult.Result == ScanResultNSFW || modResult.Result == ScanResultPolicyViolation {
			result.Status = ProcessingStatusQuarantined
			s.quarantineJob(ctx, job, fmt.Sprintf("content moderation: %s", modResult.Result))
			return result, nil
		}
	}

	// 8. Calculate content hash (SHA-256).
	hash := sha256.Sum256(data)
	result.ContentHash = fmt.Sprintf("%x", hash)

	// 9. If raster image, generate variants.
	if isRasterImageMimeType(request.MimeType) {
		specs := []VariantSpec{ThumbSpec, MediumSpec, LargeSpec}
		for _, spec := range specs {
			variant, err := s.optimizer.Optimize(ctx, data, request.MimeType, spec)
			if err != nil {
				s.failJob(ctx, job, "optimization", fmt.Sprintf("failed to generate %s variant", spec.Type), err)
				return nil, fmt.Errorf("generate %s variant: %w", spec.Type, err)
			}

			// Upload variant to storage.
			variantKey := fmt.Sprintf("%s/variants/%s.%s", request.StorageKey, spec.Type, spec.Format)
			if err := s.storage.Upload(ctx, variantKey, variant.Data, variant.MimeType); err != nil {
				s.failJob(ctx, job, "optimization", fmt.Sprintf("failed to upload %s variant", spec.Type), err)
				return nil, fmt.Errorf("upload %s variant: %w", spec.Type, err)
			}

			result.Variants = append(result.Variants, *variant)

			// Publish EventVariantCreated.
			variantEvent := NewDomainEvent(
				EventVariantCreated,
				request.TenantID,
				nil,
				job.ID,
				"MediaProcessingJob",
				map[string]interface{}{
					"processing_job_id": job.ID.String(),
					"media_asset_id":    request.MediaAssetID.String(),
					"tenant_id":         request.TenantID.String(),
					"variant_type":      string(spec.Type),
					"storage_key":       variantKey,
					"width":             variant.Width,
					"height":            variant.Height,
					"file_size":         variant.FileSize,
					"mime_type":         variant.MimeType,
				},
			)
			if err := s.events.Publish(ctx, TopicMediaProcessorEvents, variantEvent); err != nil {
				slog.ErrorContext(ctx, "failed to publish variant.created event", "error", err)
			}
		}
	}

	// 9.5. Quality assessment (Stage 2 gate — informational, does NOT block pipeline).
	// Runs on raster images after variant generation. Records quality metrics
	// for content authors; poor quality does not prevent "ready" status.
	if isRasterImageMimeType(request.MimeType) && s.qualityAssessor != nil {
		qa, qaErr := s.qualityAssessor.Assess(ctx, data, request.MimeType)
		if qaErr != nil {
			// Quality assessment failure is non-fatal — log and continue.
			slog.ErrorContext(ctx, "quality assessment failed (non-fatal)",
				"error", qaErr,
				"media_asset_id", request.MediaAssetID.String(),
			)
		} else {
			qa.ProcessingJobID = job.ID

			// Persist assessment.
			if s.qualityRepo != nil {
				if saveErr := s.qualityRepo.Save(ctx, qa); saveErr != nil {
					slog.ErrorContext(ctx, "failed to save quality assessment",
						"error", saveErr,
						"media_asset_id", request.MediaAssetID.String(),
					)
				}
			}

			result.QualityAssessment = qa

			// Publish EventQualityAssessed.
			qaEvent := NewDomainEvent(
				EventQualityAssessed,
				request.TenantID,
				nil,
				job.ID,
				"MediaProcessingJob",
				map[string]interface{}{
					"processing_job_id":   job.ID.String(),
					"media_asset_id":      request.MediaAssetID.String(),
					"tenant_id":           request.TenantID.String(),
					"blur_score":          qa.BlurScore,
					"resolution_adequate": qa.ResolutionAdequate,
					"format_valid":        qa.FormatValid,
					"ocr_text_detected":   qa.OCRTextDetected,
					"llm_ready":           qa.LLMReady,
					"quality_grade":       string(qa.QualityGrade),
					"assessed_at":         qa.AssessedAt.Format(time.RFC3339),
				},
			)
			if err := s.events.Publish(ctx, TopicMediaProcessorEvents, qaEvent); err != nil {
				slog.ErrorContext(ctx, "failed to publish quality.assessed event", "error", err)
			}
		}
	}

	// 10. Update job status to completed.
	completedAt := time.Now().UTC()
	job.Status = ProcessingJobStatusCompleted
	job.CompletedAt = &completedAt
	job.UpdatedAt = completedAt
	if err := s.jobRepo.Save(ctx, job); err != nil {
		return nil, fmt.Errorf("update job to completed: %w", err)
	}

	result.Status = ProcessingStatusReady
	result.DurationSeconds = time.Since(started).Seconds()

	// Publish EventProcessingCompleted.
	scanSummary := map[string]interface{}{
		"antivirus":  string(scanResult.Result),
		"moderation": "skipped",
	}
	if len(result.ScanResults) > 1 {
		scanSummary["moderation"] = string(result.ScanResults[1].Result)
	}

	qualitySummary := map[string]interface{}{"status": "skipped"}
	if result.QualityAssessment != nil {
		qualitySummary = map[string]interface{}{
			"grade":     string(result.QualityAssessment.QualityGrade),
			"llm_ready": result.QualityAssessment.LLMReady,
		}
	}

	completedEvent := NewDomainEvent(
		EventProcessingCompleted,
		request.TenantID,
		nil,
		job.ID,
		"MediaProcessingJob",
		map[string]interface{}{
			"processing_job_id": job.ID.String(),
			"media_asset_id":    request.MediaAssetID.String(),
			"tenant_id":         request.TenantID.String(),
			"processing_status": string(ProcessingStatusReady),
			"scan_summary":      scanSummary,
			"quality_summary":   qualitySummary,
			"variants_created":  len(result.Variants),
			"duration_ms":       time.Since(started).Milliseconds(),
		},
	)
	if err := s.events.Publish(ctx, TopicMediaProcessorEvents, completedEvent); err != nil {
		slog.ErrorContext(ctx, "failed to publish processing.completed event", "error", err)
	}

	// 11. Return ProcessingResult.
	return result, nil
}

// publishScanCompleted publishes an EventScanCompleted event.
func (s *MediaProcessorService) publishScanCompleted(ctx context.Context, jobID uuid.UUID, request ProcessingRequest, scanResult ScanResultEntry) {
	event := NewDomainEvent(
		EventScanCompleted,
		request.TenantID,
		nil,
		jobID,
		"MediaProcessingJob",
		map[string]interface{}{
			"processing_job_id": jobID.String(),
			"media_asset_id":    request.MediaAssetID.String(),
			"tenant_id":         request.TenantID.String(),
			"scan_type":         string(scanResult.ScanType),
			"result":            string(scanResult.Result),
			"details":           scanResult.Details,
			"scanned_at":        scanResult.ScannedAt.Format(time.RFC3339),
		},
	)
	if err := s.events.Publish(ctx, TopicMediaProcessorEvents, event); err != nil {
		slog.ErrorContext(ctx, "failed to publish scan.completed event", "error", err)
	}
}

// failJob updates the job to failed status and publishes a ProcessingFailed event.
func (s *MediaProcessorService) failJob(ctx context.Context, job *ProcessingJob, failedStep, reason string, err error) {
	now := time.Now().UTC()
	job.Status = ProcessingJobStatusFailed
	job.CompletedAt = &now
	job.ErrorMessage = err.Error()
	job.UpdatedAt = now
	if saveErr := s.jobRepo.Save(ctx, job); saveErr != nil {
		slog.ErrorContext(ctx, "failed to update job to failed", "error", saveErr)
	}

	failedEvent := NewDomainEvent(
		EventProcessingFailed,
		job.TenantID,
		nil,
		job.ID,
		"MediaProcessingJob",
		map[string]interface{}{
			"processing_job_id": job.ID.String(),
			"media_asset_id":    job.MediaAssetID.String(),
			"tenant_id":         job.TenantID.String(),
			"processing_status": string(ProcessingStatusFailed),
			"failure_reason":    reason,
			"failed_step":       failedStep,
			"error_message":     err.Error(),
		},
	)
	if pubErr := s.events.Publish(ctx, TopicMediaProcessorEvents, failedEvent); pubErr != nil {
		slog.ErrorContext(ctx, "failed to publish processing.failed event", "error", pubErr)
	}
}

// quarantineJob updates the job to failed status with quarantine reason and publishes a ProcessingFailed event.
func (s *MediaProcessorService) quarantineJob(ctx context.Context, job *ProcessingJob, reason string) {
	now := time.Now().UTC()
	job.Status = ProcessingJobStatusFailed
	job.CompletedAt = &now
	job.ErrorMessage = reason
	job.UpdatedAt = now
	if err := s.jobRepo.Save(ctx, job); err != nil {
		slog.ErrorContext(ctx, "failed to update job to quarantined", "error", err)
	}

	failedEvent := NewDomainEvent(
		EventProcessingFailed,
		job.TenantID,
		nil,
		job.ID,
		"MediaProcessingJob",
		map[string]interface{}{
			"processing_job_id": job.ID.String(),
			"media_asset_id":    job.MediaAssetID.String(),
			"tenant_id":         job.TenantID.String(),
			"processing_status": string(ProcessingStatusQuarantined),
			"failure_reason":    reason,
			"failed_step":       "scan",
		},
	)
	if err := s.events.Publish(ctx, TopicMediaProcessorEvents, failedEvent); err != nil {
		slog.ErrorContext(ctx, "failed to publish processing.failed event", "error", err)
	}
}

// isImageMimeType returns true if the MIME type is any image type.
func isImageMimeType(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// isRasterImageMimeType returns true if the MIME type is a raster image
// that can be resized/optimized (excludes SVG).
func isRasterImageMimeType(mimeType string) bool {
	if !isImageMimeType(mimeType) {
		return false
	}
	// SVG is vector — cannot generate raster variants from it.
	return mimeType != "image/svg+xml"
}
