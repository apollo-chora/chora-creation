// collection_error_mapping_test.go — CHO-2175: writeCollectionError's default arm.
//
// THE DEFECT THIS PINS. CHO-2174a gave the four chora-sharing verdicts their true
// statuses. Everything else still fell into one arm:
//
//	default:
//	    log.Printf(...)
//	    writeError(w, http.StatusBadRequest, "CREATION_COLLECTION_INVALID", err.Error())
//
// So a DATABASE fault, a context timeout, an unwired port — anything the switch
// did not name — came back as 400 "your request was invalid", with the raw
// internal error pasted into the message. Three things are wrong with that:
//
//  1. It BLAMES THE CALLER for our fault. A 400 says "fix your request"; the
//     caller cannot fix our database. They retry a "corrected" request forever.
//  2. It HIDES OUTAGES. 4xx is client error — no alert fires, no dashboard reddens,
//     no retry policy engages. CHO-2173 is the proof: AddAtom had NEVER worked in
//     prod, surfacing a raw `SQLSTATE 22P02` as a 400, and it hid for months
//     precisely because the status said the users were holding it wrong.
//  3. It LEAKS INTERNALS — the same leak CHO-2174a closed on the gRPC path, still
//     wide open on every other path.
//
// Worst of all: the service carefully REFUSES when a consent port is unwired
// ("an unwired gate must never pass") — and that refusal was then reported as a
// 400. A misconfigured deployment read as user error and paged nobody.
//
// The fix inverts the default. Only a genuinely-malformed request may be a 400,
// and it must say so explicitly; an unrecognised error is OUR bug and must be a
// loud 5xx. The consent gate must never degrade open, and must never degrade
// into blaming the person it refused.
package httpadapter_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
)

// dbFault is the exact shape CHO-2173 wore in prod: a pgx error, wrapped by the
// repository, arriving at the handler with nothing to distinguish it from a
// user's typo.
func dbFault() error {
	return fmt.Errorf("pg.CollectionRepository.ReuseFacts: %w",
		errors.New(`ERROR: invalid input syntax for type uuid: "" (SQLSTATE 22P02)`))
}

// assertNoDBInternalsLeaked — a 5xx tells the caller we broke. It does not hand
// them our SQLSTATE, our table names, or our call chain.
func assertNoDBInternalsLeaked(t *testing.T, body string) {
	t.Helper()
	for _, leak := range []string{
		"SQLSTATE", "22P02", "pg.CollectionRepository", "invalid input syntax",
		"collection.Service", "reuse facts",
	} {
		if jsonContains(body, leak) {
			t.Errorf("5xx response leaks internals (%q) to the caller: %s", leak, body)
		}
	}
}

// TestConvertToStudyList_ReuseFactsDBFaultIs5xxNotAnUnactionable400 — the ticket.
// The consent gate could not be EVALUATED because our database failed. That is
// not a malformed request.
func TestConvertToStudyList_ReuseFactsDBFaultIs5xxNotAnUnactionable400(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	a1 := "01970000-0000-7000-a000-00000000d001"
	id := rig.seedCollection(t, collOwner, "private", a1)

	// The DB goes down AFTER curation — the gate read is the next thing to touch it.
	rig.facts.mu.Lock()
	rig.facts.err = dbFault()
	rig.facts.mu.Unlock()

	w := rig.convert(t, id, collOwner)

	if w.Code < 500 {
		t.Fatalf("status = %d, want 5xx.\n"+
			"    A database fault is OUR failure. A 4xx tells the caller to fix a request they "+
			"cannot fix, and tells our monitoring that nothing is wrong — which is exactly how "+
			"CHO-2173 (AddAtom NEVER worked) hid in prod for months.\n    body=%s",
			w.Code, w.Body.String())
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (our datastore, our fault). 502 is reserved for a "+
			"failing UPSTREAM SERVICE (CREATION_SHARING_UNAVAILABLE) — conflating the two sends "+
			"an operator to debug chora-sharing, which is innocent.", w.Code)
	}
	env := decodeEnvelope(t, w)
	if !hasErrorCode(env, "CREATION_COLLECTION_REPO_ERROR") {
		t.Errorf("code = %v; want CREATION_COLLECTION_REPO_ERROR — a DISTINCT, actionable code. "+
			"CREATION_COLLECTION_INVALID would say the collection was bad; it was not.", env["code"])
	}
	assertNoDBInternalsLeaked(t, w.Body.String())

	// The gate must never degrade open: no grant, no study list.
	if n := rig.convertedEvents(); n != 0 {
		t.Errorf("converted event published %d× despite the gate being unevaluable — "+
			"the gate degraded OPEN", n)
	}
}

