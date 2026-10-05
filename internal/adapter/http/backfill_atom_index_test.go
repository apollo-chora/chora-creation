// backfill_atom_index_test.go — CHO-2273 follow-up: the INTERNAL re-emit
// endpoint POST /api/internal/atoms/backfill-atom-index.
//
// atom.published.v1 (backfillPublished) is UPDATE-only and cannot seed a MISSING
// atom_index row. A 2026-07-18 census found 60 published, gradeable, non-orphan
// MCQ atoms with NO atom_index row (startable but 422-not-gradeable). This
// endpoint re-emits atom.created.v1 (the UPSERT) with the DERIVED key so
// consumption INSERTs the row. It reuses emitAtomUpsert byte-for-byte.
package httpadapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// seedIndexAtom injects a PUBLISHED/DRAFT (optionally orphan) MCQ atom + its
// question + one revision straight into the fakes — no HTTP, so no emit happens
// during setup and the publisher captures ONLY the backfill.
func seedIndexAtom(t *testing.T, atomRepo *inmem.AtomRepository, qRepo *fakeQuestionRepository, status atom.Status, orphan bool) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{TenantID: tenantA, Gcid: gcidA, Title: "t", Body: "b", Mode: atom.ModeStraightUp})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	a.Status = status
	if orphan {
		// IsOrphan() keys on OrphanedFromAtomID (the parent it was repointed
		// from), not OrphanedAt.
		a.OrphanedFromAtomID = uuid.NewString()
		now := time.Now().UTC()
		a.OrphanedAt = &now
	}
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save atom: %v", err)
	}
	qid := uuid.NewString()
	qRepo.questions[qid] = &question.Question{
		QuestionID: qid, AtomID: a.AtomID, TenantID: tenantA, Type: question.TypeMCQ,
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "stored-x", Label: "Right", IsCorrect: true},
			{OptionID: "stored-y", Label: "Wrong", IsCorrect: false},
		}},
		CreatedAt: time.Now().UTC(),
	}
	qRepo.revisions[qid] = []*question.QuestionRevision{{RevisionID: uuid.NewString(), QuestionID: qid, RevisionNumber: 1}}
	return a
}

func TestBackfillAtomIndex_SeedsPublishedWithDerivedKey(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &fakeJobPublisher{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo, QuestionRepository: qRepo, JobEventPublisher: pub,
	})

	pubAtom := seedIndexAtom(t, atomRepo, qRepo, atom.StatusPublished, false)
	draftAtom := seedIndexAtom(t, atomRepo, qRepo, atom.StatusDraft, false)
	orphanAtom := seedIndexAtom(t, atomRepo, qRepo, atom.StatusPublished, true)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/internal/atoms/backfill-atom-index?tenant_id="+tenantA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("backfill: %d %s", w.Code, w.Body.String())
	}

	byAtom := map[string]map[string]any{}
	for _, e := range atomCreatedEvents(pub) {
		if id, _ := e["atom_id"].(string); id != "" {
			byAtom[id] = e
		}
	}

	// Published non-orphan → emitted, status=published, DERIVED key (the served
	// id of the correct option's display content), never the stored id.
	ev, ok := byAtom[pubAtom.AtomID]
	if !ok {
		t.Fatalf("no atom.created.v1 emitted for the published atom (row would stay missing)")
	}
	if ev["status"] != "published" {
		t.Errorf("status = %v; want published", ev["status"])
	}
	wantKey, kerr := (&question.MCQPayload{Options: []question.MCQOption{
		{Label: "Right", IsCorrect: true}, {Label: "Wrong", IsCorrect: false},
	}}).ServedCorrectOptionID()
	if kerr != nil {
		t.Fatalf("compute derived key: %v", kerr)
	}
	if ev["correct_option_id"] != wantKey {
		t.Errorf("correct_option_id = %v; want the derived served id %q (not the stored id)", ev["correct_option_id"], wantKey)
	}

	// Orphan published → skipped (ADR-229 A1: orphans never gain a projection row).
	if _, leaked := byAtom[orphanAtom.AtomID]; leaked {
		t.Errorf("orphan atom got an atom.created.v1 — ADR-229 A1 violation")
	}
	// Draft → not emitted (published-only backfill).
	if _, leaked := byAtom[draftAtom.AtomID]; leaked {
		t.Errorf("draft atom got an atom.created.v1 — the backfill is published-only")
	}
}

func TestBackfillAtomIndex_RequiresTenant(t *testing.T) {
	t.Parallel()
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: inmem.NewAtomRepository(), QuestionRepository: newFakeQRepo(), JobEventPublisher: &fakeJobPublisher{},
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/internal/atoms/backfill-atom-index", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("no tenant → %d; want 400", w.Code)
	}
}
