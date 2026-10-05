// ComposeJob — async-job aggregate for AI question authoring.
//
// 3 job types per design §2.5 + migration 0008:
//   - ai_model_answer        (5 mana, single existing question)
//   - ai_draft               (10 mana, full new question from prompt)
//   - batch_source_material  (50 mana parse + 5 mana per accepted item)
//
// 7-state lifecycle per migration 0008 (the locked deployed authority):
//
//	requested → running
//	running → {succeeded, failed}
//	succeeded → {accepted, partially_accepted, cancelled}
//	failed                                  is terminal
//	accepted / partially_accepted / cancelled are terminal
//
// NOTE — the design pseudocode (§2.5) used a 7-name set
// (pending/parsing/generating/ready_for_review/accepted/rejected/failed). The
// migration 0008 (locked at P0) is authoritative; the rename was applied at
// the contract layer (creation-questions.yaml) to match. This domain code
// follows the migration.
//
// Mana-debit semantics live in the orchestrator (it calls the ManaLedger port
// before invoking the QuestionGenerator). The aggregate just records the
// resulting `ManaCharged` integer.
package question

import (
	"fmt"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
)

// -----------------------------------------------------------------------------
// JobStatus — 7 values matching migration 0008 CHECK
// -----------------------------------------------------------------------------

type JobStatus string

const (
	JobStatusRequested         JobStatus = "requested"
	JobStatusRunning           JobStatus = "running"
	JobStatusSucceeded         JobStatus = "succeeded"
	JobStatusFailed            JobStatus = "failed"
	JobStatusAccepted          JobStatus = "accepted"
	JobStatusPartiallyAccepted JobStatus = "partially_accepted"
	JobStatusCancelled         JobStatus = "cancelled"
)

// Valid returns true iff js is one of the 7 enum members.
func (js JobStatus) Valid() bool {
	switch js {
	case JobStatusRequested, JobStatusRunning, JobStatusSucceeded, JobStatusFailed,
		JobStatusAccepted, JobStatusPartiallyAccepted, JobStatusCancelled:
		return true
	}
	return false
}

// IsTerminal returns true iff js is a final state (no outbound transitions).
func (js JobStatus) IsTerminal() bool {
	switch js {
	case JobStatusFailed, JobStatusAccepted, JobStatusPartiallyAccepted, JobStatusCancelled:
		return true
	}
	return false
}

// allowedTransitions encodes the migration-aligned state machine.
var allowedTransitions = map[JobStatus]map[JobStatus]struct{}{
	JobStatusRequested: {JobStatusRunning: {}},
	JobStatusRunning:   {JobStatusSucceeded: {}, JobStatusFailed: {}},
	JobStatusSucceeded: {
		JobStatusAccepted:          {},
		JobStatusPartiallyAccepted: {},
		JobStatusCancelled:         {},
	},
	// Terminals: no outbound edges. Explicit empty entries omitted — the
	// lookup-miss path in Transition() handles them with ErrInvalidStateTransition.
}

// -----------------------------------------------------------------------------
// ComposeJob — async job aggregate (lives in
// chora_creation.question_generation_jobs per migration 0008).
// -----------------------------------------------------------------------------

