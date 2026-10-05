// events.go — the QuestionBank event-publisher port (W3.B.1 forward seam).
//
// SCOPE NOTE (W3.B.1): event emission is DEFERRED. This sub-phase ships the
// EventPublisher port + Event envelope as a forward seam ONLY — the Service
// stores a nil-tolerant publisher and NEVER calls Publish in B.1. A later
// sub-phase wires the outbox publisher + per-event builders once the
// chora.creation.question_bank.* Protobuf contracts exist (creating those protos
// is out of scope here — contracts/codegen are explicitly untouched in B.1).
//
// Keeping the seam now (rather than retrofitting the Service signature later)
// means B.2+ only has to add builders + flip main.go from nil to a real
// publisher — the domain API stays stable.
package questionbank

import "context"

// Event is a domain-level event envelope, ready to be wrapped in the Protobuf
// EventEnvelope at the adapter boundary in a later sub-phase. Mandatory
// envelope fields per .claude/rules/ddd-enforcement.md + CLAUDE.md §6:
// event_id, idempotency_key, tenant_id, gcid, occurred_at, published_at,
// traceparent, tracestate, source_project, source_service, schema_version.
//
// Payload fields are intentionally minimal for B.1; builders that populate
// them per event type land alongside the contract work in a later sub-phase.
type Event struct {
	EventID        string
	IdempotencyKey string
	Type           string
	TenantID       string
	Gcid           string
	OccurredAt     string // RFC3339Nano
	PublishedAt    string // RFC3339Nano
	TraceParent    string
	TraceState     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32

	// Payload (domain data only).
	QuestionBankID string
	OwnerGCID      string
}

// EventPublisher is the port for emitting Pub/Sub events via the outbox. In
// B.1 it is UNWIRED (the Service receives nil). A later sub-phase satisfies
// this port with the outbox adapter once the question_bank event contracts exist.
type EventPublisher interface {
	Publish(ctx context.Context, e Event) error
}
