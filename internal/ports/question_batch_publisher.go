// QuestionBatchAcceptedPublisher — port for publishing
// chora.creation.question_batch.accepted.v1 (Lane 1c, CHO-1703 / ADR-180
// D10): the batch-accept business fact that lets chora-delivery assemble
// ONE DRAFT test set without any cross-DB read.
//
// Production wires the transactional-outbox-backed adapter
// (internal/adapter/pubsub/question_batch_publisher.go) — the row is
// BINARY-encoded at insert (protomarshal; the in_app.created dead-letter
// lesson) and drained by the existing outbox Dispatcher. Exactly-once
// intent is enforced by the CALLER via
// QuestionJobRepository.TransitionFromSucceeded — only the accept that
// wins the succeeded→accepted transition publishes.
package ports

import (
	"context"
	"time"
)

// QuestionBatchTestSetItem is one accepted candidate in FINAL curated order.
type QuestionBatchTestSetItem struct {
	QuestionAtomID string // the NEW LearningAtom minted for the candidate
	QuestionID     string // the embedded Question row id
	QuestionType   string // "mcq" | "oe"
	Points         int    // 1..100 (D5 — test-set-scoped, never on atoms)
	DisplayOrder   int    // contiguous 1-based curated position
}

// QuestionBatchSourceFile is role-tagged grounding-file provenance.
type QuestionBatchSourceFile struct {
	BlobURI  string
	MimeType string
	Role     string // "source" | "rubric"
}

// QuestionBatchAcceptedEvent is the typed payload for the publish.
type QuestionBatchAcceptedEvent struct {
	JobID              string
	HostAtomID         string
	TenantID           string
	AuthorGCID         string
	TestSetTitle       string
	TestSetDescription string
	Items              []QuestionBatchTestSetItem
	SourceFiles        []QuestionBatchSourceFile
	AcceptedAt         time.Time
	Traceparent        string
}

// QuestionBatchAcceptedPublisher publishes the accepted.v1 event.
type QuestionBatchAcceptedPublisher interface {
	PublishQuestionBatchAccepted(ctx context.Context, evt QuestionBatchAcceptedEvent) error
}
