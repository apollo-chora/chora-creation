// gcs_blob_store_1c_test.go — Lane 1c (D7 multi-file) slot-keyed blob keys.
// The legacy single-file path keeps the locked `/source` key bit-identical;
// multi-file uploads land at `/source-{n}` and the dedicated rubric at
// `/rubric` so N files on one job never overwrite each other.
package storage_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
)

func TestBlobKeyWithSlot_EmptySlotIsLegacySource(t *testing.T) {
	got := storage.BlobKeyWithSlot("t1", "j1", "")
	want := "tenants/t1/jobs/j1/source"
	if got != want {
		t.Errorf("BlobKeyWithSlot(empty) = %q; want legacy %q", got, want)
	}
	// Must equal the original helper byte-for-byte (back-compat lock).
	if got != storage.BlobKey("t1", "j1") {
		t.Error("empty-slot key must equal legacy BlobKey output")
	}
}

func TestBlobKeyWithSlot_NamedSlots(t *testing.T) {
	if got := storage.BlobKeyWithSlot("t1", "j1", "source-2"); got != "tenants/t1/jobs/j1/source-2" {
		t.Errorf("source-2 key = %q", got)
	}
	if got := storage.BlobKeyWithSlot("t1", "j1", "rubric"); got != "tenants/t1/jobs/j1/rubric" {
		t.Errorf("rubric key = %q", got)
	}
}
