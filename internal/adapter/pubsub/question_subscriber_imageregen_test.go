// question_subscriber_imageregen_test.go — RED→GREEN for CHO-1819 P3 review
// image regenerate (CHO-1822 P3a). An image_regen job publishes
// chora.creation.ai_assist.started.v1 with job_kind=image_regen + the regen spec
// (draft_id/placement/prompt) via the load-bearing SyncPublisher, and leaves the
// job RUNNING — the ai_assist terminal subscriber patches the parent candidate's
// image when the orchestrator's render completes.
package pubsub_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestSubscriber_ImageRegen_PublishesStarted_LeavesRunning(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjImageRegen, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo:         repo,
		Mana:            mana,
		Publisher:       pub,
		SyncPublisher:   sync,
		QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"parent_job_id":"01970000-0000-7000-9000-0000000000p1","draft_id":"draft-7","placement":"stem","prompt":"A clearer diagram of the light-dependent reactions","mode":"replace"}`,
		Traceparent:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Job stays running — the terminal subscriber finalises after the render.
	if job.Status != question.JobStatusRunning {
		t.Errorf("Status = %q; want running (image_regen leaves job running)", job.Status)
	}
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times; want 0 on a successful dispatch", mana.refundCalls)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	if events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("started topic = %q", events[0].Topic)
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("started payload not map[string]any: %T", events[0].Payload)
	}
	if payload["assist_id"] != job.JobID {
		t.Errorf("assist_id = %v; want job_id %s", payload["assist_id"], job.JobID)
	}
	if payload["job_kind"] != "image_regen" {
		t.Errorf("job_kind = %v; want image_regen (orchestrator routes on this)", payload["job_kind"])
	}
	// The orchestrator's validate guard needs a non-empty prompt (the image prompt).
	if payload["prompt"] != "A clearer diagram of the light-dependent reactions" {
		t.Errorf("prompt = %v; want the regen prompt", payload["prompt"])
	}
	regen, ok := payload["regen"].(map[string]any)
	if !ok {
		t.Fatalf("regen not map[string]any: %T", payload["regen"])
	}
	if regen["draft_id"] != "draft-7" || regen["placement"] != "stem" {
		t.Errorf("regen = {draft_id:%v, placement:%v}; want {draft-7, stem}", regen["draft_id"], regen["placement"])
	}
	if regen["prompt"] != "A clearer diagram of the light-dependent reactions" {
		t.Errorf("regen.prompt = %v", regen["prompt"])
	}
	if regen["mode"] != "replace" {
		t.Errorf("regen.mode = %v; want replace", regen["mode"])
	}
	if payload["traceparent"] != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %v; should propagate from the event", payload["traceparent"])
	}
	// No generation_completed on dispatch (terminal subscriber owns that).
	if len(pub.snapshot()) != 0 {
		t.Errorf("expected 0 completion events on dispatch; got %d", len(pub.snapshot()))
	}
}

// TestSubscriber_ImageRegen_LiftsQuestionContextIntoRegenSpec is the RED for
// Bug 1 (subscriber half): the regen map published on the started event MUST
// carry the question context (current_stem / current_model_answer /
// original_source) lifted out of settings_json, so protomarshal encodes
// ImageRegenSpec f5/f6/f7 and the orchestrator regenerates the RIGHT image.
func TestSubscriber_ImageRegen_LiftsQuestionContextIntoRegenSpec(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjImageRegen, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub, SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "oe",
		SettingsJSON: `{"parent_job_id":"01970000-0000-7000-9000-0000000000p1","draft_id":"draft-7","placement":"answer",` +
			`"prompt":"crayon-style","mode":"scene",` +
			`"current_stem":"Explain photosynthesis.","current_model_answer":"Light reactions then Calvin cycle.",` +
			`"original_source":"Grade 9 biology unit; biology.pdf","original_mode":"scene"}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload, _ := events[0].Payload.(map[string]any)
	regen, ok := payload["regen"].(map[string]any)
	if !ok {
		t.Fatalf("regen not map[string]any: %T", payload["regen"])
	}
	if regen["current_stem"] != "Explain photosynthesis." {
		t.Errorf("regen.current_stem = %v; want the question stem", regen["current_stem"])
	}
	if regen["current_model_answer"] != "Light reactions then Calvin cycle." {
		t.Errorf("regen.current_model_answer = %v", regen["current_model_answer"])
	}
	if regen["original_source"] != "Grade 9 biology unit; biology.pdf" {
		t.Errorf("regen.original_source = %v", regen["original_source"])
	}
}

func TestSubscriber_ImageRegen_MissingDraftID_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjImageRegen, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub, SyncPublisher: sync, QGenCrewEnabled: true,
	})
	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"parent_job_id":"p1","placement":"stem","prompt":"x"}`, // no draft_id
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed (missing draft_id)", job.Status)
	}
	if len(sync.snapshot()) != 0 {
		t.Errorf("no started event should publish on a malformed regen; got %d", len(sync.snapshot()))
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1 (fail refunds the charge)", mana.refundCalls)
	}
}
