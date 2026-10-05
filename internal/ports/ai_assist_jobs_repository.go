// AiAssistJobsRepository — port for the ai_assist_jobs table (migration 0010).
//
// Per docs/m13/ack-oe-ai-assist-plan-2026-05-17.md Step 4: the
// /api/atoms/ai-assist async surface needs a queryable projection of the
// orchestrator's qgen 2-agent crew runs.
//
// 3 callers:
//  1. POST /api/atoms/ai-assist handler   →  Create (QUEUED row)
//  2. GET  /api/atoms/ai-assist/{job_id}  →  Get   (by ID, tenant-scoped)
//  3. Pub/Sub subscriber                  →  UpdateXxx (terminal transitions)
//
// Cross-DB queries FORBIDDEN per .claude/rules/ddd-enforcement.md — the
// orchestrator's chora_ai_kernel checkpointer is its own concern; the
// chora_creation row here is what the FE reads.
//
// Per [[multi-tenant-rls]] the pg adapter calls SET LOCAL app.tenant_id
// on the GET path (handler-time reads) and uses the app_writer role
// on the UPDATE path (subscriber-time writes that bypass RLS).
package ports

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
)

// AiAssistJobsRepository is the hexagonal port for AI Assist async jobs.
type AiAssistJobsRepository interface {
	// Create persists a freshly-constructed AiAssistJob in QUEUED status.
	// Idempotent by ID — INSERT ... ON CONFLICT (id) DO NOTHING so handler
	// retries (e.g., outbox publish-then-INSERT race) don't double-create.
	Create(ctx context.Context, job *aiassist.Job) error

	// Get returns the job for (tenantID, jobID). Returns aiassist.ErrNotFound
	// when missing OR when tenant_id mismatches (RLS-style — the row
	// pretends not to exist cross-tenant).
	Get(ctx context.Context, tenantID, jobID string) (*aiassist.Job, error)

	// UpdateCompleted is the subscriber-callable terminal transition on
	// chora.creation.ai_assist.completed.v1. Idempotent — re-emits of the
	// same completed.v1 event UPDATE the row again with identical state
	// (matches outbox at-least-once delivery semantics).
	UpdateCompleted(
		ctx context.Context,
		tenantID, jobID string,
		resultPayload, pipelineTrace []byte,
		qualityWarning bool,
		attemptCount, manaCharged int,
	) error

	// UpdateRefused is the subscriber-callable terminal transition on
	// chora.creation.ai_assist.refused.v1. Idempotent (see UpdateCompleted).
	UpdateRefused(
		ctx context.Context,
		tenantID, jobID string,
		reason aiassist.RefusalReason,
		armorVerdict, userFacingMsg string,
		lastCandidatePayload, pipelineTrace []byte,
		attemptCount, manaCharged int,
	) error

	// UpdateFailed is the subscriber-callable terminal transition on
	// non-recoverable orchestrator errors. Idempotent.
	UpdateFailed(
		ctx context.Context,
		tenantID, jobID string,
		pipelineTrace []byte,
		errMsg string,
		attemptCount, manaCharged int,
	) error
}

// ErrNotFound — sentinel returned by Get when (tenantID, jobID) doesn't
// match a row. Lives in the aiassist domain package; re-exported here as
// a convenience for adapter implementations.
var ErrNotFound = aiassist.ErrNotFound
