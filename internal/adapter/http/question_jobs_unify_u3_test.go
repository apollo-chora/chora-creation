// question_jobs_unify_u3_test.go — RED→GREEN for CHO-1826 U3 (unify
// single+batch authoring onto one atom-model).
//
// U3a — collapse the isBatch special case: EVERY accepted question mints
// its own LearningAtom (1q=1atom, the existing D2 law), EXCEPT the first
// accepted candidate reuses the FE-created "route" atom (the atom_id in
// the path) so a count=1 author does not strand an empty draft container.
// A route atom that already carries a non-deleted question (editing an
// existing atom) is NEVER reused — all candidates mint fresh atoms.
//
// Correctness: the reused route atom MUST receive a chora.creation.atom.created.v1
// (upsert) carrying the MCQ grading ground-truth (correct_option_id +
// answer_count), or chora-consumption mis-grades the first question.
package httpadapter_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// mcqDraft builds a candidate-draft map with a single correct option.
func mcqDraft(draftID, prompt, correctOptID string) map[string]any {
	return map[string]any{
		"draft_id": draftID,
		"type":     "mcq",
		"prompt":   prompt,
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": correctOptID, "label": "Right", "is_correct": true, "explainer": "yes"},
				{"option_id": correctOptID + "x", "label": "Wrong", "is_correct": false, "explainer": "no"},
			},
		},
	}
}

func countTopics(events []publishedEvent) map[string]int {
	out := map[string]int{}
	for _, ev := range events {
		out[ev.Topic]++
	}
	return out
}