// TestAddAtom_ReuseFactsDBFaultIs5xxNotAnUnactionable400 — the same fault on the
// add-gate. This is the CHO-2173 path itself.
func TestAddAtom_ReuseFactsDBFaultIs5xxNotAnUnactionable400(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	id := rig.seedCollection(t, collOwner, "private")

	rig.facts.mu.Lock()
	rig.facts.err = dbFault()
	rig.facts.mu.Unlock()

	w := httptest.NewRecorder()
	rig.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+id+"/atoms",
		map[string]any{"atom_id": "01970000-0000-7000-a000-00000000d002"}, collOwner))

	if w.Code < 500 {
		t.Fatalf("status = %d, want 5xx — this is the literal CHO-2173 shape "+
			"(raw SQLSTATE 22P02 returned as a 400). body=%s", w.Code, w.Body.String())
	}
	env := decodeEnvelope(t, w)
	if !hasErrorCode(env, "CREATION_COLLECTION_REPO_ERROR") {
		t.Errorf("code = %v; want CREATION_COLLECTION_REPO_ERROR", env["code"])
	}
	assertNoDBInternalsLeaked(t, w.Body.String())
}

// TestAddAtom_UnwiredGateIs500NotAClientError — the REAL unwired path: the
// consent port is genuinely nil, exactly as a misconfigured rollout would leave
// it. The service already refuses ("an unwired gate must never pass"); the bug
// was that the refusal reached the caller as 400 "your request was invalid", so
// a broken deployment read as a user typing badly and paged nobody.
//
// Wiring the fake to *return* the not-wired message would test the fake. Leaving
// the port nil tests the system.
func TestAddAtom_UnwiredGateIs500NotAClientError(t *testing.T) {
	t.Parallel()

	// A router whose consent gate was never wired — CollectionFacts is absent.
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                 inmem.NewAtomRepository(),
		CollectionRepository: inmem.NewCollectionRepository(),
		CollectionPublisher:  &recPub{},
		// CollectionFacts:   (not wired)
		CollectionConsent: &httpConsent{},
		CollectionAuthz:   &httpAuthz{},
	})

	// Create succeeds — it does not touch the gate.
	cw := httptest.NewRecorder()
	srv.ServeHTTP(cw, collReq(http.MethodPost, "/api/v1/collections",
		map[string]any{"title": "Drill", "visibility": "private"}, collOwner))
	if cw.Code != http.StatusCreated {
		t.Fatalf("create: status = %d body=%s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(cw.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id, _ := created["collection_id"].(string)

	// Adding an atom DOES touch the gate, and the gate is not there.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections/"+id+"/atoms",
		map[string]any{"atom_id": "01970000-0000-7000-a000-00000000d003"}, collOwner))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a gate that was never WIRED is a misconfiguration we "+
			"must be paged for, not a 400 blaming the caller for a request they got right. body=%s",
			w.Code, w.Body.String())
	}
	env := decodeEnvelope(t, w)
	if !hasErrorCode(env, "CREATION_GATE_NOT_WIRED") {
		t.Errorf("code = %v; want CREATION_GATE_NOT_WIRED — an operator must be able to tell "+
			"'we forgot to wire the gate' apart from 'the database blinked'", env["code"])
	}
	assertNoDBInternalsLeaked(t, w.Body.String())
}