// ComposeJob mirrors the migration 0008 table 1-to-1 in domain
// shape. CandidateQuestionsJSON is a raw []byte for the JSONB column — the
// adapter owns wire encoding; the domain just carries the blob.
type ComposeJob struct {
	JobID      string `json:"job_id"`
	AtomID     string `json:"atom_id"`
	TenantID   string `json:"tenant_id"`
	AuthorGCID string `json:"author_gcid"`

	// ADR-195 compose model (additive — persistence is wired in WS2/WS3, hence
	// json:"-" until then). Intent is the domain discriminant, Input the
	// generation seed, Plan the per-type generation plan (new_question only).
	// Validated by ComposeJob.Validate(); see compose.go.
	Intent Intent            `json:"-"`
	Input  Input             `json:"-"`
	Plan   aiassist.TypePlan `json:"-"`

	Status         JobStatus `json:"status"`
	ManaActionCode string    `json:"mana_action_code"`
	ManaCharged    int       `json:"mana_charged"`
	SourceBlobURI  string    `json:"source_blob_uri,omitempty"`
	SourceMimeType string    `json:"source_mime_type,omitempty"`
	// SettingsJSON is the raw settings JSONB (migration 0008 column; first
	// written by Lane 1c — carries the role-tagged source_files[] for
	// multi-file batch jobs alongside count/difficulty/tags). The adapter
	// owns wire encoding; the domain carries the blob.
	SettingsJSON           []byte `json:"settings,omitempty"`
	CandidateQuestionsJSON []byte `json:"candidate_questions_jsonb,omitempty"`
	// ProposedTestSetJSON is the composer's LLM-proposed test-set structure
	// (OpenAPI ProposedTestSet; migration 0017 column). Stamped by the
	// ai_assist completed-event subscriber for batch jobs; nil otherwise.
	ProposedTestSetJSON []byte `json:"proposed_test_set_jsonb,omitempty"`
	// GenerationSummaryJSON is the CHO-1819 P2 mixed-batch outcome projection
	// (requested vs generated totals + per-type breakdown + shortfall reason;
	// migration 0018 column). Stamped by the ai_assist completed-event
	// subscriber when the orchestrator emits a generation_summary; nil
	// otherwise (single-type / legacy batches, or a pre-P2 orchestrator).
	GenerationSummaryJSON []byte `json:"generation_summary_jsonb,omitempty"`
	// PipelineTraceJSON is the per-step QGen pipeline trace (CHO-1826 Gap #4;
	// migration 0021 column) parsed from the ai_assist completed.v1 field 12
	// pipeline_trace_json — the SAME trace the single-mode AI-Assist drawer
	// renders. Stamped by the ai_assist completed-event subscriber on success;
	// nil for refused / failed jobs (the refused.v1 schema omits the field).
	PipelineTraceJSON []byte `json:"pipeline_trace_jsonb,omitempty"`
	// ChunkCandidatesJSON (ADR-251 D5, CHO-2398; migration 0035) is the
	// chunk-slot object {"0": [...], "1": [...]} the chunk_completed.v1
	// subscriber fills while the job RUNS, so the poll can serve
	// questions-so-far. The terminal CandidateQuestionsJSON stays the
	// authoritative full set. ChunkCountTotal is the plan total ("k of n").
	ChunkCandidatesJSON []byte     `json:"chunk_candidates_jsonb,omitempty"`
	ChunkCountTotal     int        `json:"chunk_count_total,omitempty"`
	Error               string     `json:"error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	AcceptedAt          *time.Time `json:"accepted_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Transition advances the job's Status per the allowed-transitions table.
// Returns ErrInvalidStateTransition wrapping a descriptive message otherwise.
//
// Side effects on success:
//   - Status is updated to `to`
//   - StartedAt is stamped on entry to JobStatusRunning (idempotent if already set)
//   - CompletedAt is stamped on entry to any terminal state
//   - AcceptedAt is stamped on entry to JobStatusAccepted or JobStatusPartiallyAccepted
//   - UpdatedAt is always bumped to now
func (j *ComposeJob) Transition(to JobStatus) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown target status %q", ErrInvalidStateTransition, string(to))
	}
	allowed, ok := allowedTransitions[j.Status]
	if !ok {
		return fmt.Errorf("%w: from terminal status %q", ErrInvalidStateTransition, string(j.Status))
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("%w: from=%q to=%q not in allowed transitions",
			ErrInvalidStateTransition, string(j.Status), string(to))
	}

	now := time.Now().UTC()
	j.Status = to
	j.UpdatedAt = now
	switch to {
	case JobStatusRunning:
		if j.StartedAt == nil {
			t := now
			j.StartedAt = &t
		}
	case JobStatusSucceeded, JobStatusFailed:
		t := now
		j.CompletedAt = &t
	case JobStatusAccepted, JobStatusPartiallyAccepted:
		t := now
		j.AcceptedAt = &t
		if j.CompletedAt == nil {
			j.CompletedAt = &t
		}
	case JobStatusCancelled:
		if j.CompletedAt == nil {
			t := now
			j.CompletedAt = &t
		}
	}
	return nil
}
