package clients_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

type fakeGateway struct {
	got  *mgv1.InvokeRequest
	resp string
	err  error
}

func (f *fakeGateway) Invoke(_ context.Context, in *mgv1.InvokeRequest, _ ...grpc.CallOption) (*mgv1.InvokeResponse, error) {
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	return &mgv1.InvokeResponse{Completion: f.resp}, nil
}

func newClient(t *testing.T, g *fakeGateway) *clients.TopicClassifierGatewayClient {
	t.Helper()
	c, err := clients.NewTopicClassifierGatewayClientWithStub(g, "longcat-2.5-preview", 0)
	if err != nil {
		t.Fatalf("NewTopicClassifierGatewayClientWithStub: %v", err)
	}
	return c
}

func req() atom.ClassifyTopicsRequest {
	return atom.ClassifyTopicsRequest{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		Gcid:        "00000000-0000-7000-8000-000000001999",
		AtomID:      "019f278f-5acb-7415-a526-eb852b6409c7",
		Title:       "Comparing Fractions",
		Stem:        "Which fraction is larger, 3/4 or 2/3?",
		TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
}

func TestClassifyTopics_ParsesJSONArray(t *testing.T) {
	g := &fakeGateway{resp: `["fractions","number-sense"]`}
	got, err := newClient(t, g).ClassifyTopics(context.Background(), req())
	if err != nil {
		t.Fatalf("ClassifyTopics: %v", err)
	}
	if len(got) != 2 || got[0] != "fractions" || got[1] != "number-sense" {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyTopics_ParsesFencedJSON(t *testing.T) {
	// Models routinely wrap JSON in a markdown fence. Refusing that would fail
	// every atom for a cosmetic reason.
	g := &fakeGateway{resp: "```json\n[\"fractions\", \"number-sense\"]\n```"}
	got, err := newClient(t, g).ClassifyTopics(context.Background(), req())
	if err != nil {
		t.Fatalf("ClassifyTopics: %v", err)
	}
	if len(got) != 2 || got[0] != "fractions" {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyTopics_ParsesJSONEmbeddedInProse(t *testing.T) {
	g := &fakeGateway{resp: "Here are the topics:\n[\"fractions\"]\nHope that helps!"}
	got, err := newClient(t, g).ClassifyTopics(context.Background(), req())
	if err != nil {
		t.Fatalf("ClassifyTopics: %v", err)
	}
	if len(got) != 1 || got[0] != "fractions" {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyTopics_StampsGatewayEnvelope(t *testing.T) {
	g := &fakeGateway{resp: `["fractions"]`}
	if _, err := newClient(t, g).ClassifyTopics(context.Background(), req()); err != nil {
		t.Fatalf("ClassifyTopics: %v", err)
	}

	in := g.got
	if in == nil {
		t.Fatal("no request captured")
	}
	if in.GetTenantId() != "11111111-1111-7111-8111-111111111111" {
		t.Fatalf("tenant_id not stamped: %q", in.GetTenantId())
	}
	if in.GetGcid() != "00000000-0000-7000-8000-000000001999" {
		t.Fatalf("gcid not stamped: %q", in.GetGcid())
	}
	if in.GetSurface() != "content_creation" {
		t.Fatalf("ADR-254 D7: InvokeRequest.surface must be stamped content_creation, got %q", in.GetSurface())
	}
	if in.GetAgentId() == "" || in.GetCrewKind() == "" {
		t.Fatal("agent_id + crew_kind must be stamped (gateway policy + Armor tier key on agent_id)")
	}
	if in.GetLogicalModelId() != "longcat-2.5-preview" {
		t.Fatalf("logical_model_id not stamped: %q", in.GetLogicalModelId())
	}
	if in.GetInvocationId() == "" {
		t.Fatal("invocation_id must be stamped (gateway ledger dedup key)")
	}
	// OTLP-everywhere: the caller's span must cross the LLM hop.
	if in.GetTraceparent() == "" {
		t.Fatal("traceparent must propagate")
	}
	// The atom's content must actually reach the prompt.
	if !strings.Contains(in.GetPrompt(), "Comparing Fractions") {
		t.Fatalf("atom title missing from prompt: %q", in.GetPrompt())
	}
	if in.GetSystemPrompt() == "" {
		t.Fatal("system prompt must constrain the output shape")
	}
}

func TestClassifyTopics_GatewayErrorFailsLoud(t *testing.T) {
	g := &fakeGateway{err: errors.New("unavailable")}
	if _, err := newClient(t, g).ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on gateway failure, got nil")
	}
}

func TestClassifyTopics_EmptyCompletionFailsLoud(t *testing.T) {
	// A blank completion must NOT degrade to "no tags" — that would write the
	// defect back as if it were a result.
	g := &fakeGateway{resp: "   "}
	if _, err := newClient(t, g).ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on empty completion, got nil")
	}
}

func TestClassifyTopics_UnparseableCompletionFailsLoud(t *testing.T) {
	g := &fakeGateway{resp: "I cannot classify this atom."}
	if _, err := newClient(t, g).ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on unparseable completion, got nil")
	}
}

func TestClassifyTopics_EmptyJSONArrayFailsLoud(t *testing.T) {
	g := &fakeGateway{resp: `[]`}
	if _, err := newClient(t, g).ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on empty tag array, got nil")
	}
}

func TestNewTopicClassifierGatewayClient_EmptyTargetFailsLoud(t *testing.T) {
	// no-inline-config: a missing CHORA_MODEL_GATEWAY_GRPC_URL must fail loud,
	// never degrade to a no-op classifier.
	if _, err := clients.NewTopicClassifierGatewayClient("", "longcat-2.5-preview", 0); !errors.Is(err, clients.ErrTopicClassifierEmptyTarget) {
		t.Fatalf("want ErrTopicClassifierEmptyTarget, got %v", err)
	}
}

func TestNewTopicClassifierGatewayClient_EmptyModelFailsLoud(t *testing.T) {
	if _, err := clients.NewTopicClassifierGatewayClient("host:9090", "", 0); err == nil {
		t.Fatal("want loud error on empty logical model id, got nil")
	}
}

// The port contract must be satisfied — a compile-time assertion in test form.
func TestTopicClassifierGatewayClient_ImplementsPort(t *testing.T) {
	var _ atom.TopicClassifier = (*clients.TopicClassifierGatewayClient)(nil)
}
