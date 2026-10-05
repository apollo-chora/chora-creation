// Package httpadapter_test exercises the HTTP adapter end-to-end against an
// in-memory repository. These tests verify endpoint contracts derived from
// chora-contracts/openapi/creation-admin.yaml.
package httpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

func newServer(t *testing.T) http.Handler {
	t.Helper()
	repo := inmem.NewAtomRepository()
	return httpadapter.NewRouter(repo)
}

func authedReq(method, path string, body any) *http.Request {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// -----------------------------------------------------------------------------
// Health + readiness
// -----------------------------------------------------------------------------

func TestHealthz_ReturnsOK(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %q; want contains status:ok", w.Body.String())
	}
}

func TestReadyz_ReturnsOK_WhenRepoInitialised(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

func TestCreateAtom_Returns201AndAtom(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	body := map[string]any{
		"title": "Sample atom",
		"body":  "Hello chora-creation",
		"tags":  []string{"test"},
		"mode":  "straight-up",
		// ADR-156 Decision #1 — stem now required on POST /api/atoms.
		"stem": "Sample question stem",
	}
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["atom_id"] == nil || got["atom_id"] == "" {
		t.Errorf("atom_id missing in response: %v", got)
	}
	if got["status"] != "draft" {
		t.Errorf("status = %v; want draft", got["status"])
	}
	if got["tenant_id"] != tenantA {
		t.Errorf("tenant_id = %v; want %s", got["tenant_id"], tenantA)
	}
	if got["gcid"] != gcidA {
		t.Errorf("gcid = %v; want %s", got["gcid"], gcidA)
	}
}

func TestCreateAtom_RejectsMissingTenantHeader(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/atoms",
		bytes.NewBufferString(`{"title":"x","body":"y","mode":"straight-up","stem":"x?"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("gcid", gcidA)
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 400 or 401 when tenant missing", w.Code)
	}
}

func TestCreateAtom_RejectsMissingGcidHeader(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/atoms",
		bytes.NewBufferString(`{"title":"x","body":"y","mode":"straight-up","stem":"x?"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", tenantA)
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 400 or 401 when gcid missing", w.Code)
	}
}

func TestCreateAtom_RejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	// Per ADR-156 Decision #1, an empty title alone is no longer invalid
	// (title is now optional). The canonical 400 trigger is a missing
	// `stem` — sent here as the empty string, leaving title also empty so
	// the only missing required field is `stem`.
	srv := newServer(t)
	w := httptest.NewRecorder()
	body := map[string]any{
		"title": "",
		"body":  "y",
		"mode":  "straight-up",
	}
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

func TestGetAtom_ReturnsAtom(t *testing.T) {
	t.Parallel()

	srv := newServer(t)

	// Create
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title": "Hello", "body": "world", "mode": "straight-up", "stem": "Hello?",
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// Get
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))

	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w2.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["atom_id"] != atomID {
		t.Errorf("atom_id roundtrip mismatch: got %v want %v", got["atom_id"], atomID)
	}
}

func TestGetAtom_404OnUnknown(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

func TestGetAtom_404OnSoftDeleted(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	// Create
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title": "soon-gone", "body": "y", "mode": "straight-up", "stem": "Doomed atom",
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// Soft delete
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if w2.Code != http.StatusNoContent && w2.Code != http.StatusOK {
		t.Fatalf("delete status = %d; want 204 or 200", w2.Code)
	}

	// Now GET should 404
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w3.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 after soft delete", w3.Code)
	}
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func TestListAtoms_FiltersByTenant(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	// Create one for tenantA
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title": "owned-by-tenantA", "body": "y", "mode": "straight-up", "stem": "Owned?",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}

	// List with tenantA
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("list status = %d", w2.Code)
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Total < 1 {
		t.Errorf("expected at least 1 atom for tenantA; got %d", resp.Total)
	}
	for _, item := range resp.Items {
		if item["tenant_id"] != tenantA {
			t.Errorf("list returned cross-tenant atom: %v", item)
		}
	}
}

// TestListAtoms_FiltersByQuery covers the entity-picker search: GET
// /api/atoms?q=<term> returns only atoms whose title contains the term
// (case-insensitive substring). This backs the R+ curriculum atom picker
// (kills raw-UUID paste) — server-side so it scales past one page of atoms.
func TestListAtoms_FiltersByQuery(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	for _, title := range []string{"Road Safety Basics", "Photosynthesis 101"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
			"title": title, "body": "y", "mode": "straight-up", "stem": "Q?",
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %q failed: %d %s", title, w.Code, w.Body.String())
		}
	}

	// q=road → only the Road atom (case-insensitive; "road" ⊂ "Road Safety").
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms?q=road", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("q=road expected exactly 1 atom; got %d (%v)", resp.Total, resp.Items)
	}
	if resp.Items[0]["title"] != "Road Safety Basics" {
		t.Errorf("q=road returned wrong atom: %v", resp.Items[0]["title"])
	}
}

