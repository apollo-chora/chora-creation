package cms

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the CMS service.
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

// TopicCMSEvents is the Cloud Pub/Sub topic for all CMS domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.cms.events" to canonical
// chora.{domain}.{aggregate}.{event_type}.v{N} form. CMS is hosted under
// chora-creation (5-core consolidation per M12.2).
const TopicCMSEvents = "chora.creation.cms.events.v1"

// CMS domain event type constants.
const (
	EventContentItemCreated       = "cms.content_item.created"
	EventContentItemUpdated       = "cms.content_item.updated"
	EventContentItemDeleted       = "cms.content_item.deleted"
	EventContentVersionCreated    = "cms.content_version.created"
	EventContentPublished         = "cms.content.published"
	EventMediaAssetUploaded       = "cms.media_asset.uploaded"
	EventMediaAssetDeleted        = "cms.media_asset.deleted"
	EventProjectCreated           = "cms.project.created"
	EventDeliverableSubmitted     = "cms.deliverable.submitted"
	EventDeliverableGraded        = "cms.deliverable.graded"
	EventMediaAssetProcessed      = "cms.media_asset.processed"
	EventMediaVariantCreated      = "cms.media_variant.created"
	EventStorageQuotaExceeded     = "cms.storage_quota.exceeded"
	EventChorapediaEntryPublished = "cms.chorapedia.entry_published"
	EventChorapediaEntryUpdated   = "cms.chorapedia.entry_updated"
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
