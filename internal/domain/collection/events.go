// Domain-level event builders for chora.creation.collection.* (WS-6a).
//
// These functions take an aggregate + trace context and return a fully-
// populated Event ready for the publisher port. They do not import any
// adapter package and stay dependency-free.
package collection

import (
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"

	"github.com/apollo-chora/chora-common/env"
)

const (
	envSourceService = "chora-creation"
	envSchemaVersion = int32(1)
)

// Was a hardcoded literal until 2026-09-02 (CHO-2419). No manifest could reach
// it, so a second org stamped every event with chora-local and any consumer
// filtering on source_project would have been filtering on a lie.
// CHORA_SOURCE_PROJECT is now set on every event-emitting service; the literal
// stays as the fallback so this estate is provably unchanged.
var envSourceProject = env.GetOrDefault("CHORA_SOURCE_PROJECT", "chora-local")

// EventType is the canonical Pub/Sub topic name per chora-contracts/CLAUDE.md
// §2 topic taxonomy. Schema lives in
// chora-contracts/proto/events/creation/collection.proto.
type EventType string

const (
	EventTypeCollectionCreated     EventType = "chora.creation.collection.created.v1"
	EventTypeCollectionUpdated     EventType = "chora.creation.collection.updated.v1"
	EventTypeCollectionAtomAdded   EventType = "chora.creation.collection.atom_added.v1"
	EventTypeCollectionAtomRemoved EventType = "chora.creation.collection.atom_removed.v1"
	EventTypeCollectionDeleted     EventType = "chora.creation.collection.deleted.v1"

	// EventTypeCollectionConvertedToStudyList — ADR-233 / spec-001 US5.
	//
	// UNLIKE its five siblings this topic is BINARY (Schema-Registry bound to
	// chora-creation-collection-converted_to_study_list-v1), because unlike them
	// it has a REAL cross-domain consumer: chora-consumption builds the
	// LearningPath from it. The publisher MUST route this one topic through
	// protomarshal — a JSON payload is rejected by the schema and dead-letters.
	EventTypeCollectionConvertedToStudyList EventType = "chora.creation.collection.converted_to_study_list.v1"
)

// Event is a domain-level event ready to be wrapped in the Protobuf
// envelope at the adapter boundary. Adapters convert this into proto
// messages with a chora.common.v1.EventEnvelope (see
// chora-contracts/proto/events/creation/collection.proto).
//
// Envelope mandatory fields per .claude/rules/ddd-enforcement.md
//   - CLAUDE.md §6:
//     event_id, idempotency_key, tenant_id, gcid, occurred_at, published_at,
//     traceparent, tracestate, source_project, source_service, schema_version.
type Event struct {
	EventID        string
	IdempotencyKey string
	Type           EventType
	TenantID       string
	Gcid           string
	OccurredAt     string // RFC3339Nano
	PublishedAt    string // RFC3339Nano
	TraceParent    string
	TraceState     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32

	// Payload fields (domain data only).
	CollectionID  string
	OwnerGcid     string
	Title         string
	Description   string
	Visibility    audience.Audience
	AtomID        string   // populated on atom_added.v1 / atom_removed.v1 only
	Position      int      // populated on atom_added.v1 only
	ChangedFields []string // populated on updated.v1 only
	DeletedByGcid string   // populated on deleted.v1 only

	// AtomIDs — populated on converted_to_study_list.v1 only. The entitled
	// atoms in CURATED order (the proto pins ordering).
	AtomIDs []string
	// StudyListEventID — populated on converted_to_study_list.v1 only. The
	// idempotency key chora-consumption dedups the LearningPath creation on;
	// per the proto comment it is "the event_id of this event by default".
	StudyListEventID string
}

// EventPublisher is the port for emitting Pub/Sub events. The outbox
// adapter satisfies this port + writes to outbox_events; the dispatcher
// drains to Cloud Pub/Sub on a background goroutine (per data-consistency
// + event-driven skills).
type EventPublisher interface {
	Publish(ctx Context, e Event) error
}

// Context is the minimal context surface the publisher port needs. We
// alias context.Context indirectly via an interface so the events.go file
// stays free of the `context` stdlib import — keeping it strictly
// payload-builder logic. The repository / publisher implementations
// satisfy this with the standard context.Context.
type Context interface {
	Deadline() (deadline time.Time, ok bool)
	Done() <-chan struct{}
	Err() error
	Value(key any) any
}

// -----------------------------------------------------------------------------
// Event builders
// -----------------------------------------------------------------------------

// NewCollectionCreatedEvent builds chora.creation.collection.created.v1.
// Called by the handler after Save returns nil.
func NewCollectionCreatedEvent(c *Collection, traceparent, tracestate string) Event {
	id, _ := uuid.NewV7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeCollectionCreated,
		TenantID:       c.TenantID,
		Gcid:           c.OwnerGcid,
		OccurredAt:     c.CreatedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		CollectionID:   c.CollectionID,
		OwnerGcid:      c.OwnerGcid,
		Title:          c.Title,
		Description:    c.Description,
		Visibility:     c.Visibility,
	}
}

