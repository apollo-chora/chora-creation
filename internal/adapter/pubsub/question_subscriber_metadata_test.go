// question_subscriber_metadata_test.go — CHO-1657 regression (re)fix coverage.
//
// The author's Subject / Cognitive Level / Difficulty selections must reach the
// qgen crew as the str->str `metadata` map on chora.creation.ai_assist.started.v1
// (proto field 12). The orchestrator stamps these as the `*_hint` session keys
// the ADK composer + critic read (reasoning_engine_executor._stamp_metadata_hints)
// AND surfaces them as ADR-197 prompt_conditions in O+ Decision Traces.
//
// CHO-1657 wired this originally; the CHO-1826 unified-canvas refactor introduced
// new ai_draft / batch request shapes that dropped the `metadata` field, so the
// started event carried an empty map and the hints/conditions silently vanished.
// These tests pin the lift on BOTH dispatch paths + the byte-stable omission when
// no hints are present.
package pubsub_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestSubscriber_AIDraft_CrewEnabled_ForwardsMetadataHints(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Light-dependent reactions","difficulty":4,` +
			`"metadata":{"subject":"Photosynthesis","cognitive_level":"analysis","difficulty":"advanced"}}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	md, ok := payload["metadata"].(map[string]string)
	if !ok {
		t.Fatalf("started payload metadata not map[string]string: %T (%v)", payload["metadata"], payload["metadata"])
	}
	if md["subject"] != "Photosynthesis" {
		t.Errorf("metadata[subject] = %q; want Photosynthesis (subject_hint must reach the crew + ADR-197 conditions)", md["subject"])
	}
	if md["cognitive_level"] != "analysis" {
		t.Errorf("metadata[cognitive_level] = %q; want analysis", md["cognitive_level"])
	}
	if md["difficulty"] != "advanced" {
		t.Errorf("metadata[difficulty] = %q; want advanced", md["difficulty"])
	}
}

func TestSubscriber_AIDraft_CrewEnabled_OmitsMetadataWhenAbsent(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
		SettingsJSON: `{"prompt":"Light-dependent reactions","difficulty":4}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	if _, present := payload["metadata"]; present {
		t.Errorf("metadata must be omitted when no author hints present (byte-stable legacy event)")
	}
}

func TestSubscriber_Batch_CrewEnabled_ForwardsMetadataHints(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedBatchJob(repo)

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true,
	})

	settings := `{"count":3,"grounding_mode":"strict","difficulty":2,` +
		`"metadata":{"subject":"Cellular respiration","cognitive_level":"evaluation","difficulty":"foundation"}}`
	if err := sub.Handle(context.Background(), batchEvt(job, settings)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	md, ok := payload["metadata"].(map[string]string)
	if !ok {
		t.Fatalf("batch started payload metadata not map[string]string: %T (%v)", payload["metadata"], payload["metadata"])
	}
	if md["subject"] != "Cellular respiration" || md["cognitive_level"] != "evaluation" || md["difficulty"] != "foundation" {
		t.Errorf("batch metadata = %v; want subject/cognitive_level/difficulty hints lifted onto the started payload", md)
	}
}
