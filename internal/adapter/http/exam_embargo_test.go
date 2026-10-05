// exam_embargo_test.go — ADR-191 D3 layer-2 (content-boundary 403) adversarial
// cross-role embargo tests.
//
// A PROCTOR-claimed caller MUST be rejected from atom CONTENT endpoints with
// 403 EXAM_CONTENT_EMBARGO_VIOLATION; a normal INSTRUCTOR / AUTHOR caller reads
// content unaffected. (The DB-RLS layer-1 "zero rows" backstop is exercised by
// the pg-package integration test.)
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const embargoCode = "EXAM_CONTENT_EMBARGO_VIOLATION"

// seedAtom creates a DRAFT atom via POST and returns its id.
func seedAtom(t *testing.T, srv http.Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := map[string]any{
		"title": "Embargoed content atom",
		"body":  "high-stakes exam content",
		"tags":  []string{"exam"},
		"mode":  "straight-up",
		"stem":  "What is the answer to the embargoed item?",
	}
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed POST /api/atoms status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("seed: invalid json: %v", err)
	}
	id, _ := got["atom_id"].(string)
	if id == "" {
		t.Fatalf("seed: no atom_id in %v", got)
	}
	return id
}

// getWithRoles issues GET path with an x-mesh-user-roles header (comma-joined).
func getWithRoles(srv http.Handler, path, roles string) *httptest.ResponseRecorder {
	r := authedReq(http.MethodGet, path, nil)
	if roles != "" {
		r.Header.Set("x-mesh-user-roles", roles)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func assertEmbargoed(t *testing.T, w *httptest.ResponseRecorder, ctx string) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("%s: status = %d body=%s; want 403", ctx, w.Code, w.Body.String())
	}
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s: invalid error json: %v (body=%s)", ctx, err, w.Body.String())
	}
	if env.Code != embargoCode {
		t.Errorf("%s: error code = %q; want %q", ctx, env.Code, embargoCode)
	}
}

// TestEmbargo_ProctorTokenBlockedOnAtomContent is the ADR-191 rollout §2 LEAD
// adversarial test (403 half).
func TestEmbargo_ProctorTokenBlockedOnAtomContent(t *testing.T) {
	srv := newServer(t)
	id := seedAtom(t, srv)

	// PROCTOR alone → 403 on the single-atom content endpoint.
	assertEmbargoed(t, getWithRoles(srv, "/api/atoms/"+id, "proctor"), "GET atom as proctor")

	// PROCTOR alone → 403 on the list (content) endpoint.
	assertEmbargoed(t, getWithRoles(srv, "/api/atoms", "proctor"), "LIST atoms as proctor")

	// INSTRUCTOR+PROCTOR → still 403 (presence of the proctor claim embargoes,
	// regardless of other roles — the restrictive "sees strictly less" model).
	assertEmbargoed(t, getWithRoles(srv, "/api/atoms/"+id, "instructor,proctor"), "GET atom as instructor+proctor")

	// Case-insensitive: PROCTOR (uppercase canonical) also blocked.
	assertEmbargoed(t, getWithRoles(srv, "/api/atoms/"+id, "PROCTOR"), "GET atom as PROCTOR uppercase")
}

// TestEmbargo_NonProctorRolesUnaffected proves the embargo does not regress
// normal content reads for INSTRUCTOR / AUTHOR (and no-roles callers).
func TestEmbargo_NonProctorRolesUnaffected(t *testing.T) {
	srv := newServer(t)
	id := seedAtom(t, srv)

	for _, roles := range []string{"instructor", "author", "instructor,author", ""} {
		if w := getWithRoles(srv, "/api/atoms/"+id, roles); w.Code != http.StatusOK {
			t.Errorf("GET atom roles=%q status = %d body=%s; want 200 (unaffected)", roles, w.Code, w.Body.String())
		}
		if w := getWithRoles(srv, "/api/atoms", roles); w.Code != http.StatusOK {
			t.Errorf("LIST atoms roles=%q status = %d body=%s; want 200 (unaffected)", roles, w.Code, w.Body.String())
		}
	}
}
