// exam_embargo.go — ADR-191 D3 layer-2: the content-boundary gate that rejects
// a PROCTOR-claimed caller from atom / atom-revision CONTENT with a
// 403 EXAM_CONTENT_EMBARGO_VIOLATION + an audit log line.
//
// This composes with (does NOT replace) the chora_creation restrictive RLS
// layer-1 (`exam_content_embargo` policy, mig 0028): the RLS layer is the DB
// backstop that yields ZERO rows to a PROCTOR-claimed session even for read
// paths that bypass this handler; this gate is the fast-fail at the API
// boundary. Two sites, adversarially tested to agree (ADR-191 Consequences).
//
// A proctor administers a sitting (check-in / open / close / roster / incident)
// but MUST NOT see the paper — the restrictive "sees strictly less" model
// (ADR-191 D2): the PRESENCE of the proctor claim embargoes content regardless
// of any other role the principal carries (e.g. INSTRUCTOR+PROCTOR still 403s).
package httpadapter

import (
	"log"
	"net/http"
	"strings"
)

// EmbargoErrorCode is the wire error code returned to an embargoed caller.
const EmbargoErrorCode = "EXAM_CONTENT_EMBARGO_VIOLATION"

// proctorRoleToken is the lowercase mesh form of the PROCTOR canonical role.
// `x-mesh-user-roles` carries the caller's role set lowercased (see
// chora-gateway meshRolesForRLS + chora-delivery roleSet); we match
// case-insensitively for defence in depth.
const proctorRoleToken = "proctor"

// callerIsProctor reports whether the inbound request asserts the PROCTOR
// canonical role on the mesh `x-mesh-user-roles` header (comma-joined token
// list; see libs/chora-go-common/auth/servicemesh.HeaderUserRoles).
func callerIsProctor(r *http.Request) bool {
	raw := strings.ToLower(r.Header.Get("x-mesh-user-roles"))
	if raw == "" {
		return false
	}
	for _, tok := range strings.Split(raw, ",") {
		if strings.TrimSpace(tok) == proctorRoleToken {
			return true
		}
	}
	return false
}

// enforceExamContentEmbargo rejects a PROCTOR-claimed caller from an atom /
// atom-revision content endpoint with 403 EXAM_CONTENT_EMBARGO_VIOLATION + an
// audit log line, and returns true (the handler MUST stop). Returns false for
// every non-proctor caller (content served normally).
func enforceExamContentEmbargo(w http.ResponseWriter, r *http.Request) bool {
	if !callerIsProctor(r) {
		return false
	}
	// Audit EVERY violation attempt (ADR-191 — IMDA D1 visibility that
	// high-stakes content was withheld from a proctor/vendor principal).
	log.Printf("audit=%s tenant=%s gcid=%s method=%s path=%s: PROCTOR role embargoed from exam content (ADR-191)",
		EmbargoErrorCode, tenantFromContext(r.Context()), gcidFromContext(r.Context()), r.Method, r.URL.Path)
	writeError(w, http.StatusForbidden, EmbargoErrorCode,
		"the PROCTOR role is embargoed from exam content (ADR-191)")
	return true
}
