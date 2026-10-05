// protomarshal_atom_archived_test.go — binary-encoding round-trip for
// chora.creation.atom.archived.v1 (ADR-217 Debt 3, the atom soft-delete signal).
//
// Without a registered encoder, MarshalPayload returns ErrUnsupportedTopic and
// the outbox JSON-falls-back, which the BINARY-encoded Schema Registry schema
// REJECTS at publish → the event dead-letters and the tenancy atom-count
// projection never decrements. This pins the archive contract (status=ARCHIVED
// / atom_id / archived_by_gcid / archived_at) AND the envelope tenant_id — the
// field the tenancy projection actually reads — through the generated binding.
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

func TestMarshalPayload_AtomArchived_BinaryEncodes(t *testing.T) {
	env := fixedEnvelope()
	const (
		atomID     = "01971a90-aaaa-7000-8000-0000000000a1"
		archivedBy = "019e58ea-8888-7b74-9f1d-0000000000cc"
	)
	payload := map[string]any{
		"atom_id":          atomID,
		"atom_type":        int32(1), // ATOM_TYPE_MULTIPLE_CHOICE
		"status":           "archived",
		"archived_by_gcid": archivedBy,
		"archived_at":      env.OccurredAt,
		// no reason — the soft-delete route carries none
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.atom.archived.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v (want a registered binary encoder, NOT ErrUnsupportedTopic)", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	var m creationv1.AtomArchived
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal AtomArchived: %v", err)
	}
	if got := m.GetAtomId(); got != atomID {
		t.Errorf("AtomArchived.atom_id = %q; want %q (field 2)", got, atomID)
	}
	if got := m.GetQuestionType(); got != creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
		t.Errorf("AtomArchived.question_type = %v; want ATOM_TYPE_MULTIPLE_CHOICE (field 3)", got)
	}
	if m.GetStatus() != creationv1.AtomStatus_ATOM_STATUS_ARCHIVED {
		t.Errorf("AtomArchived.status = %v; want ATOM_STATUS_ARCHIVED (field 4)", m.GetStatus())
	}
	if got := m.GetArchivedByGcid(); got != archivedBy {
		t.Errorf("AtomArchived.archived_by_gcid = %q; want %q (field 5)", got, archivedBy)
	}
	if m.GetArchivedAt() == nil {
		t.Errorf("AtomArchived.archived_at nil; want set (field 7)")
	}
	// The envelope tenant_id round-trips — this is the field the tenancy
	// atom-count subscriber reads (it decodes no payload fields).
	if got := m.GetEnvelope().GetTenantId(); got != env.TenantID {
		t.Errorf("envelope.tenant_id = %q; want %q", got, env.TenantID)
	}
}

func TestMarshalPayload_AtomArchived_ReasonElidedWhenEmpty(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":          "01971a90-aaaa-7000-8000-0000000000a2",
		"status":           "archived",
		"archived_by_gcid": env.GCID,
		"archived_at":      env.OccurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.archived.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AtomArchived
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := m.GetReason(); got != "" {
		t.Errorf("AtomArchived.reason = %q; want empty (elided, field 6)", got)
	}
	if got := m.GetStatus(); got != creationv1.AtomStatus_ATOM_STATUS_ARCHIVED {
		t.Errorf("status = %v; want ARCHIVED", got)
	}
}
