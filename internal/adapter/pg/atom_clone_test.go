// atom_clone_test.go — RED-first SQL-shape coverage that the pg AtomRepository
// persists + projects the ADR-199 cloned_from_atom_id provenance column.
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestAtomRepository_Save_PersistsClonedFromAtomID(t *testing.T) {
	tq := &stubTxQuerier{}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	now := time.Now().UTC()
	a := &atom.LearningAtom{
		AtomID:           "01970000-0000-7000-8000-0000000000a2",
		TenantID:         "01970000-0000-7000-8000-0000000000t1",
		Gcid:             "01970000-0000-7000-9000-0000000000ca",
		Title:            "Copy of Original",
		Body:             "b",
		Mode:             atom.ModeStraightUp,
		Status:           atom.StatusDraft,
		Stem:             "stem",
		Revision:         1,
		CreatedAt:        now,
		UpdatedAt:        now,
		ClonedFromAtomID: "01970000-0000-7000-8000-0000000000a1",
	}
	if err := r.Save(context.Background(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(tq.execSQL, "cloned_from_atom_id") {
		t.Errorf("Save SQL missing cloned_from_atom_id column; got %q", tq.execSQL)
	}
}

func TestAtomRepository_Get_ProjectsClonedFromAtomID(t *testing.T) {
	tq := &stubTxQuerier{}
	tq.queryRow = &stubRow{err: pg.ErrNoRows}
	r := pg.NewAtomRepositoryFromTxQuerier(tq)

	_, _ = r.Get(context.Background(), "01970000-0000-7000-8000-0000000000t1", "01970000-0000-7000-8000-0000000000a2")
	if !strings.Contains(tq.querySQL, "cloned_from_atom_id") {
		t.Errorf("Get SELECT projection missing cloned_from_atom_id; got %q", tq.querySQL)
	}
}
