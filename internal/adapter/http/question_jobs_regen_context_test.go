// question_jobs_regen_context_test.go — white-box unit coverage for the Bug 1
// image-regen context extractors: findCandidateRegenContext (stem + OE model
// answer lift) and originalAuthoringSource (authoring-grounding assembly).
package httpadapter

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestFindCandidateRegenContext(t *testing.T) {
	t.Parallel()
	cand := []byte(`[
		{"draft_id":"d-mcq","type":"mcq","prompt":"Which organelle performs photosynthesis?","image_gcs_uri":"gs://chora-atom-media-dev/t/a/stem.png","answer_image_gcs_uri":"gs://chora-atom-media-dev/t/a/ans.png"},
		{"draft_id":"d-oe","type":"oe","prompt":"Explain photosynthesis.","oe_payload":{"model_answer":"Light reactions then the Calvin cycle."}},
		{"draft_id":"d-untyped","prompt":"Untyped stem"}
	]`)

	t.Run("oe lifts stem + model answer", func(t *testing.T) {
		got, ok := findCandidateRegenContext(cand, "d-oe")
		if !ok {
			t.Fatal("expected found")
		}
		if got.qType != "oe" {
			t.Errorf("qType = %q; want oe", got.qType)
		}
		if got.currentStem != "Explain photosynthesis." {
			t.Errorf("currentStem = %q", got.currentStem)
		}
		if got.currentModelAnswer != "Light reactions then the Calvin cycle." {
			t.Errorf("currentModelAnswer = %q", got.currentModelAnswer)
		}
	})

	t.Run("mcq has stem, no model answer", func(t *testing.T) {
		got, ok := findCandidateRegenContext(cand, "d-mcq")
		if !ok {
			t.Fatal("expected found")
		}
		if got.currentStem != "Which organelle performs photosynthesis?" {
			t.Errorf("currentStem = %q", got.currentStem)
		}
		if got.currentModelAnswer != "" {
			t.Errorf("currentModelAnswer = %q; want empty for mcq", got.currentModelAnswer)
		}
	})

	t.Run("lifts the persisted durable gs:// object paths (ADR-210)", func(t *testing.T) {
		got, ok := findCandidateRegenContext(cand, "d-mcq")
		if !ok {
			t.Fatal("expected found")
		}
		if got.imageGcsURI != "gs://chora-atom-media-dev/t/a/stem.png" {
			t.Errorf("imageGcsURI = %q; want the stem durable ref", got.imageGcsURI)
		}
		if got.answerImageGcsURI != "gs://chora-atom-media-dev/t/a/ans.png" {
			t.Errorf("answerImageGcsURI = %q; want the answer durable ref", got.answerImageGcsURI)
		}
	})

	t.Run("absent gcs uris stay empty (pre-B2 producer)", func(t *testing.T) {
		got, ok := findCandidateRegenContext(cand, "d-oe")
		if !ok {
			t.Fatal("expected found")
		}
		if got.imageGcsURI != "" || got.answerImageGcsURI != "" {
			t.Errorf("gcs uris = {%q,%q}; want empty when the candidate has none", got.imageGcsURI, got.answerImageGcsURI)
		}
	})

	t.Run("untyped defaults to mcq", func(t *testing.T) {
		got, ok := findCandidateRegenContext(cand, "d-untyped")
		if !ok {
			t.Fatal("expected found")
		}
		if got.qType != string(question.TypeMCQ) {
			t.Errorf("qType = %q; want mcq default", got.qType)
		}
	})

	t.Run("unknown draft + empty json", func(t *testing.T) {
		if _, ok := findCandidateRegenContext(cand, "nope"); ok {
			t.Error("expected not found for unknown draft")
		}
		if _, ok := findCandidateRegenContext(nil, "d-oe"); ok {
			t.Error("expected not found for empty json")
		}
	})
}

func TestOriginalAuthoringSource(t *testing.T) {
	t.Parallel()

	t.Run("prompt-only job uses the authoring prompt", func(t *testing.T) {
		p := &question.ComposeJob{Input: question.Input{Prompt: "Author a question on the water cycle."}}
		if got := originalAuthoringSource(p); got != "Author a question on the water cycle." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("batch job folds context + source filenames, deduped", func(t *testing.T) {
		p := &question.ComposeJob{
			Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/biology.pdf"}}},
			SettingsJSON: []byte(`{"context":"Grade 9 biology","prompt":"Grade 9 biology",` +
				`"source_files":[{"filename":"biology.pdf"},{"filename":"biology.pdf"}]}`),
		}
		got := originalAuthoringSource(p)
		// "Grade 9 biology" appears once (prompt==context dedup); filename once.
		if got != "Grade 9 biology; biology.pdf" {
			t.Errorf("got %q; want deduped context + filename", got)
		}
	})

	t.Run("no recoverable grounding returns empty", func(t *testing.T) {
		p := &question.ComposeJob{Input: question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}}}
		if got := originalAuthoringSource(p); got != "" {
			t.Errorf("got %q; want empty (raw blob URIs excluded)", got)
		}
	})
}
