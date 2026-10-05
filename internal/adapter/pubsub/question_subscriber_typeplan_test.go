// question_subscriber_typeplan_test.go — RED→GREEN coverage for the CHO-1819 P2
// mixed-type batch threading: the batch dispatcher lifts a validated type_plan
// out of settings_json onto chora.creation.ai_assist.started.v1 (field 21) and
// flips content_type to the "mixed" discriminator. Legacy single-type batches
// (no type_plan) stay byte/behaviour-unchanged.
package pubsub_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestSubscriber_Batch_MixedTypePlan_ThreadsTypePlanAndContentTypeMixed(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedBatchJob(repo)
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	settings := `{"count":10,"grounding_mode":"strict","type_plan":[` +
		`{"question_type":"mcq","count":8,"max_images":2},` +
		`{"question_type":"oe","count":2,"max_images":0}]}`
	if err := sub.Handle(context.Background(), batchEvt(job, settings)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusRunning {
		t.Errorf("Status = %q; want running", job.Status)
	}

	ev := sync.snapshot()
	if len(ev) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(ev))
	}
	p := ev[0].Payload.(map[string]any)

	// content_type flips to "mixed"; requested_count stays the sum.
	if p["content_type"] != "mixed" {
		t.Errorf("content_type = %v; want mixed", p["content_type"])
	}
	if p["requested_count"] != 10 {
		t.Errorf("requested_count = %v; want 10", p["requested_count"])
	}

	tp, ok := p["type_plan"].([]map[string]any)
	if !ok {
		t.Fatalf("type_plan not []map[string]any: %T", p["type_plan"])
	}
	if len(tp) != 2 {
		t.Fatalf("type_plan len = %d; want 2", len(tp))
	}
	if tp[0]["question_type"] != "mcq" || tp[0]["count"] != 8 || tp[0]["max_images"] != 2 {
		t.Errorf("type_plan[0] = %v; want mcq/8/2", tp[0])
	}
	if tp[1]["question_type"] != "oe" || tp[1]["count"] != 2 || tp[1]["max_images"] != 0 {
		t.Errorf("type_plan[1] = %v; want oe/2/0", tp[1])
	}
}

func TestSubscriber_Batch_NoTypePlan_OmitsKeyAndKeepsSingleTypeContent(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedBatchJob(repo)
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	// Legacy single-type batch: no type_plan in settings.
	if err := sub.Handle(context.Background(), batchEvt(job, `{"count":5,"grounding_mode":"strict"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p := sync.snapshot()[0].Payload.(map[string]any)
	if _, present := p["type_plan"]; present {
		t.Errorf("type_plan key must be absent for a legacy single-type batch, got %v", p["type_plan"])
	}
	if p["content_type"] != "mcq" {
		t.Errorf("content_type = %v; want mcq (single-type, byte-stable)", p["content_type"])
	}
}
