package aiassist

import (
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// Enum invariants
// -----------------------------------------------------------------------------

func TestStatus_Valid(t *testing.T) {
	t.Parallel()
	for _, ok := range []Status{StatusQueued, StatusInProgress, StatusCompleted, StatusRefused, StatusFailed} {
		if !ok.Valid() {
			t.Errorf("expected %s.Valid()=true", ok)
		}
	}
	for _, bad := range []Status{"queued", "in_progress", "completed", "DONE", ""} {
		if Status(bad).Valid() {
			t.Errorf("expected %q.Valid()=false", bad)
		}
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	t.Parallel()
	if StatusQueued.IsTerminal() || StatusInProgress.IsTerminal() {
		t.Errorf("non-terminal status marked terminal")
	}
	for _, term := range []Status{StatusCompleted, StatusRefused, StatusFailed} {
		if !term.IsTerminal() {
			t.Errorf("expected %s.IsTerminal()=true", term)
		}
	}
}

func TestQuestionType_Valid(t *testing.T) {
	t.Parallel()
	for _, ok := range []QuestionType{QuestionTypeMCQ, QuestionTypeOE} {
		if !ok.Valid() {
			t.Errorf("expected %s.Valid()=true", ok)
		}
	}
	for _, bad := range []QuestionType{"flashcard", "video", "outline", "essay", ""} {
		if QuestionType(bad).Valid() {
			t.Errorf("expected %q.Valid()=false (Phyllis scope is mcq+oe only)", bad)
		}
	}
}

func TestRefusalReason_ValidEmptyAllowed(t *testing.T) {
	t.Parallel()
	// Empty refusal_reason is valid (means: not refused).
	if !RefusalReason("").Valid() {
		t.Errorf("empty refusal_reason should be valid (non-refused state)")
	}
	for _, ok := range []RefusalReason{RefusalReasonGuardrailPre, RefusalReasonGuardrailPost, RefusalReasonValidation} {
		if !ok.Valid() {
			t.Errorf("expected %s.Valid()=true", ok)
		}
	}
	for _, bad := range []RefusalReason{"guardrail_pre", "POLICY", "PII_LEAK"} {
		if RefusalReason(bad).Valid() {
			t.Errorf("expected %q.Valid()=false", bad)
		}
	}
}

// -----------------------------------------------------------------------------
// NewQueuedJob constructor — fail-loud guards
// -----------------------------------------------------------------------------

func TestNewQueuedJob_Happy(t *testing.T) {
	t.Parallel()
	j, err := NewQueuedJob("job-1", "tenant-1", "gcid-1", QuestionTypeOE, []byte(`{"prompt":"x"}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if j.Status != StatusQueued {
		t.Errorf("expected QUEUED initial status; got %s", j.Status)
	}
	if j.AttemptCount != 0 {
		t.Errorf("expected attempt_count=0 initial; got %d", j.AttemptCount)
	}
	if j.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be set")
	}
	if !j.CreatedAt.Equal(j.UpdatedAt) {
		t.Errorf("CreatedAt should equal UpdatedAt on construction")
	}
	if j.CompletedAt != nil {
		t.Errorf("CompletedAt should be nil on QUEUED")
	}
}

func TestNewQueuedJob_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		id, tenant string
		gcid       string
		qt         QuestionType
		payload    []byte
		wantSubstr string
	}{
		{name: "missing id", id: "", tenant: "t", gcid: "g", qt: QuestionTypeMCQ, payload: []byte(`{}`), wantSubstr: "id required"},
		{name: "missing tenant", id: "i", tenant: "", gcid: "g", qt: QuestionTypeMCQ, payload: []byte(`{}`), wantSubstr: "tenant_id"},
		{name: "missing gcid", id: "i", tenant: "t", gcid: "", qt: QuestionTypeMCQ, payload: []byte(`{}`), wantSubstr: "author_gcid"},
		{name: "invalid qt", id: "i", tenant: "t", gcid: "g", qt: QuestionType("flashcard"), payload: []byte(`{}`), wantSubstr: "invalid question_type"},
		{name: "empty payload", id: "i", tenant: "t", gcid: "g", qt: QuestionTypeOE, payload: []byte(``), wantSubstr: "request_payload"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewQueuedJob(tc.id, tc.tenant, tc.gcid, tc.qt, tc.payload)
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err %q missing substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// FSM transitions
// -----------------------------------------------------------------------------

func newTestJob(t *testing.T) *Job {
	t.Helper()
	j, err := NewQueuedJob("job-x", "tenant-x", "gcid-x", QuestionTypeMCQ, []byte(`{}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return j
}

func TestJob_MarkInProgressFromQueued(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	if err := j.MarkInProgress(); err != nil {
		t.Fatalf("MarkInProgress: %v", err)
	}
	if j.Status != StatusInProgress {
		t.Errorf("expected IN_PROGRESS; got %s", j.Status)
	}
}

func TestJob_MarkInProgressRejectsTerminal(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	_ = j.MarkCompleted([]byte(`{}`), nil, false, 1, 0)
	if err := j.MarkInProgress(); err == nil {
		t.Errorf("MarkInProgress from COMPLETED should error")
	}
}

func TestJob_MarkCompletedFromQueued(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	result := []byte(`{"stem":"x","mcq_payload":{}}`)
	trace := []byte(`[{"name":"generate","status":"COMPLETED"}]`)
	if err := j.MarkCompleted(result, trace, false, 1, 10); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if j.Status != StatusCompleted {
		t.Errorf("expected COMPLETED; got %s", j.Status)
	}
	if string(j.ResultPayload) != string(result) {
		t.Errorf("result_payload not stored")
	}
	if j.CompletedAt == nil {
		t.Errorf("CompletedAt should be set on terminal")
	}
	if j.AttemptCount != 1 || j.ManaCharged != 10 {
		t.Errorf("attempt_count/mana_charged not stored")
	}
}

func TestJob_MarkCompletedWithQualityWarning(t *testing.T) {
	t.Parallel()
	// Per user-locked semantics 2026-05-17: retries exhausted →
	// MarkCompleted with quality_warning=true (NOT MarkRefused).
	j := newTestJob(t)
	if err := j.MarkCompleted([]byte(`{}`), nil, true, 4, 30); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if j.Status != StatusCompleted {
		t.Errorf("expected COMPLETED; got %s", j.Status)
	}
	if !j.QualityWarning {
		t.Errorf("expected QualityWarning=true")
	}
}

func TestJob_MarkRefusedHappy(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	if err := j.MarkRefused(
		RefusalReasonGuardrailPre,
		"armor:pii_high_risk_block",
		"Your prompt couldn't be processed.",
		nil, nil, 0, 0,
	); err != nil {
		t.Fatalf("MarkRefused: %v", err)
	}
	if j.Status != StatusRefused {
		t.Errorf("expected REFUSED; got %s", j.Status)
	}
	if j.RefusalReason != RefusalReasonGuardrailPre {
		t.Errorf("refusal_reason not stored")
	}
	if j.CompletedAt == nil {
		t.Errorf("CompletedAt should be set on REFUSED")
	}
}

func TestJob_MarkRefusedRejectsEmptyReason(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	if err := j.MarkRefused("", "", "", nil, nil, 0, 0); err == nil {
		t.Errorf("MarkRefused with empty reason should error")
	}
}

func TestJob_MarkRefusedRejectsInvalidReason(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	if err := j.MarkRefused(RefusalReason("POLICY"), "", "", nil, nil, 0, 0); err == nil {
		t.Errorf("MarkRefused with invalid reason should error")
	}
}

func TestJob_MarkRefusedRejectsTerminal(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	_ = j.MarkCompleted([]byte(`{}`), nil, false, 1, 0)
	if err := j.MarkRefused(RefusalReasonGuardrailPre, "", "", nil, nil, 0, 0); err == nil {
		t.Errorf("MarkRefused from COMPLETED should error")
	}
}

func TestJob_MarkFailedHappy(t *testing.T) {
	t.Parallel()
	j := newTestJob(t)
	if err := j.MarkFailed(nil, "orchestrator crashed", 0, 0); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if j.Status != StatusFailed {
		t.Errorf("expected FAILED; got %s", j.Status)
	}
	if j.RefusalUserFacingMsg != "orchestrator crashed" {
		t.Errorf("error message not stored")
	}
}

func TestJob_TerminalTransitionsAreOneWay(t *testing.T) {
	t.Parallel()
	for _, term := range []func(*Job){
		func(j *Job) { _ = j.MarkCompleted([]byte(`{}`), nil, false, 1, 0) },
		func(j *Job) { _ = j.MarkRefused(RefusalReasonGuardrailPre, "", "", nil, nil, 0, 0) },
		func(j *Job) { _ = j.MarkFailed(nil, "err", 0, 0) },
	} {
		j := newTestJob(t)
		term(j)
		if err := j.MarkCompleted([]byte(`{}`), nil, false, 1, 0); err == nil {
			t.Errorf("expected double-terminal-transition error")
		}
	}
}
