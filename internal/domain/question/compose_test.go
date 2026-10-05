// compose_test.go — ADR-195 WS1 RED→GREEN coverage for the unified compose
// domain model: Intent / Input / Plan value objects + the intent-branched
// ComposeJob.Validate(). The five-value job_type enum is being collapsed into
// ONE compose operation (Intent × Input × Plan); the agent layer
// (composer_question.go) is UNCHANGED — these strings mirror it verbatim.
package question_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/aiassist"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// ─── Intent ──────────────────────────────────────────────────────────────────

func TestIntent_WireFormat(t *testing.T) {
	t.Parallel()
	// Must mirror composer_question.go:46-57 verbatim (the agent already branches
	// on these strings; a drift would silently break the agent mapping).
	cases := map[question.Intent]string{
		question.IntentNewQuestion:     "new_question",
		question.IntentModelAnswerFill: "model_answer_fill",
		question.IntentImageRegen:      "image_regen",
	}
	for i, want := range cases {
		if string(i) != want {
			t.Errorf("wire format = %q; want %q", string(i), want)
		}
	}
}

func TestIntent_Valid(t *testing.T) {
	t.Parallel()
	for _, i := range []question.Intent{
		question.IntentNewQuestion, question.IntentModelAnswerFill, question.IntentImageRegen,
	} {
		if !i.Valid() {
			t.Errorf("%q should be Valid()", string(i))
		}
	}
	if question.Intent("garbage").Valid() {
		t.Errorf("garbage intent must not be Valid()")
	}
	if question.Intent("").Valid() {
		t.Errorf("empty intent must not be Valid()")
	}
}

func TestIntent_MintsAtoms(t *testing.T) {
	t.Parallel()
	if !question.IntentNewQuestion.MintsAtoms() {
		t.Errorf("new_question MUST mint atoms")
	}
	for _, i := range []question.Intent{question.IntentModelAnswerFill, question.IntentImageRegen} {
		if i.MintsAtoms() {
			t.Errorf("%q must NOT mint atoms (it mutates an existing candidate in place)", string(i))
		}
	}
}

// ─── Input ───────────────────────────────────────────────────────────────────

func TestInput_Predicates(t *testing.T) {
	t.Parallel()
	prompt := question.Input{Prompt: "explain photosynthesis"}
	if !prompt.HasPrompt() || prompt.HasFiles() || !prompt.RequiresLLM() {
		t.Errorf("prompt-only: HasPrompt=true HasFiles=false RequiresLLM=true; got %+v", prompt)
	}
	files := question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x", MimeType: "application/pdf", Role: "source"}}}
	if files.HasPrompt() || !files.HasFiles() || !files.RequiresLLM() {
		t.Errorf("files: HasFiles=true RequiresLLM=true; got %+v", files)
	}
	byHand := question.Input{ByHand: true}
	if byHand.RequiresLLM() {
		t.Errorf("by-hand authoring must NOT require the LLM (candidates supplied inline)")
	}
}

