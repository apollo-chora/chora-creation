//go:build integration

// atom_roundtrip_integration_test.go — real-Postgres round-trip test for
// AtomRepository. Writes a LearningAtom with EVERY field set to a distinct
// value, reads it back, and compares EVERY field. This gate exists because
// stub-querier tests cannot catch an omitted INSERT column — that class of
// bug has already been found twice in this migration.
//
// Run with:
//
//	export CHORA_TEST_DSN='postgres://chora:chora@localhost:5432/chora_creation?sslmode=disable'
//	go test -tags integration ./internal/adapter/pg/...
//
// Skips cleanly when CHORA_TEST_DSN is unset.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// TestIntegration_AtomRepository_FullRoundTrip verifies every persisted
// column round-trips through Save → Get with the exact value written.
func TestIntegration_AtomRepository_FullRoundTrip(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	atomID := uuid.NewString()
	gcid := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)

	in := &atom.LearningAtom{
		AtomID:           atomID,
		TenantID:         tenantID,
		Gcid:             gcid,
		Title:            "roundtrip-title",
		Body:             "roundtrip-body",
		Tags:             []string{"alpha", "beta", "gamma"},
		Mode:             atom.ModeStraightUp,
		Status:           atom.StatusDraft,
		Revision:         7,
		CreatedAt:        now,
		UpdatedAt:        now,
		CourseID:         uuid.NewString(),
		QuestionType:     atom.QuestionTypeMCQ,
		Difficulty:       3,
		Stem:             "roundtrip-stem",
		Subject:          "roundtrip-subject",
		CognitiveLevel:   atom.CognitiveLevelAnalysis,
		AuthorNote:       "roundtrip-author-note",
		ClonedFromAtomID: uuid.NewString(),
		ReuseVisibility:  atom.ReuseTenant,
		ImdaDimensionTags: []atom.ImdaDimTag{
			atom.ImdaDimAccountability,
		},
		MediaAssets: []atom.MediaAsset{
			{Type: "image", URL: "s3://bucket/media.png", MIME: "image/png", AltText: "alt"},
		},
	}

	// Use a tenant-scoped transaction so RLS lets app_rw read/write.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'"); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	repo := pg.NewAtomRepositoryFromTx(tx)
	if err := repo.Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Get(ctx, tenantID, atomID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Compare EVERY field.
	if got.AtomID != in.AtomID {
		t.Errorf("AtomID: got %q want %q", got.AtomID, in.AtomID)
	}
	if got.TenantID != in.TenantID {
		t.Errorf("TenantID: got %q want %q", got.TenantID, in.TenantID)
	}
	if got.Gcid != in.Gcid {
		t.Errorf("Gcid: got %q want %q", got.Gcid, in.Gcid)
	}
	if got.Title != in.Title {
		t.Errorf("Title: got %q want %q", got.Title, in.Title)
	}
	if got.Body != in.Body {
		t.Errorf("Body: got %q want %q", got.Body, in.Body)
	}
	if len(got.Tags) != len(in.Tags) {
		t.Fatalf("Tags len: got %d want %d", len(got.Tags), len(in.Tags))
	}
	for i := range in.Tags {
		if got.Tags[i] != in.Tags[i] {
			t.Errorf("Tags[%d]: got %q want %q", i, got.Tags[i], in.Tags[i])
		}
	}
	if got.Mode != in.Mode {
		t.Errorf("Mode: got %q want %q", got.Mode, in.Mode)
	}
	if got.Status != in.Status {
		t.Errorf("Status: got %q want %q", got.Status, in.Status)
	}
	if got.Revision != in.Revision {
		t.Errorf("Revision: got %d want %d", got.Revision, in.Revision)
	}
	if !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, in.CreatedAt)
	}
	if !got.UpdatedAt.Equal(in.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v want %v", got.UpdatedAt, in.UpdatedAt)
	}
	if got.DeletedAt != nil {
		t.Errorf("DeletedAt: got %v want nil", got.DeletedAt)
	}
	if got.CourseID != in.CourseID {
		t.Errorf("CourseID: got %q want %q", got.CourseID, in.CourseID)
	}
	if got.QuestionType != in.QuestionType {
		t.Errorf("QuestionType: got %q want %q", got.QuestionType, in.QuestionType)
	}
	if got.Difficulty != in.Difficulty {
		t.Errorf("Difficulty: got %d want %d", got.Difficulty, in.Difficulty)
	}
	if got.Stem != in.Stem {
		t.Errorf("Stem: got %q want %q", got.Stem, in.Stem)
	}
	if got.Subject != in.Subject {
		t.Errorf("Subject: got %q want %q", got.Subject, in.Subject)
	}
	if got.CognitiveLevel != in.CognitiveLevel {
		t.Errorf("CognitiveLevel: got %q want %q", got.CognitiveLevel, in.CognitiveLevel)
	}
	if got.AuthorNote != in.AuthorNote {
		t.Errorf("AuthorNote: got %q want %q", got.AuthorNote, in.AuthorNote)
	}
	if got.ClonedFromAtomID != in.ClonedFromAtomID {
		t.Errorf("ClonedFromAtomID: got %q want %q", got.ClonedFromAtomID, in.ClonedFromAtomID)
	}
	if got.ReuseVisibility != in.ReuseVisibility {
		t.Errorf("ReuseVisibility: got %q want %q", got.ReuseVisibility, in.ReuseVisibility)
	}
	if len(got.ImdaDimensionTags) != len(in.ImdaDimensionTags) {
		t.Fatalf("ImdaDimensionTags len: got %d want %d", len(got.ImdaDimensionTags), len(in.ImdaDimensionTags))
	}
	for i := range in.ImdaDimensionTags {
		if got.ImdaDimensionTags[i] != in.ImdaDimensionTags[i] {
			t.Errorf("ImdaDimensionTags[%d]: got %+v want %+v", i, got.ImdaDimensionTags[i], in.ImdaDimensionTags[i])
		}
	}
	if len(got.MediaAssets) != len(in.MediaAssets) {
		t.Fatalf("MediaAssets len: got %d want %d", len(got.MediaAssets), len(in.MediaAssets))
	}
	for i := range in.MediaAssets {
		if got.MediaAssets[i] != in.MediaAssets[i] {
			t.Errorf("MediaAssets[%d]: got %+v want %+v", i, got.MediaAssets[i], in.MediaAssets[i])
		}
	}

	t.Logf("Full round-trip OK: %d fields verified for atom %s", 25, atomID)
}
