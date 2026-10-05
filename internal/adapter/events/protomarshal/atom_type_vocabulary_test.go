// Package protomarshal_test — CHO-2178: the atom-type vocabulary guard.
//
// THE DEFECT THIS PINS. `learning_atoms.question_type` is a free-text varchar
// with no CHECK; the wire carries a closed proto enum; and the label -> wire
// map (lookupAtomType) was hand-written and PARTIAL. Three of the domain's five
// canonical flavours — outline, flashcard, video — had no wire value at all.
// An `outline` atom therefore failed its outbox marshal, never emitted
// atom.published.v1, never reached chora-sharing's atom_projections, and so
// could never be reused by anyone but its author. Four such atoms sat dead in
// prod. The publish handler logged the marshal error and returned 200 OK.
//
// WHY THE OLD TESTS STAYED GREEN. TestEnumRoundTrip_AtomType asserts a
// hand-written table of names the map ALREADY knows. It could never ask the
// only question that mattered: *is the map total over the domain's
// vocabulary?* These tests ask exactly that, and they iterate
// atom.AllAtomTypes rather than a local list — so a new flavour added to the
// domain without a wire value breaks the build instead of silently killing
// that atom's events.
package protomarshal_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// nonDerivableWireNames is the exception table for the naming law below, and
// keeping it at EXACTLY ONE entry is the whole point. Every other flavour's
// wire name derives mechanically from its label (ATOM_TYPE_<UPPER(label)>), so
// chora-sharing and chora-consumption can decode by derivation off the
// generated descriptor instead of hand-maintaining their own reverse tables —
// which is precisely how sharing's decoder came to be missing ATOM_TYPE_ESSAY
// and silently blanked the type on every essay atom it projected.
var nonDerivableWireNames = map[atom.AtomType]string{
	atom.TypeMCQ: "ATOM_TYPE_MULTIPLE_CHOICE",
}

// expectedWireName is the naming law: a domain label's wire constant is
// ATOM_TYPE_<UPPER(label)> unless it appears in the exception table.
func expectedWireName(t atom.AtomType) string {
	if n, ok := nonDerivableWireNames[t]; ok {
		return n
	}
	return "ATOM_TYPE_" + strings.ToUpper(string(t))
}

// encodeAtomType marshals an atom.created.v1 payload carrying the given
// question_type label and reads back the wire enum. It drives the unexported
// lookupAtomType through the public encoder surface — the same route the outbox
// takes in production.
func encodeAtomType(t *testing.T, label string) (creationv1.AtomType, error) {
	t.Helper()
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.creation.atom.created.v1", env, map[string]any{
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": env.GCID,
		"atom_type":   label,
	})
	if err != nil {
		return 0, err
	}
	var m creationv1.AtomCreated
	if uerr := proto.Unmarshal(bz, &m); uerr != nil {
		t.Fatalf("atom_type=%q: unmarshal own bytes: %v", label, uerr)
	}
	return m.GetQuestionType(), nil
}

// TestAtomTypeWire_TotalOverDomainVocabulary — every flavour the domain calls
// valid MUST be expressible on the wire. This is CHO-2178's RED test: before
// the fix, outline / flashcard / video all fail the marshal outright.
func TestAtomTypeWire_TotalOverDomainVocabulary(t *testing.T) {
	for _, dt := range atom.AllAtomTypes {
		got, err := encodeAtomType(t, string(dt))
		if err != nil {
			t.Errorf("question_type=%q is a VALID domain flavour but cannot be marshalled: %v\n"+
				"    => an atom of this flavour can never emit atom.published.v1, so no other "+
				"domain will ever see it. Add ATOM_TYPE_%s to chora.creation.v1.AtomType.",
				dt, err, strings.ToUpper(string(dt)))
			continue
		}
		if got == creationv1.AtomType_ATOM_TYPE_UNSPECIFIED {
			t.Errorf("question_type=%q encodes to ATOM_TYPE_UNSPECIFIED — the flavour is "+
				"silently erased on the wire; it needs a distinct enum value", dt)
		}
	}
}

// TestAtomTypeWire_DeclaredAndDistinct — each flavour maps to a value the
// CONTRACT actually declares (not just some integer the hand-written map made
// up), and no two flavours collapse onto the same value.
func TestAtomTypeWire_DeclaredAndDistinct(t *testing.T) {
	seen := map[creationv1.AtomType]atom.AtomType{}
	for _, dt := range atom.AllAtomTypes {
		got, err := encodeAtomType(t, string(dt))
		if err != nil {
			continue // reported by TestAtomTypeWire_TotalOverDomainVocabulary
		}
		if _, declared := creationv1.AtomType_name[int32(got)]; !declared {
			t.Errorf("question_type=%q maps to wire value %d, which chora.creation.v1.AtomType "+
				"does not declare — the encoder invented a value the schema cannot carry", dt, got)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("question_type=%q and %q both encode to %s — distinct domain flavours must "+
				"not collapse to one wire value (a consumer could never tell them apart)",
				prev, dt, got)
		}
		seen[got] = dt
	}
}

// TestAtomTypeWire_NamingLaw — the wire constant's NAME derives from the domain
// label (ATOM_TYPE_<UPPER(label)>), with exactly one documented exception.
// This is what lets the two consumer services decode by derivation off the
// generated descriptor rather than hand-listing a reverse table each. Break the
// law and the reverse-derivation silently returns the wrong label, so pin it.
func TestAtomTypeWire_NamingLaw(t *testing.T) {
	for _, dt := range atom.AllAtomTypes {
		got, err := encodeAtomType(t, string(dt))
		if err != nil {
			continue // reported by TestAtomTypeWire_TotalOverDomainVocabulary
		}
		want := expectedWireName(dt)
		if got.String() != want {
			t.Errorf("question_type=%q encodes to %s, want %s\n"+
				"    => consumers derive the label back as lower(trim(%q, \"ATOM_TYPE_\")); "+
				"breaking the naming law makes them decode the WRONG flavour.",
				dt, got.String(), want, got.String())
		}
	}
}

// TestAtomTypeWire_RoundTripsToDomainLabel — the closing of the loop, and the
// law the whole bug violated: encode a domain label, derive it back off the
// descriptor, and you must land on the label you started with.
func TestAtomTypeWire_RoundTripsToDomainLabel(t *testing.T) {
	for _, dt := range atom.AllAtomTypes {
		got, err := encodeAtomType(t, string(dt))
		if err != nil {
			continue // reported by TestAtomTypeWire_TotalOverDomainVocabulary
		}
		// The derivation chora-sharing / chora-consumption perform.
		back := strings.ToLower(strings.TrimPrefix(got.String(), "ATOM_TYPE_"))
		if inv, isException := nonDerivableWireNames[dt]; isException && got.String() == inv {
			back = string(dt)
		}
		if back != string(dt) {
			t.Errorf("round-trip lost the flavour: %q -> %s -> %q", dt, got, back)
		}
	}
}
