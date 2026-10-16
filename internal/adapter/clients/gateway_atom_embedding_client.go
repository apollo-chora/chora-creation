// gateway_atom_embedding_client.go: G1' gap 1 (register 6.2), the atom
// embedder riding the model gateway's Embed RPC instead of dialing a vendor
// directly. Every atom embedding now produces a TokenUsageLedger row at the
// single un-bypassable chokepoint (ADR-163), attributed to the atom's AUTHOR
// (the topic-classifier precedent: learning_atoms.gcid).
//
// Implements the widened embedindex.Embedder port: tenant + gcid ride as
// explicit parameters because creation never stamps tracing ctx keys. The dial
// shape mirrors topic_classifier_gateway_client.go: plaintext gRPC to the mesh
// Service from env CHORA_MODEL_GATEWAY_GRPC_URL, the sidecar does mTLS,
// grpc.NewClient lazy dial, a minimal private client seam for tests.
//
// No model ID is sent. The gateway resolves the embeddings operation from the
// RPC method and serves it from that operation's own configured routes, so the
// caller no longer names a model and EMBEDDING_LOGICAL_MODEL_ID is gone. The
// model that actually answered comes back on the response and is what the
// embedindex stamps onto atom_embeddings.model_id, so the column can never
// drift from what produced the vector.
//
// 1024-d matches the vector(1024) column, sent explicitly.
//
// Error contract: parity with the retired AtomEmbeddingClient, the same
// agentengine sentinels, so the publish path's soft-fail and the backfill's
// loud per-atom failure handling are untouched.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"

	gatewayv1 "github.com/apollo-chora/chora-creation/internal/adapter/clients/gatewayv1"
)

// ErrAtomEmbedEmptyTarget fails construction when the gRPC target is empty.
// Boot wiring treats an unset URL as "indexer not wired" BEFORE constructing
// this client, so reaching this error means a wiring bug, not a config choice.
var ErrAtomEmbedEmptyTarget = errors.New(
	"gateway_atom_embedding_client: target empty (set CHORA_MODEL_GATEWAY_GRPC_URL)")

const (
	// atomEmbedDefaultTimeout bounds one embedding round-trip; embeds are fast
	// single-vector calls.
	atomEmbedDefaultTimeout = 10 * time.Second

	// atomEmbedAgentID is the fixed calling-adapter identity on the ledger row
	// (AgentRole) and in agent-guardrail-mapping.yaml (permissive entry).
	atomEmbedAgentID = "creation_atom_embedder"

	// atomEmbedCrewKind mirrors topicClassifierCrewKind, the sibling
	// creation-to-gateway caller.
	atomEmbedCrewKind = "content_creation"

	// atomEmbedTaskTypeDocument: atoms are the DOCUMENT side of the retrieval
	// pair (consumption embeds queries on its side).
	//
	// The gateway's EmbedRequest has no task_type field — the embeddings
	// operation is one route — so this rides in metadata_json, which the
	// gateway maps onto the invocation's metadata. It is bookkeeping for the
	// ledger, not a routing input.
	atomEmbedTaskTypeDocument = "RETRIEVAL_DOCUMENT"

	// atomEmbedOutputDimensions matches atom_embeddings vector(1024), the
	// embedding route's native width. Sent explicitly, never left to a remote
	// default.
	atomEmbedOutputDimensions = 1024
)

// atomEmbedGRPC is the slice of gatewayv1.ModelGatewayServiceClient this adapter
// calls (Embed only). The generated client satisfies it; tests inject a fake.
type atomEmbedGRPC interface {
	Embed(ctx context.Context, in *gatewayv1.EmbedRequest, opts ...grpc.CallOption) (*gatewayv1.EmbedResponse, error)
}

// GatewayAtomEmbeddingClient embeds atom text via the gateway for the
// atom_embeddings writer (the widened embedindex.Embedder port).
type GatewayAtomEmbeddingClient struct {
	client  atomEmbedGRPC
	timeout time.Duration
}

