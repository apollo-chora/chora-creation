package httpadapter_test

// questions_handler_event_test.go — the MANUAL question create/edit path must
// emit the chora.creation.atom.created.v1 upsert carrying the MCQ grading
// ground-truth (correct_option_id + answer_count), exactly like the qgen
// accept path does (CHO-1627). Without it, atom_index never learns the answer
// key for manually-authored MCQs and every atomic-session submit grades
// is_correct=false (found live in the L4 walk, 2026-06-10).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const testCourseID = "01970000-0000-7000-c000-000000000001"

// newQuestionServerWithPublisher mirrors newQuestionServer but wires a
// recording JobEventPublisher + binds the seed atom to a course (the upsert
// must carry course_id or it would WIPE the binding in atom_index).
func newQuestionServerWithPublisher(t *testing.T) (http.Handler, *fakeJobPublisher, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()

	a, err := atom.New(atom.NewParams{
		TenantID: tenantA,
		Gcid:     gcidA,
		Title:    "Course-bound MCQ atom",
		Body:     "Body",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	a.QuestionType = atom.AtomType("mcq")
	a.CourseID = testCourseID
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save seed atom: %v", err)
	}

	pub := &fakeJobPublisher{}
	router := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		JobEventPublisher:  pub,
	})
	return router, pub, a.AtomID
}

func mcqCreateBody(correctOption string) map[string]any {
	return map[string]any{
		"type":   "mcq",
		"prompt": "Which is a Scrum role?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "Product Owner", "is_correct": correctOption == "opt_1", "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "Project Manager", "is_correct": correctOption == "opt_2", "explainer": "Not a Scrum role"},
			},
		},
	}
}

