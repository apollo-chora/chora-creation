package media

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the media processor service.
// Mirrors the chora-core DomainEvent structure.
type DomainEvent struct {
	EventID       uuid.UUID              `json:"event_id"`
	EventType     string                 `json:"event_type"`
	Timestamp     time.Time              `json:"timestamp"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          *uuid.UUID             `json:"gcid,omitempty"`
	AggregateID   uuid.UUID              `json:"aggregate_id"`
	AggregateType string                 `json:"aggregate_type"`
	Payload       map[string]interface{} `json:"payload"`
	CorrelationID *uuid.UUID             `json:"correlation_id,omitempty"`
	CausationID   *uuid.UUID             `json:"causation_id,omitempty"`
}

// TopicMediaProcessorEvents is the Cloud Pub/Sub topic for all media processor domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.media-processor.events" to
// canonical chora.{domain}.{aggregate}.{event_type}.v{N} form. Media
// processor is hosted under chora-creation (5-core consolidation per M12.2);
// segment uses "media" (no hyphen — segments are alphanumeric per the
// canonical pattern).
const TopicMediaProcessorEvents = "chora.creation.media.events.v1"

// Media processor domain event type constants.
const (
	EventProcessingStarted       = "media.processing.started"
	EventScanCompleted           = "media.scan.completed"
	EventVariantCreated          = "media.variant.created"
	EventQualityAssessed         = "media.quality.assessed"
	EventProcessingCompleted     = "media.processing.completed"
	EventProcessingFailed        = "media.processing.failed"
	EventColdStorageTransitioned = "media.cold_storage.transitioned"
)

// NewDomainEvent creates a new DomainEvent with a generated UUIDv7 event ID
// and the current timestamp.
func NewDomainEvent(
	eventType string,
	tenantID uuid.UUID,
	gcid *uuid.UUID,
	aggregateID uuid.UUID,
	aggregateType string,
	payload map[string]interface{},
) DomainEvent {
	return DomainEvent{
		EventID:       uuid.Must(uuid.NewV7()),
		EventType:     eventType,
		Timestamp:     time.Now().UTC(),
		TenantID:      tenantID,
		GCID:          gcid,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       payload,
	}
}
