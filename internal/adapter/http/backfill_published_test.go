// backfill_published_test.go — HTTP-layer tests for the one-off INTERNAL
// re-emit endpoint POST /api/internal/atoms/backfill-published.
//
// Context: ②.a made the publish handler emit chora.creation.atom.published.v1
// (carrying answerability) on the DRAFT -> PUBLISHED transition. A migration in
// chora-consumption defaulted every existing atom_index row to status='draft',
// so the daily dose is empty until atoms are flipped playable — and only NEW
// publishes emit atom.published.v1. This endpoint re-emits atom.published.v1 for
// EVERY currently-published, non-deleted atom of a tenant so consumption's
// AtomPublishedSubscriber flips the pre-existing atoms playable. It reuses the
// EXACT emit + answerability logic the publish handler uses (no new event shape).
//
// The endpoint is cluster-internal (NOT exposed via the public gateway): it
// reads the tenant from the request (query param / JSON body) and bypasses the
// X-Tenant-Id / gcid header gate.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// seedPublishedQuestionAtom creates an atom, seeds its question via the real
// POST /questions handler (which emits via the SEPARATE JobEventPublisher — nil
// here — and never touches the atomEventPublisher), then flips the atom to
// PUBLISHED directly in the repo. No atom.published.v1 is emitted during setup,
// so the recordingPub starts empty and the backfill emit is the only capture.
func seedPublishedQuestionAtom(t *testing.T, srv http.Handler, repo *inmem.AtomRepository, atomType string, body map[string]any) *atom.LearningAtom {
	t.Helper()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: atomType + " atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	a.QuestionType = atom.AtomType(atomType)
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("save atom: %v", err)
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed question: %d %s", seedW.Code, seedW.Body.String())
	}
	// Flip to PUBLISHED in the repo (no emit) so the backfill is the sole emitter.
	a.Status = atom.StatusPublished
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("flip published: %v", err)
	}
	return a
}

