// protomarshal_reuse_visibility_test.go — ADR-229 WS-1 (CHO-2127) binary
// encoding for the reuse-consent spine.
//
// RED before the encoders land:
//   - encodeAtomCreated / encodeAtomPublished gain enum field 30
//     (reuse_visibility; label -> varint via lookupReuseVisibility).
//   - encodeAtomReuseVisibilityChanged registers the NEW topic
//     chora.creation.atom.reuse_visibility_changed.v1 (without it the outbox
//     falls back to JSON and the BINARY schema rejects at publish -> DLQ).
//
// Round-trips assert through the generated chora/creation/v1 bindings so the
// hand-rolled wire bytes stay pinned to the deployed Schema Registry shape.
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// TestMarshalPayload_AtomCreated_CarriesReuseVisibility — field 30 rides
// created.v1 as the enum varint; the label form is the producer's map value.
func TestMarshalPayload_AtomCreated_CarriesReuseVisibility(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":          "01971a90-aaaa-7000-8000-00000000c001",
		"status":           "draft",
		"title":            "reuse created",
		"author_gcid":      env.GCID,
		"reuse_visibility": "private",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomCreated: %v", err)
	}
	if got := m.GetReuseVisibility(); got != creationv1.ReuseVisibility_REUSE_VISIBILITY_PRIVATE {
		t.Errorf("AtomCreated.reuse_visibility = %v; want REUSE_VISIBILITY_PRIVATE (field 30)", got)
	}
}

// TestMarshalPayload_AtomPublished_CarriesReuseVisibility — the publish
// snapshot carries the current audience (tenant here).
func TestMarshalPayload_AtomPublished_CarriesReuseVisibility(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":                 "01971a90-aaaa-7000-8000-00000000c002",
		"status":                  "published",
		"title":                   "reuse published",
		"author_gcid":             env.GCID,
		"current_revision_id":     "019e58ea-9999-7b74-9f1d-0000000000bb",
		"current_revision_number": int32(1),
		"published_at":            env.OccurredAt,
		"reuse_visibility":        "tenant",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomPublished: %v", err)
	}
	if got := m.GetReuseVisibility(); got != creationv1.ReuseVisibility_REUSE_VISIBILITY_TENANT {
		t.Errorf("AtomPublished.reuse_visibility = %v; want REUSE_VISIBILITY_TENANT (field 30)", got)
	}
}

// TestMarshalPayload_AtomPublished_ElidesUnsetReuseVisibility — a payload
// without the key stays byte-stable (proto3 default elision): decoding yields
// UNSPECIFIED, which consumers harden to private.
func TestMarshalPayload_AtomPublished_ElidesUnsetReuseVisibility(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-00000000c003",
		"status":      "published",
		"author_gcid": env.GCID,
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetReuseVisibility(); got != creationv1.ReuseVisibility_REUSE_VISIBILITY_UNSPECIFIED {
		t.Errorf("unset reuse_visibility decoded as %v; want UNSPECIFIED (elided)", got)
	}
}

// TestMarshalPayload_AtomReuseVisibilityChanged_BinaryEncodes — the NEW topic
// round-trips: atom_id (2), reuse_visibility (3), previous_visibility (4),
// author_gcid (5), changed_at (6) + envelope.
func TestMarshalPayload_AtomReuseVisibilityChanged_BinaryEncodes(t *testing.T) {
	env := fixedEnvelope()
	const atomID = "01971a90-aaaa-7000-8000-00000000c004"
	payload := map[string]any{
		"atom_id":             atomID,
		"reuse_visibility":    "friends",
		"previous_visibility": "tenant",
		"author_gcid":         env.GCID,
		"changed_at":          env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.reuse_visibility_changed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v (want a registered binary encoder, NOT ErrUnsupportedTopic)", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	var m creationv1.AtomReuseVisibilityChanged
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomReuseVisibilityChanged: %v", err)
	}
	if got := m.GetAtomId(); got != atomID {
		t.Errorf("atom_id = %q; want %q", got, atomID)
	}
	if got := m.GetReuseVisibility(); got != creationv1.ReuseVisibility_REUSE_VISIBILITY_FRIENDS {
		t.Errorf("reuse_visibility = %v; want REUSE_VISIBILITY_FRIENDS", got)
	}
	if got := m.GetPreviousVisibility(); got != creationv1.ReuseVisibility_REUSE_VISIBILITY_TENANT {
		t.Errorf("previous_visibility = %v; want REUSE_VISIBILITY_TENANT", got)
	}
	if got := m.GetAuthorGcid(); got != env.GCID {
		t.Errorf("author_gcid = %q; want %q", got, env.GCID)
	}
	if m.GetChangedAt() == nil || m.GetChangedAt().AsTime().IsZero() {
		t.Errorf("changed_at missing on the wire")
	}
	if envMsg := m.GetEnvelope(); envMsg == nil || envMsg.GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %v; want %q", envMsg.GetEventId(), env.EventID)
	}
	if got := m.GetEnvelope().GetTenantId(); got != env.TenantID {
		t.Errorf("envelope.tenant_id = %q; want %q", got, env.TenantID)
	}
}

// TestMarshalPayload_AtomReuseVisibilityChanged_UnknownLabelFailsLoud — an
// unmapped audience label must error (never silently encode UNSPECIFIED).
func TestMarshalPayload_AtomReuseVisibilityChanged_UnknownLabelFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":          "01971a90-aaaa-7000-8000-00000000c005",
		"reuse_visibility": "everyone", // not a valid audience
		"author_gcid":      env.GCID,
	}
	if _, err := protomarshal.MarshalPayload("chora.creation.atom.reuse_visibility_changed.v1", env, payload); err == nil {
		t.Fatal("unknown reuse_visibility label must fail loud, got nil error")
	}
}
