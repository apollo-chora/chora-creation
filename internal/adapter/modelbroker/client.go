// Package modelbroker is the HTTP client adapter for chora-model-broker-router
// (Phyllis MVP §5.7). It implements the atom.ModelBrokerClient port.
//
// The base URL MUST come from the SVC_MODEL_BROKER_ROUTER_URL env var per
// the no-inline-config rule (memory feedback_no_inline_config) — the client
// constructor accepts the URL explicitly so wiring can also use Secret
// Manager / Terraform output without reading os.Getenv inside this package.
//
// The client propagates W3C trace context headers (traceparent / tracestate)
// per Tier 3 D11 and CLAUDE.md §6 ("Trace context across Pub/Sub" — same
// requirement applies to inter-service HTTP).
package modelbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// DefaultTimeout is the per-request timeout; spec says 30s for AI Assist
// (docs/m13/phyllis-mvp-2026-05-08.md §AI-Assist HTTP integration).
const DefaultTimeout = 30 * time.Second

// Client is the HTTP adapter implementing atom.ModelBrokerClient.
type Client struct {
	baseURL string
	hc      *http.Client
}

// Option configures a Client. Use functional options so the constructor
// stays open to extension without breaking callers.
type Option func(*Client)

// WithTimeout overrides the default per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.hc.Timeout = d
	}
}

// WithHTTPClient swaps the http.Client (mainly for tests + transport tuning).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.hc = hc
	}
}

// NewClient constructs a model-broker client.
//
// baseURL example: https://chora-model-broker-router.run.app  (no trailing /)
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: DefaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// generateRequestBody is the on-wire shape of POST /generate.
type generateRequestBody struct {
	TenantID    string `json:"tenant_id"`
	Gcid        string `json:"gcid"`
	CourseID    string `json:"course_id"`
	Prompt      string `json:"prompt"`
	ContentType string `json:"content_type"`
	Difficulty  int    `json:"difficulty"`
	Count       int    `json:"count"`
}

// generateResponseBody is the on-wire shape of the broker's /generate response.
type generateResponseBody struct {
	ScreeningDecision    string `json:"screening_decision"`
	ScreeningExplanation string `json:"screening_explanation"`
	ModelUsed            string `json:"model_used"`
	Items                []struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	} `json:"items"`
}

// Generate satisfies the atom.ModelBrokerClient port.
func (c *Client) Generate(ctx context.Context, req atom.GenerateRequest) (atom.GenerateResponse, error) {
	if c.baseURL == "" {
		return atom.GenerateResponse{}, errors.New("model broker URL is empty (set SVC_MODEL_BROKER_ROUTER_URL)")
	}

	b, err := json.Marshal(generateRequestBody{
		TenantID:    req.TenantID,
		Gcid:        req.Gcid,
		CourseID:    req.CourseID,
		Prompt:      req.Prompt,
		ContentType: string(req.ContentType),
		Difficulty:  req.Difficulty,
		Count:       req.Count,
	})
	if err != nil {
		return atom.GenerateResponse{}, fmt.Errorf("marshal generate request: %w", err)
	}

	url := c.baseURL + "/generate"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return atom.GenerateResponse{}, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// W3C trace propagation per Tier 3 D11.
	if req.TraceParent != "" {
		httpReq.Header.Set("traceparent", req.TraceParent)
	}
	if req.TraceState != "" {
		httpReq.Header.Set("tracestate", req.TraceState)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return atom.GenerateResponse{}, fmt.Errorf("model broker request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return atom.GenerateResponse{}, fmt.Errorf("model broker returned %d: %s", resp.StatusCode, string(body))
	}

	var rb generateResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&rb); err != nil {
		return atom.GenerateResponse{}, fmt.Errorf("decode generate response: %w", err)
	}

	out := atom.GenerateResponse{
		ScreeningDecision:    atom.ScreeningDecision(rb.ScreeningDecision),
		ScreeningExplanation: rb.ScreeningExplanation,
		ModelUsed:            rb.ModelUsed,
		Items:                make([]atom.GeneratedItem, 0, len(rb.Items)),
	}
	for _, it := range rb.Items {
		out.Items = append(out.Items, atom.GeneratedItem{Title: it.Title, Body: it.Body})
	}
	return out, nil
}
