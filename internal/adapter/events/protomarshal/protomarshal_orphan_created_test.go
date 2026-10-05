// protomarshal_orphan_created_test.go — ADR-229 Amendment A1 (CHO-2132)
// binary encoding for chora.creation.atom.orphan_created.v1.
//
// RED before the encoder lands: without a registered case the outbox row
// falls back to JSON and the BINARY Schema Registry schema
// (chora-creation-atom-orphan_created-v1) rejects it at publish → DLQ
// forever. The round-trip asserts through the generated chora/creation/v1
// binding so the hand-rolled wire bytes stay pinned to the flat proto.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

func TestMarshalPayload_AtomOrphanCreated_RoundTrips(t *testing.T) {
	env := fixedEnvelope()
	orphanedAt := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	payload := map[string]any{
		"orphan_atom_id":        "01971a90-aaaa-7000-8000-00000000d001",
		"orphaned_from_atom_id": "01971a90-aaaa-7000-8000-00000000a001",
		"source_revision_id":    "01971a90-aaaa-7000-8000-00000000f001",
		"author_gcid":           "01971a90-aaaa-7000-8000-000000000aa1",
		"trigger":               "narrowed",
		"orphaned_at":           orphanedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.orphan_created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomOrphanCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomOrphanCreated: %v", err)
	}
	if got := m.GetOrphanAtomId(); got != "01971a90-aaaa-7000-8000-00000000d001" {
		t.Errorf("orphan_atom_id = %q (field 2)", got)
	}
	if got := m.GetOrphanedFromAtomId(); got != "01971a90-aaaa-7000-8000-00000000a001" {
		t.Errorf("orphaned_from_atom_id = %q (field 3)", got)
	}
	if got := m.GetSourceRevisionId(); got != "01971a90-aaaa-7000-8000-00000000f001" {
		t.Errorf("source_revision_id = %q (field 4)", got)
	}
	if got := m.GetAuthorGcid(); got != "01971a90-aaaa-7000-8000-000000000aa1" {
		t.Errorf("author_gcid = %q (field 5)", got)
	}
	if got := m.GetTrigger(); got != "narrowed" {
		t.Errorf("trigger = %q (field 6)", got)
	}
	if got := m.GetOrphanedAt(); got == nil || got.AsTime() != orphanedAt {
		t.Errorf("orphaned_at = %v (field 7), want %v", got, orphanedAt)
	}
	// Envelope rides field 1.
	if m.GetEnvelope() == nil || m.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope not carried: %+v", m.GetEnvelope())
	}
	if m.GetEnvelope().GetTenantId() != env.TenantID {
		t.Errorf("envelope tenant = %q", m.GetEnvelope().GetTenantId())
	}
}

// Wrong-typed fields fail loud rather than emitting silently-wrong bytes.
func TestMarshalPayload_AtomOrphanCreated_WrongTypesFailLoud(t *testing.T) {
	env := fixedEnvelope()
	if _, err := protomarshal.MarshalPayload("chora.creation.atom.orphan_created.v1", env, map[string]any{
		"orphan_atom_id": 12345,
	}); err == nil {
		t.Fatalf("int orphan_atom_id must fail loud")
	}
	if _, err := protomarshal.MarshalPayload("chora.creation.atom.orphan_created.v1", env, map[string]any{
		"orphaned_at": "2026-07-11",
	}); err == nil {
		t.Fatalf("string orphaned_at must fail loud (Timestamp field)")
	}
}
