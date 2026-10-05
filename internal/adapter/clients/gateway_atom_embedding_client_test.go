// gateway_atom_embedding_client_test.go: G1' gap 1 (register 6.2), the atom
// embedder rides the gateway Embed RPC so every atom embedding produces a
// ledger row attributed to the atom's author. Wire-realistic assertions on
// every REQUIRED EmbedRequest field.
package clients

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
)

type fakeAtomEmbedGRPC struct {
	lastReq *mgv1.EmbedRequest
	calls   int
	resp    *mgv1.EmbedResponse
	err     error
}

func (f *fakeAtomEmbedGRPC) Embed(_ context.Context, in *mgv1.EmbedRequest, _ ...grpc.CallOption) (*mgv1.EmbedResponse, error) {
	f.calls++
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func okAtomEmbedResponse() *mgv1.EmbedResponse {
	return &mgv1.EmbedResponse{
		InvocationId: "0198f000-0000-7000-8000-000000000002",
		Values:       []float32{0.5, -0.25},
		Vendor:       "vertex_ai_gemini",
		ModelVersion: "text-embedding-004",
	}
}

func TestGatewayAtomEmbeddingClient_SendsWireCompleteRequest(t *testing.T) {
	fake := &fakeAtomEmbedGRPC{resp: okAtomEmbedResponse()}
	c := NewGatewayAtomEmbeddingClientFromStub(fake, 0)

	ctx := tracing.WithTraceparent(context.Background(),
		"00-33333333333333333333333333333333-4444444444444444-01")
	got, err := c.Embed(ctx, "tenant-1", "author-gcid-1", "Fractions I\nWhat is 1/2 + 1/4?")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 || got[0] != 0.5 {
		t.Fatalf("values not passed through, got %v", got)
	}
	if fake.calls != 1 {
		t.Fatalf("expected exactly 1 RPC, got %d", fake.calls)
	}

	req := fake.lastReq
	if req.GetTenantId() != "tenant-1" {
		t.Errorf("tenant_id = %q", req.GetTenantId())
	}
	if req.GetGcid() != "author-gcid-1" {
		t.Errorf("gcid = %q (must be the atom's author)", req.GetGcid())
	}
	if req.GetAgentId() != "creation_atom_embedder" {
		t.Errorf("agent_id = %q", req.GetAgentId())
	}
	if req.GetCrewKind() != "content_creation" {
		t.Errorf("crew_kind = %q (mirror the topic classifier)", req.GetCrewKind())
	}
	if req.GetText() != "Fractions I\nWhat is 1/2 + 1/4?" {
		t.Errorf("text = %q", req.GetText())
	}
	if req.GetTaskType() != "RETRIEVAL_DOCUMENT" {
		t.Errorf("task_type = %q (atoms are the DOCUMENT side)", req.GetTaskType())
	}
	if req.GetLogicalModelId() != "text-embedding-004" {
		t.Errorf("logical_model_id = %q (must be pinned, not empty)", req.GetLogicalModelId())
	}
	if req.GetOutputDimensions() != 768 {
		t.Errorf("output_dimensions = %d", req.GetOutputDimensions())
	}
	if strings.TrimSpace(req.GetInvocationId()) == "" {
		t.Errorf("invocation_id empty; ledger idempotency requires a caller UUIDv7")
	}
	if req.GetTraceparent() != "00-33333333333333333333333333333333-4444444444444444-01" {
		t.Errorf("traceparent = %q", req.GetTraceparent())
	}
}

func TestGatewayAtomEmbeddingClient_Refusals(t *testing.T) {
	cases := []struct {
		name                 string
		tenantID, gcid, text string
		nameInErr            string
	}{
		{"empty text", "tenant-1", "author-1", "   ", "text"},
		{"empty gcid", "tenant-1", "", "hello", "gcid"},
		{"empty tenant", "", "author-1", "hello", "tenant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAtomEmbedGRPC{resp: okAtomEmbedResponse()}
			c := NewGatewayAtomEmbeddingClientFromStub(fake, 0)
			_, err := c.Embed(context.Background(), tc.tenantID, tc.gcid, tc.text)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if !errors.Is(err, agentengine.ErrInvalidRequest) {
				t.Fatalf("want ErrInvalidRequest, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.nameInErr) {
				t.Fatalf("error does not name %q: %v", tc.nameInErr, err)
			}
			if fake.calls != 0 {
				t.Fatalf("RPC must not fire on refusal, got %d calls", fake.calls)
			}
		})
	}
}

func TestGatewayAtomEmbeddingClient_MapsRPCErrorsToEngineSentinels(t *testing.T) {
	cases := []struct {
		name string
		rpc  error
		want error
	}{
		{"deadline to timeout", status.Error(codes.DeadlineExceeded, "deadline"), agentengine.ErrEngineTimeout},
		{"unavailable to unavailable", status.Error(codes.Unavailable, "refused"), agentengine.ErrEngineUnavailable},
		{"invalid argument to invalid request", status.Error(codes.InvalidArgument, "bad model"), agentengine.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAtomEmbedGRPC{err: tc.rpc}
			c := NewGatewayAtomEmbeddingClientFromStub(fake, 0)
			_, err := c.Embed(context.Background(), "tenant-1", "author-1", "hello")
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want sentinel %v, got %v", tc.want, err)
			}
		})
	}
}

func TestGatewayAtomEmbeddingClient_EmptyOrNilResponseIsLoud(t *testing.T) {
	for _, resp := range []*mgv1.EmbedResponse{nil, {InvocationId: "x"}} {
		fake := &fakeAtomEmbedGRPC{resp: resp}
		c := NewGatewayAtomEmbeddingClientFromStub(fake, 0)
		_, err := c.Embed(context.Background(), "tenant-1", "author-1", "hello")
		if err == nil {
			t.Fatalf("expected error for resp=%v", resp)
		}
		if !errors.Is(err, agentengine.ErrStreamAborted) {
			t.Fatalf("want ErrStreamAborted, got %v", err)
		}
	}
}

func TestGatewayAtomEmbeddingClient_ModelIDIsPinnedConstant(t *testing.T) {
	c := NewGatewayAtomEmbeddingClientFromStub(&fakeAtomEmbedGRPC{resp: okAtomEmbedResponse()}, 0)
	if got := c.ModelID(); got != "text-embedding-004" {
		t.Fatalf("ModelID() = %q", got)
	}
}

func TestNewGatewayAtomEmbeddingClient_EmptyTargetFailsLoud(t *testing.T) {
	if _, err := NewGatewayAtomEmbeddingClient("", 0); err == nil {
		t.Fatal("expected constructor error on empty target")
	} else if !strings.Contains(err.Error(), "CHORA_MODEL_GATEWAY_GRPC_URL") {
		t.Fatalf("error must name the env var to set: %v", err)
	}
}
