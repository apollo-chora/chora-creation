// publish_atom_outbox_test.go — verifies POST /api/atoms/{atom_id}/publish
// emits a durable chora.creation.atom.published.v1 outbox event carrying
// answerability whenever it flips an atom DRAFT -> PUBLISHED.
//
// Contract (load-bearing): atom.created.v1 fires at draft-creation BEFORE a
// question exists, so it can never carry the MCQ answer key. Consumers
// (chora-consumption) therefore read answerability off atom.published.v1:
//   - MCQ:  correct_option_id (the is_correct option) + answer_count (#options)
//   - OE:   has_open_ended_question=true (answerable without an MCQ key)
//
// The emit MUST be idempotent on re-publish: the deterministic outbox
// idempotency_key (atom_id + ":published:" + revision_id) dedupes a repeat.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// TestPublishAtom_EmitsAtomPublishedOutboxEvent_MCQ — a DRAFT->PUBLISHED flip
// on an MCQ atom emits exactly one atom.published.v1 event with the MCQ
// answer key + deterministic idempotency key.
func TestPublishAtom_EmitsAtomPublishedOutboxEvent_MCQ(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "mcq publish atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	// Seed an MCQ question (opt_1 is the single correct option; 2 options).
	seedBody := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	revID, _ := resp["current_revision_id"].(string)
	if revID == "" {
		t.Fatalf("publish response missing current_revision_id; body=%s", w.Body.String())
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1 atom.published emit", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomPublished {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomPublished)
	}
	if ev.AtomID != a.AtomID {
		t.Errorf("event atom_id = %q; want %q", ev.AtomID, a.AtomID)
	}
	if ev.TenantID != tenantA {
		t.Errorf("event tenant_id = %q; want %q", ev.TenantID, tenantA)
	}
	// CHO-2255: ids are minted at persist — the wire id "opt_1" no longer
	// survives. Assert the durable invariant instead: the published key is a
	// real, non-empty id and is NOT the caller's.
	if ev.CorrectOptionID == "" || ev.CorrectOptionID == "opt_1" {
		t.Errorf("event correct_option_id = %q; want the minted id of the stored is_correct option", ev.CorrectOptionID)
	}
	if ev.AnswerCount != 2 {
		t.Errorf("event answer_count = %d; want 2", ev.AnswerCount)
	}
	if ev.HasOpenEndedQuestion {
		t.Errorf("event has_open_ended_question = true; want false for MCQ")
	}
	if ev.RevisionID != revID {
		t.Errorf("event revision_id = %q; want %q (current_revision_id)", ev.RevisionID, revID)
	}
	wantKey := a.AtomID + ":published:" + revID
	if ev.IdempotencyKey == "" {
		t.Errorf("event idempotency_key empty")
	}
	if ev.IdempotencyKey != wantKey {
		t.Errorf("event idempotency_key = %q; want %q (deterministic re-publish dedupe)", ev.IdempotencyKey, wantKey)
	}
}

// TestPublishAtom_EmitsAtomPublished_OE — an open-ended atom marks
// has_open_ended_question=true with no MCQ answer key.
func TestPublishAtom_EmitsAtomPublished_OE(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "oe publish atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	seedBody := map[string]any{
		"type":   "oe",
		"prompt": "Explain story points.",
		"oe_payload": map[string]any{
			"model_answer": "They capture complexity + risk + effort rather than time.",
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", w.Code, w.Body.String())
	}

	if len(pub.events) != 1 {
		t.Fatalf("captured events = %d; want exactly 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomPublished {
		t.Errorf("event type = %q; want %q", ev.Type, atom.EventTypeAtomPublished)
	}
	if !ev.HasOpenEndedQuestion {
		t.Errorf("event has_open_ended_question = false; want true for OE")
	}
	if ev.CorrectOptionID != "" {
		t.Errorf("event correct_option_id = %q; want empty for OE", ev.CorrectOptionID)
	}
	if ev.AnswerCount != 0 {
		t.Errorf("event answer_count = %d; want 0 for OE", ev.AnswerCount)
	}
}

// TestPublishAtom_IdempotentRepublish_DoesNotDoubleEmit — re-publishing an
// already-PUBLISHED atom MUST NOT emit a second distinct event. The handler
// short-circuits the idempotent re-publish before the emit; even if a future
// refactor emits on both, the deterministic idempotency_key MUST match so the
// outbox UNIQUE index dedupes it.
func TestPublishAtom_IdempotentRepublish_DoesNotDoubleEmit(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	pub := &recordingPub{}
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "idempotent publish atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		AtomEventPublisher: pub,
	})

	seedBody := map[string]any{
		"type":   "mcq",
		"prompt": "What is 2+2?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "4", "is_correct": true, "explainer": "2+2=4"},
				{"option_id": "opt_2", "label": "5", "is_correct": false, "explainer": "nope"},
			},
		},
	}
	seedW := httptest.NewRecorder()
	srv.ServeHTTP(seedW, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", seedBody))
	if seedW.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", seedW.Code, seedW.Body.String())
	}

	// First publish — real DRAFT->PUBLISHED transition emits exactly 1 event.
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first publish: status=%d body=%s", w1.Code, w1.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("after first publish captured = %d; want 1", len(pub.events))
	}
	firstKey := pub.events[0].IdempotencyKey

	// Second publish — idempotent (already PUBLISHED). MUST NOT double-emit.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("re-publish: status=%d body=%s", w2.Code, w2.Body.String())
	}
	switch len(pub.events) {
	case 1:
		// No new emit — the handler short-circuited the idempotent re-publish.
	case 2:
		// Tolerated ONLY if the dedupe key is identical (outbox UNIQUE index
		// drops the duplicate row before it reaches Pub/Sub).
		if pub.events[1].IdempotencyKey != firstKey {
			t.Errorf("re-publish emitted a NEW idempotency_key %q; want == first %q (dedupe contract)",
				pub.events[1].IdempotencyKey, firstKey)
		}
	default:
		t.Fatalf("captured events = %d after re-publish; want 1 (or 2 with identical dedupe key)", len(pub.events))
	}
}
