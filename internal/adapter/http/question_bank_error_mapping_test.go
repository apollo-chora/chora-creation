// question_bank_error_mapping_test.go — CHO-2175, second slice.
//
// writeQuestionBankError carries the SAME default arm the collection mapper did:
//
//	default:
//	    writeError(w, http.StatusBadRequest, "CREATION_QUESTION_BANK_INVALID", err.Error())
//
// and the same six `save: %w` repository failures plus four "not wired" refusals
// route straight into it. So a dead connection pool, an RLS-blocked write, or a
// half-wired rollout all come back as 400 "your request was invalid" with the raw
// internal error pasted in — the exact shape that let CHO-2173 hide in prod for
// months, because 4xx means "client error" and nothing alerts on it.
//
// Found by auditing the sibling mappers after fixing the collection one. Fixing
// only the ticket's path and leaving its twin in the same package would be the
// half-measure the engineering standard forbids.
package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// brokenQBLookup models the pg question-lookup with a dead datastore beneath it.
// Its own fake, so the shared memQuestionBankQuestions stays untouched.
type brokenQBLookup struct{}

func (brokenQBLookup) Resolve(_ context.Context, _, _ string) (bool, error) {
	return false, errors.New(`ERROR: canceling statement due to conflict with recovery (SQLSTATE 40001)`)
}

func (brokenQBLookup) Details(_ context.Context, _, _ string) (string, string, string, error) {
	return "", "", "", errors.New(`ERROR: canceling statement due to conflict with recovery (SQLSTATE 40001)`)
}

// newQBServerWithLookup builds the router with the supplied question lookup.
// Pass nil to leave the port UNWIRED — the misconfigured-rollout case.
func newQBServerWithLookup(lookup questionbank.QuestionLookup) http.Handler {
	return httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:                       inmem.NewAtomRepository(),
		QuestionBankRepository:     inmem.NewQuestionBankRepository(),
		QuestionBankQuestionLookup: lookup,
	})
}

// seedBank creates a question bank and returns its id.
func seedBank(t *testing.T, srv http.Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks",
		map[string]any{"name": "Algebra pool"}, ibOwner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create bank: status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"question_bank_id", "id", "bank_id"} {
		if id, _ := got[k].(string); id != "" {
			return id
		}
	}
	t.Fatalf("no bank id in %s", w.Body.String())
	return ""
}

// TestQuestionBank_LookupDBFaultIs5xxNotAnUnactionable400 — the datastore behind
// the question lookup fails. That is ours, not the caller's.
func TestQuestionBank_LookupDBFaultIs5xxNotAnUnactionable400(t *testing.T) {
	t.Parallel()

	srv := newQBServerWithLookup(brokenQBLookup{})
	id := seedBank(t, srv)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks/"+id+"/questions",
		map[string]any{"question_id": qExists}, ibOwner))

	if w.Code < 500 {
		t.Fatalf("status = %d, want 5xx — the question lookup's DATABASE failed. A 400 tells the "+
			"author to fix a request they got right, and tells our monitoring nothing is wrong.\n"+
			"    body=%s", w.Code, w.Body.String())
	}
	env := decodeEnvelope(t, w)
	if !hasErrorCode(env, "CREATION_QUESTION_BANK_REPO_ERROR") {
		t.Errorf("code = %v; want CREATION_QUESTION_BANK_REPO_ERROR (distinct + actionable)", env["code"])
	}
	for _, leak := range []string{"SQLSTATE", "40001", "questionbank.Service", "canceling statement"} {
		if jsonContains(w.Body.String(), leak) {
			t.Errorf("5xx leaks internals (%q): %s", leak, w.Body.String())
		}
	}
}

// NB — there is deliberately NO unwired-port test here, and the reason is worth
// recording: the router REFUSES TO MOUNT the question-bank routes at all unless
// QuestionBankRepository and QuestionBankQuestionLookup are both non-nil
// (handler.go RouterDeps: "missing either disables them — fail-loud"). A
// half-wired rollout therefore 404s the route rather than serving a gate it
// cannot evaluate. That is strictly better than the collection lane, which
// mounted and then refused at request time — which is why collection needed
// ErrGateNotWired and this one does not. The questionbank service keeps its
// defensive "not wired" refusals for the ports the mount guard does not cover,
// and they are wrapped too, but they are unreachable over HTTP today.

// TestQuestionBank_ValidationIsStill400 — the guard against over-reach. Inverting
// the default must not sweep genuine validation into a 5xx.
func TestQuestionBank_ValidationIsStill400(t *testing.T) {
	t.Parallel()

	tooManyTags := make([]string, 0, questionbank.MaxTags+1) // MaxTags is 20
	for i := 0; i <= questionbank.MaxTags; i++ {
		tooManyTags = append(tooManyTags, "tag"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}

	srv := newQuestionBankServer(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"empty name", map[string]any{"name": ""}},
		{"too many tags", map[string]any{"name": "Pool", "tags": tooManyTags}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, ibReq(http.MethodPost, "/api/v1/question-banks", tc.body, ibOwner))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — a malformed request IS the caller's to fix. body=%s",
					w.Code, w.Body.String())
			}
			env := decodeEnvelope(t, w)
			if !hasErrorCode(env, "CREATION_QUESTION_BANK_INVALID") {
				t.Errorf("code = %v; want CREATION_QUESTION_BANK_INVALID", env["code"])
			}
		})
	}
}
