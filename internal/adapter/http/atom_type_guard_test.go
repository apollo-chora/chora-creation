// atom_type_guard_test.go — CHO-2178: the write boundary must refuse a
// question_type the event wire cannot carry.
//
// THE DEFECT THIS PINS. `learning_atoms.question_type` is a free-text varchar
// with NO check constraint, and resolveQuestionType's default arm lowercased
// any unrecognised string and passed it straight through. So POSTing
// atom_type:"banana" minted a perfectly good atom, returned 201, and the atom
// then failed its outbox marshal forever — published, invisible, unreusable by
// anyone but its author, with not one error surfaced to the caller. That is the
// defect CLASS behind CHO-2178: the DB was more permissive than the contract,
// and the gap was discovered only when someone counted the projections.
//
// The guard closes the class at the boundary where it can still be reported: a
// flavour the domain does not declare is a 400 at authoring time, not a silent
// stranding at publish time.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateAtom_RejectsUnrepresentableQuestionType — the core guard. Each of
// these once minted a stranded or domain-invalid atom with a 201.
func TestCreateAtom_RejectsUnrepresentableQuestionType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value string
		why   string
	}{
		{"arbitrary garbage", "banana",
			"lowercased straight into the varchar; outbox marshal then failed forever"},
		{"withdrawn OpenAPI value TRUE_FALSE", "TRUE_FALSE",
			"the domain calls it invalid; it was still accepted and persisted"},
		{"withdrawn OpenAPI value SIMULATION", "SIMULATION",
			"same — advertised by the old enum, unrepresentable in the domain"},
		{"proto constant leaking through", "ATOM_TYPE_CODE",
			"a wire constant is not a domain flavour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
				"question_type": tc.value,
				"title":         "Guard probe",
				"locale":        "en",
				"stem":          "Placeholder stem",
			}))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("question_type=%q -> status %d, want 400.\n  why it matters: %s\n  body=%s",
					tc.value, w.Code, tc.why, w.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("json: %v", err)
			}
			if got["code"] != "CREATION_ATOM_TYPE_UNSUPPORTED" {
				t.Errorf("code = %v; want CREATION_ATOM_TYPE_UNSUPPORTED (an actionable code — "+
					"the caller must be told WHICH field it got wrong)", got["code"])
			}
		})
	}
}

// TestCreateAtom_AcceptsEveryRepresentableQuestionType — the guard must not
// over-reach. Every alias the A+/R+ authoring UIs actually send still works,
// and OUTLINE — which the old OpenAPI enum could not even express — is now
// first-class.
func TestCreateAtom_AcceptsEveryRepresentableQuestionType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		send string
		want string
	}{
		{"MULTIPLE_CHOICE", "mcq"},  // A+ / R+ authoring send this
		{"SHORT_ANSWER", "essay"},   // A+ / R+ authoring send this
		{"ESSAY", "essay"},          //
		{"FILL_BLANK", "flashcard"}, //
		{"MULTIMEDIA", "video"},     //
		{"OUTLINE", "outline"},      // NEW — was undeclarable via the API
		{"mcq", "mcq"},              // lowercase domain labels accepted
		{"outline", "outline"},      //
		{"flashcard", "flashcard"},  //
		{"video", "video"},          //
		{"essay", "essay"},          //
	} {
		t.Run(tc.send, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
				"question_type": tc.send,
				"title":         "Representable",
				"locale":        "en",
				"stem":          "Placeholder stem",
			}))
			if w.Code != http.StatusCreated {
				t.Fatalf("question_type=%q -> status %d, want 201; body=%s",
					tc.send, w.Code, w.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("json: %v", err)
			}
			if got["question_type"] != tc.want {
				t.Errorf("question_type = %v; want %q", got["question_type"], tc.want)
			}
		})
	}
}

// TestCreateAtom_AbsentQuestionTypeStillAllowed — "unset" is not "invalid".
// One published atom in prod carries an empty question_type and it projects
// fine (as UNSPECIFIED); the guard must not retroactively outlaw that shape.
func TestCreateAtom_AbsentQuestionTypeStillAllowed(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title":  "No type at all",
		"locale": "en",
		"stem":   "Placeholder stem",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("absent question_type -> status %d, want 201; body=%s", w.Code, w.Body.String())
	}
}
