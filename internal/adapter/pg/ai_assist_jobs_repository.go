// ai_assist_jobs_repository.go — pgx-backed implementation of
// ports.AiAssistJobsRepository for chora_creation.ai_assist_jobs
// (migration 0010).
//
// Per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md Step 4b. Wraps every
// query in RunInTenantTx so SET LOCAL chora.tenant_id runs BEFORE any
// tenant-scoped read/write (chora_creation_app_rw is NOBYPASSRLS and
// ai_assist_jobs carries ai_assist_jobs_tenant_isolation RLS policy).
//
// Mirrors the question_job_repository.go pattern verbatim — same tx
// seam, same `ON CONFLICT (id) DO NOTHING` idempotency on INSERT, same
// fail-loud guards.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// AiAssistJobsRepository is the pgx-backed AiAssistJobs port implementation.
type AiAssistJobsRepository struct {
	tx TxQuerier
}

// NewAiAssistJobsRepository wraps a *PgxPoolQuerier for production wiring.
func NewAiAssistJobsRepository(querier *PgxPoolQuerier) *AiAssistJobsRepository {
	return &AiAssistJobsRepository{tx: querier}
}

// NewAiAssistJobsRepositoryFromTxQuerier wraps a TxQuerier (production OR
// test stub).
func NewAiAssistJobsRepositoryFromTxQuerier(tq TxQuerier) *AiAssistJobsRepository {
	return &AiAssistJobsRepository{tx: tq}
}

// Compile-time check.
var _ ports.AiAssistJobsRepository = (*AiAssistJobsRepository)(nil)

const (
	sqlInsertAiAssistJob = `
        INSERT INTO ai_assist_jobs (
            id, tenant_id, author_gcid,
            status, question_type,
            request_payload,
            attempt_count, mana_charged,
            created_at, updated_at
        ) VALUES (
            $1, $2, $3,
            $4, $5,
            COALESCE(NULLIF($6, '')::jsonb, '{}'::jsonb),
            $7, $8,
            $9, $10
        )
        ON CONFLICT (id) DO NOTHING
    `

	sqlSelectAiAssistJob = `
        SELECT
            id::text,
            tenant_id::text,
            author_gcid::text,
            status,
            question_type,
            COALESCE(request_payload::text, '{}'),
            COALESCE(result_payload::text, ''),
            COALESCE(pipeline_trace::text, ''),
            quality_warning,
            COALESCE(refusal_reason, ''),
            COALESCE(refusal_armor_verdict, ''),
            COALESCE(refusal_user_facing_msg, ''),
            attempt_count,
            mana_charged,
            created_at,
            updated_at,
            completed_at
        FROM ai_assist_jobs
        WHERE id = $1 AND tenant_id = $2
    `

	sqlUpdateAiAssistJobCompleted = `
        UPDATE ai_assist_jobs SET
            status = 'COMPLETED',
            result_payload = COALESCE(NULLIF($3, '')::jsonb, NULL),
            pipeline_trace = COALESCE(NULLIF($4, '')::jsonb, NULL),
            quality_warning = $5,
            attempt_count = $6,
            mana_charged = $7,
            completed_at = NOW(),
            updated_at = NOW()
        WHERE id = $1 AND tenant_id = $2
    `

	sqlUpdateAiAssistJobRefused = `
        UPDATE ai_assist_jobs SET
            status = 'REFUSED',
            refusal_reason = $3,
            refusal_armor_verdict = NULLIF($4, ''),
            refusal_user_facing_msg = NULLIF($5, ''),
            result_payload = COALESCE(NULLIF($6, '')::jsonb, NULL),
            pipeline_trace = COALESCE(NULLIF($7, '')::jsonb, NULL),
            attempt_count = $8,
            mana_charged = $9,
            completed_at = NOW(),
            updated_at = NOW()
        WHERE id = $1 AND tenant_id = $2
    `

	sqlUpdateAiAssistJobFailed = `
        UPDATE ai_assist_jobs SET
            status = 'FAILED',
            pipeline_trace = COALESCE(NULLIF($3, '')::jsonb, NULL),
            refusal_user_facing_msg = NULLIF($4, ''),
            attempt_count = $5,
            mana_charged = $6,
            completed_at = NOW(),
            updated_at = NOW()
        WHERE id = $1 AND tenant_id = $2
    `
)

// Create INSERTs a freshly-constructed Job in QUEUED status. Idempotent
// on (id) — re-emission of the same POST (e.g., outbox retry race)
// silently no-ops via ON CONFLICT.
func (r *AiAssistJobsRepository) Create(ctx context.Context, job *aiassist.Job) error {
	if job == nil {
		return errors.New("aiassist: Create: job is nil")
	}
	if job.ID == "" || job.TenantID == "" {
		return errors.New("aiassist: Create: id + tenant_id required")
	}
	return r.tx.RunInTenantTx(ctx, job.TenantID, func(ctx context.Context, tx Tx) error {
		return tx.Exec(ctx, sqlInsertAiAssistJob,
			job.ID,
			job.TenantID,
			job.AuthorGCID,
			string(job.Status),
			string(job.QuestionType),
			string(job.RequestPayload),
			job.AttemptCount,
			job.ManaCharged,
			job.CreatedAt,
			job.UpdatedAt,
		)
	})
}

