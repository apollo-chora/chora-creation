// compose_model_test.go — ADR-195 WS7 (D7) coverage for the {intent, input_kind}
// derivation that stamps the .v2 compose events. ComposeModelForJob reads the
// explicit persisted compose VOs directly (j.Intent, j.Input.Kind()); the legacy
// job_type fallback was retired with the enum in WS9 step 3.
package question_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestComposeModelForJob(t *testing.T) {
	tests := []struct {
		name          string
		job           *question.ComposeJob
		wantIntent    question.Intent
		wantInputKind question.InputKind
	}{
		{
			name:          "nil job defaults to new_question/prompt",
			job:           nil,
			wantIntent:    question.IntentNewQuestion,
			wantInputKind: question.InputPrompt,
		},
		{
			name: "explicit VOs — new_question/by_hand",
			job: &question.ComposeJob{
				Intent: question.IntentNewQuestion,
				Input:  question.Input{ByHand: true},
			},
			wantIntent:    question.IntentNewQuestion,
			wantInputKind: question.InputByHand,
		},
		{
			name: "explicit VOs — new_question/source_files",
			job: &question.ComposeJob{
				Intent: question.IntentNewQuestion,
				Input:  question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x"}}},
			},
			wantIntent:    question.IntentNewQuestion,
			wantInputKind: question.InputSourceFiles,
		},
		{
			name: "explicit VOs — new_question/prompt",
			job: &question.ComposeJob{
				Intent: question.IntentNewQuestion,
				Input:  question.Input{Prompt: "p"},
			},
			wantIntent:    question.IntentNewQuestion,
			wantInputKind: question.InputPrompt,
		},
		{
			name: "explicit VOs — image_regen/prompt",
			job: &question.ComposeJob{
				Intent: question.IntentImageRegen,
				Input:  question.Input{Prompt: "p"},
			},
			wantIntent:    question.IntentImageRegen,
			wantInputKind: question.InputPrompt,
		},
		{
			name: "explicit VOs — model_answer_fill/prompt",
			job: &question.ComposeJob{
				Intent: question.IntentModelAnswerFill,
				Input:  question.Input{Prompt: "p"},
			},
			wantIntent:    question.IntentModelAnswerFill,
			wantInputKind: question.InputPrompt,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotIntent, gotInputKind := question.ComposeModelForJob(tc.job)
			if gotIntent != tc.wantIntent || gotInputKind != tc.wantInputKind {
				t.Errorf("ComposeModelForJob = (%q, %q); want (%q, %q)",
					gotIntent, gotInputKind, tc.wantIntent, tc.wantInputKind)
			}
		})
	}
}

// TestReconstructInput_RoundTrips covers ADR-195 WS9 step 1: the repo read rebuilds
// the Input VO from the persisted input_kind column (the authoritative Input.Kind()
// value backfilled by migration 0022). ReconstructInput is the inverse of Kind() —
// for every real kind k, ReconstructInput(k, ...).Kind() == k — so a loaded
// ComposeJob carries an Input the domain can branch on, retiring the job_type
// fallback. by_hand / source_files MUST set their discriminating field (the
// job_type fallback that previously classified them is dropped in WS9 step 2/3);
// prompt-kind is the catch-all (an empty seed still classifies as prompt).
func TestReconstructInput_RoundTrips(t *testing.T) {
	t.Parallel()
	files := []question.SourceFileRef{{BlobURI: "gs://b/m.pdf", MimeType: "application/pdf", Role: "source"}}
	cases := []struct {
		name      string
		kind      question.InputKind
		prompt    string
		files     []question.SourceFileRef
		wantInput question.Input
		wantKind  question.InputKind
	}{
		{"by_hand", question.InputByHand, "", nil, question.Input{ByHand: true}, question.InputByHand},
		{"source_files", question.InputSourceFiles, "", files, question.Input{SourceFiles: files}, question.InputSourceFiles},
		{"prompt recovered", question.InputPrompt, "explain photosynthesis", nil, question.Input{Prompt: "explain photosynthesis"}, question.InputPrompt},
		{"prompt empty seed still prompt-kind", question.InputPrompt, "", nil, question.Input{}, question.InputPrompt},
		{"unknown/NULL kind falls to prompt", question.InputKind(""), "p", nil, question.Input{Prompt: "p"}, question.InputPrompt},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := question.ReconstructInput(tc.kind, tc.prompt, tc.files)
			if got.ByHand != tc.wantInput.ByHand || got.Prompt != tc.wantInput.Prompt || len(got.SourceFiles) != len(tc.wantInput.SourceFiles) {
				t.Fatalf("ReconstructInput(%q,...) = %+v; want %+v", tc.kind, got, tc.wantInput)
			}
			if got.Kind() != tc.wantKind {
				t.Errorf("round-trip Kind() = %q; want %q", got.Kind(), tc.wantKind)
			}
		})
	}
}