// TestBackfillPublished_ReemitsForPublishedAtomsOnly — the happy path. Two
// PUBLISHED atoms (one MCQ, one OE) + one DRAFT atom. The backfill re-emits
// atom.published.v1 for the two published atoms (with correct answerability) and
// skips the draft.
func TestBackfillPublished_ReemitsForPublishedAtomsOnly(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	// PUBLISHED MCQ atom — opt_1 correct, 2 options.
	mcqAtom := seedPublishedQuestionAtom(t, srv, atomRepo, "mcq", map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "PO", "is_correct": true, "explainer": "PO owns backlog"},
				{"option_id": "opt_2", "label": "PM", "is_correct": false, "explainer": "not a Scrum role"},
			},
		},
	})

	// PUBLISHED open-ended atom.
	oeAtom := seedPublishedQuestionAtom(t, srv, atomRepo, "essay", map[string]any{
		"type":   "oe",
		"prompt": "Explain story points.",
		"oe_payload": map[string]any{
			"model_answer": "They capture complexity + risk + effort rather than time.",
		},
	})

	// DRAFT atom — must be skipped (never published, so never listed/emitted).
	draft, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "draft atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New draft: %v", err)
	}
	if err := atomRepo.Save(context.Background(), draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Sanity — setup emitted nothing on the atomEventPublisher.
	if len(pub.events) != 0 {
		t.Fatalf("setup emitted %d events; want 0 before backfill", len(pub.events))
	}

	// Backfill — tenant via query param, NO auth headers (proves the
	// /api/internal/* path bypasses the X-Tenant-Id / gcid gate).
	req := httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published?tenant_id="+tenantA, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("backfill: status=%d body=%s", w.Code, w.Body.String())
	}

	// Exactly 2 events — the 2 published atoms; the draft is not emitted.
	if len(pub.events) != 2 {
		t.Fatalf("emitted %d events; want exactly 2 (the published MCQ + OE)", len(pub.events))
	}

	byAtom := map[string]atom.Event{}
	for _, ev := range pub.events {
		if ev.Type != atom.EventTypeAtomPublished {
			t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomPublished)
		}
		if ev.TenantID != tenantA {
			t.Errorf("event tenant_id = %q; want %q", ev.TenantID, tenantA)
		}
		byAtom[ev.AtomID] = ev
	}

	// MCQ — a real correct_option_id, answer_count=2, hasOE=false.
	mcqEv, ok := byAtom[mcqAtom.AtomID]
	if !ok {
		t.Fatalf("no event for MCQ atom %s", mcqAtom.AtomID)
	}
	// CHO-2255: ids are minted at persist, so the seeded wire id "opt_1" is
	// gone. The backfill's job is to re-emit the STORED key — assert that, not
	// a literal.
	if mcqEv.CorrectOptionID == "" || mcqEv.CorrectOptionID == "opt_1" {
		t.Errorf("MCQ correct_option_id = %q; want the minted id of the stored is_correct option", mcqEv.CorrectOptionID)
	}
	if mcqEv.AnswerCount != 2 {
		t.Errorf("MCQ answer_count = %d; want 2", mcqEv.AnswerCount)
	}
	if mcqEv.HasOpenEndedQuestion {
		t.Errorf("MCQ has_open_ended_question = true; want false")
	}

	// OE — hasOE=true, no MCQ answer key.
	oeEv, ok := byAtom[oeAtom.AtomID]
	if !ok {
		t.Fatalf("no event for OE atom %s", oeAtom.AtomID)
	}
	if !oeEv.HasOpenEndedQuestion {
		t.Errorf("OE has_open_ended_question = false; want true")
	}
	if oeEv.CorrectOptionID != "" {
		t.Errorf("OE correct_option_id = %q; want empty", oeEv.CorrectOptionID)
	}
	if oeEv.AnswerCount != 0 {
		t.Errorf("OE answer_count = %d; want 0", oeEv.AnswerCount)
	}

	// The DRAFT atom must NOT be emitted.
	if _, found := byAtom[draft.AtomID]; found {
		t.Errorf("draft atom %s was emitted; want skipped", draft.AtomID)
	}

	// Response counts.
	var resp struct {
		TenantID string `json:"tenant_id"`
		Emitted  int    `json:"emitted"`
		Skipped  int    `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if resp.Emitted != 2 {
		t.Errorf("response emitted = %d; want 2", resp.Emitted)
	}
	if resp.TenantID != tenantA {
		t.Errorf("response tenant_id = %q; want %q", resp.TenantID, tenantA)
	}
}

// TestBackfillPublished_EmitsQuestionlessAtoms_ButNeverOrphanEditions —
// the two skip rules are NOT the same rule, and conflating them was the bug.
//
//   - A published atom with NO QUESTION REVISION still EMITS. Its consent facts
//     (owner, reuse_visibility, published) are what chora-sharing's projection
//     needs; the question revision is enrichment. Skipping the emit left 19 of
//     118 live atoms unprojected and therefore un-reusable by anyone but their
//     author.
//   - A published ADR-229 A1 ORPHAN EDITION never emits. A projection row would
//     make it re-discoverable, violating "reachable only via repointed grants".
//
// The tenant is read from the JSON body here (not the query), exercising that path.
func TestBackfillPublished_EmitsQuestionlessAtoms_ButNeverOrphanEditions(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	// One PUBLISHED MCQ atom WITH a question → emitted.
	good := seedPublishedQuestionAtom(t, srv, atomRepo, "mcq", map[string]any{
		"type":   "mcq",
		"prompt": "2 + 2 = ?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "4", "is_correct": true, "explainer": "yes"},
				{"option_id": "opt_2", "label": "5", "is_correct": false, "explainer": "no"},
			},
		},
	})

	// A PUBLISHED atom with NO question revision. This used to be SKIPPED, and
	// that was the bug: 19 of 118 live published atoms are exactly this shape —
	// question_type on the learning_atoms row, revisioned in atom_revisions, but
	// no row in question_revisions. Skipping the emit left them with no
	// projection in chora-sharing, so AuthorizeAtomUse 412'd an atom it could not
	// see and NOBODY but the author could reuse them — including converting them
	// into a study list (ADR-233's primary route into the daily dose).
	//
	// A missing QUESTION revision is not a missing atom. It must now EMIT, so the
	// CONSENT FACTS (owner, reuse_visibility, published) reach the projection.
	// chora-sharing migration 0036 makes the question columns nullable for this.
	questionless, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "published atom, no question revision", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New questionless: %v", err)
	}
	questionless.Status = atom.StatusPublished
	if err := atomRepo.Save(context.Background(), questionless); err != nil {
		t.Fatalf("save questionless: %v", err)
	}

	// A REAL ADR-229 A1 orphan edition. This MUST still be skipped: a projection
	// row would make it re-discoverable, violating "reachable ONLY via repointed
	// grants". The previous test conflated these two — it named a merely
	// question-less atom "orphan" — which is part of how the defect survived.
	orphan, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "ADR-229 A1 orphan edition", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New orphan: %v", err)
	}
	orphan.Status = atom.StatusPublished
	orphan.OrphanedFromAtomID = good.AtomID // makes IsOrphan() true
	if err := atomRepo.Save(context.Background(), orphan); err != nil {
		t.Fatalf("save orphan: %v", err)
	}

	// Backfill — tenant in the JSON body (exercises the body-read path).
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published",
		bodyReader(map[string]any{"tenant_id": tenantA})))
	if w.Code != http.StatusOK {
		t.Fatalf("backfill: status=%d body=%s", w.Code, w.Body.String())
	}

	// The resolvable atom AND the question-less one emit. The orphan does not.
	if len(pub.events) != 2 {
		t.Fatalf("emitted %d events; want 2 (the resolvable atom + the question-less one). "+
			"A missing QUESTION revision must not withhold an atom's CONSENT FACTS", len(pub.events))
	}
	emittedIDs := map[string]bool{}
	for _, e := range pub.events {
		emittedIDs[e.AtomID] = true
	}
	if !emittedIDs[good.AtomID] {
		t.Errorf("the resolvable atom %q did not emit", good.AtomID)
	}
	if !emittedIDs[questionless.AtomID] {
		t.Errorf("the question-less atom %q did not emit — this is the CHO-2174 defect", questionless.AtomID)
	}
	if emittedIDs[orphan.AtomID] {
		t.Errorf("the ADR-229 A1 orphan edition %q EMITTED — a projection row would make it "+
			"re-discoverable, violating 'reachable only via repointed grants'", orphan.AtomID)
	}

	var resp struct {
		Emitted int `json:"emitted"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if resp.Emitted != 2 {
		t.Errorf("response emitted = %d; want 2", resp.Emitted)
	}
	if resp.Skipped != 1 {
		t.Errorf("response skipped = %d; want 1 (the orphan edition ONLY)", resp.Skipped)
	}
}