// NewCollectionUpdatedEvent builds chora.creation.collection.updated.v1.
// changedFields is the projection from UpdateParams.ChangedFields() so
// subscribers can filter without diffing.
func NewCollectionUpdatedEvent(c *Collection, changedFields []string, traceparent, tracestate string) Event {
	id, _ := uuid.NewV7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeCollectionUpdated,
		TenantID:       c.TenantID,
		Gcid:           c.OwnerGcid,
		OccurredAt:     c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		CollectionID:   c.CollectionID,
		OwnerGcid:      c.OwnerGcid,
		Title:          c.Title,
		Description:    c.Description,
		Visibility:     c.Visibility,
		ChangedFields:  append([]string(nil), changedFields...),
	}
}

// NewCollectionAtomAddedEvent builds chora.creation.collection.atom_added.v1.
func NewCollectionAtomAddedEvent(c *Collection, a *CollectionAtom, traceparent, tracestate string) Event {
	id, _ := uuid.NewV7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeCollectionAtomAdded,
		TenantID:       c.TenantID,
		Gcid:           c.OwnerGcid,
		OccurredAt:     a.AddedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		CollectionID:   c.CollectionID,
		OwnerGcid:      c.OwnerGcid,
		AtomID:         a.AtomID,
		Position:       a.Position,
	}
}

// NewCollectionAtomRemovedEvent builds chora.creation.collection.atom_removed.v1.
func NewCollectionAtomRemovedEvent(c *Collection, atomID, traceparent, tracestate string) Event {
	id, _ := uuid.NewV7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeCollectionAtomRemoved,
		TenantID:       c.TenantID,
		Gcid:           c.OwnerGcid,
		OccurredAt:     c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		CollectionID:   c.CollectionID,
		OwnerGcid:      c.OwnerGcid,
		AtomID:         atomID,
	}
}

// NewCollectionConvertedToStudyListEvent builds
// chora.creation.collection.converted_to_study_list.v1 (ADR-233 / spec-001 US5) —
// the event chora_consumption turns into a LearningPath.
//
// atomIDs are the ENTITLED atoms only, already re-sorted into curated order by
// Collection.ConvertToStudyList — the excluded ones never reach the wire (they
// are reported to the caller in the HTTP response instead).
//
// StudyListEventID is set to the event's OWN EventID: the proto comment pins
// it as "the event_id of this event by default; consumption dedups on this".
// Carrying it as an explicit payload field (rather than making consumption reach
// into the envelope) keeps the dedup key inside the contract.
//
// 🔴 actorGCID — NOT c.OwnerGcid — is the learner this study list BELONGS TO, and
// it is an explicit parameter precisely so the two can never be confused again.
//
// Both identity fields name the ACTOR:
//
//   - Gcid       — whose action this was.
//   - OwnerGcid  — whose study list this becomes. chora_consumption reads this
//     field straight into learning_paths.owner_gcid (falling back to the envelope
//     gcid), so it names the owner of the DERIVED LIST, not the owner of the
//     source collection.
//
// Until CHO-2165 the convert policy was `actor == owner`, so stamping these from
// the Collection was tautologically correct and no test could tell the difference.
// The moment a non-owner may fork, it stops being correct in both directions at
// once: the forker gets no study list, and the SHARER gets one they never asked
// for, carrying the forker's atom set — a stranger writing into someone else's
// account. Found by hand-probing the deployed service; the unit suite was green.
//
// The sharer is not lost. Provenance rides CollectionID → the derived path's
// source_id (ADR-233 D2), which is where a consumer should look for "who curated
// this", never at owner_gcid.
func NewCollectionConvertedToStudyListEvent(
	c *Collection, actorGCID string, atomIDs []string, traceparent, tracestate string,
) Event {
	id, _ := uuid.NewV7()
	eventID := id.String()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:        eventID,
		IdempotencyKey: eventID,
		Type:           EventTypeCollectionConvertedToStudyList,
		TenantID:       c.TenantID,
		Gcid:           actorGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,

		CollectionID:     c.CollectionID,
		OwnerGcid:        actorGCID,
		AtomIDs:          append([]string(nil), atomIDs...),
		StudyListEventID: eventID,
	}
}

// NewCollectionDeletedEvent builds chora.creation.collection.deleted.v1.
// deletedByGcid is the actor that performed the soft-delete; typically the
// owner, but admins may delete via entitled roles in later milestones.
func NewCollectionDeletedEvent(c *Collection, deletedByGcid, traceparent, tracestate string) Event {
	id, _ := uuid.NewV7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	deletedAt := time.Now().UTC()
	if c.DeletedAt != nil {
		deletedAt = *c.DeletedAt
	}
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeCollectionDeleted,
		TenantID:       c.TenantID,
		Gcid:           c.OwnerGcid,
		OccurredAt:     deletedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		CollectionID:   c.CollectionID,
		OwnerGcid:      c.OwnerGcid,
		DeletedByGcid:  deletedByGcid,
	}
}