// NewGatewayAtomEmbeddingClient dials the gateway at the mesh target. An empty
// target fails loud. grpc.NewClient is lazy: a healthy return does not prove
// reachability; the first RPC surfaces Unavailable, which the publish path
// soft-fails and the backfill records loudly.
func NewGatewayAtomEmbeddingClient(target string, timeout time.Duration) (*GatewayAtomEmbeddingClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrAtomEmbedEmptyTarget
	}
	if timeout <= 0 {
		timeout = atomEmbedDefaultTimeout
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gateway_atom_embedding_client: dial %q: %w", target, err)
	}
	return &GatewayAtomEmbeddingClient{client: gatewayv1.NewModelGatewayServiceClient(conn), timeout: timeout}, nil
}

// NewGatewayAtomEmbeddingClientFromStub injects a fake gRPC client (tests).
func NewGatewayAtomEmbeddingClientFromStub(stub atomEmbedGRPC, timeout time.Duration) *GatewayAtomEmbeddingClient {
	if timeout <= 0 {
		timeout = atomEmbedDefaultTimeout
	}
	return &GatewayAtomEmbeddingClient{client: stub, timeout: timeout}
}

// Embed produces one 1024-d document embedding for the supplied atom text,
// attributed to the given tenant + actor (the atom's author). All three are
// REQUIRED by the gateway; a gap refuses loud here, before any RPC.
//
// The second return is the upstream model that answered, which the caller
// stamps onto atom_embeddings.model_id.
func (c *GatewayAtomEmbeddingClient) Embed(ctx context.Context, tenantID, gcid, text string) ([]float32, string, error) {
	if c == nil || c.client == nil {
		return nil, "", agentengine.ErrEngineUnavailable
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, "", fmt.Errorf("%w: tenant required for ledger attribution", agentengine.ErrInvalidRequest)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, "", fmt.Errorf("%w: gcid required for ledger attribution (pass the atom author's gcid)", agentengine.ErrInvalidRequest)
	}
	if strings.TrimSpace(text) == "" {
		return nil, "", fmt.Errorf("%w: embed text required", agentengine.ErrInvalidRequest)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// The gateway's EmbedRequest carries no tenant, gcid or agent field: the
	// credential is the tenant and the request claims none. The caller's own
	// validation still requires tenant + gcid, because the ledger row it
	// writes is attributed to the atom's author.
	//
	// `user` is the one identity-shaped field the request has, so the author's
	// gcid rides there; crew_kind and task_type ride in metadata_json.
	resp, err := c.client.Embed(withTraceparent(callCtx), &gatewayv1.EmbedRequest{
		Input:        []string{text},
		Dimensions:   proto.Int32(atomEmbedOutputDimensions),
		User:         gcid,
		MetadataJson: []byte(fmt.Sprintf(`{"crew_kind":%q,"task_type":%q}`, atomEmbedCrewKind, atomEmbedTaskTypeDocument)),
	})
	if err != nil {
		return nil, "", mapAtomEmbedRPCError(err)
	}
	vec, err := firstVector(resp)
	if err != nil {
		return nil, "", err
	}
	return vec, resp.GetUpstreamModel(), nil
}

// firstVector returns the response's first vector, converted to float32 —
// the protobuf field is float64, the embedindex port is float32.
func firstVector(resp *gatewayv1.EmbedResponse) ([]float32, error) {
	if resp == nil {
		return nil, fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	data := resp.GetData()
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	src := data[0].GetEmbedding()
	out := make([]float32, len(src))
	for i, v := range src {
		out[i] = float32(v)
	}
	return out, nil
}

// withTraceparent moves the traceparent onto the gRPC metadata. The gateway's
// Embed RPC has no traceparent field; trace context travels as the
// `traceparent` metadata key, which is where the OTel interceptor reads it.
//
// AppendToOutgoingContext appends rather than replaces, so an existing value is
// left alone rather than sent twice.
func withTraceparent(ctx context.Context) context.Context {
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get("traceparent")) > 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "traceparent", tracing.TraceparentFromContext(ctx))
}

// mapAtomEmbedRPCError translates gRPC status codes onto the canonical
// agentengine sentinels the retired direct-Vertex client used.
func mapAtomEmbedRPCError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
	switch st.Code() {
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %v", agentengine.ErrInvalidRequest, err)
	default:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
}