// U3a — ai_draft with N>1 candidates: the legacy single-on-parent path
// 409'd on the 2nd accept (D2). Under the unified model each accepted
// question is its own atom, with the FIRST reusing the route atom.
func TestAcceptAIDraft_CountN_MintsOneAtomPerQuestion_ReuseRouteForFirst(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, qRepo, pub, routeAtomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	drafts := []map[string]any{
		mcqDraft("d1", "MCQ #1", "a1"),
		mcqDraft("d2", "MCQ #2", "b1"),
		mcqDraft("d3", "MCQ #3", "c1"),
	}
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: routeAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaActionCode: "question_authoring_ai_draft", ManaCharged: 10,
		CandidateQuestionsJSON: draftsJSON,
		CreatedAt:              time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{
		"accepted_candidates": []map[string]any{
			{"draft_id": "d1"}, {"draft_id": "d2"}, {"draft_id": "d3"},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (ai_draft N>1 must NOT 409 under 1q=1atom)", w.Code, w.Body.String())
	}

	var resp struct {
		Persisted []struct {
			QuestionID string `json:"question_id"`
			AtomID     string `json:"atom_id"`
		} `json:"persisted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid resp: %v body=%s", err, w.Body.String())
	}
	if len(resp.Persisted) != 3 {
		t.Fatalf("persisted len = %d; want 3", len(resp.Persisted))
	}
	// First reuses the route atom; the rest mint new atoms.
	if resp.Persisted[0].AtomID != routeAtomID {
		t.Errorf("persisted[0].atom_id = %q; want route atom %q (reuse-for-first)", resp.Persisted[0].AtomID, routeAtomID)
	}
	distinct := map[string]bool{}
	for i, p := range resp.Persisted {
		if p.AtomID == "" || p.QuestionID == "" {
			t.Errorf("persisted[%d] missing ids: %+v", i, p)
		}
		if i > 0 && p.AtomID == routeAtomID {
			t.Errorf("persisted[%d].atom_id reused route atom; want a NEW atom (1q=1atom)", i)
		}
		distinct[p.AtomID] = true
	}
	if len(distinct) != 3 {
		t.Errorf("distinct atom ids = %d; want 3", len(distinct))
	}
	// 3 questions persisted across 3 atoms.
	if len(qRepo.questions) != 3 {
		t.Errorf("qRepo questions = %d; want 3", len(qRepo.questions))
	}
	// One atom.created.v1 (incl. the route-atom upsert) + one question.authored.v1 per question.
	tc := countTopics(pub.snapshot())
	if tc["chora.creation.atom.created.v1"] != 3 {
		t.Errorf("atom.created.v1 = %d; want 3 (incl. route-atom upsert)", tc["chora.creation.atom.created.v1"])
	}
	if tc["chora.creation.question.authored.v1"] != 3 {
		t.Errorf("question.authored.v1 = %d; want 3", tc["chora.creation.question.authored.v1"])
	}
}

// U3a correctness — the reused route atom's atom.created.v1 carries the
// MCQ grading ground-truth (else consumption mis-grades the first question).
func TestAcceptAIDraft_RouteAtomCarriesGradingKey(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, pub, routeAtomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	draftsJSON, _ := json.Marshal([]map[string]any{mcqDraft("d1", "what is X?", "a1")})
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: routeAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}

	var found bool
	for _, ev := range pub.snapshot() {
		if ev.Topic != "chora.creation.atom.created.v1" {
			continue
		}
		p, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("atom.created payload type = %T; want map[string]any", ev.Payload)
		}
		if p["atom_id"] != routeAtomID {
			continue
		}
		found = true
		// CHO-2272 — the grading key is the DERIVED served id of the correct
		// option's display content (label "Right" in mcqDraft), NOT the stored
		// wire id "a1". This is what the learner is served + graded on.
		wantKey, kerr := (&question.MCQPayload{Options: []question.MCQOption{
			{Label: "Right", IsCorrect: true},
			{Label: "Wrong", IsCorrect: false},
		}}).ServedCorrectOptionID()
		if kerr != nil {
			t.Fatalf("compute expected derived key: %v", kerr)
		}
		if p["correct_option_id"] != wantKey {
			t.Errorf("route-atom atom.created correct_option_id = %v; want the derived served id %q (not the stored wire id)", p["correct_option_id"], wantKey)
		}
		if p["correct_option_id"] == "a1" {
			t.Errorf("route-atom atom.created leaked the stored wire id a1")
		}
		if ac, _ := p["answer_count"].(int32); ac != 2 {
			t.Errorf("route-atom atom.created answer_count = %v; want 2", p["answer_count"])
		}
	}
	if !found {
		t.Fatalf("no atom.created.v1 emitted for the route atom %q (grading key would be missing)", routeAtomID)
	}
}

// U3a gate — editing an existing atom (route atom already has a question):
// NONE of the newly accepted candidates may reuse it; all mint fresh atoms.
func TestAcceptAIDraft_OccupiedRouteAtom_AllCandidatesMintNew(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, qRepo, _, routeAtomID := newJobsServer(t, mana)

	// Seed an existing non-deleted question on the route atom.
	seedQ := &question.Question{
		QuestionID: uuid.NewString(), AtomID: routeAtomID, TenantID: tenantA,
		Type: question.TypeMCQ, Prompt: "pre-existing", CreatedAt: time.Now().UTC(),
	}
	qRepo.questions[seedQ.QuestionID] = seedQ
	qRepo.revisions[seedQ.QuestionID] = []*question.QuestionRevision{{
		RevisionID: uuid.NewString(), QuestionID: seedQ.QuestionID, RevisionNumber: 1,
	}}

	jid := uuid.NewString()
	draftsJSON, _ := json.Marshal([]map[string]any{
		mcqDraft("d1", "MCQ #1", "a1"), mcqDraft("d2", "MCQ #2", "b1"),
	})
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: routeAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 10, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}, {"draft_id": "d2"}}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (occupied route atom → mint fresh, no 409)", w.Code, w.Body.String())
	}
	var resp struct {
		Persisted []struct {
			AtomID string `json:"atom_id"`
		} `json:"persisted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Persisted) != 2 {
		t.Fatalf("persisted = %d; want 2", len(resp.Persisted))
	}
	for i, p := range resp.Persisted {
		if p.AtomID == routeAtomID {
			t.Errorf("persisted[%d] reused the occupied route atom %q; want a fresh atom", i, routeAtomID)
		}
	}
	// The pre-existing question is untouched: 1 seed + 2 new = 3.
	if len(qRepo.questions) != 3 {
		t.Errorf("qRepo questions = %d; want 3 (seed + 2 new)", len(qRepo.questions))
	}
}

// seedAIDraftJob wires a succeeded ai_draft job with the given candidate
// drafts on the route atom.
func seedAIDraftJob(jobRepo *fakeJobRepo, routeAtomID string, drafts []map[string]any) string {
	jid := uuid.NewString()
	draftsJSON, _ := json.Marshal(drafts)
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: routeAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{Prompt: "p"}, Status: question.JobStatusSucceeded,
		ManaCharged: 0, CandidateQuestionsJSON: draftsJSON,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	return jid
}

func debitItemCounts(reqs []ports.DeductManaReq) map[string]string {
	out := map[string]string{}
	for _, dr := range reqs {
		out[dr.ActionCode] = dr.Context["item_count"]
	}
	return out
}

// U3b — accept-time per-question pricing: base 10 per text AI question, 20
// per AI question carrying any image (stem and/or answer). One debit per
// action code (the price-plan layer multiplies by item_count).
func TestAccept_PerQuestionPricing_TextAndImage(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, _, routeAtomID := newJobsServer(t, mana)

	d3 := mcqDraft("d3", "Q3 (with stem image)", "c1")
	d3["image_url"] = "gs://chora-ai-assist-images-dev/tenants/t/jobs/j/d3-stem.png"
	jid := seedAIDraftJob(jobRepo, routeAtomID, []map[string]any{
		mcqDraft("d1", "Q1", "a1"),
		mcqDraft("d2", "Q2", "b1"),
		d3,
	})

	body := map[string]any{"accepted_candidates": []map[string]any{
		{"draft_id": "d1"}, {"draft_id": "d2"}, {"draft_id": "d3"},
	}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp struct {
		ManaDebited int `json:"mana_debited"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	// 2 text × 10 + 1 image × 20 = 40.
	if resp.ManaDebited != 40 {
		t.Errorf("mana_debited = %d; want 40 (2×10 text + 1×20 image)", resp.ManaDebited)
	}
	counts := debitItemCounts(mana.deductReqs)
	if counts["question_authoring_generate"] != "2" {
		t.Errorf("generate item_count = %q; want 2", counts["question_authoring_generate"])
	}
	if counts["question_authoring_generate_image"] != "1" {
		t.Errorf("generate_image item_count = %q; want 1", counts["question_authoring_generate_image"])
	}
	// No legacy upfront/per-item codes charged.
	if _, ok := counts["question_authoring_batch_per_item"]; ok {
		t.Errorf("legacy per_item code charged; want only generate/_image")
	}
}

func TestAccept_PerQuestionPricing_AllText_OneDebit(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, _, routeAtomID := newJobsServer(t, mana)
	jid := seedAIDraftJob(jobRepo, routeAtomID, []map[string]any{
		mcqDraft("d1", "Q1", "a1"), mcqDraft("d2", "Q2", "b1"), mcqDraft("d3", "Q3", "c1"),
	})
	body := map[string]any{"accepted_candidates": []map[string]any{
		{"draft_id": "d1"}, {"draft_id": "d2"}, {"draft_id": "d3"},
	}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp struct {
		ManaDebited int `json:"mana_debited"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ManaDebited != 30 {
		t.Errorf("mana_debited = %d; want 30 (3×10)", resp.ManaDebited)
	}
	// All-text ⇒ exactly one debit (generate, item_count=3); no image debit.
	if mana.deductCalls != 1 {
		t.Errorf("deduct calls = %d; want 1 (single generate debit)", mana.deductCalls)
	}
	if debitItemCounts(mana.deductReqs)["question_authoring_generate"] != "3" {
		t.Errorf("generate item_count = %q; want 3", debitItemCounts(mana.deductReqs)["question_authoring_generate"])
	}
}

// U3b — insufficient mana 402s at accept BEFORE any atom/question is persisted.
func TestAccept_InsufficientMana_402_NoPersist(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{insufficient: true}
	srv, jobRepo, qRepo, pub, routeAtomID := newJobsServer(t, mana)
	jid := seedAIDraftJob(jobRepo, routeAtomID, []map[string]any{
		mcqDraft("d1", "Q1", "a1"), mcqDraft("d2", "Q2", "b1"),
	})
	body := map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}, {"draft_id": "d2"}}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d body=%s; want 402", w.Code, w.Body.String())
	}
	if len(qRepo.questions) != 0 {
		t.Errorf("questions persisted = %d; want 0 (charge fails before persistence)", len(qRepo.questions))
	}
	for _, ev := range pub.snapshot() {
		if ev.Topic == "chora.creation.question.authored.v1" {
			t.Errorf("question.authored emitted on 402; want none")
		}
	}
}

