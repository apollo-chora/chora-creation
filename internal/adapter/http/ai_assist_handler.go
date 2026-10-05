package httpadapter

import (
	"net/http"
	"strings"
)

// aiAssist is the POST /api/atoms/ai-assist endpoint. It is served exclusively
// by the GKE qgen crew (async): the request publishes ai_assist.started.v1 via
// the outbox + INSERTs an ai_assist_jobs row + returns 202 + an AiAssistJob
// envelope (see aiAssistAsync; poll via GET /api/atoms/ai-assist/{job_id}).
//
// The legacy Vertex AI Agent Engine sync dispatch (QGen-direct + AI Kernel
// Orchestrator, both us-central1) was retired with the engine decommission
// (ADR-169; CHO-1920) — both pointed at dead engines and were unreachable
// behind the crew dispatch.
func (h *AtomHandler) aiAssist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/atoms/ai-assist")
		return
	}
	h.aiAssistAsync(w, r)
}

// tenantAndGCIDFromRequest reads the canonical mesh-asserted identity
// headers (preferred path through the BFF) with X-Tenant-Id + gcid
// fallback for direct-call dev scenarios.
func tenantAndGCIDFromRequest(r *http.Request) (string, string) {
	tenantID := r.Header.Get("chora-tenant-id")
	if tenantID == "" {
		tenantID = r.Header.Get("X-Tenant-Id")
	}
	gcid := r.Header.Get("chora-gcid")
	if gcid == "" {
		gcid = r.Header.Get("gcid")
	}
	return strings.TrimSpace(tenantID), strings.TrimSpace(gcid)
}
