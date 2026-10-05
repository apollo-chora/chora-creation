package media

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Event Type Constants
// ---------------------------------------------------------------------------

func TestEventTypeConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"ProcessingStarted", EventProcessingStarted, "media.processing.started"},
		{"ScanCompleted", EventScanCompleted, "media.scan.completed"},
		{"VariantCreated", EventVariantCreated, "media.variant.created"},
		{"QualityAssessed", EventQualityAssessed, "media.quality.assessed"},
		{"ProcessingCompleted", EventProcessingCompleted, "media.processing.completed"},
		{"ProcessingFailed", EventProcessingFailed, "media.processing.failed"},
		{"ColdStorageTransitioned", EventColdStorageTransitioned, "media.cold_storage.transitioned"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.constant)
		})
	}
}

func TestTopicMediaProcessorEvents(t *testing.T) {
	// Per M12.3.E topic taxonomy migration the constant tracks the
	// canonical chora.{domain}.{aggregate}.{event_type}.v{N} form.
	assert.Equal(t, "chora.creation.media.events.v1", TopicMediaProcessorEvents)
}

// ---------------------------------------------------------------------------
// NewDomainEvent
// ---------------------------------------------------------------------------

func TestNewDomainEvent(t *testing.T) {
	tenantID := uuid.New()
	aggregateID := uuid.New()
	gcid := uuid.New()
	payload := map[string]interface{}{"key": "value"}

	t.Run("with nil GCID", func(t *testing.T) {
		event := NewDomainEvent(
			EventProcessingStarted,
			tenantID,
			nil,
			aggregateID,
			"MediaProcessingJob",
			payload,
		)

		require.NotEqual(t, uuid.Nil, event.EventID, "EventID must be generated")
		assert.Equal(t, EventProcessingStarted, event.EventType)
		assert.Equal(t, tenantID, event.TenantID)
		assert.Nil(t, event.GCID)
		assert.Equal(t, aggregateID, event.AggregateID)
		assert.Equal(t, "MediaProcessingJob", event.AggregateType)
		assert.Equal(t, payload, event.Payload)
		assert.False(t, event.Timestamp.IsZero(), "Timestamp must be set")
		assert.Nil(t, event.CorrelationID, "CorrelationID defaults to nil")
		assert.Nil(t, event.CausationID, "CausationID defaults to nil")
	})

	t.Run("with non-nil GCID", func(t *testing.T) {
		event := NewDomainEvent(
			EventScanCompleted,
			tenantID,
			&gcid,
			aggregateID,
			"MediaProcessingJob",
			payload,
		)

		require.NotNil(t, event.GCID)
		assert.Equal(t, gcid, *event.GCID)
		assert.Equal(t, EventScanCompleted, event.EventType)
	})

	t.Run("with nil payload", func(t *testing.T) {
		event := NewDomainEvent(
			EventVariantCreated,
			tenantID,
			nil,
			aggregateID,
			"MediaProcessingJob",
			nil,
		)

		assert.Nil(t, event.Payload)
		assert.Equal(t, EventVariantCreated, event.EventType)
	})

	t.Run("generates unique EventIDs", func(t *testing.T) {
		event1 := NewDomainEvent(EventProcessingStarted, tenantID, nil, aggregateID, "MediaProcessingJob", nil)
		event2 := NewDomainEvent(EventProcessingStarted, tenantID, nil, aggregateID, "MediaProcessingJob", nil)

		assert.NotEqual(t, event1.EventID, event2.EventID, "each event must have a unique ID")
	})

	t.Run("with empty payload map", func(t *testing.T) {
		event := NewDomainEvent(
			EventProcessingCompleted,
			tenantID,
			nil,
			aggregateID,
			"MediaProcessingJob",
			map[string]interface{}{},
		)

		assert.NotNil(t, event.Payload)
		assert.Empty(t, event.Payload)
	})
}

// ---------------------------------------------------------------------------
// DomainEvent struct fields
// ---------------------------------------------------------------------------

func TestDomainEventStruct(t *testing.T) {
	tenantID := uuid.New()
	aggregateID := uuid.New()

	event := NewDomainEvent(
		EventProcessingFailed,
		tenantID,
		nil,
		aggregateID,
		"MediaProcessingJob",
		map[string]interface{}{
			"failure_reason": "scan failed",
		},
	)

	t.Run("payload access", func(t *testing.T) {
		reason, ok := event.Payload["failure_reason"]
		require.True(t, ok)
		assert.Equal(t, "scan failed", reason)
	})

	t.Run("timestamp is UTC", func(t *testing.T) {
		assert.Equal(t, "UTC", event.Timestamp.Location().String())
	})
}