// TestCollectionErrors_ValidationIsStill400 — the guard against over-reach. A
// genuinely-malformed request is the ONE thing that belongs in a 400, and it must
// keep its human message so the author can fix it.
func TestCollectionErrors_ValidationIsStill400(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"empty title", map[string]any{"title": "", "visibility": "private"}},
		{"bad visibility", map[string]any{"title": "Drill", "visibility": "PUBLIC"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newConvertRig(t)
			w := httptest.NewRecorder()
			rig.srv.ServeHTTP(w, collReq(http.MethodPost, "/api/v1/collections", tc.body, collOwner))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — a malformed request IS the caller's to fix; "+
					"the inversion must not swallow real validation into a 5xx. body=%s",
					w.Code, w.Body.String())
			}
			env := decodeEnvelope(t, w)
			if !hasErrorCode(env, "CREATION_COLLECTION_INVALID") {
				t.Errorf("code = %v; want CREATION_COLLECTION_INVALID", env["code"])
			}
			if msg, _ := env["message"].(string); msg == "" {
				t.Error("validation 400 has no message — the author cannot fix what we will not name")
			}
		})
	}
}

// -----------------------------------------------------------------------------
// The mirror-image bug — found by LIVE-PROBING the deployed fix, not by a test.
//
// Inverting the default arm made an unrecognised error a 500. Correct — except
// that `atom_id: "banana"` was reaching Postgres unvalidated, dying there on
// `invalid input syntax for type uuid (SQLSTATE 22P02)`, and arriving as a
// repository failure. So the fix cheerfully answered
//
//	500 "something went wrong on our side"
//
// for the caller's own typo. Blaming ourselves for their mistake is the same
// crime as blaming them for ours, just pointed the other way.
//
// THE ROOT CAUSE, finally named: the handler let POSTGRES'S TYPE PARSER be its
// input validator. That is CHO-2173's disease exactly — the database's error
// becomes the API's contract. `questions_handler.go` in this very service parses
// its UUIDs at the boundary; the collection lane simply never did.
//
// A malformed id is a malformed REQUEST. It is caught here, in the handler,
// where we can name the field — never by a 22P02 from a query that should never
// have been built.
// -----------------------------------------------------------------------------

func TestCollectionRoutes_MalformedUUIDIs400NotOurFault(t *testing.T) {
	t.Parallel()

	rig := newConvertRig(t)
	good := rig.seedCollection(t, collOwner, "private")
	const bad = "banana" // not a UUID, and Postgres is not our validator

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"add atom with a non-UUID atom_id", http.MethodPost,
			"/api/v1/collections/" + good + "/atoms", map[string]any{"atom_id": bad}},
		{"add atom to a non-UUID collection", http.MethodPost,
			"/api/v1/collections/" + bad + "/atoms",
			map[string]any{"atom_id": "01970000-0000-7000-a000-00000000e001"}},
		{"remove a non-UUID atom", http.MethodDelete,
			"/api/v1/collections/" + good + "/atoms/" + bad, nil},
		{"convert a non-UUID collection", http.MethodPost,
			"/api/v1/collections/" + bad + "/convert-to-study-list", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			rig.srv.ServeHTTP(w, collReq(tc.method, tc.path, tc.body, collOwner))

			// 400, and ONLY 400.
			//
			// Not 5xx: that takes the blame for the caller's typo (what the live
			// probe caught — pg threw 22P02 and we called it a repository failure).
			//
			// Not 404 either: 404 means "I looked and it is not there". We did not
			// look. "banana" is not an id, so there was never a lookup to perform,
			// and saying otherwise invents a fact.
			//
			// ⚠ AND NOTE WHY THIS ASSERTION HAS TO BE THIS STRICT: with the inmem
			// repository a non-UUID is a perfectly good map key, so a laxer test
			// passes while production 500s. Only rejecting at the HANDLER — before
			// any repository is reached — makes this behaviour adapter-independent,
			// which is exactly why the boundary is the right place for it.
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400.\n"+
					"    A malformed id is a malformed REQUEST. Reject it here, where we can name "+
					"the field — never by letting Postgres's type parser be our validator "+
					"(a 22P02 is not an API contract).\n    body=%s", w.Code, w.Body.String())
			}
			assertNoDBInternalsLeaked(t, w.Body.String())
		})
	}
}
