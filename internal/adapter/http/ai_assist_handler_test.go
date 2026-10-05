package httpadapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
)

// TestAIAssist_Rejects_NonPOST — the /api/atoms/ai-assist endpoint only accepts POST.
func TestAIAssist_Rejects_NonPOST(t *testing.T) {
	router := NewRouterWithDeps(RouterDeps{Repo: inmem.NewAtomRepository()})
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/atoms/ai-assist", nil)
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestAIAssist_POST_RoutesToAsyncCrew — CHO-1920 regression guard. The endpoint
// is now served EXCLUSIVELY by the GKE qgen crew (async); the legacy Vertex AI
// Agent Engine sync paths were retired. With the crew repo unwired, a POST falls
// through to aiAssistAsync's fail-loud guard (503 ai_assist_jobs_not_wired) —
// proving it routes to the crew, NOT a retired engine path (which would have
// returned qgen_engine_not_configured).
func TestAIAssist_POST_RoutesToAsyncCrew(t *testing.T) {
	// AiAssistJobs intentionally nil ⇒ the async handler's not-wired guard fires.
	router := NewRouterWithDeps(RouterDeps{Repo: inmem.NewAtomRepository()})
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/atoms/ai-assist",
		strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "tenant-test")
	req.Header.Set("gcid", "gcid-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "ai_assist_jobs_not_wired" {
		t.Errorf("code = %v, want ai_assist_jobs_not_wired (crew routing, not a retired engine path)", body["code"])
	}
}
