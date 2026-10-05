// ComposeJob tests — RED first. Asserts §2.5 invariants
// reconciled against migration 0008_question_generation_jobs.up.sql's 7-state
// model: requested / running / succeeded / failed / accepted /
// partially_accepted / cancelled.
//
// State machine:
//   - requested → running
//   - running → {succeeded, failed}
//   - succeeded → {accepted, partially_accepted, cancelled}
//   - failed is terminal
//   - accepted/partially_accepted/cancelled are terminal
package question_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestJobStatus_TransitionsValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		from question.JobStatus
		to   question.JobStatus
	}{
		{"requested→running", question.JobStatusRequested, question.JobStatusRunning},
		{"running→succeeded", question.JobStatusRunning, question.JobStatusSucceeded},
		{"running→failed", question.JobStatusRunning, question.JobStatusFailed},
		{"succeeded→accepted", question.JobStatusSucceeded, question.JobStatusAccepted},
		{"succeeded→partially_accepted", question.JobStatusSucceeded, question.JobStatusPartiallyAccepted},
		{"succeeded→cancelled", question.JobStatusSucceeded, question.JobStatusCancelled},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := &question.ComposeJob{Status: tc.from}
			if err := j.Transition(tc.to); err != nil {
				t.Fatalf("Transition(%s→%s) unexpected error: %v", tc.from, tc.to, err)
			}
			if j.Status != tc.to {
				t.Errorf("Status = %q; want %q", j.Status, tc.to)
			}
		})
	}
}

func TestJobStatus_TransitionsRejectInvalid(t *testing.T) {
	t.Parallel()
	terminals := []question.JobStatus{
		question.JobStatusFailed,
		question.JobStatusAccepted,
		question.JobStatusPartiallyAccepted,
		question.JobStatusCancelled,
	}
	// Terminals: NO transitions allowed outwards (must reject any to).
	allTargets := []question.JobStatus{
		question.JobStatusRequested, question.JobStatusRunning, question.JobStatusSucceeded,
		question.JobStatusFailed, question.JobStatusAccepted, question.JobStatusPartiallyAccepted,
		question.JobStatusCancelled,
	}
	for _, term := range terminals {
		for _, tgt := range allTargets {
			term, tgt := term, tgt
			t.Run(string(term)+"→"+string(tgt)+"_must_reject", func(t *testing.T) {
				t.Parallel()
				j := &question.ComposeJob{Status: term}
				err := j.Transition(tgt)
				if !errors.Is(err, question.ErrInvalidStateTransition) {
					t.Errorf("terminal %s→%s: err = %v; want ErrInvalidStateTransition", term, tgt, err)
				}
			})
		}
	}

	// Other illegal transitions
	illegal := []struct {
		from question.JobStatus
		to   question.JobStatus
	}{
		{question.JobStatusRequested, question.JobStatusSucceeded}, // skip running
		{question.JobStatusRequested, question.JobStatusFailed},    // skip running
		{question.JobStatusRequested, question.JobStatusAccepted},
		{question.JobStatusRunning, question.JobStatusAccepted},          // succeeded must come first
		{question.JobStatusRunning, question.JobStatusPartiallyAccepted}, // succeeded must come first
		{question.JobStatusRunning, question.JobStatusCancelled},         // succeeded must come first
		{question.JobStatusSucceeded, question.JobStatusRunning},         // can't go back
		{question.JobStatusSucceeded, question.JobStatusFailed},          // can't fail after success
	}
	for _, c := range illegal {
		c := c
		t.Run("illegal_"+string(c.from)+"→"+string(c.to), func(t *testing.T) {
			t.Parallel()
			j := &question.ComposeJob{Status: c.from}
			err := j.Transition(c.to)
			if !errors.Is(err, question.ErrInvalidStateTransition) {
				t.Errorf("%s→%s: err = %v; want ErrInvalidStateTransition", c.from, c.to, err)
			}
		})
	}
}

func TestJobStatus_RejectsUnknownTarget(t *testing.T) {
	t.Parallel()
	j := &question.ComposeJob{Status: question.JobStatusRequested}
	err := j.Transition(question.JobStatus("not_real"))
	if !errors.Is(err, question.ErrInvalidStateTransition) {
		t.Errorf("err = %v; want ErrInvalidStateTransition for unknown target", err)
	}
}

func TestJobStatus_IsTerminal(t *testing.T) {
	t.Parallel()
	terminals := []question.JobStatus{
		question.JobStatusFailed,
		question.JobStatusAccepted,
		question.JobStatusPartiallyAccepted,
		question.JobStatusCancelled,
	}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("%q should be IsTerminal()", string(s))
		}
	}
	nonTerminals := []question.JobStatus{
		question.JobStatusRequested,
		question.JobStatusRunning,
		question.JobStatusSucceeded,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("%q should NOT be IsTerminal()", string(s))
		}
	}
	if question.JobStatus("nope").IsTerminal() {
		t.Errorf("unknown status should not be IsTerminal()")
	}
}

func TestTransition_StampsTimestamps(t *testing.T) {
	t.Parallel()
	j := &question.ComposeJob{Status: question.JobStatusRequested}
	if err := j.Transition(question.JobStatusRunning); err != nil {
		t.Fatalf("requested→running: %v", err)
	}
	if j.StartedAt == nil {
		t.Errorf("StartedAt should be set after entering running")
	}
	if err := j.Transition(question.JobStatusSucceeded); err != nil {
		t.Fatalf("running→succeeded: %v", err)
	}
	if j.CompletedAt == nil {
		t.Errorf("CompletedAt should be set after entering succeeded")
	}
	if err := j.Transition(question.JobStatusAccepted); err != nil {
		t.Fatalf("succeeded→accepted: %v", err)
	}
	if j.AcceptedAt == nil {
		t.Errorf("AcceptedAt should be set after entering accepted")
	}
}

func TestTransition_CancelledStampsCompletedAt(t *testing.T) {
	t.Parallel()
	j := &question.ComposeJob{Status: question.JobStatusSucceeded}
	if err := j.Transition(question.JobStatusCancelled); err != nil {
		t.Fatalf("succeeded→cancelled: %v", err)
	}
	if j.CompletedAt == nil {
		t.Errorf("CompletedAt should be set after entering cancelled")
	}
}