// -----------------------------------------------------------------------------
// Patch
// -----------------------------------------------------------------------------

func TestPatchAtom_BumpsRevision(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	// Create
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title": "v1", "body": "y", "mode": "straight-up", "stem": "Patch me?",
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	prevRev, _ := created["revision"].(float64)

	// Patch
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodPatch, "/api/atoms/"+atomID, map[string]any{
		"title": "v2",
	}))
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w2.Code, w2.Body.String())
	}
	var updated map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &updated)
	if updated["title"] != "v2" {
		t.Errorf("title = %v; want v2", updated["title"])
	}
	gotRev, _ := updated["revision"].(float64)
	if gotRev != prevRev+1 {
		t.Errorf("revision = %v; want %v", gotRev, prevRev+1)
	}
}

// -----------------------------------------------------------------------------
// Delete (soft)
// -----------------------------------------------------------------------------

func TestDeleteAtom_SoftDeletesNotHard(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	// Create
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", map[string]any{
		"title": "to-delete", "body": "y", "mode": "straight-up", "stem": "Bye atom",
	}))
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// Delete
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if w2.Code != http.StatusNoContent && w2.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w2.Code)
	}

	// Confirm via direct repo lookup (white-box) that the row is still there
	// but with deleted_at set. We can't reach the repo from a black-box test
	// without exposing it, so instead we assert via /api/atoms/{id} returning
	// 404 (handler filters soft-deleted) — same as TestGetAtom_404OnSoftDeleted.
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w3.Code != http.StatusNotFound {
		t.Errorf("get-after-delete status = %d; want 404 (soft-deleted)", w3.Code)
	}

	// And confirm a redundant DELETE is also 404 (cannot delete a deleted atom).
	w4 := httptest.NewRecorder()
	srv.ServeHTTP(w4, authedReq(http.MethodDelete, "/api/atoms/"+atomID, nil))
	if w4.Code != http.StatusNotFound {
		t.Errorf("redundant delete status = %d; want 404 (already deleted)", w4.Code)
	}
}

// _ = atom guards atom import for tests that may not reference the package
// directly through type names. The package is used via the in-memory
// repository constructor, but linters can mis-flag the import.
var _ atom.Mode = atom.ModeStraightUp

// -----------------------------------------------------------------------------
// F2 — GET /api/atoms/new draft template (Phyllis demo Step 3 unblock)
// -----------------------------------------------------------------------------

func TestGetAtomsNew_Returns200WithEmptyDraftTemplate(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/new", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v body = %s", err, w.Body.String())
	}
	// Shape MUST match chora-web AtomDraft model exactly (camelCase keys).
	cases := []struct {
		key  string
		want any
	}{
		{"atomId", nil},
		{"title", ""},
		{"body", ""},
		{"courseCode", ""},
		{"topic", ""},
		{"cognitiveLevel", "remembering"},
		{"state", "DRAFT"},
	}
	for _, c := range cases {
		got, ok := resp[c.key]
		if !ok {
			t.Errorf("missing key %q in response; body=%s", c.key, w.Body.String())
			continue
		}
		if got != c.want {
			t.Errorf("key %q: got %v want %v", c.key, got, c.want)
		}
	}
	for _, k := range []string{"tags", "prerequisites", "objectives"} {
		arr, ok := resp[k].([]any)
		if !ok {
			t.Errorf("key %q: not an array; got %T", k, resp[k])
			continue
		}
		if len(arr) != 0 {
			t.Errorf("key %q: expected empty array; got %v", k, arr)
		}
	}
}

func TestGetAtomsNew_RejectsNonGet(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, authedReq(m, "/api/atoms/new", nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("method %s: status %d; want 405", m, w.Code)
			}
		})
	}
}

