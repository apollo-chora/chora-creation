// question_batch_publisher_test.go — RED→GREEN coverage for the Lane 1c
// outbox-backed question_batch.accepted.v1 publisher: BINARY payload at
// insert time (round-trip-proven against the generated pb), canonical
// envelope attributes, topic validation.
package pubsub_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

func batchAcceptedEvent() ports.QuestionBatchAcceptedEvent {
	return ports.QuestionBatchAcceptedEvent{
		JobID:              "01970000-0000-7000-8000-00000000b001",
		HostAtomID:         "01970000-0000-7000-8000-00000000a001",
		TenantID:           "22222222-2222-7222-8222-222222222222",
		AuthorGCID:         "00000000-0000-7000-8000-000000001001",
		TestSetTitle:       "Cell Energy Quiz",
		TestSetDescription: "From notes.md",
		Items: []ports.QuestionBatchTestSetItem{
			{QuestionAtomID: "atom-1", QuestionID: "q-1", QuestionType: "mcq", Points: 5, DisplayOrder: 1},
			{QuestionAtomID: "atom-2", QuestionID: "q-2", QuestionType: "oe", Points: 10, DisplayOrder: 2},
		},
		SourceFiles: []ports.QuestionBatchSourceFile{
			{BlobURI: "gs://b/t/j/source-1", MimeType: "application/pdf", Role: "source"},
			{BlobURI: "gs://b/t/j/rubric", MimeType: "text/plain", Role: "rubric"},
		},
		AcceptedAt: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
	}
}

func TestQuestionBatchOutboxPublisher_WritesBinaryRow(t *testing.T) {
	store := creationoutbox.NewInMemoryStore()
	pub := pubsub.NewQuestionBatchOutboxPublisher(pubsub.QuestionBatchOutboxConfig{
		Store:         store,
		SourceProject: "chora-local",
		SourceService: "chora-creation",
	})

	evt := batchAcceptedEvent()
	if err := pub.PublishQuestionBatchAccepted(context.Background(), evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != "chora.creation.question_batch.accepted.v1" {
		t.Errorf("topic = %q", row.Topic)
	}
	if row.TenantID != evt.TenantID || row.GCID != evt.AuthorGCID {
		t.Errorf("row identity = %q/%q", row.TenantID, row.GCID)
	}
	if row.AggregateType != "question_generation_job" {
		t.Errorf("aggregate_type = %q; want question_generation_job", row.AggregateType)
	}
	if row.AggregateID != evt.JobID {
		t.Errorf("aggregate_id = %q; want job_id", row.AggregateID)
	}
	// Idempotency key derives from the job (the broker-side dedupe handle).
	if row.IdempotencyKey == "" || row.Envelope["idempotency_key"] == "" {
		t.Error("idempotency_key missing")
	}
	if row.Envelope["chora_imda_dimension"] != "accountability" {
		t.Errorf("imda dimension = %q; want accountability (D1)", row.Envelope["chora_imda_dimension"])
	}

	// THE load-bearing assertion: the stored payload is BINARY protobuf that
	// the generated binding decodes (NOT JSON — JSON dead-letters).
	var m creationv1.QuestionBatchAccepted
	if err := proto.Unmarshal(row.Payload, &m); err != nil {
		t.Fatalf("payload is not binary QuestionBatchAccepted: %v (first bytes %q)", err, string(row.Payload[:min(20, len(row.Payload))]))
	}
	if m.GetJobId() != evt.JobID || m.GetTestSet().GetTitle() != "Cell Energy Quiz" {
		t.Errorf("decoded = job %q title %q", m.GetJobId(), m.GetTestSet().GetTitle())
	}
	if len(m.GetItems()) != 2 || m.GetItems()[1].GetDisplayOrder() != 2 {
		t.Errorf("items = %+v", m.GetItems())
	}
	if len(m.GetSourceFiles()) != 2 {
		t.Errorf("source_files = %+v", m.GetSourceFiles())
	}
}

func TestQuestionBatchOutboxPublisher_RequiresStoreAndIdentity(t *testing.T) {
	pub := pubsub.NewQuestionBatchOutboxPublisher(pubsub.QuestionBatchOutboxConfig{})
	if err := pub.PublishQuestionBatchAccepted(context.Background(), batchAcceptedEvent()); err == nil {
		t.Error("expected error when Store is nil")
	}

	store := creationoutbox.NewInMemoryStore()
	pub = pubsub.NewQuestionBatchOutboxPublisher(pubsub.QuestionBatchOutboxConfig{Store: store})
	evt := batchAcceptedEvent()
	evt.JobID = ""
	if err := pub.PublishQuestionBatchAccepted(context.Background(), evt); err == nil {
		t.Error("expected error for empty JobID")
	}
	evt = batchAcceptedEvent()
	evt.TenantID = ""
	if err := pub.PublishQuestionBatchAccepted(context.Background(), evt); err == nil {
		t.Error("expected error for empty TenantID")
	}
	evt = batchAcceptedEvent()
	evt.TestSetTitle = "   "
	if err := pub.PublishQuestionBatchAccepted(context.Background(), evt); err == nil {
		t.Error("expected error for blank test-set title")
	}
	evt = batchAcceptedEvent()
	evt.Items = nil
	if err := pub.PublishQuestionBatchAccepted(context.Background(), evt); err == nil {
		t.Error("expected error for empty items")
	}
}
