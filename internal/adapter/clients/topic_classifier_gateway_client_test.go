package clients_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"

	gatewayv1 "github.com/apollo-chora/chora-creation/internal/adapter/clients/gatewayv1"
)

type fakeGateway struct {
	got  *gatewayv1.InvokeRequest
	md   metadata.MD
	resp string
	err  error
}

func (f *fakeGateway) Invoke(ctx context.Context, in *gatewayv1.InvokeRequest, _ ...grpc.CallOption) (*gatewayv1.InvokeResponse, error) {
	f.got = in
	f.md, _ = metadata.FromOutgoingContext(ctx)
	if f.err != nil {
		return nil, f.err
	}
	return &gatewayv1.InvokeResponse{
		FinishReason: gatewayv1.FinishReason_FINISH_REASON_STOP,
		Output: []*gatewayv1.OutputItem{{
			Item: &gatewayv1.OutputItem_Content{Content: &gatewayv1.ContentPart{
				Part: &gatewayv1.ContentPart_Text{Text: &gatewayv1.TextPart{Text: f.resp}},
			}},
		}},
	}, nil
}

func newClient(t *testing.T, g *fakeGateway) *clients.TopicClassifierGatewayClient {
	t.Helper()
	c, err := clients.NewTopicClassifierGatewayClientWithStub(g, 0)
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
	if in.GetAgentId() == "" {
		t.Fatal("agent_id must be stamped (gateway policy keys on agent_id)")
	}
	// The gateway resolves the operation from the RPC method, so the caller
	// sends no model ID.
	if in.GetModel() != "" {
		t.Fatalf("model = %q, want empty", in.GetModel())
	}
	if in.GetInvocationId() == "" {
		t.Fatal("invocation_id must be stamped (gateway ledger dedup key)")
	}
	// crew_kind has no Invoke field; it rides in metadata_json.
	var meta map[string]any
	if err := json.Unmarshal(in.GetMetadataJson(), &meta); err != nil {
		t.Fatalf("metadata_json is not valid JSON: %v (bytes: %q)", err, in.GetMetadataJson())
	}
	if meta["crew_kind"] != "content_creation" {
		t.Fatalf("crew_kind = %v", meta["crew_kind"])
	}
	// OTLP-everywhere: the caller's span must cross the LLM hop. The Invoke RPC
	// has no traceparent field, so it travels as gRPC metadata.
	if got := g.md.Get("traceparent"); len(got) != 1 || got[0] != req().TraceParent {
		t.Fatalf("traceparent metadata = %v, want the caller's span", got)
	}
	// The atom's content must actually reach the prompt, which is now a user
	// message rather than a request field.
	if len(in.GetMessages()) != 1 {
		t.Fatalf("messages len = %d, want 1", len(in.GetMessages()))
	}
	prompt := in.GetMessages()[0].GetContent()[0].GetText().GetText()
	if !strings.Contains(prompt, "Comparing Fractions") {
		t.Fatalf("atom title missing from prompt: %q", prompt)
	}
	if in.GetSystemInstruction() == "" {
		t.Fatal("system instruction must constrain the output shape")
	}
}

func TestClassifyTopics_NonStopFinishReasonFailsLoud(t *testing.T) {
	// A truncated answer must not be parsed as a complete one.
	trunc := &truncatingGateway{}
	c, err := clients.NewTopicClassifierGatewayClientWithStub(trunc, 0)
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	if _, err := c.ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on a non-STOP finish reason, got nil")
	}
}

// truncatingGateway reports a LENGTH finish reason, i.e. a truncated answer.
type truncatingGateway struct{}

func (t *truncatingGateway) Invoke(_ context.Context, _ *gatewayv1.InvokeRequest, _ ...grpc.CallOption) (*gatewayv1.InvokeResponse, error) {
	return &gatewayv1.InvokeResponse{
		FinishReason:    gatewayv1.FinishReason_FINISH_REASON_LENGTH,
		RawFinishReason: proto.String("hit ceiling"),
	}, nil
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
	if _, err := clients.NewTopicClassifierGatewayClient("", 0); !errors.Is(err, clients.ErrTopicClassifierEmptyTarget) {
		t.Fatalf("want ErrTopicClassifierEmptyTarget, got %v", err)
	}
}

// A refusal is the gateway declining to answer. Dropping it silently would
// parse a truncated answer as a complete one.
func TestClassifyTopics_RefusalFailsLoud(t *testing.T) {
	g := &refusingGateway{}
	c, err := clients.NewTopicClassifierGatewayClientWithStub(g, 0)
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	if _, err := c.ClassifyTopics(context.Background(), req()); err == nil {
		t.Fatal("want loud error on a refusal, got nil")
	}
}

type refusingGateway struct{}

func (r *refusingGateway) Invoke(_ context.Context, _ *gatewayv1.InvokeRequest, _ ...grpc.CallOption) (*gatewayv1.InvokeResponse, error) {
	return &gatewayv1.InvokeResponse{
		FinishReason: gatewayv1.FinishReason_FINISH_REASON_STOP,
		Output: []*gatewayv1.OutputItem{{
			Item: &gatewayv1.OutputItem_Content{Content: &gatewayv1.ContentPart{
				Part: &gatewayv1.ContentPart_Refusal{Refusal: &gatewayv1.RefusalPart{Text: "not allowed"}},
			}},
		}},
	}, nil
}

// An existing traceparent must not suppress a tracestate the caller supplied:
// the two halves of the W3C context are decided independently.
func TestClassifyTopics_PreservesExistingParentAndAddsState(t *testing.T) {
	g := &fakeGateway{resp: `["fractions"]`}
	c, err := clients.NewTopicClassifierGatewayClientWithStub(g, 0)
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	r := req()
	r.TraceState = "congo=t61rcWkgMzE"
	ctx := metadata.AppendToOutgoingContext(context.Background(), "traceparent", "00-aaaa-bbbb-01")
	if _, err := c.ClassifyTopics(ctx, r); err != nil {
		t.Fatalf("ClassifyTopics: %v", err)
	}
	if got := g.md.Get("traceparent"); len(got) != 1 || got[0] != "00-aaaa-bbbb-01" {
		t.Errorf("traceparent = %v, want the existing value only", got)
	}
	if got := g.md.Get("tracestate"); len(got) != 1 || got[0] != "congo=t61rcWkgMzE" {
		t.Errorf("tracestate = %v, want the caller's value", got)
	}
}

// The port contract must be satisfied — a compile-time assertion in test form.
func TestTopicClassifierGatewayClient_ImplementsPort(t *testing.T) {
	var _ atom.TopicClassifier = (*clients.TopicClassifierGatewayClient)(nil)
}