func atomCreatedEvents(pub *fakeJobPublisher) []map[string]any {
	out := []map[string]any{}
	for _, e := range pub.snapshot() {
		if e.Topic != "chora.creation.atom.created.v1" {
			continue
		}
		if m, ok := e.Payload.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestCreateQuestion_MCQ_EmitsAtomUpsertWithAnswerKey(t *testing.T) {
	t.Parallel()
	srv, pub, atomID := newQuestionServerWithPublisher(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", mcqCreateBody("opt_1")))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}

	events := atomCreatedEvents(pub)
	if len(events) != 1 {
		t.Fatalf("atom.created upserts = %d, want exactly 1 (the manual path must backfill the answer key like qgen does)", len(events))
	}
	p := events[0]
	if p["atom_id"] != atomID {
		t.Errorf("atom_id = %v, want %s", p["atom_id"], atomID)
	}
	// CHO-2255: option ids are MINTED at persist, so the wire id "opt_1" is
	// gone. The invariant that actually matters is unchanged and is what this
	// asserts: the key on the event must be the id that was STORED — that is
	// what atom_index grades against, and a drift there mis-grades every take.
	gotKey, _ := p["correct_option_id"].(string)
	if gotKey == "" || gotKey == "opt_1" {
		t.Errorf("correct_option_id = %v; want the minted id of the stored is_correct option (never the wire id)", p["correct_option_id"])
	}
	if fmt.Sprintf("%v", p["answer_count"]) != "2" {
		t.Errorf("answer_count = %v, want 2", p["answer_count"])
	}
	if p["course_id"] != testCourseID {
		t.Errorf("course_id = %v, want %s (omitting it would wipe the course binding in atom_index)", p["course_id"], testCourseID)
	}
	if p["atom_type"] != "mcq" {
		t.Errorf("atom_type = %v, want mcq", p["atom_type"])
	}
}

func TestPatchQuestion_MCQ_ReEmitsAnswerKeyOnEdit(t *testing.T) {
	t.Parallel()
	srv, pub, atomID := newQuestionServerWithPublisher(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", mcqCreateBody("opt_1")))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Question struct {
			QuestionID string `json:"question_id"`
		} `json:"question"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// Flip the correct option — the edit must re-emit the new key.
	patch := map[string]any{
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "Product Owner", "is_correct": false, "explainer": "Flipped for the edit test"},
				{"option_id": "opt_2", "label": "Project Manager", "is_correct": true, "explainer": "Flipped for the edit test"},
			},
		},
	}
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodPatch, "/api/atoms/"+atomID+"/questions/"+created.Question.QuestionID, patch))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w2.Code, w2.Body.String())
	}

	// CHO-2272 — the re-emitted grading key is the DERIVED served id of the
	// correct option's display content ("Project Manager"), NOT the stored id and
	// NOT the caller's wire id "opt_2". This is exactly what the learner is served
	// and graded on; a stale or wrong key mis-grades every subsequent take.
	wantKey, kerr := (&question.MCQPayload{Options: []question.MCQOption{
		{Label: "Product Owner", IsCorrect: false},
		{Label: "Project Manager", IsCorrect: true},
	}}).ServedCorrectOptionID()
	if kerr != nil {
		t.Fatalf("compute expected derived key: %v", kerr)
	}

	events := atomCreatedEvents(pub)
	if len(events) != 2 {
		t.Fatalf("atom.created upserts = %d, want 2 (create + edit)", len(events))
	}
	if got := events[1]["correct_option_id"]; got != wantKey {
		t.Errorf("edited correct_option_id = %v, want the derived served id %q of the flipped-correct option (stale answer keys mis-grade every subsequent take)", got, wantKey)
	}
	if got := events[1]["correct_option_id"]; got == "opt_2" {
		t.Errorf("edited correct_option_id leaked the caller wire id opt_2")
	}
}

// CHO-2272 — the full HTTP round-trip: GET /api/atoms/{id} serves DERIVED option
// ids (never the stored id or order), and the grading key emitted on
// atom.created.v1 is one of those served ids — so a learner who picks the correct
// option submits exactly what grades correct. This is the end-to-end proof that
// served == emitted == graded across the serve door and the event.
func TestGetAtom_ServesDerivedIDs_RoundTripToGradingKey(t *testing.T) {
	t.Parallel()
	srv, pub, atomID := newQuestionServerWithPublisher(t)

	// The live legacy shape: answer typed FIRST, ids that name/position the key.
	body := map[string]any{
		"type": "mcq", "prompt": "capital of France?",
		"mcq_payload": map[string]any{"options": []map[string]any{
			{"option_id": "opt_correct", "label": "Paris", "is_correct": true, "explainer": "yes"},
			{"option_id": "opt_1", "label": "London", "is_correct": false, "explainer": "no"},
			{"option_id": "opt_2", "label": "Rome", "is_correct": false},
			{"option_id": "opt_3", "label": "Berlin", "is_correct": false},
		}},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	events := atomCreatedEvents(pub)
	if len(events) == 0 {
		t.Fatalf("no atom.created.v1 emitted on create")
	}
	emittedKey, _ := events[len(events)-1]["correct_option_id"].(string)
	if emittedKey == "" {
		t.Fatalf("no correct_option_id on the emitted event")
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get atom: %d %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	mcq, ok := got["mcq_payload"].(map[string]any)
	if !ok {
		t.Fatalf("no mcq_payload in GET: %s", w2.Body.String())
	}
	rawOpts, _ := mcq["options"].([]any)
	if len(rawOpts) != 4 {
		t.Fatalf("served %d options; want 4", len(rawOpts))
	}

	hex16 := regexp.MustCompile(`^[0-9a-f]{16}$`)
	served := map[string]bool{}
	prev := ""
	for i, ro := range rawOpts {
		o := ro.(map[string]any)
		id, _ := o["option_id"].(string)
		if !hex16.MatchString(id) {
			t.Errorf("served option[%d] id %q is not a 16-hex derived id — a stored id leaked", i, id)
		}
		if low := strings.ToLower(id); strings.Contains(low, "opt_") || strings.Contains(low, "correct") {
			t.Errorf("served option[%d] id %q carries stored-id signal", i, id)
		}
		if ic, present := o["is_correct"]; present && ic == true {
			t.Errorf("served option[%d] leaked is_correct=true", i)
		}
		served[id] = true
		// Deterministic-shuffle property: served options are ordered by their id.
		if prev != "" && id < prev {
			t.Errorf("served options are not ordered by id (not a deterministic shuffle): %q came before %q", prev, id)
		}
		prev = id
	}
	// The round-trip: the emitted grading key IS one of the served ids.
	if !served[emittedKey] {
		t.Errorf("emitted grading key %q is not among the served option ids %v — honest submissions would grade WRONG", emittedKey, served)
	}
}