// bodyReader marshals v to a JSON request body reader.
func bodyReader(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// TestBackfillPublished_400_WhenTenantMissing — tenant_id is REQUIRED.
func TestBackfillPublished_400_WhenTenantMissing(t *testing.T) {
	t.Parallel()
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               inmem.NewAtomRepository(),
		AtomEventPublisher: &recordingPub{},
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (tenant required); body=%s", w.Code, w.Body.String())
	}
}

// TestBackfillPublished_500_WhenPublisherNotWired — the backfill REQUIRES the
// outbox publisher; a nil publisher is a 500 (fail-loud, never a silent no-op).
func TestBackfillPublished_500_WhenPublisherNotWired(t *testing.T) {
	t.Parallel()
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: inmem.NewAtomRepository(),
		// AtomEventPublisher intentionally nil.
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published?tenant_id="+tenantA, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (publisher not wired); body=%s", w.Code, w.Body.String())
	}
}

// TestBackfillPublished_ReemitSalt_SaltsIdempotencyKeys — CHO-2128 F1: with
// ?reemit_salt= the emitted events carry the SALTED deterministic key
// (atom:published:rev + ":reemit:" + salt) so the outbox UNIQUE index admits a
// re-run for events it already dedupes, and the response echoes the salt.
func TestBackfillPublished_ReemitSalt_SaltsIdempotencyKeys(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	a := seedPublishedQuestionAtom(t, srv, atomRepo, "mcq", map[string]any{
		"type":   "mcq",
		"prompt": "Salted?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "yes", "is_correct": true, "explainer": "y"},
				{"option_id": "opt_2", "label": "no", "is_correct": false, "explainer": "n"},
			},
		},
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published?tenant_id="+tenantA+"&reemit_salt=seed-2026-07-11", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("backfill: status=%d body=%s", w.Code, w.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("emitted %d events; want 1", len(pub.events))
	}
	ev := pub.events[0]
	wantKey := a.AtomID + ":published:" + ev.RevisionID + ":reemit:seed-2026-07-11"
	if ev.IdempotencyKey != wantKey {
		t.Errorf("idempotency_key = %q; want %q", ev.IdempotencyKey, wantKey)
	}

	var resp struct {
		Emitted    int    `json:"emitted"`
		ReemitSalt string `json:"reemit_salt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if resp.Emitted != 1 {
		t.Errorf("response emitted = %d; want 1", resp.Emitted)
	}
	if resp.ReemitSalt != "seed-2026-07-11" {
		t.Errorf("response reemit_salt = %q; want seed-2026-07-11", resp.ReemitSalt)
	}
}

// TestBackfillPublished_NoSalt_KeepsDeterministicKey — regression: without
// ?reemit_salt the key stays the plain deterministic atom:published:rev shape
// (the outbox dedupe contract for the normal publish path is untouched).
func TestBackfillPublished_NoSalt_KeepsDeterministicKey(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	a := seedPublishedQuestionAtom(t, srv, atomRepo, "mcq", map[string]any{
		"type":   "mcq",
		"prompt": "Plain?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "yes", "is_correct": true, "explainer": "y"},
				{"option_id": "opt_2", "label": "no", "is_correct": false, "explainer": "n"},
			},
		},
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/internal/atoms/backfill-published?tenant_id="+tenantA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("backfill: status=%d body=%s", w.Code, w.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("emitted %d events; want 1", len(pub.events))
	}
	ev := pub.events[0]
	if want := a.AtomID + ":published:" + ev.RevisionID; ev.IdempotencyKey != want {
		t.Errorf("idempotency_key = %q; want %q (unsalted)", ev.IdempotencyKey, want)
	}
}

// TestBackfillPublished_400_BlankOrInvalidReemitSalt — a PRESENT reemit_salt
// must be a sane token: present-but-blank, whitespace, over-long (>64) or
// non [A-Za-z0-9._-] salts are operator errors → fail-loud 400, nothing emits.
func TestBackfillPublished_400_BlankOrInvalidReemitSalt(t *testing.T) {
	t.Parallel()
	pub := &recordingPub{}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               inmem.NewAtomRepository(),
		QuestionRepository: newFakeQRepo(),
		AtomEventPublisher: pub,
	})

	for _, salt := range []string{"", "%20%20", strings.Repeat("x", 65), "bad%20salt", "col:on"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
			"/api/internal/atoms/backfill-published?tenant_id="+tenantA+"&reemit_salt="+salt, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("salt %q: status = %d; want 400", salt, w.Code)
		}
	}
	if len(pub.events) != 0 {
		t.Errorf("invalid salts emitted %d events; want 0", len(pub.events))
	}
}

// TestBackfillPublished_RejectsNonPost — only POST is supported.
func TestBackfillPublished_RejectsNonPost(t *testing.T) {
	t.Parallel()
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               inmem.NewAtomRepository(),
		AtomEventPublisher: &recordingPub{},
	})
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(m,
			"/api/internal/atoms/backfill-published?tenant_id="+tenantA, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: status %d; want 405", m, w.Code)
		}
	}
}
