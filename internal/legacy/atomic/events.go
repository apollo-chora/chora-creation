package atomic

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the Atomic service.
// Follows the chora-core DomainEvent contract.
type DomainEvent struct {
	EventID       uuid.UUID      `json:"event_id"`
	EventType     string         `json:"event_type"`
	Timestamp     time.Time      `json:"timestamp"`
	TenantID      uuid.UUID      `json:"tenant_id"`
	GCID          *uuid.UUID     `json:"gcid,omitempty"`
	AggregateID   uuid.UUID      `json:"aggregate_id"`
	AggregateType string         `json:"aggregate_type"`
	Payload       map[string]any `json:"payload"`
	CorrelationID *uuid.UUID     `json:"correlation_id,omitempty"`
	CausationID   *uuid.UUID     `json:"causation_id,omitempty"`
}

// Event type constants for the Atomic domain.
const (
	EventTypeAtomCreated           = "atom.created"
	EventTypeAtomUpdated           = "atom.updated"
	EventTypeAtomArchived          = "atom.archived"
	EventTypeAtomRevisionPublished = "atom.revision.published"
	EventTypeAtomRevisionDrafted   = "atom.revision.drafted"
	EventTypeTopicNodeCreated      = "topic_node.created"
	EventTypeTopicNodeMoved        = "topic_node.moved"
	EventTypeAnswerValidated       = "answer.validated"
)

// TopicAtomicEvents is the Pub/Sub topic for all Atomic domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.atomic.events" to the
// canonical chora.{domain}.{aggregate}.{event_type}.v{N} form. The
// legacy single-topic fan-out pattern (one topic carrying many event
// types differentiated by EventType field) is preserved — only the
// topic NAME is migrated. New emissions per-event-type live on the
// canonical chora.creation.atom.{created,revised}.v1 streams emitted
// by internal/adapter/outbox.
const TopicAtomicEvents = "chora.creation.atom.events.v1"

// Aggregate type constants.
const (
	AggregateTypeLearningAtom   = "LearningAtom"
	AggregateTypeTopicNode      = "TopicNode"
	AggregateTypeAtomCompletion = "AtomCompletion"
)

// Assessment event type constants.
const (
	EventTypeAssessmentStarted   = "assessment.started"
	EventTypeAssessmentSubmitted = "assessment.submitted"
	EventTypeAssessmentGraded    = "assessment.graded"
	EventTypePathEnrolled        = "path.enrolled"
	EventTypePathCompleted       = "path.completed"
	EventTypeInteractionRecorded = "interaction.recorded"
)

// Assessment aggregate type constants.
const (
	AggregateTypeAssessmentSession = "AssessmentSession"
	AggregateTypeLockedPath        = "LockedPath"
	AggregateTypeAtomInteraction   = "AtomInteraction"
)

// Extended quiz event type constants (Phase 52.3).
const (
	EventTypeAtomMarked            = "atom.marked"
	EventTypeDocumentImportStarted = "document_import.started"
)

// Extended quiz aggregate type constants (Phase 52.3).
const (
	AggregateTypeImportJob = "ImportJob"
)

// Study list event type constants (Phase 57.4).
const (
	EventTypeStudyListCreated = "study_list.created"
	EventTypeStudyListUpdated = "study_list.updated"
	EventTypeStudyListDeleted = "study_list.deleted"
)

// Study list aggregate type constant.
const (
	AggregateTypeStudyList = "StudyList"
)

// EventPublisher abstracts the event bus (Cloud Pub/Sub in prod,
// in-memory in tests). Defined in domain to avoid import cycles
// (ports imports domain for DomainEvent type).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event DomainEvent) error
}

// NewDomainEvent creates a new DomainEvent with a generated UUIDv7 event ID
// and the current timestamp.
func NewDomainEvent(
	eventType string,
	tenantID uuid.UUID,
	aggregateID uuid.UUID,
	aggregateType string,
	payload map[string]any,
) DomainEvent {
	return DomainEvent{
		EventID:       uuid.Must(uuid.NewV7()),
		EventType:     eventType,
		Timestamp:     time.Now().UTC(),
		TenantID:      tenantID,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       payload,
	}
}
