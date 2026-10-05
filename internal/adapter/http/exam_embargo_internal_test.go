// exam_embargo_internal_test.go — direct unit tests for the security-critical
// unexported embargo matchers (ADR-191). callerIsProctor gates the 403; the
// tenantContext bridge (meshRolesForRLS) produces the chora.user_roles GUC the
// restrictive RLS policy matches. Both must be exact-token, injection-safe.
package httpadapter

import (
	"net/http/httptest"
	"testing"
)

func TestCallerIsProctor(t *testing.T) {
	cases := []struct {
		roles string
		want  bool
	}{
		{"", false},
		{"instructor", false},
		{"proctor", true},
		{"PROCTOR", true},             // case-insensitive
		{"Instructor,Proctor", true},  // multi-role principal
		{"instructor, proctor", true}, // spaces trimmed
		{"proctoring", false},         // anchored token — substring must NOT match
		{"exam_proctor", false},       // different token must NOT match
		{"auditor,instructor,proctor", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/atoms/x", nil)
		if c.roles != "" {
			r.Header.Set("x-mesh-user-roles", c.roles)
		}
		if got := callerIsProctor(r); got != c.want {
			t.Errorf("callerIsProctor(%q) = %v; want %v", c.roles, got, c.want)
		}
	}
}

func TestMeshRolesForRLS_SanitisesAndLowercases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"PROCTOR", "proctor"},
		{"Instructor, Proctor", "instructor,proctor"},
		{"training-admin,tenant_admin", "training-admin,tenant_admin"},
		{"bad token,proctor", "proctor"},     // token with a space is dropped
		{"proctor';DROP--,author", "author"}, // injection-shaped token dropped
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/atoms", nil)
		if c.in != "" {
			r.Header.Set("x-mesh-user-roles", c.in)
		}
		if got := meshRolesForRLS(r); got != c.want {
			t.Errorf("meshRolesForRLS(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
