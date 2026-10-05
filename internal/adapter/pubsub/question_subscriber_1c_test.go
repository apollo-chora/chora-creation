// question_subscriber_1c_test.go — RED→GREEN coverage for the Lane 1c
// batch dispatch: settings_json.source_files must be lifted onto the
// ai_assist.started.v1 payload (proto f20 — the encoder serialises the
// `source_files` key) while f17/18 keep mirroring source_files[0].
package pubsub_test

import (
	"context"
	"testing"

	pubsub "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func seed1cBatchJob(repo *fakeSubJobRepo, jobID string) *question.ComposeJob {
	job := &question.ComposeJob{
		JobID:          jobID,
		AtomID:         "atom-1",
		TenantID:       "22222222-2222-7222-8222-222222222222",
		AuthorGCID:     "00000000-0000-7000-8000-000000001001",
		Intent:         question.IntentNewQuestion,
		Input:          question.Input{SourceFiles: []question.SourceFileRef{{BlobURI: "gs://x/m.pdf"}}},
		Status:         question.JobStatusRequested,
		ManaActionCode: "question_authoring_batch_parse",
		ManaCharged:    50,
	}
	repo.jobs[job.JobID] = job
	return job
}

func TestBatchDispatch_LiftsSourceFilesOntoStartedPayload(t *testing.T) {
	repo := newSubJobRepo()
	job := seed1cBatchJob(repo, "job-1c-files")

	sync := &fakeSyncPublisher{}
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo:         repo,
		Mana:            &fakeSubMana{},
		Publisher:       &fakeSubPublisher{},
		SyncPublisher:   sync,
		QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID:          job.JobID,
		AtomID:         job.AtomID,
		AuthorGCID:     job.AuthorGCID,
		TenantID:       job.TenantID,
		QuestionType:   "mcq",
		SourceBlobURI:  "gs://b/tenants/t/jobs/j/source-1",
		SourceMimeType: "text/markdown",
		SettingsJSON: `{"count":3,"source_files":[` +
			`{"blob_uri":"gs://b/tenants/t/jobs/j/source-1","mime_type":"text/markdown","role":"source","filename":"notes.md"},` +
			`{"blob_uri":"gs://b/tenants/t/jobs/j/rubric","mime_type":"text/plain","role":"rubric","filename":"marks.txt"}]}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("sync publishes = %d; want 1 started.v1", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", events[0].Payload)
	}

	files, ok := payload["source_files"].([]map[string]any)
	if !ok {
		t.Fatalf("payload.source_files missing or wrong type: %T", payload["source_files"])
	}
	if len(files) != 2 {
		t.Fatalf("source_files len = %d; want 2", len(files))
	}
	if files[0]["blob_uri"] != "gs://b/tenants/t/jobs/j/source-1" || files[0]["role"] != "source" {
		t.Errorf("files[0] = %v", files[0])
	}
	if files[1]["role"] != "rubric" || files[1]["mime_type"] != "text/plain" {
		t.Errorf("files[1] = %v", files[1])
	}
	// The wire shape is exactly {blob_uri, mime_type, role} — the settings'
	// display filename must NOT leak onto the proto field.
	if _, leaked := files[0]["filename"]; leaked {
		t.Error("filename leaked onto the started payload source_files")
	}
	// f17/18 mirror retained (back-compat during the rollout window).
	if payload["source_blob_uri"] != "gs://b/tenants/t/jobs/j/source-1" {
		t.Errorf("source_blob_uri = %v; want mirror of files[0]", payload["source_blob_uri"])
	}
	if payload["source_mime_type"] != "text/markdown" {
		t.Errorf("source_mime_type = %v", payload["source_mime_type"])
	}
}

func TestBatchDispatch_NoSourceFiles_OmitsKey(t *testing.T) {
	repo := newSubJobRepo()
	job := seed1cBatchJob(repo, "job-1c-legacy")

	sync := &fakeSyncPublisher{}
	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo:         repo,
		Mana:            &fakeSubMana{},
		Publisher:       &fakeSubPublisher{},
		SyncPublisher:   sync,
		QGenCrewEnabled: true,
	})

	evt := pubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID:     job.TenantID,
		QuestionType: "mcq", SourceBlobURI: "gs://b/x", SourceMimeType: "application/pdf",
		SettingsJSON: `{"count":2}`,
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("sync publishes = %d; want 1", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	if _, present := payload["source_files"]; present {
		t.Error("source_files key must be absent for pre-1c settings (byte-stable wire)")
	}
}
