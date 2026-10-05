// collection_converted_publisher_test.go — ADR-233 outbox slice (WS-4),
// RED-first.
//
// The collection outbox publisher is now BIMODAL, and it has to be:
//
//	converted_to_study_list.v1  → BINARY protobuf (Schema-Registry bound to
//	                              chora-creation-collection-converted_to_study_list-v1;
//	                              a real cross-domain consumer reads it)
//	the other five topics       → JSON (deliberately schemaless — nothing
//	                              consumes them, and re-encoding them binary
//	                              would need five schemas nobody wants)
//
// Get the routing wrong and the publish is rejected by the schema and
// dead-letters — silently, from the FE's point of view, because the HTTP 201
// already returned. So this test round-trips the persisted outbox payload bytes
// back through proto.Unmarshal, exactly as the broker will.
package pubsub_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	creationpubsub "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

func convertFixture(t *testing.T) (*collection.Collection, []string) {
	t.Helper()
	c := &collection.Collection{
		CollectionID: wsCollID,
		TenantID:     wsTenantID,
		OwnerGcid:    wsGcid,
		Title:        "Decimals drill",
		Visibility:   audience.Private,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	atomIDs := []string{
		"01970000-0000-7000-a000-000000000001",
		"01970000-0000-7000-a000-000000000002",
		"01970000-0000-7000-a000-000000000003",
	}
	return c, atomIDs
}

func TestCollectionPublisher_ConvertedToStudyList_IsBinaryProto(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{Store: store})

	c, atomIDs := convertFixture(t)
	// The actor — a FORKER, not the collection's owner (CHO-2165). The wire format
	// must carry the learner the study list belongs to.
	ev := collection.NewCollectionConvertedToStudyListEvent(c, wsActorGcid, atomIDs,
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "vendor=x")

	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(store.rows))
	}
	row := store.rows[0]

	if row.Topic != "chora.creation.collection.converted_to_study_list.v1" {
		t.Errorf("topic = %q", row.Topic)
	}
	if row.AggregateType != "collection" {
		t.Errorf("aggregate_type = %q; want collection", row.AggregateType)
	}
	if row.AggregateID != wsCollID {
		t.Errorf("aggregate_id = %q; want the collection id", row.AggregateID)
	}
	if row.IdempotencyKey != ev.EventID {
		t.Errorf("idempotency_key = %q; want the event id", row.IdempotencyKey)
	}

	// The payload must be BINARY protobuf, not JSON. If it were JSON, the
	// Schema Registry would reject the publish and the row would dead-letter.
	if json.Valid(row.Payload) {
		t.Errorf("payload parses as JSON — this topic is BINARY; the broker will reject it:\n%s", row.Payload)
	}

	var got creationv1.CollectionConvertedToStudyList
	if err := proto.Unmarshal(row.Payload, &got); err != nil {
		t.Fatalf("outbox payload does not parse as CollectionConvertedToStudyList — it WILL dead-letter: %v", err)
	}

	if got.GetCollectionId() != wsCollID {
		t.Errorf("collection_id = %q; want %q", got.GetCollectionId(), wsCollID)
	}
	// owner_gcid on the WIRE names the owner of the DERIVED STUDY LIST — the actor
	// who forked — not the owner of the source collection (CHO-2165). chora-
	// consumption reads this field straight into learning_paths.owner_gcid, so a
	// collection owner here would build the list for the wrong learner.
	if got.GetOwnerGcid() != wsActorGcid {
		t.Errorf("owner_gcid = %q; want the ACTOR %q", got.GetOwnerGcid(), wsActorGcid)
	}
	if got.GetOwnerGcid() == wsGcid {
		t.Errorf("owner_gcid is the COLLECTION's owner (%q) — the derived study list would be "+
			"built for the sharer, not the forker", wsGcid)
	}
	if got.GetStudyListEventId() != ev.EventID {
		t.Errorf("study_list_event_id = %q; want the event's own id %q", got.GetStudyListEventId(), ev.EventID)
	}
	if len(got.GetAtomIds()) != 3 {
		t.Fatalf("atom_ids = %v; want 3", got.GetAtomIds())
	}
	for i, want := range atomIDs {
		if got.GetAtomIds()[i] != want {
			t.Errorf("atom_ids[%d] = %q; want %q (curated order must survive)", i, got.GetAtomIds()[i], want)
		}
	}

	// The envelope rides field 1 and must carry the trace context.
	e := got.GetEnvelope()
	if e == nil {
		t.Fatal("envelope missing")
	}
	// The envelope gcid is the ACTOR too — whose action this was.
	if e.GetTenantId() != wsTenantID || e.GetGcid() != wsActorGcid {
		t.Errorf("envelope identity = %s/%s; want %s/%s (tenant/ACTOR)",
			e.GetTenantId(), e.GetGcid(), wsTenantID, wsActorGcid)
	}
	if e.GetTraceparent() != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %q; want the propagated W3C trace context", e.GetTraceparent())
	}

	// The row's own envelope map (the JSONB column the dispatcher reads) must
	// still carry the 11 mandatory fields.
	for _, k := range []string{
		"event_id", "idempotency_key", "tenant_id", "gcid", "occurred_at",
		"published_at", "traceparent", "tracestate", "source_project",
		"source_service", "schema_version",
	} {
		if _, ok := row.Envelope[k]; !ok {
			t.Errorf("outbox envelope missing mandatory field %q", k)
		}
	}
}

// The five siblings keep their JSON payload path — re-encoding them binary would
// need five Schema Registry schemas nobody asked for, and they have no consumer.
func TestCollectionPublisher_SiblingTopicsStayJSON(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{Store: store})

	c, _ := convertFixture(t)
	if err := pub.Publish(context.Background(), collection.NewCollectionCreatedEvent(c, "", "")); err != nil {
		t.Fatalf("Publish created: %v", err)
	}
	if err := pub.Publish(context.Background(), collection.NewCollectionDeletedEvent(c, wsGcid, "", "")); err != nil {
		t.Fatalf("Publish deleted: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(store.rows))
	}
	for _, row := range store.rows {
		if !json.Valid(row.Payload) {
			t.Errorf("%s: payload is not JSON; the schemaless siblings must stay JSON-wire", row.Topic)
		}
		var m map[string]any
		if err := json.Unmarshal(row.Payload, &m); err != nil {
			t.Errorf("%s: json.Unmarshal: %v", row.Topic, err)
			continue
		}
		if m["collection_id"] != wsCollID {
			t.Errorf("%s: collection_id = %v", row.Topic, m["collection_id"])
		}
	}
}

// The audience value object must reach the JSON wire as its canonical lowercase
// token, never the retired PRIVATE/TENANT_INTERNAL/PUBLIC vocabulary.
func TestCollectionPublisher_JSONCarriesCanonicalAudience(t *testing.T) {
	t.Parallel()

	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{Store: store})

	c, _ := convertFixture(t)
	c.Visibility = audience.Tenant
	if err := pub.Publish(context.Background(), collection.NewCollectionCreatedEvent(c, "", "")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(store.rows[0].Payload, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["visibility"] != "tenant" {
		t.Errorf("visibility = %v; want the canonical audience token \"tenant\"", m["visibility"])
	}
}
