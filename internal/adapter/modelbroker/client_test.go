// Package modelbroker_test exercises the HTTP client that talks to
// chora-model-broker-router /generate per docs/m13/phyllis-mvp-2026-05-08.md
// §5.7. Uses httptest to mock the broker; verifies request shape, traceparent
// propagation, timeout handling, and screening verdict pass-through.
package modelbroker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/modelbroker"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestClient_SendsRequestAndParsesResponse(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	var capturedTraceparent string
	var capturedTracestate string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTraceparent = r.Header.Get("traceparent")
		capturedTracestate = r.Header.Get("tracestate")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
		  "screening_decision": "allow",
		  "screening_explanation": "ok",
		  "model_used": "vertex/gemini-1.5-pro",
		  "items": [{"title":"t1","body":"b1"},{"title":"t2","body":"b2"}]
		}`))
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL, modelbroker.WithTimeout(2*time.Second))
	resp, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt:      "test prompt",
		ContentType: atom.TypeMCQ,
		Difficulty:  3,
		Count:       2,
		TraceParent: "00-aaaa-bbbb-01",
		TraceState:  "rojo=00f067aa0ba902b7",
	})
	if err != nil {
		t.Fatalf("Generate unexpected: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningAllow {
		t.Errorf("ScreeningDecision = %q; want allow", resp.ScreeningDecision)
	}
	if len(resp.Items) != 2 {
		t.Errorf("Items len = %d; want 2", len(resp.Items))
	}
	if resp.ModelUsed != "vertex/gemini-1.5-pro" {
		t.Errorf("ModelUsed = %q", resp.ModelUsed)
	}
	if capturedTraceparent != "00-aaaa-bbbb-01" {
		t.Errorf("traceparent header = %q; want 00-aaaa-bbbb-01", capturedTraceparent)
	}
	if capturedTracestate != "rojo=00f067aa0ba902b7" {
		t.Errorf("tracestate header missing or wrong: %q", capturedTracestate)
	}
	if capturedBody["prompt"] != "test prompt" {
		t.Errorf("body.prompt = %v; want test prompt", capturedBody["prompt"])
	}
	if capturedBody["count"].(float64) != 2 {
		t.Errorf("body.count = %v; want 2", capturedBody["count"])
	}
	if capturedBody["course_id"] != "c1" {
		t.Errorf("body.course_id = %v; want c1", capturedBody["course_id"])
	}
}

func TestClient_RewriteVerdictPassesThrough(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "screening_decision": "rewrite",
		  "screening_explanation": "balanced version provided",
		  "model_used": "vertex/gemini-1.5-flash",
		  "items": [{"title":"balanced","body":"compare"}]
		}`))
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL)
	resp, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningRewrite {
		t.Errorf("ScreeningDecision = %q; want rewrite", resp.ScreeningDecision)
	}
	if resp.ScreeningExplanation != "balanced version provided" {
		t.Errorf("ScreeningExplanation = %q", resp.ScreeningExplanation)
	}
}

func TestClient_RefuseVerdictReturnsZeroItems(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "screening_decision": "refuse",
		  "screening_explanation": "hateful content policy",
		  "items": []
		}`))
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL)
	resp, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningRefuse {
		t.Errorf("ScreeningDecision = %q; want refuse", resp.ScreeningDecision)
	}
	if len(resp.Items) != 0 {
		t.Errorf("Items len = %d; want 0 on refuse", len(resp.Items))
	}
}

func TestClient_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream"}`))
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL)
	_, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err == nil {
		t.Errorf("expected error on 502; got nil")
	}
	if !strings.Contains(err.Error(), "502") && !strings.Contains(err.Error(), "broker") {
		t.Errorf("expected error to mention 502 or broker; got %v", err)
	}
}

func TestClient_TimeoutReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL, modelbroker.WithTimeout(50*time.Millisecond))
	_, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err == nil {
		t.Errorf("expected timeout error; got nil")
	}
}

func TestClient_RejectsEmptyURL(t *testing.T) {
	t.Parallel()

	c := modelbroker.NewClient("")
	_, err := c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err == nil {
		t.Errorf("expected error when broker URL is empty (no inline config); got nil")
	}
}

func TestClient_PostsToGenerateEndpoint(t *testing.T) {
	t.Parallel()

	var capturedPath string
	var capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"screening_decision":"allow","items":[],"model_used":"x"}`))
	}))
	defer srv.Close()

	c := modelbroker.NewClient(srv.URL)
	_, _ = c.Generate(context.Background(), atom.GenerateRequest{
		TenantID: "t1", Gcid: "g1", CourseID: "c1",
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if capturedMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", capturedMethod)
	}
	if capturedPath != "/generate" && capturedPath != "/v1/generate" {
		t.Errorf("path = %q; want /generate or /v1/generate", capturedPath)
	}
}