func TestGetAtomsNew_DoesNotCollideWithItemHandler(t *testing.T) {
	t.Parallel()
	// Confirm that the literal "new" path segment doesn't get treated as an
	// atom id (which would 404). Bare GET /api/atoms/new is always the
	// draft template, regardless of repo state.
	srv := newServer(t)

	// Pre-seed an atom that would never collide with "new" id, then assert
	// /api/atoms/new still returns 200 + the template envelope.
	createW := httptest.NewRecorder()
	srv.ServeHTTP(createW, authedReq(http.MethodPost, "/api/atoms",
		map[string]string{"title": "ignore", "body": "ignore", "mode": "straight-up", "stem": "Ignore me?"}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("seed atom: status %d; body %s", createW.Code, createW.Body.String())
	}

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/new", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; want 200 (template)", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"state":"DRAFT"`) {
		t.Errorf("response should be the empty draft template; got %s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// A16 — GET /api/atoms/{id} adds learner-safe mcq_payload / essay_payload
// when a Question exists. Sensitive fields (is_correct, explainer,
// model_answer, rubric) MUST be stripped from the projection.
// -----------------------------------------------------------------------------

func TestGetAtom_IncludesMCQPayload_LearnerProjection(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "mcq atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	// Seed a question against the atom.
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: status=%d body=%s", w.Code, w.Body.String())
	}

	// GET the atom.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get atom: status=%d body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	mcq, ok := got["mcq_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcq_payload in GET /api/atoms/{id}; got %s", w2.Body.String())
	}
	options, _ := mcq["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected 2 options; got %v", options)
	}
	// LEARNER projection — is_correct + explainer MUST be absent or zero-value.
	for i, opt := range options {
		om := opt.(map[string]any)
		if ic, present := om["is_correct"]; present && ic == true {
			t.Errorf("option[%d] leaked is_correct=true in learner projection: %v", i, om)
		}
		if ex, present := om["explainer"]; present {
			if s, _ := ex.(string); s != "" {
				t.Errorf("option[%d] leaked explainer=%q in learner projection", i, s)
			}
		}
	}
}

func TestGetAtom_IncludesEssayPayload_LearnerProjection(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "essay atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain story points.",
		"oe_payload": map[string]any{
			"model_answer": "Because they capture complexity + risk + effort rather than time.",
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	essay, ok := got["essay_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected essay_payload in GET /api/atoms/{id}; got %s", w2.Body.String())
	}
	// LEARNER projection — model_answer MUST be absent / empty.
	if ma, present := essay["model_answer"]; present {
		if s, _ := ma.(string); s != "" {
			t.Errorf("leaked model_answer=%q in learner projection", s)
		}
	}
}

// -----------------------------------------------------------------------------
// WS-0b A16 — unified question_payload envelope (discriminated MCQ | OE).
//
// Contract: chora-contracts/openapi/gateway.yaml §LearningAtomEnvelope.
// chora-creation surfaces `question_payload` ALONGSIDE the legacy
// `mcq_payload` / `essay_payload` shims so new FE consumers can read the
// unified shape. Per `feedback_no_stubs_real_wiring`: a question with
// type=mcq but no options[] is a domain-invariant violation; the handler
// 500s on such malformed Question records rather than silently emitting
// an empty payload.
// -----------------------------------------------------------------------------

// TestGetAtom_QuestionPayload_MCQ_LearnerSafe — MCQ projection surfaces
// type=mcq, options (label only — no is_correct, no explainer), xp_on_correct,
// and NEVER carries correct_option_id (chora-creation does not emit it —
// the field is gated at the BFF per gateway.yaml §LearningAtomEnvelope).
func TestGetAtom_QuestionPayload_MCQ_LearnerSafe(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "mcq atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	body := map[string]any{
		"type":   "mcq",
		"prompt": "Which Scrum role owns the backlog?",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "opt_1", "label": "PO", "is_correct": true, "explainer": "PO owns backlog priority"},
				{"option_id": "opt_2", "label": "PM", "is_correct": false, "explainer": "Not a Scrum role"},
			},
			"xp_on_correct": 50,
			"timer_seconds": 60,
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: status=%d body=%s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET atom: status=%d body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}

	qp, ok := got["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload in GET /api/atoms/{id}; got %s", w2.Body.String())
	}
	if qp["type"] != "mcq" {
		t.Errorf("question_payload.type = %v; want mcq", qp["type"])
	}
	if _, present := qp["question_id"]; !present {
		t.Errorf("question_payload.question_id is required; missing in %v", qp)
	}
	if qp["prompt"] != "Which Scrum role owns the backlog?" {
		t.Errorf("question_payload.prompt = %v", qp["prompt"])
	}
	if xp, _ := qp["xp_on_correct"].(float64); xp != 50 {
		t.Errorf("question_payload.xp_on_correct = %v; want 50", qp["xp_on_correct"])
	}
	if ts, _ := qp["timer_seconds"].(float64); ts != 60 {
		t.Errorf("question_payload.timer_seconds = %v; want 60", qp["timer_seconds"])
	}

	options, _ := qp["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected 2 options; got %v", options)
	}
	// LEARNER projection — is_correct + explainer MUST NOT be carried on
	// question_payload.options[]. The chora-creation handler does NOT emit
	// correct_option_id either; that field is gated at the BFF per
	// gateway.yaml §LearningAtomEnvelope.
	for i, opt := range options {
		om := opt.(map[string]any)
		if ic, present := om["is_correct"]; present && ic == true {
			t.Errorf("option[%d] leaked is_correct=true in learner projection: %v", i, om)
		}
		if ex, present := om["explainer"]; present {
			if s, _ := ex.(string); s != "" {
				t.Errorf("option[%d] leaked explainer=%q in learner projection", i, s)
			}
		}
		// option_id + label are required (per feedback_mcq_option_naming —
		// label is author-typed payload; marker is computed at render time
		// and never stored).
		if _, ok := om["option_id"].(string); !ok {
			t.Errorf("option[%d] option_id missing", i)
		}
		if _, ok := om["label"].(string); !ok {
			t.Errorf("option[%d] label missing", i)
		}
	}
	// correct_option_id is BFF-gated; chora-creation MUST NOT emit it.
	if _, present := qp["correct_option_id"]; present {
		t.Errorf("question_payload.correct_option_id leaked from chora-creation; should be BFF-gated")
	}
}

// CHO-1638 — the LEARNER-safe projection (GET /api/atoms/{id}) must surface the
// signed QUESTION illustration on BOTH the question_payload envelope (what
// atomic-session / duels read) and the legacy mcq_payload shim, while NEVER
// projecting the model-answer illustration (Security-wins: a learner taking the
// assessment must not see the answer image pre-grade). The question image is
// minted from its durable gs:// ref exactly like the author getQuestion path.
// RED before the learner projection learns to mint the question image.
func TestGetAtom_QuestionPayload_MCQ_QuestionImageOnly(t *testing.T) {
	t.Parallel()
	var signedURIs []string
	signer := fakeDownloadSigner{fn: func(_ context.Context, gsURI string) (ports.SignedDownloadURL, error) {
		signedURIs = append(signedURIs, gsURI)
		return ports.SignedDownloadURL{URL: "https://signed.example/" + gsURI, ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
	}}
	srv, atomID := seedMCQAtomWithSigner(t, signer)

	stemGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png"
	ansGS := "gs://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/ans.png"
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Identify the condensation stage.",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "a", "label": "Condensation", "is_correct": true, "explainer": "Yes"},
				{"option_id": "b", "label": "Evaporation", "is_correct": false, "explainer": "No"},
			},
			"xp_on_correct": 50,
		},
		"image_url":        stemGS,
		"answer_image_url": ansGS,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET atom: %d %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}

	// --- question_payload (A16 — what atomic-session reads) ---
	qp, ok := got["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload; got %s", w2.Body.String())
	}
	qImg, _ := qp["image_url"].(string)
	if !strings.HasPrefix(qImg, "https://signed.example/") {
		t.Errorf("question_payload.image_url not minted; got %q", qImg)
	}
	if strings.HasPrefix(qImg, "gs://") {
		t.Errorf("raw gs:// question image leaked to the wire: %q", qImg)
	}
	if _, present := qp["answer_image_url"]; present {
		t.Errorf("SECURITY: question_payload leaked answer_image_url to learner projection: %v", qp["answer_image_url"])
	}

	// --- mcq_payload (legacy shim) ---
	mp, ok := got["mcq_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcq_payload; got %s", w2.Body.String())
	}
	if mImg, _ := mp["image_url"].(string); !strings.HasPrefix(mImg, "https://signed.example/") {
		t.Errorf("mcq_payload.image_url not minted; got %q", mImg)
	}
	if _, present := mp["answer_image_url"]; present {
		t.Errorf("SECURITY: mcq_payload leaked answer_image_url to learner projection: %v", mp["answer_image_url"])
	}

	// The signer must be invoked exactly ONCE — for the QUESTION image only.
	// The answer image must NEVER be signed on the learner path.
	if len(signedURIs) != 1 {
		t.Errorf("expected exactly 1 mint call (question image only); got %d: %v", len(signedURIs), signedURIs)
	}
	if len(signedURIs) == 1 && signedURIs[0] != stemGS {
		t.Errorf("signed the wrong ref; got %q want %q", signedURIs[0], stemGS)
	}
}

// TestGetAtom_QuestionPayload_OE_LearnerSafe — OE projection surfaces
// type=oe, question_id, prompt, max_score, AND a learner-safe rubric
// (criteria + weights — but never model_answer). Per the user spec the
// rubric IS visible to learners so they understand grading criteria.
func TestGetAtom_QuestionPayload_OE_LearnerSafe(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "oe atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain story points.",
		"oe_payload": map[string]any{
			"model_answer": "Because they capture complexity + risk + effort rather than time.",
			"rubric": []map[string]any{
				{"criterion_id": "c1", "title": "Clarity", "weight": 0.4},
				{"criterion_id": "c2", "title": "Completeness", "weight": 0.6},
			},
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: status=%d body=%s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET atom: %d %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)

	qp, ok := got["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload in GET /api/atoms/{id}; got %s", w2.Body.String())
	}
	if qp["type"] != "oe" {
		t.Errorf("question_payload.type = %v; want oe", qp["type"])
	}
	if _, present := qp["question_id"]; !present {
		t.Errorf("question_payload.question_id missing")
	}
	if qp["prompt"] != "Explain story points." {
		t.Errorf("question_payload.prompt = %v", qp["prompt"])
	}
	// max_score required — default 100 when rubric weights sum to 100 (the
	// canonical OE domain invariant).
	maxScore, ok := qp["max_score"].(float64)
	if !ok || maxScore <= 0 {
		t.Errorf("question_payload.max_score must be a positive int; got %v", qp["max_score"])
	}
	// Learner-safe rubric surfaced. model_answer MUST be absent.
	rubric, _ := qp["rubric"].(map[string]any)
	if rubric == nil {
		t.Fatalf("question_payload.rubric is required for OE with a weighted rubric")
	}
	crit, _ := rubric["criteria"].([]any)
	if len(crit) != 2 {
		t.Errorf("question_payload.rubric.criteria length = %d; want 2", len(crit))
	}
	if _, present := qp["model_answer"]; present {
		t.Errorf("question_payload.model_answer leaked from chora-creation; should be stripped")
	}
}

// TestGetAtom_QuestionPayload_OE_NoRubric_MaxScoreDefault — OE without an
// explicit weighted rubric still produces a `max_score` (default 100) and
// omits the `rubric` field. Confirms graceful degradation when authoring
// produced a minimal OE.
func TestGetAtom_QuestionPayload_OE_NoRubric_MaxScoreDefault(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "oe atom no rubric", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("essay")
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	body := map[string]any{
		"type":   "oe",
		"prompt": "Explain why.",
		"oe_payload": map[string]any{
			"model_answer": "Because of physics.",
		},
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET atom: %d %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	qp, ok := got["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload; got %s", w2.Body.String())
	}
	maxScore, ok := qp["max_score"].(float64)
	if !ok || maxScore != 100 {
		t.Errorf("question_payload.max_score default = %v; want 100", qp["max_score"])
	}
	if _, present := qp["rubric"]; present {
		t.Errorf("question_payload.rubric should be omitted when no weighted rubric is set; got %v", qp["rubric"])
	}
}

// TestGetAtom_QuestionPayload_MalformedMCQ_FailsLoud — per
// `feedback_no_stubs_real_wiring`: a question with type=mcq but no MCQ
// payload (or empty options[]) MUST 500 with an explicit error envelope
// rather than silently emitting an empty payload that would render
// nothing in the FE.
func TestGetAtom_QuestionPayload_MalformedMCQ_FailsLoud(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "malformed mcq atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	// Directly inject a malformed Question — type=mcq, MCQ=nil. Bypasses
	// New() validation to simulate a corrupted/legacy persistence state.
	bad := &question.Question{
		QuestionID: "bad-q-id",
		AtomID:     a.AtomID,
		TenantID:   tenantA,
		AuthorGcid: gcidA,
		Type:       question.TypeMCQ,
		Prompt:     "Malformed?",
		SourceType: atom.SourceManual,
		Revision:   1,
		// MCQ intentionally nil
	}
	rev := &question.QuestionRevision{
		RevisionID:     "bad-rev",
		QuestionID:     bad.QuestionID,
		RevisionNumber: 1,
	}
	qRepo.questions[bad.QuestionID] = bad
	qRepo.revisions[bad.QuestionID] = []*question.QuestionRevision{rev}

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("malformed mcq must fail loud; got status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_QUESTION_MALFORMED") {
		t.Errorf("expected explicit error envelope CREATION_QUESTION_MALFORMED; got %s", w.Body.String())
	}
}

// TestGetAtom_NoQuestion_NoQuestionPayload — when the atom has no
// attached Question, `question_payload` is absent. The atom envelope still
// returns 200 with the baseline atom fields.
func TestGetAtom_NoQuestion_NoQuestionPayload(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "bare atom", Body: "just text", Mode: atom.ModeStraightUp,
	})
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
	})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET atom: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if _, present := got["question_payload"]; present {
		t.Errorf("question_payload should be absent on bare atom; got %v", got["question_payload"])
	}
}

// -----------------------------------------------------------------------------
// ADR-156 Phase 1 — createAtom + patchAtom accept the locked Phase 1 envelope
//
// Plan: docs/m13/atom-phase1-execution-plan-2026-05-17.md §5 (Wave 2 Agent
// B5). Contract: chora-contracts/openapi/creation-admin.yaml §createAtom
// + §updateAtom. Domain validation: atom.ValidatePhase1() shipped by B1
// at a113fcc4.
// -----------------------------------------------------------------------------

// TestCreateAtom_Phase1Fields_Echoed — POST with the full Phase 1 envelope
// returns 201 and the response echoes every Phase 1 field; a subsequent GET
// returns the same shape.
func TestCreateAtom_Phase1Fields_Echoed(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	body := map[string]any{
		"question_type":       "mcq",
		"stem":                "What gas do plants release as a byproduct of photosynthesis?",
		"title":               "Photosynthesis basics",
		"subject":             "biology",
		"cognitive_level":     "comprehension",
		"imda_dimension_tags": []string{"transparency"},
		"author_note":         "Phyllis demo MCQ",
		"media_assets":        []map[string]any{},
		"body":                "",
		"mode":                "straight-up",
		"difficulty":          1,
		"locale":              "en-SG",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body = %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("json: %v", err)
	}

	// Verify every Phase 1 field round-trips.
	if created["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq", created["question_type"])
	}
	if created["stem"] != body["stem"] {
		t.Errorf("stem = %v; want %v", created["stem"], body["stem"])
	}
	if created["title"] != "Photosynthesis basics" {
		t.Errorf("title = %v; want Photosynthesis basics", created["title"])
	}
	if created["subject"] != "biology" {
		t.Errorf("subject = %v; want biology", created["subject"])
	}
	if created["cognitive_level"] != "comprehension" {
		t.Errorf("cognitive_level = %v; want comprehension", created["cognitive_level"])
	}
	if created["author_note"] != "Phyllis demo MCQ" {
		t.Errorf("author_note = %v; want Phyllis demo MCQ", created["author_note"])
	}
	imdaTags, ok := created["imda_dimension_tags"].([]any)
	if !ok || len(imdaTags) != 1 || imdaTags[0] != "transparency" {
		t.Errorf("imda_dimension_tags = %v; want [transparency]", created["imda_dimension_tags"])
	}
	if difficulty, _ := created["difficulty"].(float64); difficulty != 1 {
		t.Errorf("difficulty = %v; want 1", created["difficulty"])
	}

	// GET the atom and verify the same shape.
	atomID := created["atom_id"].(string)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want 200; body = %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["question_type"] != "mcq" {
		t.Errorf("GET question_type = %v; want mcq", got["question_type"])
	}
	if got["stem"] != body["stem"] {
		t.Errorf("GET stem = %v; want %v", got["stem"], body["stem"])
	}
	if got["subject"] != "biology" {
		t.Errorf("GET subject = %v; want biology", got["subject"])
	}
	if got["cognitive_level"] != "comprehension" {
		t.Errorf("GET cognitive_level = %v; want comprehension", got["cognitive_level"])
	}
}

// TestCreateAtom_AtomTypeBackwardsCompat — the legacy `atom_type` REQUEST
// field is accepted as a synonym for `question_type` for one release cycle
// per ADR-156 §8 backwards-compat shim. Persisted + echoed as
// `question_type` (the canonical field name).
func TestCreateAtom_AtomTypeBackwardsCompat(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"atom_type": "mcq", // deprecated synonym
		"stem":      "Legacy atom_type field",
		"locale":    "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq (mapped from deprecated atom_type=mcq)", got["question_type"])
	}
}

// TestCreateAtom_AtomTypeBackwardsCompat_QuestionTypeWins — when BOTH
// question_type AND atom_type are supplied on the request envelope,
// question_type wins per ADR-156 §8 shim semantics.
func TestCreateAtom_AtomTypeBackwardsCompat_QuestionTypeWins(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type": "essay",
		"atom_type":     "mcq", // deprecated synonym — IGNORED when question_type present
		"stem":          "Both fields supplied",
		"locale":        "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["question_type"] != "essay" {
		t.Errorf("question_type = %v; want essay (question_type wins over atom_type)", got["question_type"])
	}
}

// TestCreateAtom_StemRequired — Decision #1 structural change. POST without
// `stem` returns 400 with the canonical CREATION_INVALID_ATOM envelope.
func TestCreateAtom_StemRequired(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type": "mcq",
		"title":         "Has title but no stem",
		"locale":        "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (stem missing); body = %s", w.Code, w.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "CREATION_INVALID_ATOM" {
		t.Errorf("error code = %v; want CREATION_INVALID_ATOM", errResp["code"])
	}
	if msg, _ := errResp["message"].(string); !strings.Contains(msg, "stem") {
		t.Errorf("error message = %q; want it to mention 'stem'", msg)
	}
}

// TestCreateAtom_TitleOptional — Decision #1 relaxation. POST without
// `title` returns 201 (title is optional; FE derives display_label from
// stem when title is empty).
func TestCreateAtom_TitleOptional(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type": "mcq",
		"stem":          "What is the speed of light?",
		// title intentionally omitted
		"locale": "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (title is optional per Decision #1); body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	// title is omitempty — should be absent or empty string in the response.
	if title, present := got["title"]; present && title != "" && title != nil {
		t.Errorf("title = %v; want empty when omitted from request", title)
	}
}

// TestCreateAtom_InvalidCognitiveLevel — cognitive_level must be one of the
// Bloom 6-enum values per Decision #3. Anything else returns 400.
func TestCreateAtom_InvalidCognitiveLevel(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type":   "mcq",
		"stem":            "Bad cognitive_level",
		"cognitive_level": "invalid",
		"locale":          "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 on invalid cognitive_level; body = %s", w.Code, w.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if msg, _ := errResp["message"].(string); !strings.Contains(msg, "cognitive_level") {
		t.Errorf("error message = %q; want it to mention 'cognitive_level'", msg)
	}
}

// TestCreateAtom_InvalidImdaDimensionTag — imda_dimension_tags entries must
// match the canonical ADR-141 4-label taxonomy. Pre-ADR-141 v1 labels
// (internal_governance, risk_levels, ...) are REJECTED per ADR-141 +
// atom.ImdaDimTag.Valid().
func TestCreateAtom_InvalidImdaDimensionTag(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type":       "mcq",
		"stem":                "Bad IMDA tag",
		"imda_dimension_tags": []string{"internal_governance"}, // pre-ADR-141 v1 label, REJECTED
		"locale":              "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 on invalid IMDA tag; body = %s", w.Code, w.Body.String())
	}
}

// TestCreateAtom_MediaAssetsCapAt1 — Phase 1 supports ≤1 media asset entry
// per Decision #5 (audio/video deferred to Phase 4). >1 entry returns 400.
func TestCreateAtom_MediaAssetsCapAt1(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type": "mcq",
		"stem":          "Too many media assets",
		"media_assets": []map[string]any{
			{
				"type":       "image",
				"url":        "gs://chora-atom-media-dev/tenants/t1/atoms/a1/m1.png",
				"mime":       "image/png",
				"size_bytes": 1024,
			},
			{
				"type":       "image",
				"url":        "gs://chora-atom-media-dev/tenants/t1/atoms/a1/m2.png",
				"mime":       "image/png",
				"size_bytes": 2048,
			},
		},
		"locale": "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 on >1 media assets per Phase 1 cap; body = %s", w.Code, w.Body.String())
	}
}

// TestCreateAtom_MediaAssetsMIMEEnum — Phase 1 image MIME allowlist:
// jpeg | png | webp per atom.MediaAsset.Validate. image/gif rejected.
func TestCreateAtom_MediaAssetsMIMEEnum(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	body := map[string]any{
		"question_type": "mcq",
		"stem":          "Bad media MIME",
		"media_assets": []map[string]any{
			{
				"type":       "image",
				"url":        "gs://chora-atom-media-dev/tenants/t1/atoms/a1/m1.gif",
				"mime":       "image/gif", // NOT in the allowlist
				"size_bytes": 1024,
			},
		},
		"locale": "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 on bad MIME; body = %s", w.Code, w.Body.String())
	}
}

// TestPatchAtom_Phase1Fields — PATCH applies each Phase 1 field
// individually and verifies only that field changed.
func TestPatchAtom_Phase1Fields(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	// Seed an atom with minimal Phase 1 envelope.
	createBody := map[string]any{
		"question_type": "mcq",
		"stem":          "Original stem",
		"title":         "Original title",
		"locale":        "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed status = %d; body = %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// Verify each Phase 1 field can be patched independently.
	cases := []struct {
		name  string
		patch map[string]any
		key   string
		want  any
	}{
		{"stem", map[string]any{"stem": "Updated stem"}, "stem", "Updated stem"},
		{"subject", map[string]any{"subject": "history"}, "subject", "history"},
		{"cognitive_level", map[string]any{"cognitive_level": "analysis"}, "cognitive_level", "analysis"},
		{"author_note", map[string]any{"author_note": "Patched note"}, "author_note", "Patched note"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pw := httptest.NewRecorder()
			srv.ServeHTTP(pw, authedReq(http.MethodPatch, "/api/atoms/"+atomID, tc.patch))
			if pw.Code != http.StatusOK {
				t.Fatalf("PATCH %s status = %d; body = %s", tc.name, pw.Code, pw.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(pw.Body.Bytes(), &resp)
			if got := resp[tc.key]; got != tc.want {
				t.Errorf("PATCH %s: resp[%q] = %v; want %v", tc.name, tc.key, got, tc.want)
			}
		})
	}

	// Patch imda_dimension_tags as an array.
	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, authedReq(http.MethodPatch, "/api/atoms/"+atomID,
		map[string]any{"imda_dimension_tags": []string{"accountability", "transparency"}}))
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH imda_dimension_tags status = %d; body = %s", pw.Code, pw.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(pw.Body.Bytes(), &resp)
	tags, ok := resp["imda_dimension_tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Errorf("imda_dimension_tags = %v; want [accountability, transparency]", resp["imda_dimension_tags"])
	}
}

// TestPatchAtom_QuestionTypeImmutable — per ADR-156 Decision #2 +
// plan §1, question_type is IMMUTABLE on PATCH. Surfacing the field
// returns 400 with envelope CREATION_QUESTION_TYPE_IMMUTABLE rather
// than silently dropping it.
func TestPatchAtom_QuestionTypeImmutable(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	// Seed an atom.
	createBody := map[string]any{
		"question_type": "mcq",
		"stem":          "Original stem",
		"locale":        "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed status = %d; body = %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	// PATCH attempts to mutate question_type → 400.
	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, authedReq(http.MethodPatch, "/api/atoms/"+atomID,
		map[string]any{"question_type": "essay"}))
	if pw.Code != http.StatusBadRequest {
		t.Fatalf("PATCH question_type status = %d; want 400; body = %s", pw.Code, pw.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(pw.Body.Bytes(), &errResp)
	if errResp["code"] != "CREATION_QUESTION_TYPE_IMMUTABLE" {
		t.Errorf("error code = %v; want CREATION_QUESTION_TYPE_IMMUTABLE", errResp["code"])
	}

	// The deprecated atom_type field is ALSO rejected on PATCH (same
	// immutability gate applies — the synonym doesn't mean it's mutable).
	pw2 := httptest.NewRecorder()
	srv.ServeHTTP(pw2, authedReq(http.MethodPatch, "/api/atoms/"+atomID,
		map[string]any{"atom_type": "essay"}))
	if pw2.Code != http.StatusBadRequest {
		t.Errorf("PATCH atom_type status = %d; want 400 (immutability gate)", pw2.Code)
	}
}

// TestPatchAtom_InvalidCognitiveLevelRejected — invalid cognitive_level on
// PATCH returns 400 (ValidatePhase1 runs after PATCH applies fields).
func TestPatchAtom_InvalidCognitiveLevelRejected(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	// Seed.
	createBody := map[string]any{
		"question_type": "mcq",
		"stem":          "Original stem",
		"locale":        "en",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed status = %d; body = %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	atomID := created["atom_id"].(string)

	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, authedReq(http.MethodPatch, "/api/atoms/"+atomID,
		map[string]any{"cognitive_level": "memorisation"})) // not in Bloom 6-enum
	if pw.Code != http.StatusBadRequest {
		t.Errorf("PATCH invalid cognitive_level status = %d; want 400", pw.Code)
	}
}

// TestGetAtom_ReturnsPhase1Fields — GET returns the full envelope including
// every Phase 1 field. Subset-check distinct from the round-trip test above
// (uses a directly-seeded LearningAtom via the in-mem repo to avoid handler
// coupling on the seed side).
func TestGetAtom_ReturnsPhase1Fields(t *testing.T) {
	t.Parallel()
	atomRepo := inmem.NewAtomRepository()
	a := &atom.LearningAtom{
		AtomID:            "01970000-0000-7000-aaaa-000000000001",
		TenantID:          tenantA,
		Gcid:              gcidA,
		Title:             "Direct seed",
		Stem:              "Direct seed stem",
		Subject:           "physics",
		CognitiveLevel:    atom.CognitiveLevelApplication,
		ImdaDimensionTags: []atom.ImdaDimTag{atom.ImdaDimSafetyRobustness},
		AuthorNote:        "Seeded for GET test",
		QuestionType:      atom.TypeMCQ,
		Mode:              atom.ModeStraightUp,
		Status:            atom.StatusDraft,
		Revision:          1,
	}
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{Repo: atomRepo})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/"+a.AtomID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)

	cases := []struct {
		key  string
		want any
	}{
		{"stem", "Direct seed stem"},
		{"subject", "physics"},
		{"cognitive_level", "application"},
		{"author_note", "Seeded for GET test"},
		{"question_type", "mcq"},
	}
	for _, c := range cases {
		if got[c.key] != c.want {
			t.Errorf("GET response[%q] = %v; want %v", c.key, got[c.key], c.want)
		}
	}
	imdaTags, ok := got["imda_dimension_tags"].([]any)
	if !ok || len(imdaTags) != 1 || imdaTags[0] != "safety_robustness" {
		t.Errorf("imda_dimension_tags = %v; want [safety_robustness]", got["imda_dimension_tags"])
	}
}