func TestInput_Kind(t *testing.T) {
	t.Parallel()
	// Precedence: by_hand > source_files > prompt.
	cases := []struct {
		name string
		in   question.Input
		want question.InputKind
	}{
		{"prompt", question.Input{Prompt: "p"}, question.InputPrompt},
		{"files", question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x"}}}, question.InputSourceFiles},
		{"by_hand", question.Input{ByHand: true}, question.InputByHand},
		{"by_hand_beats_files", question.Input{ByHand: true, SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x"}}}, question.InputByHand},
		{"files_beat_prompt", question.Input{Prompt: "p", SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x"}}}, question.InputSourceFiles},
		{"empty_defaults_prompt", question.Input{}, question.InputPrompt},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.Kind(); got != tc.want {
				t.Errorf("Kind() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestInput_Validate(t *testing.T) {
	t.Parallel()
	files := []question.SourceFileRef{{BlobURI: "gs://x", MimeType: "application/pdf", Role: "source"}}
	cases := []struct {
		name    string
		intent  question.Intent
		in      question.Input
		wantErr error // nil = must pass
	}{
		{"new_question prompt ok", question.IntentNewQuestion, question.Input{Prompt: "p"}, nil},
		{"new_question files ok", question.IntentNewQuestion, question.Input{SourceFiles: files}, nil},
		{"new_question by-hand ok", question.IntentNewQuestion, question.Input{ByHand: true}, nil},
		{"new_question empty seed", question.IntentNewQuestion, question.Input{}, question.ErrComposeInputEmpty},
		{"by-hand must not carry prompt", question.IntentNewQuestion, question.Input{ByHand: true, Prompt: "p"}, question.ErrComposeByHandSeeded},
		{"by-hand must not carry files", question.IntentNewQuestion, question.Input{ByHand: true, SourceFiles: files}, question.ErrComposeByHandSeeded},
		{"fill prompt ok (author stem)", question.IntentModelAnswerFill, question.Input{Prompt: "stem"}, nil},
		{"fill must not carry files", question.IntentModelAnswerFill, question.Input{SourceFiles: files}, question.ErrComposeNoFilesForIntent},
		{"fill by-hand rejected", question.IntentModelAnswerFill, question.Input{ByHand: true}, question.ErrComposeByHandIntent},
		{"image_regen prompt ok (refine)", question.IntentImageRegen, question.Input{Prompt: "tighter crop"}, nil},
		{"image_regen must not carry files", question.IntentImageRegen, question.Input{SourceFiles: files}, question.ErrComposeNoFilesForIntent},
		{"image_regen by-hand rejected", question.IntentImageRegen, question.Input{ByHand: true}, question.ErrComposeByHandIntent},
		{"invalid intent", question.Intent("nope"), question.Input{Prompt: "p"}, question.ErrInvalidIntent},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate(tc.intent)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("Validate(%s) unexpected error: %v", tc.intent, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%s) err = %v; want %v", tc.intent, err, tc.wantErr)
			}
		})
	}
}

// ─── Plan (NewComposePlan — count always drives a non-empty plan) ─────────────

func TestNewComposePlan_SingleTypeCountDrivesPlan(t *testing.T) {
	t.Parallel()
	// The bug ADR-195 kills: "count=5 -> 1 question". A single-type request with
	// count=N must yield a real N-total 1-entry plan (no SetMode shortcut).
	for _, n := range []int{1, 5, 10} {
		plan, err := question.NewComposePlan(nil, "mcq", n)
		if err != nil {
			t.Fatalf("count=%d: unexpected error %v", n, err)
		}
		if plan.IsEmpty() {
			t.Errorf("count=%d: plan must be non-empty (count drives the plan)", n)
		}
		if plan.TotalCount() != n {
			t.Errorf("count=%d: TotalCount = %d; want %d", n, plan.TotalCount(), n)
		}
	}
}

func TestNewComposePlan_MixedQuotas(t *testing.T) {
	t.Parallel()
	quotas := []aiassist.TypeQuota{{QuestionType: "mcq", Count: 8}, {QuestionType: "oe", Count: 2}}
	plan, err := question.NewComposePlan(quotas, "", 10)
	if err != nil {
		t.Fatalf("mixed quotas summing to count: unexpected error %v", err)
	}
	if plan.TotalCount() != 10 {
		t.Errorf("TotalCount = %d; want 10", plan.TotalCount())
	}
}

func TestNewComposePlan_FailLoud(t *testing.T) {
	t.Parallel()
	// Mixed quotas whose sum != count → fail-loud via aiassist.NewTypePlan.
	quotas := []aiassist.TypeQuota{{QuestionType: "mcq", Count: 8}, {QuestionType: "oe", Count: 2}}
	if _, err := question.NewComposePlan(quotas, "", 9); !errors.Is(err, aiassist.ErrTypePlanCountMismatch) {
		t.Errorf("sum!=count: err = %v; want ErrTypePlanCountMismatch", err)
	}
	// Single-type with a non-positive count is rejected.
	if _, err := question.NewComposePlan(nil, "mcq", 0); err == nil {
		t.Errorf("count=0: want a fail-loud error, got nil")
	}
}

// ─── ComposeJob.Validate (the domain branches on intent) ─────────────────────

func mustPlan(t *testing.T, n int) aiassist.TypePlan {
	t.Helper()
	p, err := question.NewComposePlan(nil, "mcq", n)
	if err != nil {
		t.Fatalf("mustPlan(%d): %v", n, err)
	}
	return p
}

func TestComposeJob_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		job     question.ComposeJob
		wantErr error
	}{
		{
			"new_question LLM with plan ok",
			question.ComposeJob{Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Plan: mustPlan(t, 5)},
			nil,
		},
		{
			"new_question LLM without plan",
			question.ComposeJob{Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}},
			question.ErrComposePlanEmpty,
		},
		{
			"new_question by-hand without plan ok",
			question.ComposeJob{Intent: question.IntentNewQuestion, Input: question.Input{ByHand: true}},
			nil,
		},
		{
			"new_question by-hand with plan rejected",
			question.ComposeJob{Intent: question.IntentNewQuestion, Input: question.Input{ByHand: true}, Plan: mustPlan(t, 3)},
			question.ErrComposePlanNotApplicable,
		},
		{
			"model_answer_fill without plan ok",
			question.ComposeJob{Intent: question.IntentModelAnswerFill, Input: question.Input{Prompt: "stem"}},
			nil,
		},
		{
			"model_answer_fill with plan rejected",
			question.ComposeJob{Intent: question.IntentModelAnswerFill, Input: question.Input{Prompt: "stem"}, Plan: mustPlan(t, 2)},
			question.ErrComposePlanNotApplicable,
		},
		{
			"image_regen without plan ok",
			question.ComposeJob{Intent: question.IntentImageRegen, Input: question.Input{}},
			nil,
		},
		{
			"invalid intent",
			question.ComposeJob{Intent: question.Intent("nope"), Input: question.Input{Prompt: "p"}},
			question.ErrInvalidIntent,
		},
		{
			"propagates input error",
			question.ComposeJob{Intent: question.IntentNewQuestion, Input: question.Input{}},
			question.ErrComposeInputEmpty,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := tc.job
			err := job.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate() err = %v; want %v", err, tc.wantErr)
			}
		})
	}
}

// ComposeJob is a type alias of ComposeJob — it shares the 7-state
// FSM and the existing aggregate fields. Proves the additive evolution.
func TestComposeJob_AliasOfComposeJob(t *testing.T) {
	t.Parallel()
	job := question.ComposeJob{Status: question.JobStatusRequested, Intent: question.IntentNewQuestion}
	if err := job.Transition(question.JobStatusRunning); err != nil {
		t.Fatalf("ComposeJob.Transition: %v", err)
	}
	if job.Status != question.JobStatusRunning || job.StartedAt == nil {
		t.Errorf("alias must share the ComposeJob FSM; got status=%q startedAt=%v", job.Status, job.StartedAt)
	}
	// Assignable both ways (identical types).
	var legacy *question.ComposeJob = &job
	if legacy.Intent != question.IntentNewQuestion {
		t.Errorf("alias and base type must be identical")
	}
}
