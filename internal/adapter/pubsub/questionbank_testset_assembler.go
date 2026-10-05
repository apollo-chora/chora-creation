// questionbank_testset_assembler.go — W3.B.2 thin adapter mapping the
// questionbank domain's TestSetAssemblyPublisher port onto the EXISTING
// ports.QuestionBatchAcceptedPublisher (chora.creation.question_batch.accepted.v1).
//
// No new event / topic / proto: assemble-TestSet-from-QuestionBank reuses the
// batch-accept contract the chora-delivery batch_testset_inbox subscriber
// already consumes (origin-agnostic — it assembles a DRAFT TestSet from ANY
// publisher of this event). Bank-assembled sets carry no grounding files
// (SourceFiles nil) and use the question_bank_id as host_atom_id provenance —
// the subscriber treats host_atom_id as opaque provenance, never reading it for
// assembly.
package pubsub

import (
	"context"
	"errors"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// QuestionBankTestSetAssembler adapts questionbank.AssembledTestSet onto
// ports.QuestionBatchAcceptedEvent + publishes via the wired outbox publisher.
type QuestionBankTestSetAssembler struct {
	pub ports.QuestionBatchAcceptedPublisher
}

// NewQuestionBankTestSetAssembler wraps the EXISTING batch-accept publisher.
func NewQuestionBankTestSetAssembler(pub ports.QuestionBatchAcceptedPublisher) *QuestionBankTestSetAssembler {
	return &QuestionBankTestSetAssembler{pub: pub}
}

// Compile-time port assertion.
var _ questionbank.TestSetAssemblyPublisher = (*QuestionBankTestSetAssembler)(nil)

// PublishAssembledTestSet maps the domain payload to the existing event DTO and
// publishes it through the outbox-backed QuestionBatchAcceptedPublisher.
func (a *QuestionBankTestSetAssembler) PublishAssembledTestSet(ctx context.Context, evt questionbank.AssembledTestSet) error {
	if a == nil || a.pub == nil {
		return errors.New("questionbank_testset_assembler: QuestionBatchAcceptedPublisher not wired")
	}
	items := make([]ports.QuestionBatchTestSetItem, 0, len(evt.Items))
	for _, it := range evt.Items {
		items = append(items, ports.QuestionBatchTestSetItem{
			QuestionAtomID: it.QuestionAtomID,
			QuestionID:     it.QuestionID,
			QuestionType:   it.QuestionType,
			Points:         it.Points,
			DisplayOrder:   it.DisplayOrder,
		})
	}
	return a.pub.PublishQuestionBatchAccepted(ctx, ports.QuestionBatchAcceptedEvent{
		JobID:              evt.JobID,
		HostAtomID:         evt.HostAtomID,
		TenantID:           evt.TenantID,
		AuthorGCID:         evt.AuthorGCID,
		TestSetTitle:       evt.Title,
		TestSetDescription: evt.Description,
		Items:              items,
		SourceFiles:        nil, // bank-assembled sets carry no grounding files
		AcceptedAt:         evt.AcceptedAt,
		Traceparent:        evt.Traceparent,
	})
}
