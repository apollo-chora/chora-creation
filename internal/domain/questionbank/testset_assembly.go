// testset_assembly.go — the W3.B.2 assemble-TestSet-from-QuestionBank seam.
//
// Service.AssembleTestSet (see service.go) turns a QuestionBank's active
// question references into ONE DRAFT TestSet by REUSING the existing
// chora.creation.question_batch.accepted.v1 event: the chora-delivery
// batch_testset_inbox subscriber is origin-agnostic and already assembles a
// DRAFT TestSet from that event. There is deliberately NO new event / topic /
// proto here — W3.B.2 is pure reuse of the batch-accept contract.
//
// This file owns the domain-level port + DTOs the Service depends on. A thin
// adapter (internal/adapter/pubsub/questionbank_testset_assembler.go) maps
// AssembledTestSet → ports.QuestionBatchAcceptedEvent and publishes via the
// EXISTING outbox-backed QuestionBatchAcceptedPublisher — keeping this package
// dependency-free w.r.t. infrastructure (hexagonal: the domain owns the port).
package questionbank

import (
	"context"
	"errors"
	"time"
)

const (
	// DefaultTestSetPoints is the per-item mark a v1 bank-assembled test set
	// stamps on every question (the author refines the DRAFT later). Mirrors
	// the delivery composer's D5 fallback (defaultBatchItemPoints = 10).
	DefaultTestSetPoints = 10
	// MaxTestSetTitleLength caps the assembled test-set title.
	MaxTestSetTitleLength = 256
	// MaxTestSetDescriptionLength caps the assembled test-set description.
	MaxTestSetDescriptionLength = 2048
	// TestSetStatusAssembling is the async status returned on the 202 accept —
	// the DRAFT test set materialises in chora-delivery off the published event.
	TestSetStatusAssembling = "assembling"
)

// ErrEmptyBank is returned by AssembleTestSet when the bank has zero active
// questions. Mapped to HTTP 422 (fail-loud — cannot assemble an empty bank).
var ErrEmptyBank = errors.New("cannot assemble a test set from an empty question bank")

// ErrTitleRequired is returned by AssembleTestSet when the title is blank.
// Mapped to HTTP 400.
var ErrTitleRequired = errors.New("test set title is required")

// ErrAssemblerNotWired is returned by AssembleTestSet when no
// TestSetAssemblyPublisher is attached. Mapped to HTTP 503 (fail-loud — the
// endpoint never silently no-ops per feedback_no_stubs_real_wiring).
var ErrAssemblerNotWired = errors.New("test set assembly publisher not wired")

// AssembledTestSetItem is one bank question projected into the test-set event,
// mirroring ports.QuestionBatchTestSetItem (the per-item shape the delivery
// batch_testset_inbox subscriber consumes).
type AssembledTestSetItem struct {
	QuestionAtomID string // the question's owning LearningAtom id
	QuestionID     string // the embedded Question row id
	QuestionType   string // "mcq" | "oe"
	Points         int    // DefaultTestSetPoints in v1
	DisplayOrder   int    // contiguous 1-based curated position
}

// AssembledTestSet is the domain-level payload for a bank→test-set assembly. A
// thin adapter maps it onto ports.QuestionBatchAcceptedEvent (no new contract).
type AssembledTestSet struct {
	JobID       string // new UUIDv7 assembly id (delivery's source_job_id idempotency key)
	HostAtomID  string // provenance = the question_bank_id (see Service.AssembleTestSet)
	TenantID    string
	AuthorGCID  string
	Title       string
	Description string
	Items       []AssembledTestSetItem
	AcceptedAt  time.Time
	Traceparent string
}

// TestSetAssemblyPublisher publishes an AssembledTestSet. Production satisfies
// it with a thin adapter over the EXISTING QuestionBatchAcceptedPublisher
// (internal/adapter/pubsub). Nil ⇒ AssembleTestSet fails loud (ErrAssemblerNotWired).
type TestSetAssemblyPublisher interface {
	PublishAssembledTestSet(ctx context.Context, evt AssembledTestSet) error
}

// AssembleTestSetInput is the parameter envelope for Service.AssembleTestSet.
type AssembleTestSetInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string // caller GCID; must own the bank
	Title          string
	Description    string
	Traceparent    string // W3C trace context propagated into the event
}

// AssembleTestSetResult is the 202 payload.
type AssembleTestSetResult struct {
	JobID  string
	Status string // always TestSetStatusAssembling
}
