// questionbank_testset_assembler_test.go — unit coverage for the W3.B.2 thin
// adapter that maps questionbank.AssembledTestSet → ports.QuestionBatchAcceptedEvent
// and publishes via the EXISTING QuestionBatchAcceptedPublisher (no new contract).
package pubsub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// captureBatchPublisher records the last ports.QuestionBatchAcceptedEvent so the
// mapping can be asserted. Satisfies ports.QuestionBatchAcceptedPublisher.
type captureBatchPublisher struct {
	last  ports.QuestionBatchAcceptedEvent
	calls int
	err   error
}

func (c *captureBatchPublisher) PublishQuestionBatchAccepted(_ context.Context, evt ports.QuestionBatchAcceptedEvent) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	c.last = evt
	return nil
}

func TestQuestionBankTestSetAssembler_MapsToBatchAcceptedEvent(t *testing.T) {
	t.Parallel()

	cap := &captureBatchPublisher{}
	asm := pubsubadapter.NewQuestionBankTestSetAssembler(cap)

	now := time.Now().UTC()
	err := asm.PublishAssembledTestSet(context.Background(), questionbank.AssembledTestSet{
		JobID:       "job-1",
		HostAtomID:  "bank-1",
		TenantID:    "tenant-1",
		AuthorGCID:  "owner-1",
		Title:       "Midterm",
		Description: "ch1-3",
		AcceptedAt:  now,
		Traceparent: "00-trace-01",
		Items: []questionbank.AssembledTestSetItem{
			{QuestionAtomID: "atom-1", QuestionID: "q-1", QuestionType: "mcq", Points: 10, DisplayOrder: 1},
			{QuestionAtomID: "atom-2", QuestionID: "q-2", QuestionType: "oe", Points: 10, DisplayOrder: 2},
		},
	})
	if err != nil {
		t.Fatalf("PublishAssembledTestSet: %v", err)
	}
	if cap.calls != 1 {
		t.Fatalf("publisher calls = %d; want 1", cap.calls)
	}
	got := cap.last
	if got.JobID != "job-1" || got.HostAtomID != "bank-1" || got.TenantID != "tenant-1" || got.AuthorGCID != "owner-1" {
		t.Errorf("envelope mismatch: %+v", got)
	}
	if got.TestSetTitle != "Midterm" || got.TestSetDescription != "ch1-3" {
		t.Errorf("title/desc = %q/%q; want Midterm/ch1-3", got.TestSetTitle, got.TestSetDescription)
	}
	if !got.AcceptedAt.Equal(now) || got.Traceparent != "00-trace-01" {
		t.Errorf("acceptedAt/traceparent mismatch: %v / %q", got.AcceptedAt, got.Traceparent)
	}
	if got.SourceFiles != nil {
		t.Errorf("SourceFiles = %+v; want nil (bank-assembled sets carry no grounding files)", got.SourceFiles)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d; want 2", len(got.Items))
	}
	if got.Items[0] != (ports.QuestionBatchTestSetItem{QuestionAtomID: "atom-1", QuestionID: "q-1", QuestionType: "mcq", Points: 10, DisplayOrder: 1}) {
		t.Errorf("item0 = %+v; want mapped 1:1", got.Items[0])
	}
	if got.Items[1] != (ports.QuestionBatchTestSetItem{QuestionAtomID: "atom-2", QuestionID: "q-2", QuestionType: "oe", Points: 10, DisplayOrder: 2}) {
		t.Errorf("item1 = %+v; want mapped 1:1", got.Items[1])
	}
}

func TestQuestionBankTestSetAssembler_PropagatesPublishError(t *testing.T) {
	t.Parallel()

	cap := &captureBatchPublisher{err: errors.New("outbox insert failed")}
	asm := pubsubadapter.NewQuestionBankTestSetAssembler(cap)
	if err := asm.PublishAssembledTestSet(context.Background(), questionbank.AssembledTestSet{JobID: "j"}); err == nil {
		t.Fatalf("expected publish error to propagate")
	}
}

func TestQuestionBankTestSetAssembler_NilPublisherFailsLoud(t *testing.T) {
	t.Parallel()

	asm := pubsubadapter.NewQuestionBankTestSetAssembler(nil)
	if err := asm.PublishAssembledTestSet(context.Background(), questionbank.AssembledTestSet{JobID: "j"}); err == nil {
		t.Fatalf("expected fail-loud error when the wrapped publisher is nil")
	}
}
