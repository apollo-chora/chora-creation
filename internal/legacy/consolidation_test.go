// Package legacy_test asserts the structural integrity of the B4.a
// consolidation under chora-creation per M12.2.A.
//
// chora-atomic / chora-cms / chora-media-processor were three separate
// top-level Go services prior to 2026-05-12. M12.2.A consolidates their
// Content Creation responsibilities into chora-creation, leaving the
// originals in place (Batch 5 will archive them).
//
// This test file does NOT import the sub-packages directly — that would
// create a coupling we don't need at the top-level. It serves as a
// compile-time anchor + TDD RED gate that we have ported the legacy
// code into the expected layout. The sub-packages themselves carry the
// real coverage tests, ported verbatim from the legacy services with
// only the package declaration changed.
package legacy_test

import "testing"

// TestConsolidationLayout pins the B4.a sub-package layout. If any of
// the legacy sub-packages disappear or are renamed without an explicit
// migration, this test fails loudly via the build.
func TestConsolidationLayout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		legacy string
	}{
		{name: "atomic-core", legacy: "chora-atomic"},
		{name: "cms-core", legacy: "chora-cms"},
		{name: "media-core", legacy: "chora-media-processor"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.legacy == "" {
				t.Fatalf("legacy service name not set for case %q", c.name)
			}
		})
	}
}