// U3b — a persistence failure after charging refunds the mana (defer guard).
func TestAccept_PersistFailure_RefundsCharge(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, qRepo, _, routeAtomID := newJobsServer(t, mana)
	qRepo.saveErr = errors.New("pg down")
	jid := seedAIDraftJob(jobRepo, routeAtomID, []map[string]any{
		mcqDraft("d1", "Q1", "a1"), mcqDraft("d2", "Q2", "b1"),
	})
	body := map[string]any{"accepted_candidates": []map[string]any{{"draft_id": "d1"}, {"draft_id": "d2"}}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code < 500 {
		t.Fatalf("status = %d; want 5xx on persist failure", w.Code)
	}
	if mana.refundCalls < 1 {
		t.Errorf("refund called %d times; want ≥1 (charge refunded on persist failure)", mana.refundCalls)
	}
	if mana.lastRefundReq.ActionCode != "question_authoring_generate" {
		t.Errorf("refund action = %q; want question_authoring_generate", mana.lastRefundReq.ActionCode)
	}
}

// inlineManualMCQ builds an accept-body candidate with NO draft_id — an inline
// manually-authored question (U3c).
func inlineManualMCQ(prompt, correctOptID string) map[string]any {
	return map[string]any{
		"type":            "mcq",
		"prompt_override": prompt,
		"mcq_payload_override": map[string]any{"options": []map[string]any{
			{"option_id": correctOptID, "label": "Right", "is_correct": true, "explainer": "correct because"},
			{"option_id": correctOptID + "x", "label": "Wrong", "is_correct": false, "explainer": "wrong because"},
		}},
	}
}

// U3c — a manual authoring session is a manual_draft job: created already
// 'succeeded' (no LLM dispatch), free, immediately acceptable.
func TestCreateManualJob_SucceededNoCharge(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, pub, atomID := newJobsServer(t, mana)

	body := map[string]any{"type": "manual"}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+atomID+"/question-jobs", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["status"] != "succeeded" {
		t.Errorf("status = %v; want succeeded (manual job is immediately acceptable)", got["status"])
	}
	jid, _ := got["job_id"].(string)
	if jid == "" {
		t.Fatal("job_id missing")
	}
	if mana.deductCalls != 0 {
		t.Errorf("DeductMana called %d; want 0 (manual generation is free)", mana.deductCalls)
	}
	if j := jobRepo.jobs[jid]; j == nil || j.Input.Kind() != question.InputByHand {
		t.Errorf("input kind = %v; want by_hand", jobRepo.jobs[jid])
	}
	for _, ev := range pub.snapshot() {
		if ev.Topic == "chora.creation.question.generation_requested.v2" {
			t.Errorf("manual job published generation_requested; want none (no orchestrator dispatch)")
		}
	}
}

// U3c — accepting a pure-manual job: all candidates inline (no draft_id),
// minted as atoms, charged nothing, source_type=manual.
func TestAccept_PureManualJob_AllInline_Free(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, pub, routeAtomID := newJobsServer(t, mana)

	jid := uuid.NewString()
	jobRepo.jobs[jid] = &question.ComposeJob{
		JobID: jid, AtomID: routeAtomID, TenantID: tenantA, AuthorGCID: gcidA,
		Intent: question.IntentNewQuestion, Input: question.Input{ByHand: true}, Status: question.JobStatusSucceeded,
		ManaCharged: 0, CandidateQuestionsJSON: []byte("[]"),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	body := map[string]any{"accepted_candidates": []map[string]any{
		inlineManualMCQ("Manual Q1", "a"),
		{"type": "oe", "prompt_override": "Manual Q2 essay", "oe_payload_override": map[string]any{"model_answer": "the answer"}},
	}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Persisted []struct {
			AtomID string `json:"atom_id"`
		} `json:"persisted"`
		ManaDebited int `json:"mana_debited"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Persisted) != 2 {
		t.Fatalf("persisted = %d; want 2", len(resp.Persisted))
	}
	if resp.ManaDebited != 0 {
		t.Errorf("mana_debited = %d; want 0 (manual is free)", resp.ManaDebited)
	}
	if mana.deductCalls != 0 {
		t.Errorf("deduct calls = %d; want 0 (manual is free)", mana.deductCalls)
	}
	var sawAuthored bool
	for _, ev := range pub.snapshot() {
		if ev.Topic == "chora.creation.question.authored.v1" {
			sawAuthored = true
			p, _ := ev.Payload.(map[string]any)
			if p["source_type"] != "manual" {
				t.Errorf("question.authored source_type = %v; want manual", p["source_type"])
			}
		}
	}
	if !sawAuthored {
		t.Error("no question.authored.v1 emitted")
	}
}

// U3c — interleave: one accept persists a MIX of AI drafts (charged) and an
// inline manual question (free), each its own atom.
func TestAccept_Interleave_AIAndManual_ChargesOnlyAI(t *testing.T) {
	t.Parallel()
	mana := &fakeManaLedger{successDebits: 10}
	srv, jobRepo, _, _, routeAtomID := newJobsServer(t, mana)
	jid := seedAIDraftJob(jobRepo, routeAtomID, []map[string]any{
		mcqDraft("d1", "AI Q1", "a1"), mcqDraft("d2", "AI Q2", "b1"),
	})

	body := map[string]any{"accepted_candidates": []map[string]any{
		{"draft_id": "d1"},
		inlineManualMCQ("Manual inserted between AI", "m1"),
		{"draft_id": "d2"},
	}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJobJSON(http.MethodPost, "/api/atoms/"+routeAtomID+"/question-jobs/"+jid+"/accept", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Persisted []struct {
			AtomID string `json:"atom_id"`
		} `json:"persisted"`
		ManaDebited int `json:"mana_debited"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Persisted) != 3 {
		t.Fatalf("persisted = %d; want 3 (2 AI + 1 manual)", len(resp.Persisted))
	}
	// Only the 2 AI drafts are charged (2 × 10); the inline manual is free.
	if resp.ManaDebited != 20 {
		t.Errorf("mana_debited = %d; want 20 (2 AI text × 10; manual free)", resp.ManaDebited)
	}
	if c := debitItemCounts(mana.deductReqs)["question_authoring_generate"]; c != "2" {
		t.Errorf("generate item_count = %q; want 2 (only AI)", c)
	}
}
