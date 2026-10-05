package audience_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
)

// ADR-233 D7 — ONE audience vocabulary across the platform: private | friends |
// tenant. Before this, atoms used TEXT('private','friends','tenant') while
// collections used a PG enum ('PRIVATE','TENANT_INTERNAL','PUBLIC') — the same
// domain concept expressed two incompatible ways.

func TestAudience_ValidValues(t *testing.T) {
	for _, a := range []audience.Audience{audience.Private, audience.Friends, audience.Tenant} {
		if !a.Valid() {
			t.Fatalf("Audience(%q).Valid() = false, want true", a)
		}
	}
}

func TestAudience_RejectsUnknownAndLegacyValues(t *testing.T) {
	// The legacy collection vocabulary MUST NOT validate — mig 0031 rewrites
	// those rows. PUBLIC in particular is retired: RLS caps collections at the
	// tenant, and cross-tenant distribution is a syndication concern
	// (ADR-229 fork (a)), never a visibility level.
	for _, bad := range []audience.Audience{
		"", "PRIVATE", "TENANT_INTERNAL", "PUBLIC", "public", "everyone", "Private",
	} {
		if bad.Valid() {
			t.Fatalf("Audience(%q).Valid() = true, want false (retired/unknown value)", bad)
		}
	}
}

func TestAudience_ParseLegacyCollectionVisibility(t *testing.T) {
	// Used by mig 0031's Go-side backfill assertions + any lingering legacy
	// payload. PUBLIC collapses to tenant — it never could cross a tenant.
	cases := map[string]audience.Audience{
		"PRIVATE":         audience.Private,
		"TENANT_INTERNAL": audience.Tenant,
		"PUBLIC":          audience.Tenant,
	}
	for legacy, want := range cases {
		got, err := audience.ParseLegacyCollectionVisibility(legacy)
		if err != nil {
			t.Fatalf("ParseLegacyCollectionVisibility(%q) unexpected err: %v", legacy, err)
		}
		if got != want {
			t.Fatalf("ParseLegacyCollectionVisibility(%q) = %q, want %q", legacy, got, want)
		}
	}
	if _, err := audience.ParseLegacyCollectionVisibility("NOPE"); err == nil {
		t.Fatal("ParseLegacyCollectionVisibility(\"NOPE\") = nil err, want fail-loud error")
	}
}

func TestAudience_DefaultIsPrivate(t *testing.T) {
	// Consent-safe default (ADR-229 D1): an unset audience is the NARROWEST.
	if audience.Default != audience.Private {
		t.Fatalf("audience.Default = %q, want %q (consent-safe default)", audience.Default, audience.Private)
	}
}