// Get returns the row for (tenantID, jobID). Returns aiassist.ErrNotFound
// when missing or cross-tenant.
func (r *AiAssistJobsRepository) Get(ctx context.Context, tenantID, jobID string) (*aiassist.Job, error) {
	if tenantID == "" || jobID == "" {
		return nil, errors.New("aiassist: Get: tenant_id + job_id required")
	}
	var out *aiassist.Job
	err := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		row := tx.QueryRow(ctx, sqlSelectAiAssistJob, jobID, tenantID)
		out = &aiassist.Job{}
		var (
			requestPayload, resultPayload, pipelineTrace string
			refusalReason                                string
			armorVerdict, userFacingMsg                  string
			completedAt                                  *time.Time
		)
		if scanErr := row.Scan(
			&out.ID,
			&out.TenantID,
			&out.AuthorGCID,
			(*string)(&out.Status),
			(*string)(&out.QuestionType),
			&requestPayload,
			&resultPayload,
			&pipelineTrace,
			&out.QualityWarning,
			&refusalReason,
			&armorVerdict,
			&userFacingMsg,
			&out.AttemptCount,
			&out.ManaCharged,
			&out.CreatedAt,
			&out.UpdatedAt,
			&completedAt,
		); scanErr != nil {
			if errors.Is(scanErr, ErrNoRows) {
				return aiassist.ErrNotFound
			}
			return fmt.Errorf("aiassist: Get: scan: %w", scanErr)
		}
		out.RequestPayload = []byte(requestPayload)
		if resultPayload != "" {
			out.ResultPayload = []byte(resultPayload)
		}
		if pipelineTrace != "" {
			out.PipelineTrace = []byte(pipelineTrace)
		}
		out.RefusalReason = aiassist.RefusalReason(refusalReason)
		out.RefusalArmorVerdict = armorVerdict
		out.RefusalUserFacingMsg = userFacingMsg
		out.CompletedAt = completedAt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCompleted is the subscriber-callable terminal transition on
// chora.creation.ai_assist.completed.v1.
func (r *AiAssistJobsRepository) UpdateCompleted(
	ctx context.Context,
	tenantID, jobID string,
	resultPayload, pipelineTrace []byte,
	qualityWarning bool,
	attemptCount, manaCharged int,
) error {
	if tenantID == "" || jobID == "" {
		return errors.New("aiassist: UpdateCompleted: tenant_id + job_id required")
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		return tx.Exec(ctx, sqlUpdateAiAssistJobCompleted,
			jobID,
			tenantID,
			string(resultPayload),
			string(pipelineTrace),
			qualityWarning,
			attemptCount,
			manaCharged,
		)
	})
}

// UpdateRefused is the subscriber-callable terminal transition on
// chora.creation.ai_assist.refused.v1.
func (r *AiAssistJobsRepository) UpdateRefused(
	ctx context.Context,
	tenantID, jobID string,
	reason aiassist.RefusalReason,
	armorVerdict, userFacingMsg string,
	lastCandidatePayload, pipelineTrace []byte,
	attemptCount, manaCharged int,
) error {
	if tenantID == "" || jobID == "" {
		return errors.New("aiassist: UpdateRefused: tenant_id + job_id required")
	}
	if reason == "" || !reason.Valid() {
		return fmt.Errorf("aiassist: UpdateRefused: invalid refusal_reason %q", reason)
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		return tx.Exec(ctx, sqlUpdateAiAssistJobRefused,
			jobID,
			tenantID,
			string(reason),
			armorVerdict,
			userFacingMsg,
			string(lastCandidatePayload),
			string(pipelineTrace),
			attemptCount,
			manaCharged,
		)
	})
}

// UpdateFailed is the subscriber-callable terminal transition on
// non-recoverable orchestrator errors.
func (r *AiAssistJobsRepository) UpdateFailed(
	ctx context.Context,
	tenantID, jobID string,
	pipelineTrace []byte,
	errMsg string,
	attemptCount, manaCharged int,
) error {
	if tenantID == "" || jobID == "" {
		return errors.New("aiassist: UpdateFailed: tenant_id + job_id required")
	}
	return r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		return tx.Exec(ctx, sqlUpdateAiAssistJobFailed,
			jobID,
			tenantID,
			string(pipelineTrace),
			errMsg,
			attemptCount,
			manaCharged,
		)
	})
}
